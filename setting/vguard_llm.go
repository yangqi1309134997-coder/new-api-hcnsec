package setting

// ============================================================
// VGuard LLM 内容安全审核配置
// 支持两类模型：
//
//	safety   内容安全模型（如 nvidia/nemotron-3.5-content-safety），
//	         直接把用户消息发给模型，按 unsafe/safe 关键词判定；
//	decision 决策模型：按提示词模板让模型判定，输出 unsafe/safe。
//
// 触发策略 VGuardLLMTrigger：
//
//	off        不触发（仅词表）
//	weak_word  命中弱词(前置词)后交给模型复核
//	always     所有请求都过模型
//
// 联动：VGuardLLMSustainHours>0 时，用户命中弱词或被判 unsafe 后，
//
//	接下来 N 小时内其所有请求都过模型（持续观察）。
//
// 判 unsafe 后动作 VGuardLLMAction：block 拦截当前请求；record 只记录。
// 计数：模型判 unsafe 与弱词共用同一累计器与阈值封禁（阶梯生效），
//
//	事件类型 kind=llm 记入 v_guard_events，随每日摘要/导出统计。
//
// 失败策略 VGuardLLMFailOpen：模型超时/异常时放行（默认）或拦截。
// ============================================================
var (
	VGuardLLMEnabled        = false
	VGuardLLMProtocol       = "chat"   // chat | systemone（决策模型结构化判定接口）
	VGuardLLMMode           = "safety" // safety | decision（chat 协议下的两种用法）
	VGuardLLMBaseURL        = ""       // 如 http://127.0.0.1:3000
	VGuardLLMKey            = ""
	VGuardLLMModel          = ""
	VGuardLLMPromptTemplate = ""          // decision 模式提示词模板，支持 {text}
	VGuardLLMTrigger        = "weak_word" // off | weak_word | always
	VGuardLLMAction         = "block"     // record | block（默认拦截当前请求，计数照走）
	VGuardLLMSustainHours   = 3
	VGuardLLMTimeoutSec     = 30
	VGuardLLMMaxTextChars   = 60000
	VGuardLLMUnsafePattern  = `(?i)\bunsafe\b`
	VGuardLLMSafePattern    = `(?i)\bsafe\b`
	VGuardLLMFailOpen       = true
	VGuardLLMAsyncEnabled   = true // 慢尾转异步补判总开关
	VGuardLLMFastSeconds    = 10   // 同步等待窗口：N 秒内出判定当场处理
	VGuardLLMAsyncSeconds   = 300  // 异步总预算（含同步窗口）
	// 上游保护：审核模型与正常聊天共享上游按模型全局并发池，
	// 并发闸限制同时在途送审数（含重试）；失败在预算内退避重试。
	VGuardLLMMaxConcurrent    = 3   // 全局送审并发上限；0=不限
	VGuardLLMErrorCooldownSec = 300 // llm_error 事件同标签限噪窗口（秒）；0=不限噪
	// 审核模型池（负载均衡）：每行"模型名[,协议[,模式]]"，# 后为注释，
	// 协议缺省跟随 VGuardLLMProtocol、模式缺省跟随 VGuardLLMMode；
	// 留空退回单模型 VGuardLLMModel。按实时成功率/延迟加权选择，
	// 连续失败自动熔断切换、恢复后半开探测。
	VGuardLLMModels = "nvidia/nemotron-3.5-content-safety,chat,safety\n" +
		"d3-flash,systemone\n" +
		"Clef,systemone\n" +
		"d3,systemone"
	// 连续安全递归出窗：观察窗内连续 VGuardLLMSafeExitStreak 次判安全 →
	// 剩余观察时间减半，低于 VGuardLLMSafeExitFloorMin 分钟直接出窗；
	// Streak<=0 关闭递归出窗。
	VGuardLLMSafeExitStreak   = 3
	VGuardLLMSafeExitFloorMin = 5
	// 弱词命中即重新进观察窗（窗口拉满、安全连击清零）。
	VGuardLLMWeakReenter = true
	// systemone 协议（决策模型）问题文案：state.message 放被审文本，
	// verdict 用 choice(safe/unsafe) 出判定，flag 用 noul 做同问双保险。
	VGuardLLMSoInstructions    = DefaultVGuardLLMSoInstructions
	VGuardLLMSoUnsafeCriteria  = DefaultVGuardLLMSoUnsafeCriteria
	VGuardLLMSoSafeCriteria    = DefaultVGuardLLMSoSafeCriteria
	VGuardLLMSoNoulInstruction = DefaultVGuardLLMSoNoulInstruction
)

// DefaultVGuardLLMPrompt decision 模式默认提示词。
const DefaultVGuardLLMPrompt = `你是内容安全审核员。请判断下面用户消息是否违反内容安全规范（包括但不限于：色情、涉未成年人不当内容、暴恐、制毒制爆、诈骗黑产等）。只需输出一个词：unsafe 或 safe。

用户消息：
{text}`

// systemone 协议默认问题文案（实测对正常/弱词无害/色情/制毒/诈骗/赌博文本判定全对）。
const (
	DefaultVGuardLLMSoInstructions    = `这条用户消息是否违反内容安全规范（色情、涉未成年不当内容、暴恐、制毒制爆、诈骗黑产等）？`
	DefaultVGuardLLMSoUnsafeCriteria  = `消息包含色情露骨内容、制毒制爆、诈骗黑产、暴恐等违规内容。`
	DefaultVGuardLLMSoSafeCriteria    = `消息是正常无害的日常或教育类内容。`
	DefaultVGuardLLMSoNoulInstruction = `这条用户消息是否违反内容安全规范（色情、暴恐、制毒、诈骗等）？`
)
