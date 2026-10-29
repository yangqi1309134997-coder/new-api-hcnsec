package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// ============================================================
// VGuard × LLM 内容安全审核（快慢双路 + 判定后计数 + 模型池负载均衡）
//   - 模型池 VGuardLLMModels：多模型按实时健康度（成功率/延迟）加权
//     选择，失败立即故障转移，连续失败熔断冷却、恢复后半开探测
//   - 协议 VGuardLLMProtocol：chat（OpenAI 兼容）/ systemone（决策模型
//     结构化判定，choice/noul 出概率判决，无生成 token）
//   - safety 模式：内容安全模型直接审文本；decision：按提示词判定
//   - 触发：off / weak_word(前置词命中复核) / always(全量)
//   - 持续观察：命中弱词或判 unsafe 后 N 小时内该用户全量送审；
//     弱词命中即重新进窗（VGuardLLMWeakReenter）
//   - 快路：VGuardLLMFastSeconds 内出判定（约96%的调用）→ 当场
//     block / 计数；慢路（异步补判）：超过同步窗口 → 用户请求立即
//     放行，同一调用转后台等 VGuardLLMAsyncSeconds，判定后照常
//     写事件、计数、封禁（下一跳生效）
//   - 计数语义：模型会复核的消息，弱词计数推迟到判定之后——
//     判 SAFE = 无罪不计入；判 UNSAFE = 计 1 次（不与弱词重复计）；
//     模型异常/超时 = 只延长观察窗口，不计弱词不封禁（上游拥堵
//     不转化为对用户的处罚，2026-10-26 风暴中旧回退曾误封用户）
//   - 递归出窗：窗口内连续 VGuardLLMSafeExitStreak 次判安全 → 剩余
//     观察时间减半，低于 VGuardLLMSafeExitFloorMin 分钟直接出窗
//   - 上游保护：全局送审并发闸 VGuardLLMMaxConcurrent（含重试）+
//     失败退避重试（带抖动）；llm_error 事件按 VGuardLLMErrorCooldownSec
//     同标签限噪合并
//   - VGuardLLMTimeoutSec：异步补判关闭时使用的同步总超时
// ============================================================

type llmVerdict int

const (
	llmVerdictSafe llmVerdict = iota
	llmVerdictUnsafe
	llmVerdictUnknown
)

func vguardLLMReady() bool {
	return setting.VGuardEnabled && setting.VGuardLLMEnabled &&
		strings.TrimSpace(setting.VGuardLLMBaseURL) != "" &&
		strings.TrimSpace(setting.VGuardLLMModel) != ""
}

func llmCompilePatterns() (*regexp.Regexp, *regexp.Regexp) {
	// 模式可被管理员热更新：每次调用重新编译（仅触发的请求才会走到这里，开销可忽略）
	unsafeRe, err1 := regexp.Compile(setting.VGuardLLMUnsafePattern)
	safeRe, err2 := regexp.Compile(setting.VGuardLLMSafePattern)
	if err1 != nil {
		unsafeRe = regexp.MustCompile(`(?i)\bunsafe\b`)
	}
	if err2 != nil {
		safeRe = regexp.MustCompile(`(?i)\bsafe\b`)
	}
	return unsafeRe, safeRe
}

// vguardLLMWillReview 当前这条消息是否会被模型复核（VGuardGate 与
// vguardMaybeLLM 共用同一判定，保证"推迟计数"与"实际送审"完全一致）。
func vguardLLMWillReview(st *VGuardUserState, weakHit bool) bool {
	if !vguardLLMReady() {
		return false
	}
	trigger := setting.VGuardLLMTrigger
	if trigger == "off" {
		return false
	}
	now := time.Now().Unix()
	st.mu.Lock()
	sustained := st.llmUntil > now
	st.mu.Unlock()
	return trigger == "always" || (weakHit && trigger == "weak_word") || sustained
}

// vguardExtendLLMWindow 延长持续观察窗口。
func vguardExtendLLMWindow(st *VGuardUserState, now int64) {
	if setting.VGuardLLMSustainHours <= 0 {
		return
	}
	st.mu.Lock()
	if until := now + int64(setting.VGuardLLMSustainHours)*3600; until > st.llmUntil {
		st.llmUntil = until
	}
	st.mu.Unlock()
}

// ============================================================
// 审核模型池（负载均衡）：多模型按实时健康度（滑动窗口成功率/延迟）
// 加权选择；失败立即切换下一个健康模型（故障转移），连续失败熔断
// 冷却（60s 起步翻倍，封顶 16 分钟），冷却到期后半开探测。
// 配置 VGuardLLMModels 每行一个：模型名[,协议[,模式]]，协议缺省跟随
// 全局「接口格式」，模式缺省跟随全局「判定方式」；留空退回单模型。
// ============================================================

type vguardModelEntry struct {
	Model    string
	Protocol string // chat | systemone
	Mode     string // safety | decision（仅 chat）
}

func vguardLLMPool() []vguardModelEntry {
	lines := strings.Split(setting.VGuardLLMModels, "\n")
	pool := make([]vguardModelEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		e := vguardModelEntry{Model: strings.TrimSpace(parts[0])}
		if e.Model == "" {
			continue
		}
		e.Protocol = "chat"
		e.Mode = setting.VGuardLLMMode
		if len(parts) > 1 {
			if p := strings.TrimSpace(parts[1]); p != "" {
				e.Protocol = p
			}
		}
		if len(parts) > 2 {
			if m := strings.TrimSpace(parts[2]); m != "" {
				e.Mode = m
			}
		}
		pool = append(pool, e)
	}
	if len(pool) == 0 && strings.TrimSpace(setting.VGuardLLMModel) != "" {
		pool = append(pool, vguardModelEntry{
			Model: setting.VGuardLLMModel, Protocol: setting.VGuardLLMProtocol, Mode: setting.VGuardLLMMode,
		})
	}
	return pool
}

// vguardModelHealth 单模型近期健康度（10 分钟滑动窗口）与熔断状态。
type vguardModelHealth struct {
	mu            sync.Mutex
	records       []vguardCallRecord
	consecFails   int
	cooldownUntil int64
}

type vguardCallRecord struct {
	ts int64
	ok bool
	ms float64
}

const (
	vguardHealthWindowSec = 600
	vguardHealthMaxRecs   = 512
	vguardBreakerTrips    = 3 // 连续失败 N 次熔断
)

var vguardModelHealths sync.Map // model -> *vguardModelHealth

func vguardHealthFor(model string) *vguardModelHealth {
	v, _ := vguardModelHealths.LoadOrStore(model, &vguardModelHealth{})
	return v.(*vguardModelHealth)
}

func (h *vguardModelHealth) record(ok bool, ms float64) {
	now := time.Now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, vguardCallRecord{ts: now, ok: ok, ms: ms})
	if len(h.records) > vguardHealthMaxRecs {
		h.records = h.records[len(h.records)-vguardHealthMaxRecs:]
	}
	if ok {
		h.consecFails = 0
		return
	}
	h.consecFails++
	if h.consecFails >= vguardBreakerTrips {
		// 冷却随连续失败次数翻倍：60s、120s、240s……封顶 16 分钟
		shift := h.consecFails - vguardBreakerTrips
		if shift > 4 {
			shift = 4
		}
		h.cooldownUntil = now + int64(60)<<uint(shift)
	}
}

// score 返回 (窗口内成功率, 成功平均延迟ms, 熔断剩余秒)；无数据给 0.8
// 先验，保证新模型有机会被尝试。
func (h *vguardModelHealth) score() (rate float64, avgMs float64, cooling int64) {
	now := time.Now().Unix()
	h.mu.Lock()
	defer h.mu.Unlock()
	if cooling = h.cooldownUntil - now; cooling < 0 {
		cooling = 0
	}
	cutoff := now - vguardHealthWindowSec
	ok, total := 0, 0
	var sum float64
	for _, r := range h.records {
		if r.ts < cutoff {
			continue
		}
		total++
		if r.ok {
			ok++
			sum += r.ms
		}
	}
	rate, avgMs = 0.8, 0
	if total > 0 {
		rate = float64(ok) / float64(total)
		if ok > 0 {
			avgMs = sum / float64(ok)
		}
	}
	return
}

// vguardPickModel 加权随机挑一个健康模型（权重 = 成功率/(1+平均延迟秒)）；
// 全部熔断时返回冷却最先到期的那个（半开探测）。
func vguardPickModel(pool []vguardModelEntry) vguardModelEntry {
	fallback := pool[0]
	fallbackCool := int64(1 << 62)
	type cand struct {
		e vguardModelEntry
		w float64
	}
	cands := make([]cand, 0, len(pool))
	var total float64
	for _, e := range pool {
		h := vguardHealthFor(e.Model)
		rate, avgMs, cooling := h.score()
		if cooling < fallbackCool {
			fallbackCool, fallback = cooling, e
		}
		if cooling > 0 {
			continue
		}
		w := rate / (1 + avgMs/1000)
		if w < 0.01 {
			w = 0.01
		}
		cands = append(cands, cand{e: e, w: w})
		total += w
	}
	if len(cands) == 0 {
		return fallback
	}
	pick := rand.Float64() * total
	for _, c := range cands {
		pick -= c.w
		if pick <= 0 {
			return c.e
		}
	}
	return cands[len(cands)-1].e
}

func vguardPoolHasOtherEligible(pool []vguardModelEntry, failed string) bool {
	now := time.Now().Unix()
	for _, e := range pool {
		if e.Model == failed {
			continue
		}
		h := vguardHealthFor(e.Model)
		h.mu.Lock()
		cooling := h.cooldownUntil > now
		h.mu.Unlock()
		if !cooling {
			return true
		}
	}
	return false
}

// VGuardLLMPoolHealth 供后台状态接口展示各模型实时健康度。
func VGuardLLMPoolHealth() []map[string]any {
	out := make([]map[string]any, 0, 8)
	for _, e := range vguardLLMPool() {
		rate, avgMs, cooling := vguardHealthFor(e.Model).score()
		out = append(out, map[string]any{
			"model":        e.Model,
			"protocol":     e.Protocol,
			"success_rate": rate,
			"avg_ms":       int64(avgMs),
			"cooldown_sec": cooling,
		})
	}
	return out
}

// vguardLLMCheckTextWithDeadline 送审并按给定总预算(秒)等待判定，
// 返回实际使用的审核模型。每次尝试前按健康度选模型；失败立即切换
// 下一个健康模型，无可切换时指数退避重试；全程持有全局送审并发闸。
func vguardLLMCheckTextWithDeadline(text string, deadlineSeconds int) (llmVerdict, string, string, error) {
	if deadlineSeconds <= 0 || deadlineSeconds > 600 {
		deadlineSeconds = 120
	}
	pool := vguardLLMPool()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(deadlineSeconds)*time.Second)
	defer cancel()
	release, ok := vguardLLMGateAcquire(ctx)
	if !ok {
		return llmVerdictUnknown, "", "", errors.New("送审并发闸排队超时")
	}
	defer release()
	backoff := 800 * time.Millisecond
	lastModel := ""
	const maxAttempts = 12
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		e := vguardPickModel(pool)
		lastModel = e.Model
		var (
			verdict llmVerdict
			reply   string
			status  int
			err     error
		)
		start := time.Now()
		if e.Protocol == "systemone" {
			verdict, reply, status, err = vguardLLMCheckSystemoneOnce(ctx, e.Model, text)
		} else {
			verdict, reply, status, err = vguardLLMCheckChatOnce(ctx, e.Model, e.Mode, text)
		}
		ok := err == nil && status == http.StatusOK && verdict != llmVerdictUnknown
		vguardHealthFor(e.Model).record(ok, float64(time.Since(start).Microseconds())/1000)
		if ok {
			return verdict, reply, e.Model, nil
		}
		if err == nil && status != http.StatusOK {
			err = fmt.Errorf("审核模型 HTTP %d", status)
		}
		if ctx.Err() != nil {
			return llmVerdictUnknown, "", e.Model, err
		}
		if vguardPoolHasOtherEligible(pool, e.Model) {
			continue
		}
		wait := backoff + time.Duration(rand.Int63n(int64(400*time.Millisecond)))
		if backoff < 8*time.Second {
			backoff *= 2
		}
		common.SysLog(fmt.Sprintf("VGuardLLM送审失败退避重试: attempt=%d model=%s status=%d wait=%dms",
			attempt, e.Model, status, wait.Milliseconds()))
		select {
		case <-ctx.Done():
			return llmVerdictUnknown, "", e.Model, err
		case <-time.After(wait):
		}
	}
	return llmVerdictUnknown, "", lastModel, fmt.Errorf("审核模型连续 %d 次尝试失败", maxAttempts)
}

// vguardLLMCheckChatOnce chat 协议（OpenAI 兼容）单次送审；
// status 返回上游 HTTP 状态码（0=请求未发出），供熔断统计与故障转移判断。
func vguardLLMCheckChatOnce(ctx context.Context, model, mode, text string) (llmVerdict, string, int, error) {
	limited := text
	if setting.VGuardLLMMaxTextChars > 0 && len(limited) > setting.VGuardLLMMaxTextChars {
		limited = limited[:setting.VGuardLLMMaxTextChars]
	}
	var content string
	if mode == "decision" {
		tpl := strings.TrimSpace(setting.VGuardLLMPromptTemplate)
		if tpl == "" {
			tpl = setting.DefaultVGuardLLMPrompt
		}
		content = strings.ReplaceAll(tpl, "{text}", limited)
	} else {
		content = limited
	}
	payload, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    []map[string]string{{"role": "user", "content": content}},
		"max_tokens":  200,
		"temperature": 0,
	})
	base := strings.TrimRight(setting.VGuardLLMBaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions",
		bytes.NewReader(payload))
	if err != nil {
		return llmVerdictUnknown, "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := strings.TrimSpace(setting.VGuardLLMKey); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return llmVerdictUnknown, "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return llmVerdictUnknown, "", resp.StatusCode, fmt.Errorf("审核模型 HTTP %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		return llmVerdictUnknown, "", resp.StatusCode, errors.New("审核模型响应解析失败")
	}
	reply := out.Choices[0].Message.Content
	unsafeRe, safeRe := llmCompilePatterns()
	if unsafeRe.MatchString(reply) {
		return llmVerdictUnsafe, reply, resp.StatusCode, nil
	}
	if safeRe.MatchString(reply) {
		return llmVerdictSafe, reply, resp.StatusCode, nil
	}
	return llmVerdictUnknown, reply, resp.StatusCode, nil
}

type llmCallResult struct {
	verdict llmVerdict
	reply   string
	err     error
	model   string // 实际使用的审核模型
}

// soText 空值回退：后台该项被清空时（options 表存在空行会覆盖默认值）
// 回落到内置默认文案，避免 systemone 请求带空 instructions 导致上游报错。
func soText(v, def string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	return def
}

// vguardLLMCheckSystemoneOnce 决策模型（/v1/systemone 结构化判定协议）单次送审：
// state.message 放被审文本，verdict(choice safe/unsafe) 出判定，flag(noul)
// 同问双保险；返回的 reply 形如 "choice=unsafe conf=0.996 noul=0.995"，
// 会随 kind=llm 事件入库，便于事后核查置信度。
// status 返回上游 HTTP 状态码（0=请求未发出），供 429 重试判断。
func vguardLLMCheckSystemoneOnce(ctx context.Context, model, text string) (llmVerdict, string, int, error) {
	limited := text
	if setting.VGuardLLMMaxTextChars > 0 && len(limited) > setting.VGuardLLMMaxTextChars {
		limited = limited[:setting.VGuardLLMMaxTextChars]
	}
	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"state": map[string]any{"message": limited},
		"questions": map[string]any{
			"verdict": map[string]any{
				"type":         "choice",
				"instructions": setting.VGuardLLMSoInstructions,
				"criteria": map[string]string{
					"unsafe": setting.VGuardLLMSoUnsafeCriteria,
					"safe":   setting.VGuardLLMSoSafeCriteria,
				},
			},
			"flag": map[string]any{
				"type":         "noul",
				"instructions": setting.VGuardLLMSoNoulInstruction,
			},
		},
	})
	base := strings.TrimRight(setting.VGuardLLMBaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/systemone",
		bytes.NewReader(payload))
	if err != nil {
		return llmVerdictUnknown, "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := strings.TrimSpace(setting.VGuardLLMKey); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return llmVerdictUnknown, "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return llmVerdictUnknown, "", resp.StatusCode, fmt.Errorf("审核决策模型 HTTP %d", resp.StatusCode)
	}
	var out struct {
		Answers struct {
			Verdict struct {
				Choice     string  `json:"choice"`
				Confidence float64 `json:"confidence"`
			} `json:"verdict"`
			Flag struct {
				Noul float64 `json:"noul"`
			} `json:"flag"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return llmVerdictUnknown, "", resp.StatusCode, errors.New("决策模型响应解析失败")
	}
	reply := fmt.Sprintf("choice=%s conf=%.4f noul=%.4f",
		out.Answers.Verdict.Choice, out.Answers.Verdict.Confidence, out.Answers.Flag.Noul)
	switch strings.ToLower(strings.TrimSpace(out.Answers.Verdict.Choice)) {
	case "unsafe":
		return llmVerdictUnsafe, reply, resp.StatusCode, nil
	case "safe":
		return llmVerdictSafe, reply, resp.StatusCode, nil
	}
	return llmVerdictUnknown, reply, resp.StatusCode, nil
}

// vguardLLMCallCtx 同步/异步共用的请求上下文快照（后台协程不能碰 gin.Context）。
type vguardLLMCallCtx struct {
	uid         int
	username    string
	modelName   string
	requestId   string
	reviewModel string // 实际执行本次送审的审核模型
	text        string
	lower       string
	weakHit     bool
	st          *VGuardUserState
}

func (a *vguardLLMCallCtx) recordEvent(kind, words string) {
	firstWord := ""
	if parts := strings.Split(words, ","); len(parts) > 0 {
		firstWord = parts[0]
	}
	ctxText := vguardContextAround(a.text, a.lower, firstWord)
	detail := a.text
	if len(detail) > vguardMaxDetailCap {
		detail = detail[:vguardMaxDetailCap]
	}
	model.RecordVGuardEvent(&model.VGuardEvent{
		UserId: a.uid, Username: a.username, Kind: kind, Words: words,
		Model: a.modelName, RequestId: a.requestId, Context: ctxText, Detail: detail,
		CreatedAt: time.Now().Unix(),
	})
}

// vguardLLMApplyUnsafe 判不安全：事件 + 持续观察 + 计 1 次（弱词计数已推迟到此）。
// 返回 (是否新触发封禁, 封禁小时数)。
func vguardLLMApplyUnsafe(a *vguardLLMCallCtx) (bool, int64) {
	now := time.Now().Unix()
	a.recordEvent("llm", "llm:unsafe:"+a.reviewModel)
	vguardResetSafeStreak(a.st)
	vguardExtendLLMWindow(a.st, now)
	return vguardCountWeak(a.st, now)
}

// 全局送审并发闸：限制同时在途的送审调用数（含 429 重试），上限
// VGuardLLMMaxConcurrent（默认 3，0=不限）。审核模型与正常聊天共享
// 上游按模型全局并发池，不设闸时大流量送审会挤占正常请求、抬高全站 429。
var (
	vguardLLMGateMu    sync.Mutex
	vguardLLMGateInUse int
)

func vguardLLMGateAcquire(ctx context.Context) (func(), bool) {
	limit := setting.VGuardLLMMaxConcurrent
	if limit <= 0 {
		return func() {}, true
	}
	for {
		vguardLLMGateMu.Lock()
		if vguardLLMGateInUse < limit {
			vguardLLMGateInUse++
			vguardLLMGateMu.Unlock()
			break
		}
		vguardLLMGateMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(200 * time.Millisecond):
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			vguardLLMGateMu.Lock()
			vguardLLMGateInUse--
			vguardLLMGateMu.Unlock()
		})
	}, true
}

// llm_error 事件限噪：同一事件标签在 VGuardLLMErrorCooldownSec 窗口内
// 只落库 1 条，其余以系统日志合并计数，避免上游拥堵时事件表/后台被
// 同一错误刷屏（2026-10-26 风暴 1 小时落库 3470 条）。
var (
	vguardErrEvtMu     sync.Mutex
	vguardErrEvtLast   = map[string]int64{}
	vguardErrEvtMerged = map[string]int{}
)

func (a *vguardLLMCallCtx) recordErrEvent(words string) {
	cooldown := setting.VGuardLLMErrorCooldownSec
	if cooldown <= 0 {
		a.recordEvent("llm", words)
		return
	}
	now := time.Now().Unix()
	vguardErrEvtMu.Lock()
	if last, ok := vguardErrEvtLast[words]; ok && now-last < int64(cooldown) {
		vguardErrEvtMerged[words]++
		n := vguardErrEvtMerged[words]
		vguardErrEvtMu.Unlock()
		common.SysLog(fmt.Sprintf("VGuardLLM送审失败(限噪合并#%d): %s uid=%d", n, words, a.uid))
		return
	}
	vguardErrEvtLast[words] = now
	merged := vguardErrEvtMerged[words]
	vguardErrEvtMerged[words] = 0
	vguardErrEvtMu.Unlock()
	if merged > 0 {
		words = fmt.Sprintf("%s (+%d)", words, merged)
	}
	a.recordEvent("llm", words)
}

// vguardLLMFallbackObserve 模型异常时的回退：只延长持续观察窗口，
// 不计弱词、不触发封禁——上游拥堵/送审通道故障不应转化为对用户的
// 处罚（旧回退按弱词计数，2026-10-26 风暴中曾因此误封 27 人）。
// 观察期内后续消息继续送审，模型恢复后照常出判定。
func vguardLLMFallbackObserve(a *vguardLLMCallCtx) {
	vguardExtendLLMWindow(a.st, time.Now().Unix())
}

// vguardLLMRecordSafe 连续安全递归出窗：窗口内安全判定连击达到
// VGuardLLMSafeExitStreak 后，剩余观察时间减半（低于 VGuardLLMSafeExitFloorMin
// 分钟直接出窗），连击清零——即"进窗-连续安全-提前出窗"循环。
// 返回 (是否发生减半, 减半后的剩余分钟数)。窗口未激活时无操作。
func vguardLLMRecordSafe(st *VGuardUserState, now int64) (bool, int64) {
	n := setting.VGuardLLMSafeExitStreak
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.llmUntil <= now {
		st.safeStreak = 0
		return false, 0
	}
	st.safeStreak++
	if n <= 0 || st.safeStreak < n {
		return false, (st.llmUntil - now) / 60
	}
	st.safeStreak = 0
	remaining := (st.llmUntil - now) / 2
	if floor := int64(setting.VGuardLLMSafeExitFloorMin) * 60; floor > 0 && remaining < floor {
		remaining = 0
	}
	st.llmUntil = now + remaining
	return true, remaining / 60
}

// vguardResetSafeStreak 判不安全时清零安全连击。
func vguardResetSafeStreak(st *VGuardUserState) {
	st.mu.Lock()
	st.safeStreak = 0
	st.mu.Unlock()
}

// vguardLLMReenter 弱词命中即重新进观察窗：窗口拉满至 SustainHours、
// 安全连击清零（可在后台通过 VGuardLLMWeakReenter 关闭）。
func vguardLLMReenter(st *VGuardUserState, now int64) {
	if setting.VGuardLLMSustainHours <= 0 {
		return
	}
	st.mu.Lock()
	if until := now + int64(setting.VGuardLLMSustainHours)*3600; until > st.llmUntil {
		st.llmUntil = until
	}
	st.safeStreak = 0
	st.mu.Unlock()
}

func vguardLLMBanIfNewlyBlocked(a *vguardLLMCallCtx, hours int64) {
	a.st.mu.Lock()
	until := a.st.blockedUntil
	a.st.mu.Unlock()
	a.recordEvent("blocked", fmt.Sprintf("llm_hits>=%d", setting.VGuardWeakThreshold))
	vguardSendBanEmail(a.st, a.uid, nil, nil, hours, until)
}

// vguardMaybeLLM VGate 内的模型审核编排；返回非 nil 表示需拦截当前请求。
func vguardMaybeLLM(c *gin.Context, info *relaycommon.RelayInfo, st *VGuardUserState,
	uid int, username, text, lower string, weakHit bool) *types.NewAPIError {
	if !vguardLLMReady() || strings.TrimSpace(text) == "" {
		return nil
	}
	if !vguardLLMWillReview(st, weakHit) {
		return nil
	}

	asyncEnabled := setting.VGuardLLMAsyncEnabled
	fastSec := setting.VGuardLLMTimeoutSec
	totalSec := setting.VGuardLLMTimeoutSec
	if asyncEnabled {
		fastSec = setting.VGuardLLMFastSeconds
		totalSec = setting.VGuardLLMAsyncSeconds
		if fastSec < 1 {
			fastSec = 1
		}
		if fastSec > 300 {
			fastSec = 300
		}
		if totalSec < fastSec+2 {
			totalSec = fastSec + 2
		}
		if totalSec > 600 {
			totalSec = 600
		}
	}

	modelName := ""
	requestId := ""
	if info != nil {
		modelName = info.OriginModelName
	}
	if c != nil {
		requestId = c.GetString(common.RequestIdKey)
	}
	actx := &vguardLLMCallCtx{
		uid: uid, username: username, modelName: modelName, requestId: requestId,
		text: text, lower: lower, weakHit: weakHit, st: st,
	}

	ch := make(chan llmCallResult, 1)
	go func() {
		v, reply, mdl, err := vguardLLMCheckTextWithDeadline(text, totalSec)
		ch <- llmCallResult{verdict: v, reply: reply, err: err, model: mdl}
	}()

	select {
	case res := <-ch:
		return vguardLLMHandleSync(res, actx, c)
	case <-time.After(time.Duration(fastSec) * time.Second):
		if !asyncEnabled {
			logger.LogWarn(c, fmt.Sprintf("VGuardLLM审核超时(uid=%d): %ds failOpen=%v", uid, totalSec, setting.VGuardLLMFailOpen))
			actx.recordErrEvent("llm_error:timeout")
			if actx.weakHit {
				vguardLLMFallbackObserve(actx)
			}
			if !setting.VGuardLLMFailOpen {
				return types.NewError(errors.New("内容安全审核服务暂不可用，请稍后再试。"), types.ErrorCodeSensitiveWordsDetected)
			}
			return nil
		}
		// 慢尾：用户请求立即放行，同一调用转后台补判
		logger.LogInfo(c, fmt.Sprintf("VGuardLLM转异步补判: uid=%d fast=%ds total=%ds", uid, fastSec, totalSec))
		go func() {
			defer func() {
				if r := recover(); r != nil {
					common.SysError(fmt.Sprintf("VGuardLLM异步补判panic: uid=%d %v", actx.uid, r))
				}
			}()
			select {
			case res := <-ch:
				vguardLLMHandleAsync(res, actx)
			case <-time.After(time.Duration(totalSec-fastSec+3) * time.Second):
				common.SysLog(fmt.Sprintf("VGuardLLM异步补判超时放弃: uid=%d total=%ds", actx.uid, totalSec))
				actx.recordErrEvent("llm_error:async_timeout")
				if actx.weakHit {
					vguardLLMFallbackObserve(actx)
				}
			}
		}()
		return nil
	}
}

// vguardLLMHandleSync 同步窗口内拿到结果：可当场拦截。
func vguardLLMHandleSync(res llmCallResult, a *vguardLLMCallCtx, c *gin.Context) *types.NewAPIError {
	a.reviewModel = res.model
	if a.reviewModel == "" {
		a.reviewModel = setting.VGuardLLMModel
	}
	if res.err != nil || res.verdict == llmVerdictUnknown {
		logger.LogWarn(c, fmt.Sprintf("VGuardLLM审核异常(uid=%d model=%s): %v reply=%s", a.uid, a.reviewModel, res.err, truncateStr(res.reply, 120)))
		a.recordErrEvent("llm_error:" + a.reviewModel)
		if a.weakHit {
			vguardLLMFallbackObserve(a)
		}
		if !setting.VGuardLLMFailOpen {
			return types.NewError(errors.New("内容安全审核服务暂不可用，请稍后再试。"), types.ErrorCodeSensitiveWordsDetected)
		}
		return nil
	}
	if res.verdict == llmVerdictSafe {
		// 判定安全 = 无罪：不计入弱词累计；连续安全则递归提前出观察窗
		if halved, remainMin := vguardLLMRecordSafe(a.st, time.Now().Unix()); halved {
			common.SysLog(fmt.Sprintf("VGuardLLM连续%d次判定安全,观察窗减半: uid=%d 剩余%d分钟",
				setting.VGuardLLMSafeExitStreak, a.uid, remainMin))
		}
		return nil
	}
	logger.LogWarn(c, fmt.Sprintf("VGuardLLM判定不安全: uid=%d model=%s reply=%s", a.uid, a.reviewModel, truncateStr(res.reply, 80)))
	newlyBlocked, hours := vguardLLMApplyUnsafe(a)
	if newlyBlocked {
		vguardLLMBanIfNewlyBlocked(a, hours)
		return types.NewError(fmt.Errorf("检测到多次违规请求，您的账号已被临时限制 %d 小时。", hours), types.ErrorCodeSensitiveWordsDetected)
	}
	if setting.VGuardLLMAction == "block" {
		return types.NewError(errors.New("您的请求经内容安全审核未通过，已被拒绝。如有疑问请联系管理员。"), types.ErrorCodeSensitiveWordsDetected)
	}
	return nil
}

// vguardLLMHandleAsync 慢尾补判：不再拦截已放行的请求，但计数/封禁照常生效。
func vguardLLMHandleAsync(res llmCallResult, a *vguardLLMCallCtx) {
	a.reviewModel = res.model
	if a.reviewModel == "" {
		a.reviewModel = setting.VGuardLLMModel
	}
	if res.err != nil || res.verdict == llmVerdictUnknown {
		common.SysLog(fmt.Sprintf("VGuardLLM异步补判异常: uid=%d model=%s err=%v reply=%s", a.uid, a.reviewModel, res.err, truncateStr(res.reply, 100)))
		a.recordErrEvent("llm_error:async:" + a.reviewModel)
		if a.weakHit {
			vguardLLMFallbackObserve(a)
		}
		return
	}
	if res.verdict == llmVerdictSafe {
		if halved, remainMin := vguardLLMRecordSafe(a.st, time.Now().Unix()); halved {
			common.SysLog(fmt.Sprintf("VGuardLLM连续%d次判定安全,观察窗减半: uid=%d 剩余%d分钟",
				setting.VGuardLLMSafeExitStreak, a.uid, remainMin))
		} else {
			common.SysLog(fmt.Sprintf("VGuardLLM异步补判安全: uid=%d", a.uid))
		}
		return
	}
	common.SysLog(fmt.Sprintf("VGuardLLM异步补判不安全: uid=%d model=%s (已计数，若达阈值已封禁，后续请求将被拦截)", a.uid, a.reviewModel))
	newlyBlocked, hours := vguardLLMApplyUnsafe(a)
	if newlyBlocked {
		vguardLLMBanIfNewlyBlocked(a, hours)
	}
}
