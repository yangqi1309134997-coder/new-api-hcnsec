package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withLoadBalanceSettings 临时改写智能路由设置并在测试结束后还原。
func withLoadBalanceSettings(t *testing.T, mutate func(*operation_setting.LoadBalanceSetting)) {
	t.Helper()
	setting := operation_setting.GetLoadBalanceSetting()
	previous := *setting
	previousByGroup := make(map[string]string, len(setting.ByGroup))
	for key, value := range setting.ByGroup {
		previousByGroup[key] = value
	}
	mutate(setting)
	t.Cleanup(func() {
		*setting = previous
		setting.ByGroup = previousByGroup
	})
}

func TestStrategyForGroup(t *testing.T) {
	withLoadBalanceSettings(t, func(setting *operation_setting.LoadBalanceSetting) {
		setting.ByGroup = map[string]string{
			"svip": "weighted-random",
			"bad":  "nope",
			"dft":  "smart",
		}
	})
	SetLoadBalanceStrategy("least-connections")
	t.Cleanup(func() { SetLoadBalanceStrategy("weighted-random") })

	assert.Equal(t, StrategySmart, StrategyForGroup("dft"), "按分组覆盖生效")
	assert.Equal(t, StrategyWeightedRandom, StrategyForGroup("svip"))
	assert.Equal(t, StrategyLeastConnections, StrategyForGroup("unset"), "未覆盖分组回退全局")
	assert.Equal(t, StrategyLeastConnections, StrategyForGroup("bad"), "非法覆盖值回退全局")
	assert.Equal(t, StrategyLeastConnections, StrategyForGroup(""))
	assert.True(t, IsValidLoadBalanceStrategy("smart"))
	assert.False(t, IsValidLoadBalanceStrategy("nope"))
}

func TestRecordChannelAttemptHealth(t *testing.T) {
	withLoadBalanceSettings(t, func(setting *operation_setting.LoadBalanceSetting) {
		setting.SmartFailCooldownSeconds = 45
		setting.SmartLatencyHalfLifeMinutes = 10
		setting.SmartMinSamples = 10
	})
	channelId := 990001
	t.Cleanup(func() { channelHealthStore.Delete(channelId) })
	base := time.Unix(1800000000, 0)

	RecordChannelAttemptHealth(channelId, true, 500, 5000, base)
	snapshot := GetChannelHealthSnapshot(channelId, base)
	assert.Equal(t, 1.0, snapshot.Samples)
	assert.Equal(t, 1.0, snapshot.SuccessRate)
	assert.Equal(t, 500.0, snapshot.TtftMs)
	assert.False(t, snapshot.CoolingDown)

	// EMA：0.7*500 + 0.3*600 = 530
	RecordChannelAttemptHealth(channelId, true, 600, 0, base.Add(2*time.Second))
	snapshot = GetChannelHealthSnapshot(channelId, base.Add(2*time.Second))
	assert.InDelta(t, 530.0, snapshot.TtftMs, 1e-9)

	// 失败计入成败并触发冷却，但不污染延迟样本
	RecordChannelAttemptHealth(channelId, false, 0, 0, base.Add(3*time.Second))
	snapshot = GetChannelHealthSnapshot(channelId, base.Add(3*time.Second))
	assert.InDelta(t, 3.0, snapshot.Samples, 0.01)
	assert.InDelta(t, 2.0/3.0, snapshot.SuccessRate, 0.01)
	assert.True(t, snapshot.CoolingDown)
	assert.InDelta(t, 530.0, snapshot.TtftMs, 0.01)

	// 冷却到期后恢复（失败在 +3s，冷却 45s，+49s 已过期）
	assert.False(t, GetChannelHealthSnapshot(channelId, base.Add(49*time.Second)).CoolingDown)

	// 半衰期衰减：10 分钟后样本减半
	assert.InDelta(t, 1.5, GetChannelHealthSnapshot(channelId, base.Add(3*time.Second+10*time.Minute)).Samples, 0.01)
}

func TestChannelHealthConfidence(t *testing.T) {
	// 样本不足时健康度向中性 0.5 收缩（1 个失败样本：0.5 + (0-0.5)*1/11）
	snapshot := ChannelHealthSnapshot{SuccessRate: 0, Samples: 1}
	assert.InDelta(t, 0.454545, channelHealthConfidence(snapshot, 10), 1e-5)
	snapshot = ChannelHealthSnapshot{SuccessRate: 0, Samples: 90}
	assert.InDelta(t, 0.05, channelHealthConfidence(snapshot, 10), 1e-9)
	assert.InDelta(t, 0.5, channelHealthConfidence(ChannelHealthSnapshot{}, 10), 1e-9)
}

func TestSelectSmartPrefersHealthyFastChannel(t *testing.T) {
	withLoadBalanceSettings(t, func(setting *operation_setting.LoadBalanceSetting) {
		setting.SmartExplorePercent = 0
		setting.SmartTopN = 1 // 只返回排名第一的渠道，排序断言确定化
		setting.SmartMinSamples = 1
		setting.SmartLoadPenalty = 0
		setting.SmartFailCooldownSeconds = 45
		setting.SmartLatencyHalfLifeMinutes = 10
	})
	now := time.Now()
	fast := &Channel{Id: 101, ResponseTime: 800}
	slow := &Channel{Id: 102, ResponseTime: 8000}
	cooling := &Channel{Id: 103, ResponseTime: 9000}
	t.Cleanup(func() { channelHealthStore.Delete(103) })

	RecordChannelAttemptHealth(103, false, 0, 0, now)
	for range 20 {
		picked := selectSmart([]*Channel{fast, slow, cooling})
		require.NotNil(t, picked)
		assert.Equal(t, 101, picked.Id)
	}
}

func TestSmartModelScores(t *testing.T) {
	withLoadBalanceSettings(t, func(setting *operation_setting.LoadBalanceSetting) {
		setting.SmartMinSamples = 10
		setting.SmartLoadPenalty = 0
		setting.SmartFailCooldownSeconds = 45
	})
	previousGroups, previousIDM := group2model2channels, channelsIDM
	group2model2channels = map[string]map[string][]int{
		"g1": {"mA": {1, 2}, "mB": {3}, "mC": {4}},
	}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Status: common.ChannelStatusEnabled, ResponseTime: 800},
		2: {Id: 2, Status: common.ChannelStatusEnabled, ResponseTime: 9000},
		3: {Id: 3, Status: common.ChannelStatusEnabled, ResponseTime: 500},
		4: {Id: 4, Status: common.ChannelStatusEnabled, ResponseTime: 100},
	}
	t.Cleanup(func() {
		group2model2channels = previousGroups
		channelsIDM = previousIDM
		modelHealthStore.Delete("g1|mA")
		modelHealthStore.Delete("g1|mB")
		modelHealthStore.Delete("g1|mC")
	})
	now := time.Now()
	// mA：健康且快（TTFT 800ms，证据充足）
	for i := range 30 {
		RecordModelAttemptHealth("g1", "mA", true, 800, now.Add(time.Duration(i)*time.Second))
	}
	// mB：渠道测试延迟很快，但模型级证据是持续失败 → 应被压过甚至踢出
	RecordModelAttemptHealth("g1", "mB", false, 0, now)
	RecordModelAttemptHealth("g1", "mB", false, 0, now.Add(time.Second))
	RecordModelAttemptHealth("g1", "mB", true, 100, now.Add(2*time.Second))

	scores := SmartModelScores([]string{"g1"}, []string{"mA", "mB", "mC", "mZ"})
	// mA 证据充足且被采信为池内最快 → 居首；mB 样本不足速度不采信；mC 冷模型中性分
	assert.Greater(t, scores["mA"], scores["mB"], "模型级健康证据决定排序")
	assert.Less(t, scores["mB"], scores["mC"], "低成功率模型即使样本不足也不应高于冷模型的中性分")
	assert.Contains(t, scores, "mC", "冷模型保留中性分以获得探索机会")
	assert.NotContains(t, scores, "mZ", "分组中不存在的模型无得分")
	assert.Empty(t, SmartModelScores(nil, []string{"mA"}), "未初始化缓存时返回空表")
}

func TestSmartModelScoresVetoAndRecovery(t *testing.T) {
	withLoadBalanceSettings(t, func(setting *operation_setting.LoadBalanceSetting) {
		setting.SmartMinSamples = 10
		setting.SmartLoadPenalty = 0
		setting.SmartLatencyHalfLifeMinutes = 10
	})
	previousGroups, previousIDM := group2model2channels, channelsIDM
	group2model2channels = map[string]map[string][]int{"g1": {"mBad": {1}}}
	channelsIDM = map[int]*Channel{1: {Id: 1, Status: common.ChannelStatusEnabled, ResponseTime: 500}}
	t.Cleanup(func() {
		group2model2channels = previousGroups
		channelsIDM = previousIDM
		modelHealthStore.Delete("g1|mBad")
	})
	now := time.Now()
	for i := range 20 {
		RecordModelAttemptHealth("g1", "mBad", false, 0, now.Add(time.Duration(i)*time.Second))
	}
	scores := SmartModelScores([]string{"g1"}, []string{"mBad"})
	assert.NotContains(t, scores, "mBad", "成功率<25%且样本充足的模型被踢出候选")

	// 样本按半衰期衰减到阈值以下后，封锁解除（可重新参与排序/回流）
	afterDecay := now.Add(4 * time.Hour) // 20 个失败样本衰减为 20*(1/2^24) ≈ 0
	RecordModelAttemptHealth("g1", "mBad", true, 500, afterDecay)
	scores = SmartModelScores([]string{"g1"}, []string{"mBad"})
	assert.Contains(t, scores, "mBad", "样本衰减后封锁解除")
}

func TestGetRandomSatisfiedChannelSmartIgnoresPriority(t *testing.T) {
	withLoadBalanceSettings(t, func(setting *operation_setting.LoadBalanceSetting) {
		setting.ByGroup = map[string]string{"g1": "smart"}
		setting.SmartExplorePercent = 0
		setting.SmartTopN = 12
		setting.SmartMinSamples = 1
		setting.SmartLoadPenalty = 0
	})
	lo, hi := int64(0), int64(10)
	previousGroups, previousIDM := group2model2channels, channelsIDM
	previousCacheEnabled := common.MemoryCacheEnabled
	group2model2channels = map[string]map[string][]int{"g1": {"m1": {1, 2}}}
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Status: common.ChannelStatusEnabled, Priority: &lo, ResponseTime: 3000},
		2: {Id: 2, Status: common.ChannelStatusEnabled, Priority: &hi, ResponseTime: 9000},
	}
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		group2model2channels = previousGroups
		channelsIDM = previousIDM
		common.MemoryCacheEnabled = previousCacheEnabled
		channelHealthStore.Delete(1)
		channelHealthStore.Delete(2)
	})

	picked, err := GetRandomSatisfiedChannelEx("g1", "m1", 0, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, picked)
	assert.Contains(t, []int{1, 2}, picked.Id)

	// 智能路由无视优先级：低优先级但更快的渠道也会被选中（加权随机只会选高优先级层）
	RecordChannelAttemptHealth(1, true, 3000, 0, time.Now())
	pickedLowPriority := false
	pickedHighPriority := false
	for range 200 {
		picked, err = GetRandomSatisfiedChannelEx("g1", "m1", 0, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, picked)
		if picked.Id == 1 {
			pickedLowPriority = true
		} else if picked.Id == 2 {
			pickedHighPriority = true
		}
	}
	assert.True(t, pickedLowPriority, "更快但低优先级的渠道应被选中")
	assert.True(t, pickedHighPriority, "软选择下冷渠道保留流量份额")

	// 重试排除：排除全部候选后无渠道可用，排除其一后只返回另一个
	picked, err = GetRandomSatisfiedChannelEx("g1", "m1", 1, nil, map[int]bool{1: true, 2: true})
	require.NoError(t, err)
	assert.Nil(t, picked)
	picked, err = GetRandomSatisfiedChannelEx("g1", "m1", 1, nil, map[int]bool{1: true})
	require.NoError(t, err)
	require.NotNil(t, picked)
	assert.Equal(t, 2, picked.Id)
}
