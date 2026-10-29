package operation_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
)

// AutoSmartModelName 触发智能模型路由的请求模型名。
const AutoSmartModelName = "auto"

// AutoModelSetting auto 智能模型配置。
// 开启后，请求模型名为 auto 时不再走渠道静态映射，而是在候选池内按
// 渠道健康分动态选出真实模型并把请求改写为该模型。
// 计费固定按 auto 本身的倍率（BillingModelName 保持 auto），用户价格无感。
type AutoModelSetting struct {
	Enabled bool `json:"enabled"`
	// Pool 候选模型白名单。只放对话类模型；不在任何目标分组中的模型会被忽略。
	Pool []string `json:"pool"`
}

var autoModelSetting = AutoModelSetting{
	Enabled: true,
	Pool: []string{
		"DeepSeek-V4-Flash",
		"DeepSeek-V4-Pro",
		"DeepSeek-V4.1-Flash",
		"glm-5.3",
		"glm-5.3-flash",
		"MiMo-V2.6-Flash",
		"MiniCPM-V-4.7-35B-A3B",
		"MiniMax-M3.1-Flash",
		"Qwen3.8-27B",
		"Qwen3.8-Flash-Next",
		"sensenova-6.8-flash-lite",
		"sensenova-u1.5-lite",
		"step-5-preview",
	},
}

func init() {
	config.GlobalConfig.Register("auto_model_setting", &autoModelSetting)
}

// GetAutoModelSetting 返回归一化后的 auto 智能模型配置。
func GetAutoModelSetting() *AutoModelSetting {
	pool := make([]string, 0, len(autoModelSetting.Pool))
	seen := make(map[string]bool, len(autoModelSetting.Pool))
	for _, model := range autoModelSetting.Pool {
		model = strings.TrimSpace(model)
		if model == "" || model == AutoSmartModelName || seen[model] {
			continue
		}
		seen[model] = true
		pool = append(pool, model)
	}
	autoModelSetting.Pool = pool
	return &autoModelSetting
}
