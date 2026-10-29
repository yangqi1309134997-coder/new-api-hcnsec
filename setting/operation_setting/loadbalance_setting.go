package operation_setting

import (
	"github.com/QuantumNous/new-api/setting/config"
)

// LoadBalanceSetting 渠道负载均衡的可调参数（智能路由策略使用）。
// 通过 GlobalConfig 注册，前台控制台保存后热生效。
// ByGroup 为按分组覆盖的策略表（分组名 -> 策略名），
// 未配置的分组回退到全局 ChannelLoadBalanceStrategy 选项。
type LoadBalanceSetting struct {
	ByGroup map[string]string `json:"by_group"`
	// SmartTopN 智能路由第一梯队大小：只在得分前 N 名内按分数加权随机，
	// 避免赢者通吃把流量集中到单一渠道。
	SmartTopN int `json:"smart_top_n"`
	// SmartExplorePercent 探索率（百分比）：以此概率忽略得分全量随机，
	// 保证无数据/新渠道能被采样。
	SmartExplorePercent int `json:"smart_explore_percent"`
	// SmartFailCooldownSeconds 失败冷却：渠道失败后在该秒数内得分大幅降权。
	SmartFailCooldownSeconds int `json:"smart_fail_cooldown_seconds"`
	// SmartMinSamples 置信门控：近期样本不足该数时，健康度向中性 0.5 收缩。
	SmartMinSamples int `json:"smart_min_samples"`
	// SmartLoadPenalty 负载惩罚系数 k：负载分 = 1/(1+k*活跃请求数)。
	SmartLoadPenalty float64 `json:"smart_load_penalty"`
	// SmartLatencyHalfLifeMinutes 延迟/成败计数的指数衰减半衰期（分钟）。
	SmartLatencyHalfLifeMinutes float64 `json:"smart_latency_half_life_minutes"`
}

const (
	defaultSmartTopN                = 12
	defaultSmartExplorePercent      = 5
	defaultSmartFailCooldownSeconds = 45
	defaultSmartMinSamples          = 10
	defaultSmartLoadPenalty         = 1.0
	defaultSmartHalfLifeMinutes     = 10
)

var loadBalanceSetting = LoadBalanceSetting{
	ByGroup:                     map[string]string{"default": "smart"},
	SmartTopN:                   defaultSmartTopN,
	SmartExplorePercent:         defaultSmartExplorePercent,
	SmartFailCooldownSeconds:    defaultSmartFailCooldownSeconds,
	SmartMinSamples:             defaultSmartMinSamples,
	SmartLoadPenalty:            defaultSmartLoadPenalty,
	SmartLatencyHalfLifeMinutes: defaultSmartHalfLifeMinutes,
}

func init() {
	config.GlobalConfig.Register("loadbalance_setting", &loadBalanceSetting)
}

// GetLoadBalanceSetting 返回归一化后的负载均衡设置。
func GetLoadBalanceSetting() *LoadBalanceSetting {
	if loadBalanceSetting.ByGroup == nil {
		loadBalanceSetting.ByGroup = map[string]string{}
	}
	loadBalanceSetting.SmartTopN = clampInt(loadBalanceSetting.SmartTopN, 1, 64, defaultSmartTopN)
	loadBalanceSetting.SmartExplorePercent = clampInt(loadBalanceSetting.SmartExplorePercent, 0, 50, defaultSmartExplorePercent)
	loadBalanceSetting.SmartFailCooldownSeconds = clampInt(loadBalanceSetting.SmartFailCooldownSeconds, 0, 600, defaultSmartFailCooldownSeconds)
	loadBalanceSetting.SmartMinSamples = clampInt(loadBalanceSetting.SmartMinSamples, 1, 1000, defaultSmartMinSamples)
	if loadBalanceSetting.SmartLoadPenalty < 0 || loadBalanceSetting.SmartLoadPenalty > 100 {
		loadBalanceSetting.SmartLoadPenalty = defaultSmartLoadPenalty
	}
	if loadBalanceSetting.SmartLatencyHalfLifeMinutes < 0.5 || loadBalanceSetting.SmartLatencyHalfLifeMinutes > 1440 {
		loadBalanceSetting.SmartLatencyHalfLifeMinutes = defaultSmartHalfLifeMinutes
	}
	return &loadBalanceSetting
}

func clampInt(value, min, max, fallback int) int {
	if value < min || value > max {
		return fallback
	}
	return value
}
