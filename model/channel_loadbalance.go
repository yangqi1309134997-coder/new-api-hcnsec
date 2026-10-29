package model

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ============================================================================
// 负载均衡策略 (Channel Load Balancing)
// ============================================================================
//
// 背景：
//   原生 new-api 的渠道选择只有「优先级分层 + 加权随机」一种策略，对于
//   LLM 这种请求耗时不均（3s ~ 120s）的场景，加权随机会导致慢渠道堆积、
//   快渠道闲置。本模块在不破坏原有优先级机制的前提下，引入可配置的
//   「同优先级层内」负载均衡策略：
//
//     - weighted-random  : 原生行为，按 Weight 做加权随机（默认，向后兼容）
//     - least-connections: 选当前活跃请求数最少的渠道，并列时按权重随机
//     - random           : 同优先级层内完全随机（忽略权重）
//     - round-robin      : 轮询，按候选列表签名记忆游标
//
// 计数器设计（单机部署）：
//   channelActiveCount 是进程内 sync.Map[channelId -> *atomic.Int64]。
//   请求开始 +1，请求结束（含流式 EOF / 客户端断开 / 超时 / panic）-1。
//   单机部署下进程内计数即真实并发数，无需 Redis。
//   多机部署需改造为 Redis INCR/DECR（本项目当前为单机，故用内存实现）。
//
// 流式请求准确性：
//   StreamScannerHandler 是同步阻塞的，会等到流真正结束（EOF/超时/客户端
//   断开）才返回，因此 relayHandler 返回即代表该渠道请求结束，计数器 -1
//   用 defer 包住 relayHandler 调用即可对流式/非流式都准确。
//
// 循环依赖说明：
//   本文件放在 model 包（而非 service 包），因为 service 包依赖 model 包，
//   若反向依赖会产生 import cycle。策略选择函数只用到 model.Channel，
//   放在 model 包内天然合理。策略开关 CurrentLoadBalanceStrategy 由
//   service/option 侧通过 setter 设置（model 不反向依赖 service）。
// ============================================================================

// LoadBalanceStrategy 负载均衡策略类型
type LoadBalanceStrategy string

const (
	// StrategyWeightedRandom 加权随机（原生默认行为）
	StrategyWeightedRandom LoadBalanceStrategy = "weighted-random"
	// StrategyLeastConnections 最少连接数
	StrategyLeastConnections LoadBalanceStrategy = "least-connections"
	// StrategyRandom 纯随机（忽略权重）
	StrategyRandom LoadBalanceStrategy = "random"
	// StrategyRoundRobin 轮询
	StrategyRoundRobin LoadBalanceStrategy = "round-robin"
	// StrategySmart 智能路由：按渠道健康分（成功率/首字延迟/负载）选路，
	// 无视优先级与权重；失败自动降权冷却，防打爆见 selectSmart。
	StrategySmart LoadBalanceStrategy = "smart"
)

// CurrentLoadBalanceStrategy 当前生效的全局负载均衡策略。
// 默认 weighted-random 保持完全向后兼容；运行时可由 option 层通过
// SetLoadBalanceStrategy 修改（后台设置保存后热生效，无需重启）。
var CurrentLoadBalanceStrategy = StrategyWeightedRandom

// IsValidLoadBalanceStrategy 报告策略名是否受支持。
func IsValidLoadBalanceStrategy(s string) bool {
	switch LoadBalanceStrategy(s) {
	case StrategyWeightedRandom, StrategyLeastConnections, StrategyRandom, StrategyRoundRobin, StrategySmart:
		return true
	}
	return false
}

// SetLoadBalanceStrategy 由 option 层调用以热更新全局策略。
// 传入未知值时回退到默认的加权随机，保证健壮性。
func SetLoadBalanceStrategy(s string) {
	old := CurrentLoadBalanceStrategy
	switch LoadBalanceStrategy(s) {
	case StrategyWeightedRandom, StrategyLeastConnections, StrategyRandom, StrategyRoundRobin, StrategySmart:
		CurrentLoadBalanceStrategy = LoadBalanceStrategy(s)
	default:
		CurrentLoadBalanceStrategy = StrategyWeightedRandom
	}
	if old != CurrentLoadBalanceStrategy {
		common.SysLog(fmt.Sprintf("[LB] channel load balance strategy switched: %s -> %s", old, CurrentLoadBalanceStrategy))
	}
}

// StrategyForGroup 解析某分组实际生效的策略：
// 先查 loadbalance_setting.by_group 覆盖表，未配置或值非法时回退全局策略。
func StrategyForGroup(group string) LoadBalanceStrategy {
	if group != "" {
		if s, ok := operation_setting.GetLoadBalanceSetting().ByGroup[group]; ok && IsValidLoadBalanceStrategy(s) {
			return LoadBalanceStrategy(s)
		}
	}
	return CurrentLoadBalanceStrategy
}

// channelActiveCount 每个渠道当前的活跃请求计数（并发数）
var channelActiveCount sync.Map // map[int]*atomic.Int64

// rrState 轮询策略的游标状态
var rrState sync.Map // map[string]*rrCursor

type rrCursor struct {
	mu    sync.Mutex
	index int
}

// ---------------------------------------------------------------------------
// 并发计数器（供 controller 在请求生命周期调用）
// ---------------------------------------------------------------------------

// IncrChannelActive 增加渠道活跃计数，返回增加后的值
func IncrChannelActive(channelID int) int64 {
	v, _ := channelActiveCount.LoadOrStore(channelID, new(atomic.Int64))
	return v.(*atomic.Int64).Add(1)
}

// DecrChannelActive 减少渠道活跃计数（不会低于 0），返回减少后的值
func DecrChannelActive(channelID int) int64 {
	v, ok := channelActiveCount.Load(channelID)
	if !ok {
		return 0
	}
	cnt := v.(*atomic.Int64).Add(-1)
	if cnt < 0 {
		// 防御性：异常情况下（如重复 Decr）钳制为 0
		v.(*atomic.Int64).Store(0)
		return 0
	}
	return cnt
}

// GetChannelActiveCount 返回某渠道当前活跃请求数（供调试/监控）
func GetChannelActiveCount(channelID int) int64 {
	if v, ok := channelActiveCount.Load(channelID); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// GetChannelActiveSnapshot 返回所有渠道活跃计数的快照（供调试/监控）
func GetChannelActiveSnapshot() map[int]int64 {
	m := make(map[int]int64)
	channelActiveCount.Range(func(k, v any) bool {
		m[k.(int)] = v.(*atomic.Int64).Load()
		return true
	})
	return m
}

// ---------------------------------------------------------------------------
// 策略分发：从同优先级层的候选渠道中选一个
// ---------------------------------------------------------------------------

// SelectChannelByStrategy 根据分组生效的策略从候选渠道（已限定到某个优先级层）中选一个。
// channels 与 sumWeight 由调用方（GetRandomSatisfiedChannel）预先计算好。
// smoothingFactor / smoothingAdjustment 保留原生加权随机的平滑逻辑。
func SelectChannelByStrategy(group string, targetChannels []*Channel, sumWeight, smoothingFactor, smoothingAdjustment int) *Channel {
	if len(targetChannels) == 0 {
		return nil
	}
	if len(targetChannels) == 1 {
		return targetChannels[0]
	}

	switch StrategyForGroup(group) {
	case StrategyLeastConnections:
		return selectLeastConnections(targetChannels, sumWeight, smoothingFactor, smoothingAdjustment)
	case StrategyRandom:
		return selectRandom(targetChannels)
	case StrategyRoundRobin:
		return selectRoundRobin(targetChannels)
	case StrategySmart:
		// 智能路由在 GetRandomSatisfiedChannel 的 smart 分支提前返回（需跳过优先级分层），
		// 正常不会走到这里；兜底退化为加权随机。
		return selectWeightedRandom(targetChannels, sumWeight, smoothingFactor, smoothingAdjustment)
	case StrategyWeightedRandom:
		fallthrough
	default:
		// 默认/未知策略：走原生加权随机，保证向后兼容
		return selectWeightedRandom(targetChannels, sumWeight, smoothingFactor, smoothingAdjustment)
	}
}

// selectWeightedRandom 原生加权随机算法（从 channel_cache.go 原样迁移）
func selectWeightedRandom(targetChannels []*Channel, sumWeight, smoothingFactor, smoothingAdjustment int) *Channel {
	totalWeight := sumWeight * smoothingFactor
	if totalWeight <= 0 {
		// 兜底：极小概率的权重异常，退化为随机
		return selectRandom(targetChannels)
	}
	randomWeight := rand.Intn(totalWeight)
	for _, channel := range targetChannels {
		randomWeight -= channel.GetWeight()*smoothingFactor + smoothingAdjustment
		if randomWeight < 0 {
			return channel
		}
	}
	return targetChannels[len(targetChannels)-1]
}

// selectLeastConnections 最少连接数：选 activeCount 最小的；并列时按权重随机 tie-break。
// 相比纯 least-conn，加权 tie-break 避免了并列渠道间的「惊群」和权重被完全忽略。
func selectLeastConnections(targetChannels []*Channel, sumWeight, smoothingFactor, smoothingAdjustment int) *Channel {
	// 1. 找最小活跃数
	minCount := int64(-1)
	for _, ch := range targetChannels {
		cnt := getActiveCount(ch.Id)
		if minCount < 0 || cnt < minCount {
			minCount = cnt
		}
	}
	// 2. 收集所有并列最小的候选
	var tied []*Channel
	for _, ch := range targetChannels {
		if getActiveCount(ch.Id) == minCount {
			tied = append(tied, ch)
		}
	}
	if len(tied) == 1 {
		lbLogSample(targetChannels, tied[0], minCount)
		return tied[0]
	}
	// 3. 并列时用加权随机 tie-break（复用原生算法）
	tiedSumWeight := 0
	for _, ch := range tied {
		tiedSumWeight += ch.GetWeight()
	}
	// 平滑处理，与原生保持一致
	tSmoothingFactor := 1
	tSmoothingAdjustment := 0
	if tiedSumWeight == 0 {
		tiedSumWeight = len(tied) * 100
		tSmoothingAdjustment = 100
	} else if tiedSumWeight/len(tied) < 10 {
		tSmoothingFactor = 100
	}
	selected := selectWeightedRandom(tied, tiedSumWeight, tSmoothingFactor, tSmoothingAdjustment)
	lbLogSample(targetChannels, selected, minCount)
	return selected
}

// lbLogSample 采样记录 least-conn 的选择决策（每 50 次记录 1 次），用于验证策略生效。
// 记录内容：所有候选的 (channelId,activeCount) 以及最终选中的渠道，可直观看到
// 确实选中了活跃数最少的渠道。
var lbLogCounter atomic.Int64

func lbLogSample(allCandidates []*Channel, selected *Channel, selectedCount int64) {
	n := lbLogCounter.Add(1)
	if n%50 != 1 { // 每 50 次采样 1 次，避免日志爆炸
		return
	}
	summary := ""
	for _, ch := range allCandidates {
		summary += fmt.Sprintf("#%d=%d ", ch.Id, getActiveCount(ch.Id))
	}
	common.SysLog(fmt.Sprintf("[LB] least-conn pick #%d (active=%d) | candidates: %s", selected.Id, selectedCount, summary))
}

// selectRandom 纯随机（忽略权重）
func selectRandom(targetChannels []*Channel) *Channel {
	if len(targetChannels) == 1 {
		return targetChannels[0]
	}
	return targetChannels[rand.Intn(len(targetChannels))]
}

// selectRoundRobin 轮询。游标按候选列表签名记忆。
// 候选列表由 channel_cache.go 按渠道 id 稳定生成，故顺序稳定。
func selectRoundRobin(targetChannels []*Channel) *Channel {
	if len(targetChannels) == 1 {
		return targetChannels[0]
	}
	key := roundRobinKeyForChannels(targetChannels)
	cur, _ := rrState.LoadOrStore(key, &rrCursor{})
	c := cur.(*rrCursor)
	c.mu.Lock()
	idx := c.index % len(targetChannels)
	c.index = (c.index + 1) % len(targetChannels)
	c.mu.Unlock()
	return targetChannels[idx]
}

// roundRobinKeyForChannels 用候选列表的 id 集合生成稳定签名作为游标 key
func roundRobinKeyForChannels(targetChannels []*Channel) string {
	if len(targetChannels) == 0 {
		return ""
	}
	ids := make([]int, len(targetChannels))
	for i, ch := range targetChannels {
		ids[i] = ch.Id
	}
	sort.Ints(ids)
	sig := ""
	for _, id := range ids {
		sig += "-" + strconv.Itoa(id)
	}
	return sig
}

// getActiveCount 内部辅助：返回渠道活跃计数
func getActiveCount(channelID int) int64 {
	if v, ok := channelActiveCount.Load(channelID); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// ---------------------------------------------------------------------------
// 智能路由（smart 策略）
// ---------------------------------------------------------------------------

// smartChannelScore 单渠道综合得分（0~1）：
//   - health：置信健康度（样本不足向 0.5 收缩）
//   - speed ：延迟分，候选集内相对（最快渠道 = 1.0），无延迟数据给中性 0.5
//   - load  ：1/(1+k*活跃数)，实时反馈防单渠道被打爆
//
// 冷却中的渠道总分乘 0.1：不排除（可能是唯一候选），但基本不会被选中。
func smartChannelScore(channel *Channel, snapshot ChannelHealthSnapshot, bestLatencyMs float64) float64 {
	setting := operation_setting.GetLoadBalanceSetting()
	health := channelHealthConfidence(snapshot, setting.SmartMinSamples)
	latencyMs := channelEffectiveLatencyMs(snapshot, channel.ResponseTime)
	speed := 0.5
	if latencyMs > 0 && bestLatencyMs > 0 {
		speed = bestLatencyMs / latencyMs
		if speed > 1 {
			speed = 1
		}
	}
	score := 0.45*health + 0.35*speed + 0.20*channelLoadFactor(GetChannelActiveCount(channel.Id))
	if snapshot.CoolingDown {
		score *= 0.1
	}
	return score
}

// selectSmart 智能路由选择：
//  1. 以探索率 ε 全量随机（冷启动采样）；
//  2. 否则按综合得分取前 TopN 组成第一梯队，在梯队内按 score² 加权随机——
//     软选择而非赢者通吃，单一渠道最多吃到梯队内的一部分流量。
func selectSmart(targetChannels []*Channel) *Channel {
	if len(targetChannels) == 1 {
		return targetChannels[0]
	}
	setting := operation_setting.GetLoadBalanceSetting()
	if setting.SmartExplorePercent > 0 && rand.Intn(100) < setting.SmartExplorePercent {
		return selectRandom(targetChannels)
	}

	now := time.Now()
	bestLatencyMs := 0.0
	snapshots := make(map[int]ChannelHealthSnapshot, len(targetChannels))
	for _, ch := range targetChannels {
		snapshot := GetChannelHealthSnapshot(ch.Id, now)
		snapshots[ch.Id] = snapshot
		// 冷却中的渠道不参与“最快”基准，避免它把其他渠道的速度分全部拉低
		if snapshot.CoolingDown {
			continue
		}
		if latencyMs := channelEffectiveLatencyMs(snapshot, ch.ResponseTime); latencyMs > 0 {
			if bestLatencyMs == 0 || latencyMs < bestLatencyMs {
				bestLatencyMs = latencyMs
			}
		}
	}

	type scoredChannel struct {
		channel *Channel
		score   float64
	}
	scored := make([]scoredChannel, 0, len(targetChannels))
	for _, ch := range targetChannels {
		scored = append(scored, scoredChannel{channel: ch, score: smartChannelScore(ch, snapshots[ch.Id], bestLatencyMs)})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })

	topN := min(setting.SmartTopN, len(scored))
	tier := scored[:topN]
	total := 0.0
	weights := make([]float64, topN)
	for i, sc := range tier {
		weights[i] = sc.score * sc.score
		total += weights[i]
	}
	if total <= 0 {
		return selectRandom(targetChannels)
	}
	pick := rand.Float64() * total
	for i, w := range weights {
		pick -= w
		if pick <= 0 {
			return tier[i].channel
		}
	}
	return tier[topN-1].channel
}
