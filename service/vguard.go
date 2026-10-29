package service

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// ============================================================
// VGuard 违规防护 v3
//   - 输入侧：只查最新用户消息；强词拦截+封禁，弱词计数+注入
//   - 阶梯封禁：VGuardBanTiers 逗号分隔小时档位，第 N 次封禁取第 N 档
//   - 输出侧：被标记用户的模型回复扫弱词计数（kind=output）
//   - 上游信号：CSAM 类标记=强违规；content_policy_violation=弱计数
//   - 邮件：弱词命中发警告（按天限频）；封禁发通知（模板可配）
//   - 分组作用域 + 用户白名单 + 管理员豁免
// ============================================================

const (
	vguardMaxScanBytes  = 128 * 1024
	vguardMaxDetailCap  = 131072
	vguardContextRadius = 90
)

type VGuardUserState struct {
	mu            sync.Mutex
	weakHits      []int64
	strongCount   int
	blockCount    int
	blockedUntil  int64
	llmUntil      int64
	safeStreak    int // 观察窗内连续安全判定次数（达到阈值递归减半提前出窗）
	Username      string
	Email         string
	warnEmailDay  int64
	warnEmailSent int
}

var vguardStates sync.Map

func vguardStateOf(uid int) *VGuardUserState {
	v, ok := vguardStates.Load(uid)
	if !ok {
		v, _ = vguardStates.LoadOrStore(uid, &VGuardUserState{})
	}
	st, _ := v.(*VGuardUserState)
	return st
}

func vguardPrune(hits []int64, cutoff int64) []int64 {
	out := hits[:0]
	for _, t := range hits {
		if t >= cutoff {
			out = append(out, t)
		}
	}
	return out
}

func vguardWindowSeconds() int64 {
	s := int64(setting.VGuardWindowMinutes) * 60
	if s < 60 {
		s = 3600
	}
	return s
}

// vguardApplyBlock 阶梯封禁：第 N 次（N 从 1 起）取第 N 档，超出取末档。返回本次封禁小时数。
func vguardApplyBlock(st *VGuardUserState, now int64) int64 {
	tiers := setting.VGuardBanTierHours()
	idx := st.blockCount
	if idx >= len(tiers) {
		idx = len(tiers) - 1
	}
	hours := tiers[idx]
	if hours < 1 {
		hours = 1
	}
	st.blockCount++
	until := now + hours*3600
	if st.blockedUntil < until {
		st.blockedUntil = until
	}
	return hours
}

func vguardDayKey(now int64) int64 {
	t := time.Unix(now, 0)
	return int64(t.Year()*1000 + t.YearDay())
}

// ---------------- 作用域 / 白名单 ----------------

func vguardGroupApplies(group string) bool {
	list := setting.VGuardApplyGroups
	if strings.TrimSpace(list) == "" {
		return true
	}
	g := strings.ToLower(strings.TrimSpace(group))
	for _, part := range strings.Split(list, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == g {
			return true
		}
	}
	return false
}

func vguardUserWhitelisted(uid int, username string) bool {
	list := setting.VGuardUserWhitelist
	if strings.TrimSpace(list) == "" {
		return false
	}
	un := strings.ToLower(strings.TrimSpace(username))
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil {
			if n == uid {
				return true
			}
		} else if un != "" && strings.ToLower(p) == un {
			return true
		}
	}
	return false
}

func vguardInScope(c *gin.Context, info *relaycommon.RelayInfo, uid int, username string) bool {
	if c.GetInt("role") >= common.RoleAdminUser {
		return false
	}
	group := ""
	if info != nil {
		group = info.UserGroup
	}
	if !vguardGroupApplies(group) {
		return false
	}
	if vguardUserWhitelisted(uid, username) {
		return false
	}
	return true
}

// ---------------- 匹配 ----------------

func vguardIsASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func vguardIsPureASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func vguardContains(lowerText, word string) bool {
	if word == "" || lowerText == "" {
		return false
	}
	if !vguardIsPureASCII(word) {
		return strings.Contains(lowerText, word)
	}
	from := 0
	for {
		i := strings.Index(lowerText[from:], word)
		if i < 0 {
			return false
		}
		i += from
		start, end := i, i+len(word)
		okBefore := start == 0 || !vguardIsASCIIAlnum(lowerText[start-1])
		okAfter := end >= len(lowerText) || !vguardIsASCIIAlnum(lowerText[end])
		if okBefore && okAfter {
			return true
		}
		from = i + 1
		if from >= len(lowerText) {
			return false
		}
	}
}

func vguardMatchWords(lowerText string, words []string) []string {
	hits := make([]string, 0, 2)
	for _, w := range words {
		if vguardContains(lowerText, w) {
			hits = append(hits, w)
			if len(hits) >= 5 {
				break
			}
		}
	}
	return hits
}

func vguardContextAround(text, lowerText, firstWord string) string {
	if firstWord == "" {
		return ""
	}
	i := strings.Index(lowerText, firstWord)
	if i < 0 {
		i = 0
	}
	start := i - vguardContextRadius
	if start < 0 {
		start = 0
	}
	end := i + len(firstWord) + vguardContextRadius
	if end > len(text) {
		end = len(text)
	}
	ctx := strings.ReplaceAll(text[start:end], "\n", " ")
	ctx = strings.ReplaceAll(ctx, "\r", " ")
	if len(ctx) > vguardContextRadius*2+120 {
		ctx = ctx[:vguardContextRadius*2+120]
	}
	return ctx
}

// ---------------- 最新用户消息提取 ----------------

func vguardAnyText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, item := range v {
			if mp, ok := item.(map[string]any); ok {
				if t, _ := mp["type"].(string); t == "text" {
					if s, ok := mp["text"].(string); ok {
						sb.WriteString(s)
						sb.WriteByte('\n')
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

func vguardExtractLastUserText(req dto.Request) string {
	switch r := req.(type) {
	case *dto.GeneralOpenAIRequest:
		for i := len(r.Messages) - 1; i >= 0; i-- {
			m := &r.Messages[i]
			if m.Role != "user" {
				continue
			}
			var t string
			if s, ok := m.Content.(string); ok {
				t = s
			} else {
				var sb strings.Builder
				for _, mc := range m.ParseContent() {
					if mc.Type == "text" {
						sb.WriteString(mc.Text)
						sb.WriteByte('\n')
					}
				}
				t = sb.String()
			}
			t = strings.TrimSpace(t)
			if t != "" {
				return t
			}
		}
	case *dto.ClaudeRequest:
		for i := len(r.Messages) - 1; i >= 0; i-- {
			m := &r.Messages[i]
			if m.Role != "user" {
				continue
			}
			t := strings.TrimSpace(vguardAnyText(m.Content))
			if t != "" {
				return t
			}
		}
	}
	return ""
}

// ---------------- 注入 ----------------

func vguardInject(info *relaycommon.RelayInfo, prompt string) {
	switch r := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		msg := dto.Message{Role: r.GetSystemRoleName(), Content: prompt}
		r.Messages = append([]dto.Message{msg}, r.Messages...)
	case *dto.ClaudeRequest:
		if r.System == nil {
			r.SetStringSystem(prompt)
			return
		}
		switch sv := r.System.(type) {
		case string:
			r.SetStringSystem(prompt + "\n" + sv)
		case []any:
			block := map[string]any{"type": "text", "text": prompt}
			r.System = append([]any{block}, sv...)
		case []dto.ClaudeMediaMessage:
			nb := dto.ClaudeMediaMessage{Type: "text"}
			nb.SetText(prompt)
			r.System = append([]dto.ClaudeMediaMessage{nb}, sv...)
		}
	}
}

// ---------------- 邮件 ----------------

func vguardRender(tpl string, vars map[string]string) string {
	r := strings.NewReplacer(
		"{username}", vars["username"],
		"{time}", vars["time"],
		"{model}", vars["model"],
		"{request_id}", vars["request_id"],
		"{hits}", vars["hits"],
		"{threshold}", vars["threshold"],
		"{hours}", vars["hours"],
		"{until}", vars["until"],
		"{block_count}", vars["block_count"],
	)
	return r.Replace(tpl)
}

func vguardSendWarnEmail(st *VGuardUserState, uid int, info *relaycommon.RelayInfo, c *gin.Context, hits, threshold int) {
	if !setting.VGuardWarnEmailEnabled || st.Email == "" {
		return
	}
	now := time.Now().Unix()
	day := vguardDayKey(now)
	st.mu.Lock()
	if st.warnEmailDay != day {
		st.warnEmailDay = day
		st.warnEmailSent = 0
	}
	if st.warnEmailSent >= setting.VGuardEmailThrottlePerDay {
		st.mu.Unlock()
		return
	}
	st.warnEmailSent++
	st.mu.Unlock()
	vars := map[string]string{
		"username": st.Username, "time": time.Unix(now, 0).Format("2006-01-02 15:04:05"),
		"model": "", "request_id": "",
		"hits": strconv.Itoa(hits), "threshold": strconv.Itoa(threshold),
	}
	if info != nil {
		vars["model"] = info.OriginModelName
	}
	if c != nil {
		vars["request_id"] = c.GetString(common.RequestIdKey)
	}
	subject := vguardRender(setting.VGuardWarnEmailSubject, vars)
	body := vguardRender(setting.VGuardWarnEmailBody, vars)
	go func() {
		if err := common.SendEmail(subject, st.Email, body); err != nil {
			common.SysError("VGuard警告邮件发送失败 uid=" + strconv.Itoa(uid) + ": " + err.Error())
		}
	}()
}

func vguardSendBanEmail(st *VGuardUserState, uid int, info *relaycommon.RelayInfo, c *gin.Context, hours int64, blockedUntil int64) {
	if !setting.VGuardBanEmailEnabled || st.Email == "" {
		return
	}
	st.mu.Lock()
	name := st.Username
	bc := st.blockCount
	email := st.Email
	st.mu.Unlock()
	vars := map[string]string{
		"username": name, "time": time.Now().Format("2006-01-02 15:04:05"),
		"model": "", "request_id": "",
		"hours":       strconv.FormatInt(hours, 10),
		"until":       time.Unix(blockedUntil, 0).Format("2006-01-02 15:04:05"),
		"block_count": strconv.Itoa(bc),
	}
	if info != nil {
		vars["model"] = info.OriginModelName
	}
	if c != nil {
		vars["request_id"] = c.GetString(common.RequestIdKey)
	}
	subject := vguardRender(setting.VGuardBanEmailSubject, vars)
	body := vguardRender(setting.VGuardBanEmailBody, vars)
	go func() {
		if err := common.SendEmail(subject, email, body); err != nil {
			common.SysError("VGuard封禁邮件发送失败 uid=" + strconv.Itoa(uid) + ": " + err.Error())
		}
	}()
}

// ---------------- 事件 ----------------

func vguardRecordEvent(kind string, uid int, username, words string, info *relaycommon.RelayInfo, c *gin.Context, text string, lowerText string) {
	modelName := ""
	if info != nil {
		modelName = info.OriginModelName
	}
	rid := ""
	if c != nil {
		rid = c.GetString(common.RequestIdKey)
	}
	firstWord := ""
	if parts := strings.Split(words, ","); len(parts) > 0 {
		firstWord = parts[0]
	}
	ctx := vguardContextAround(text, lowerText, firstWord)
	detail := text
	if len(detail) > vguardMaxDetailCap {
		detail = detail[:vguardMaxDetailCap]
	}
	finalName := username
	go func() {
		if finalName == "" && uid > 0 {
			if u, err := model.GetUserById(uid, false); err == nil && u != nil {
				finalName = u.Username
			}
		}
		model.RecordVGuardEvent(&model.VGuardEvent{
			UserId: uid, Username: finalName, Kind: kind, Words: words,
			Model: modelName, RequestId: rid, Context: ctx, Detail: detail,
			CreatedAt: time.Now().Unix(),
		})
	}()
}

// ---------------- 弱词计数（输入/输出/上游共用） ----------------

// vguardCountWeak 计一次弱违规；返回 (是否新触发封禁, 封禁小时数)。
func vguardCountWeak(st *VGuardUserState, now int64) (bool, int64) {
	threshold := setting.VGuardWeakThreshold
	if threshold < 1 {
		threshold = 1
	}
	st.mu.Lock()
	st.weakHits = vguardPrune(st.weakHits, now-vguardWindowSeconds())
	st.weakHits = append(st.weakHits, now)
	count := len(st.weakHits)
	if count < threshold {
		st.mu.Unlock()
		return false, 0
	}
	hours := vguardApplyBlock(st, now)
	st.mu.Unlock()
	return true, hours
}

// ---------------- 主入口（计费前，输入侧） ----------------

func VGuardGate(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if !setting.VGuardEnabled {
		return nil
	}
	uid := 0
	if info != nil {
		uid = info.UserId
	}
	if uid == 0 {
		uid = c.GetInt("id")
	}
	if uid == 0 {
		return nil
	}
	username := c.GetString("username")
	if !vguardInScope(c, info, uid, username) {
		return nil
	}
	now := time.Now().Unix()
	st := vguardStateOf(uid)
	email := ""
	if info != nil {
		email = info.UserEmail
	}
	st.mu.Lock()
	if username != "" {
		st.Username = username
	}
	if email != "" {
		st.Email = email
	}
	blockedUntil := st.blockedUntil
	st.mu.Unlock()
	if blockedUntil > now {
		remainMin := (blockedUntil - now + 59) / 60
		return types.NewError(fmt.Errorf("您的账号因触发违规内容检测已被临时限制，剩余约 %d 分钟，请稍后再试。如有疑问请联系管理员。", remainMin), types.ErrorCodeSensitiveWordsDetected)
	}

	var req dto.Request
	if info != nil {
		req = info.Request
	}
	if req == nil {
		return nil
	}
	text := vguardExtractLastUserText(req)
	if text == "" {
		return nil
	}
	if len(text) > vguardMaxScanBytes {
		text = text[:vguardMaxScanBytes]
	}
	lower := strings.ToLower(text)

	strong := vguardMatchWords(lower, setting.VGuardStrongWordsList())
	if len(strong) > 0 {
		st.mu.Lock()
		st.strongCount++
		hours := vguardApplyBlock(st, now)
		until := st.blockedUntil
		name, mail := st.Username, st.Email
		st.mu.Unlock()
		vguardRecordEvent("strong", uid, username, strings.Join(strong, ","), info, c, text, lower)
		vguardRecordEvent("blocked", uid, username, "strong:"+strings.Join(strong, ","), info, c, text, lower)
		logger.LogWarn(c, fmt.Sprintf("VGuard强词拦截: uid=%d words=%v hours=%d", uid, strong, hours))
		vguardSendBanEmail(st, uid, info, c, hours, until)
		_ = name
		_ = mail
		return types.NewError(fmt.Errorf("请求包含严重违规内容，已被拒绝。该账号已被临时限制使用 %d 小时。", hours), types.ErrorCodeSensitiveWordsDetected)
	}

	weak := vguardMatchWords(lower, setting.VGuardWeakWordsList())
	weakHit := len(weak) > 0
	if weakHit {
		vguardRecordEvent("weak", uid, username, strings.Join(weak, ","), info, c, text, lower)
		// 弱词命中即重新进观察窗（后台可关）：窗口拉满、安全连击清零，
		// 形成"进窗-连续安全提前出窗-再命中再进窗"循环
		if setting.VGuardLLMWeakReenter {
			vguardLLMReenter(st, now)
		}
		// 模型会复核这条消息时：弱词计数推迟到判定之后 ——
		// SAFE=无罪不计入 / UNSAFE=计1次 / 模型异常=只延长观察不计数
		if !vguardLLMWillReview(st, weakHit) {
			newlyBlocked, hours := vguardCountWeak(st, now)
			vguardExtendLLMWindow(st, now)
			if newlyBlocked {
				st.mu.Lock()
				until := st.blockedUntil
				st.mu.Unlock()
				vguardRecordEvent("blocked", uid, username, fmt.Sprintf("weak_hits>=%d", setting.VGuardWeakThreshold), info, c, text, lower)
				logger.LogWarn(c, fmt.Sprintf("VGuard弱词累计封禁: uid=%d hours=%d", uid, hours))
				vguardSendBanEmail(st, uid, info, c, hours, until)
				return types.NewError(fmt.Errorf("检测到连续多次违规请求，您的账号已被临时限制 %d 小时。再次违规封禁时长将升级，多次违规将永久封禁。", hours), types.ErrorCodeSensitiveWordsDetected)
			}
			st.mu.Lock()
			count := len(vguardPrune(st.weakHits, now-vguardWindowSeconds()))
			st.mu.Unlock()
			vguardSendWarnEmail(st, uid, info, c, count, setting.VGuardWeakThreshold)
		}
	}

	if llmErr := vguardMaybeLLM(c, info, st, uid, username, text, lower, weakHit); llmErr != nil {
		return llmErr
	}

	// 注入：命中过弱词（窗口内被标记）的用户，每次请求前置注入
	if setting.VGuardInjectEnabled && setting.VGuardInjectPrompt != "" {
		st.mu.Lock()
		marked := len(vguardPrune(st.weakHits, now-vguardWindowSeconds())) > 0
		st.mu.Unlock()
		if marked {
			vguardInject(info, setting.VGuardInjectPrompt)
			logger.LogInfo(c, fmt.Sprintf("VGuard已注入防护提示词: uid=%d", uid))
		}
	}
	return nil
}

// ---------------- 输出侧检测（请求成功完成后调用） ----------------

func vguardTaskOutput(c *gin.Context) string {
	v := c.Value(common.TaskLogStateContextKey)
	if v == nil {
		return ""
	}
	if tls, ok := v.(*common.TaskLogState); ok && tls != nil {
		return tls.Output.String()
	}
	return ""
}

func VGuardCheckOutput(c *gin.Context, info *relaycommon.RelayInfo) {
	if !setting.VGuardEnabled || !setting.VGuardOutputCheckEnabled {
		return
	}
	uid := 0
	if info != nil {
		uid = info.UserId
	}
	if uid == 0 {
		return
	}
	now := time.Now().Unix()
	st := vguardStateOf(uid)
	st.mu.Lock()
	marked := len(vguardPrune(st.weakHits, now-vguardWindowSeconds())) > 0
	st.mu.Unlock()
	if !marked {
		return
	}
	out := vguardTaskOutput(c)
	if out == "" {
		return
	}
	if len(out) > vguardMaxScanBytes {
		out = out[:vguardMaxScanBytes]
	}
	lower := strings.ToLower(out)
	weak := vguardMatchWords(lower, setting.VGuardWeakWordsList())
	if len(weak) == 0 {
		return
	}
	username := ""
	if info != nil {
		username = c.GetString("username")
	}
	vguardRecordEvent("output", uid, username, strings.Join(weak, ","), info, c, out, lower)
	logger.LogWarn(c, fmt.Sprintf("VGuard输出侧命中: uid=%d words=%v", uid, weak))
	vguardCountWeak(st, now)
}

// ---------------- 上游违规信号（请求失败路径调用） ----------------

var vguardUpstreamWeakMarkers = []string{"content_policy_violation", "content management policy", "usage policy violation"}

func VGuardUpstreamViolation(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) {
	if !setting.VGuardEnabled || apiErr == nil {
		return
	}
	uid := 0
	if info != nil {
		uid = info.UserId
	}
	if uid == 0 {
		uid = c.GetInt("id")
	}
	if uid == 0 {
		return
	}
	username := c.GetString("username")
	now := time.Now().Unix()
	msg := strings.ToLower(apiErr.Error())

	if HasCSAMViolationMarker(apiErr) { // 上游判定 CSAM/严重违规 → 等同强词
		st := vguardStateOf(uid)
		if !vguardInScope(c, info, uid, username) {
			return
		}
		st.mu.Lock()
		st.strongCount++
		hours := vguardApplyBlock(st, now)
		until := st.blockedUntil
		st.mu.Unlock()
		vguardRecordEvent("upstream_strong", uid, username, "csam_violation_marker", info, c, msg[:min(200, len(msg))], msg)
		vguardRecordEvent("blocked", uid, username, "upstream:csam", info, c, "", "")
		logger.LogWarn(c, fmt.Sprintf("VGuard上游严重违规: uid=%d hours=%d", uid, hours))
		vguardSendBanEmail(st, uid, info, c, hours, until)
		return
	}
	for _, m := range vguardUpstreamWeakMarkers {
		if strings.Contains(msg, m) {
			if !vguardInScope(c, info, uid, username) {
				return
			}
			st := vguardStateOf(uid)
			vguardRecordEvent("upstream_weak", uid, username, m, info, c, msg[:min(200, len(msg))], msg)
			vguardCountWeak(st, now)
			return
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------- 管理接口 ----------------

type VGuardStatusItem struct {
	UserId       int    `json:"user_id"`
	Username     string `json:"username"`
	WeakHits     int    `json:"weak_hits"`
	StrongCount  int    `json:"strong_count"`
	BlockCount   int    `json:"block_count"`
	Blocked      bool   `json:"blocked"`
	BlockedUntil int64  `json:"blocked_until"`
	LlmUntil     int64  `json:"llm_until"`
}

func VGuardStatusSnapshot() []VGuardStatusItem {
	now := time.Now().Unix()
	items := make([]VGuardStatusItem, 0, 16)
	vguardStates.Range(func(k, v any) bool {
		uid, ok := k.(int)
		if !ok {
			return true
		}
		st, ok := v.(*VGuardUserState)
		if !ok {
			return true
		}
		st.mu.Lock()
		st.weakHits = vguardPrune(st.weakHits, now-vguardWindowSeconds())
		item := VGuardStatusItem{
			UserId: uid, Username: st.Username,
			WeakHits: len(st.weakHits), StrongCount: st.strongCount,
			BlockCount: st.blockCount, BlockedUntil: st.blockedUntil,
			LlmUntil: st.llmUntil,
			Blocked:  st.blockedUntil > now,
		}
		st.mu.Unlock()
		if item.WeakHits > 0 || item.StrongCount > 0 || item.Blocked {
			items = append(items, item)
		}
		return true
	})
	return items
}

func VGuardUnblock(uid int) bool {
	v, ok := vguardStates.Load(uid)
	if !ok {
		return false
	}
	st, _ := v.(*VGuardUserState)
	st.mu.Lock()
	st.blockedUntil = 0
	st.weakHits = nil
	st.mu.Unlock()
	return true
}
