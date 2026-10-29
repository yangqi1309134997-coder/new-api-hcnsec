package model

import (
	"math"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ============================================================================
// 渠道健康分（智能路由策略的数据源）
// ============================================================================
//
// 每次中继尝试结束后记录一次（controller/relay.go 的尝试结束钩子）：
//   - 成功：更新成败衰减计数 + 首字延迟（流式）或总耗时（非流式）EWMA
//   - 失败（仅可重试的渠道级失败才会计入）：成败计数 + 失败冷却
//
// 纯内存、无表结构，进程重启后自然冷启动（探索率 + 渠道测试延迟先验兜底）。
// 时间衰减在每次读写时按半衰期惰性结算，无需后台协程。
// ============================================================================

const channelHealthEWMAAlpha = 0.3

type channelHealthEntry struct {
	mu                    sync.Mutex
	succWeight            float64
	failWeight            float64
	ewmaTtftMs            float64
	ewmaLatencyMs         float64
	hasTtft               bool
	hasLatency            bool
	lastUpdateUnixNano    int64
	cooldownUntilUnixNano int64
}

var channelHealthStore sync.Map // map[int]*channelHealthEntry

// ChannelHealthSnapshot 某渠道健康数据的只读快照。
type ChannelHealthSnapshot struct {
	SuccessRate float64 // 衰减后成功率；无样本时为 0
	Samples     float64 // 衰减后样本数（succ+fail）
	TtftMs      float64 // 首字延迟 EWMA；未知为 0
	LatencyMs   float64 // 总耗时 EWMA；未知为 0
	CoolingDown bool    // 处于失败冷却期
}

func channelHealthHalfLife() time.Duration {
	minutes := operation_setting.GetLoadBalanceSetting().SmartLatencyHalfLifeMinutes
	return time.Duration(minutes * float64(time.Minute))
}

func channelHealthDecay(entry *channelHealthEntry, now time.Time) {
	lastNano := entry.lastUpdateUnixNano
	entry.lastUpdateUnixNano = now.UnixNano()
	if lastNano == 0 {
		return
	}
	halfLife := channelHealthHalfLife()
	if halfLife <= 0 {
		return
	}
	elapsed := now.UnixNano() - lastNano
	if elapsed <= 0 {
		return
	}
	factor := math.Pow(0.5, float64(elapsed)/float64(halfLife.Nanoseconds()))
	entry.succWeight *= factor
	entry.failWeight *= factor
}

// RecordChannelAttemptHealth 记录一次中继尝试结果。
// success=false 时仅累计失败计数并触发冷却，不污染延迟样本。
func RecordChannelAttemptHealth(channelId int, success bool, ttftMs int64, totalMs int64, now time.Time) {
	if channelId <= 0 {
		return
	}
	raw, _ := channelHealthStore.LoadOrStore(channelId, &channelHealthEntry{})
	entry := raw.(*channelHealthEntry)
	cooldown := time.Duration(operation_setting.GetLoadBalanceSetting().SmartFailCooldownSeconds) * time.Second

	entry.mu.Lock()
	defer entry.mu.Unlock()
	channelHealthDecay(entry, now)
	if success {
		entry.succWeight++
		if ttftMs > 0 {
			if entry.hasTtft {
				entry.ewmaTtftMs = (1-channelHealthEWMAAlpha)*entry.ewmaTtftMs + channelHealthEWMAAlpha*float64(ttftMs)
			} else {
				entry.ewmaTtftMs = float64(ttftMs)
				entry.hasTtft = true
			}
		}
		if ttftMs <= 0 && totalMs > 0 {
			if entry.hasLatency {
				entry.ewmaLatencyMs = (1-channelHealthEWMAAlpha)*entry.ewmaLatencyMs + channelHealthEWMAAlpha*float64(totalMs)
			} else {
				entry.ewmaLatencyMs = float64(totalMs)
				entry.hasLatency = true
			}
		}
	} else {
		entry.failWeight++
		if cooldown > 0 {
			entry.cooldownUntilUnixNano = now.Add(cooldown).UnixNano()
		}
	}
}

// GetChannelHealthSnapshot 读取某渠道的健康快照（读取时结算时间衰减）。
func GetChannelHealthSnapshot(channelId int, now time.Time) ChannelHealthSnapshot {
	raw, ok := channelHealthStore.Load(channelId)
	if !ok {
		return ChannelHealthSnapshot{}
	}
	entry := raw.(*channelHealthEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	channelHealthDecay(entry, now)
	snapshot := ChannelHealthSnapshot{
		Samples:   entry.succWeight + entry.failWeight,
		TtftMs:    entry.ewmaTtftMs,
		LatencyMs: entry.ewmaLatencyMs,
	}
	if entry.cooldownUntilUnixNano > now.UnixNano() {
		snapshot.CoolingDown = true
	}
	if snapshot.Samples > 0 {
		snapshot.SuccessRate = entry.succWeight / snapshot.Samples
	}
	return snapshot
}

// modelHealthStore 模型级健康记忆（key: group|model）。
// 渠道级样本只反映单渠道；auto 智能模型选型需要"这个模型整体好不好"的证据，
// 且必须覆盖不触发重试的失败（如挂起后 stop 的慢失败），因此独立记录。
// 数据源是请求结束处 perf_metrics 的同一套结果分类（成功/失败/忽略）。
var modelHealthStore sync.Map // map[string]*channelHealthEntry

// RecordModelAttemptHealth 记录一次请求级结果到 (group, model) 维度健康记忆。
func RecordModelAttemptHealth(group, modelName string, success bool, ttftMs int64, now time.Time) {
	if modelName == "" {
		return
	}
	raw, _ := modelHealthStore.LoadOrStore(group+"|"+modelName, &channelHealthEntry{})
	entry := raw.(*channelHealthEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	channelHealthDecay(entry, now)
	if success {
		entry.succWeight++
		if ttftMs > 0 {
			if entry.hasTtft {
				entry.ewmaTtftMs = (1-channelHealthEWMAAlpha)*entry.ewmaTtftMs + channelHealthEWMAAlpha*float64(ttftMs)
			} else {
				entry.ewmaTtftMs = float64(ttftMs)
				entry.hasTtft = true
			}
		}
	} else {
		entry.failWeight++
	}
}

// getModelHealthSnapshot 读取模型级健康快照；多个候选分组时取样本最多的一份。
func getModelHealthSnapshot(groups []string, modelName string, now time.Time) ChannelHealthSnapshot {
	var best ChannelHealthSnapshot
	for _, group := range groups {
		raw, ok := modelHealthStore.Load(group + "|" + modelName)
		if !ok {
			continue
		}
		entry := raw.(*channelHealthEntry)
		entry.mu.Lock()
		channelHealthDecay(entry, now)
		snapshot := ChannelHealthSnapshot{
			Samples:     entry.succWeight + entry.failWeight,
			TtftMs:      entry.ewmaTtftMs,
			SuccessRate: entry.succWeight,
		}
		entry.mu.Unlock()
		if snapshot.Samples > 0 {
			snapshot.SuccessRate = snapshot.SuccessRate / snapshot.Samples
		}
		if snapshot.Samples >= best.Samples {
			best = snapshot
		}
	}
	return best
}

// channelHealthConfidence 置信门控：样本少时把成功率向中性 0.5 收缩。
func channelHealthConfidence(snapshot ChannelHealthSnapshot, minSamples int) float64 {
	if snapshot.Samples <= 0 {
		return 0.5
	}
	confidence := snapshot.Samples / (snapshot.Samples + float64(minSamples))
	return 0.5 + (snapshot.SuccessRate-0.5)*confidence
}

// channelEffectiveLatencyMs 智能路由使用的延迟信号：
// 流式首字延迟优先，其次非流式总耗时，最后渠道测试延迟先验。均未知返回 0。
func channelEffectiveLatencyMs(snapshot ChannelHealthSnapshot, priorResponseTimeMs int) float64 {
	if snapshot.TtftMs > 0 {
		return snapshot.TtftMs
	}
	if snapshot.LatencyMs > 0 {
		return snapshot.LatencyMs
	}
	return float64(priorResponseTimeMs)
}

// channelLoadFactor 负载分：活跃请求越多分数越低，k 来自设置。
func channelLoadFactor(activeCount int64) float64 {
	k := operation_setting.GetLoadBalanceSetting().SmartLoadPenalty
	return 1.0 / (1.0 + k*float64(activeCount))
}

// SmartModelScores 计算候选池内每个模型在目标分组下的综合路由分（0~1）。
//   - health：模型级健康记忆（含不触发重试的慢失败），置信门控
//   - speed ：模型级首字延迟 EWMA 的相对速度（池内最快=1.0），无数据时
//     退回渠道测试延迟先验；速度锚点取池内最优而非固定值，自适应校准
//   - load  ：该模型各候选渠道活跃请求数之和
//
// 近期样本足够且成功率 <25% 的模型得 0 分（踢出候选，含探索路径）；
// 随样本按半衰期衰减，退出封锁后可自然回流。
func SmartModelScores(groups []string, models []string) map[string]float64 {
	scores := make(map[string]float64, len(models))
	now := time.Now()
	setting := operation_setting.GetLoadBalanceSetting()

	type modelEvidence struct {
		activeSum int64
		present   bool
	}
	evidence := make(map[string]*modelEvidence, len(models))

	channelSyncLock.RLock()
	for _, modelName := range models {
		e := &modelEvidence{}
		for _, group := range groups {
			for _, channelId := range group2model2channels[group][modelName] {
				channel, ok := channelsIDM[channelId]
				if !ok || channel.Status != common.ChannelStatusEnabled {
					continue
				}
				e.present = true
				e.activeSum += GetChannelActiveCount(channelId)
			}
		}
		if e.present {
			evidence[modelName] = e
		}
	}
	channelSyncLock.RUnlock()

	const neutralSpeed = 0.5
	healths := make(map[string]float64, len(models))
	speeds := make(map[string]float64, len(models))
	minTtftMs := 0.0
	for _, modelName := range models {
		if _, ok := evidence[modelName]; !ok {
			continue
		}
		snapshot := getModelHealthSnapshot(groups, modelName, now)
		healths[modelName] = channelHealthConfidence(snapshot, setting.SmartMinSamples)
		// 速度只在样本量达到置信阈值时采信（防个别快请求登顶）；
		// 无采信数据的模型用中性速度，靠健康度与探索路径积累样本
		if snapshot.Samples >= float64(setting.SmartMinSamples) && snapshot.TtftMs > 0 {
			speeds[modelName] = snapshot.TtftMs
			if minTtftMs == 0 || snapshot.TtftMs < minTtftMs {
				minTtftMs = snapshot.TtftMs
			}
		} else {
			speeds[modelName] = 0
		}
	}
	for _, modelName := range models {
		e, ok := evidence[modelName]
		if !ok {
			continue
		}
		speed := neutralSpeed
		if ms := speeds[modelName]; ms > 0 && minTtftMs > 0 {
			speed = minTtftMs / ms
			if speed > 1 {
				speed = 1
			}
		}
		score := 0.5*healths[modelName] + 0.35*speed + 0.15*channelLoadFactor(e.activeSum)
		snapshot := getModelHealthSnapshot(groups, modelName, now)
		if snapshot.Samples >= float64(setting.SmartMinSamples) && snapshot.SuccessRate < 0.25 {
			score = 0
		}
		if score > 0 {
			scores[modelName] = score
		}
	}
	return scores
}
