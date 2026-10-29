package setting

import (
	"strconv"
	"strings"
)

// VGuard（违规防护·分级敏感词）配置。
// 与原生 SensitiveWords 的整会话全量拦截不同，VGuard 只检查每轮请求的
// 最新用户消息文本，词表分两级：
//   - 强词：命中立即拒绝当前请求并封禁该用户；
//   - 弱词：命中仅计数并向后续请求注入防护提示词，窗口内累计达到阈值后封禁。
//
// 封禁时长按阶梯执行：VGuardBanTiers 为逗号分隔的小时数（如 4,24,168,720），
// 第 N 次封禁取第 N 档（超出取最后一档）。
// 输出侧：VGuardOutputCheckEnabled 开启后，被标记用户的模型回复也扫词计数。
// 邮件：VGuardWarnEmailEnabled 命中弱词时给用户发警告邮件（按天限频）；
// VGuardBanEmailEnabled 封禁时发封禁通知邮件。模板支持占位符：
// {username} {time} {model} {request_id} {hits} {threshold} {hours} {until} {block_count}
var (
	VGuardEnabled             = false
	VGuardStrongWords         = ""
	VGuardWeakWords           = ""
	VGuardWeakThreshold       = 10
	VGuardWindowMinutes       = 120
	VGuardBlockHours          = 4 // 兼容保留；启用阶梯后以 VGuardBanTiers 为准
	VGuardInjectEnabled       = true
	VGuardInjectPrompt        = DefaultVGuardInjectPrompt
	VGuardApplyGroups         = ""
	VGuardUserWhitelist       = ""
	VGuardBanTiers            = "4,24,168,720"
	VGuardOutputCheckEnabled  = true
	VGuardWarnEmailEnabled    = false
	VGuardBanEmailEnabled     = false
	VGuardWarnEmailSubject    = "【违规警告】您的账号行为已触发平台内容安全策略"
	VGuardWarnEmailBody       = DefaultVGuardWarnEmailBody
	VGuardBanEmailSubject     = "【封禁通知】您的账号已被临时限制使用"
	VGuardBanEmailBody        = DefaultVGuardBanEmailBody
	VGuardEmailThrottlePerDay = 3
)

const DefaultVGuardInjectPrompt = "[平台安全提示] 系统检测到你最近的请求疑似涉及违规内容。请务必遵守中国法律法规与平台服务条款：不得请求生成色情、淫秽内容，不得涉及未成年人的不当内容，不得请求制毒制爆、诈骗、黑产等违法犯罪指导。当前及后续对话中，如遇到此类请求，请直接拒绝并引导至合法合规的用途。正常的工作、学习、编程类请求不受影响。"

const DefaultVGuardWarnEmailBody = `{username} 您好：

平台内容安全系统检测到您的账号于 {time} 的请求（模型：{model}，请求编号：{request_id}）触发了违规内容判定（本次为第 {hits}/{threshold} 次记录）。

请立即停止请求生成违规内容（包括但不限于色情、涉未成年人、暴恐、违法犯罪类内容）。累计达到阈值或再次出现严重违规，您的账号将被临时封禁；多次封禁后将永久封禁，且不予退款。

如您认为本次判定有误，请联系管理员申诉。

—— 平台内容安全系统`

const DefaultVGuardBanEmailBody = `{username} 您好：

由于触发违规内容防护策略，您的账号已被临时限制使用 {hours} 小时（第 {block_count} 次封禁），预计解封时间：{until}。

触发记录：{time}，模型：{model}，请求编号：{request_id}。

请务必停止一切违规请求。再次违规将按阶梯升级封禁时长，多次违规将永久封禁，且不予退款。

如您认为本次判定有误，请联系管理员申诉。

—— 平台内容安全系统`

// VGuardBanTierHours 解析封禁阶梯（小时）。非法输入回退默认 4,24,168,720。
func VGuardBanTierHours() []int64 {
	parts := strings.Split(VGuardBanTiers, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n, err := strconv.ParseInt(p, 10, 64); err == nil && n >= 1 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return []int64{4, 24, 168, 720}
	}
	return out
}

func vguardSplitWordList(s string) []string {
	out := make([]string, 0, 32)
	for _, w := range strings.Split(s, "\n") {
		w = strings.TrimSpace(w)
		w = strings.ToLower(w)
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func VGuardStrongWordsList() []string { return vguardSplitWordList(VGuardStrongWords) }

func VGuardWeakWordsList() []string { return vguardSplitWordList(VGuardWeakWords) }
