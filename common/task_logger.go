package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 任务级完整日志（Task Log）
// ------------------------------------------------------------
// 与 prompt_logger 互补：
//   - prompt_logger 只记录请求体（输入）
//   - task_logger   记录 IP + 输入 + 输出（含流式输出）+ 任务级聚合
//
// 设计要点：
//   - 全部异步写文件，不阻塞请求链路
//   - 不截断：输入/输出全量记录（磁盘压力由 3 次/天轮转归档消化）
//   - 任务边界：优先用客户端 metadata.user_id 里的 session_id；
//     无 session_id 时按「同一用户相邻调用间隔 > 15 分钟切新任务」。
//   - 文件：/data/prompt_logs/tasks_YYYYMMDD.jsonl（每次调用一条）
//           /data/prompt_logs/task_index_YYYYMMDD.jsonl（每任务一条聚合）
// ============================================================

const (
	// TaskLogStateContextKey 任务日志状态在 gin context 中的 key
	TaskLogStateContextKey = "task_logger_state"

	// TaskIdleTimeout 会话无新调用超过该时长，视为一次任务结束（聚合落盘）
	TaskIdleTimeout = 15 * time.Minute

	// taskIndexFlushInterval 聚合落盘扫描间隔
	taskIndexFlushInterval = 60 * time.Second
)

var (
	taskLogDir = func() string {
		if d := os.Getenv("TASK_LOG_DIR"); d != "" {
			return d
		}
		return "/data/prompt_logs"
	}()

	taskLogEnabled = func() bool {
		return os.Getenv("TASK_LOG_ENABLED") != "0"
	}()

	// taskLogBlockedUsers 不记录日志的用户（可扩展：逗号分隔的用户名或用户ID）。
	// 通过环境变量 TASK_LOG_BLOCKED_USERS 配置，例如 "mianfei,1002,test_user"。
	// 命中即整体跳过该用户的输入/输出/聚合记录，不落任何文件。
	taskLogBlockedUsers = func() map[string]struct{} {
		set := make(map[string]struct{})
		raw := os.Getenv("TASK_LOG_BLOCKED_USERS")
		if raw == "" {
			return set
		}
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				set[p] = struct{}{}
			}
		}
		return set
	}()

	// taskLogWriteMu 序列化文件写入
	taskLogWriteMu sync.Mutex
)

// taskLogBlocked 是否命中不记录日志的黑名单（支持用户名或用户ID）。
func taskLogBlocked(userID int, username string) bool {
	if len(taskLogBlockedUsers) == 0 {
		return false
	}
	if _, ok := taskLogBlockedUsers[username]; ok {
		return true
	}
	if _, ok := taskLogBlockedUsers[strconv.Itoa(userID)]; ok {
		return true
	}
	return false
}

// TaskLogState 单次请求的日志状态，挂在 gin context 上，随请求生命周期存活
type TaskLogState struct {
	mu sync.Mutex

	StartTime time.Time
	IP        string
	UserID    int
	Username  string
	Model     string
	TokenName string
	Path      string
	RequestID string
	SessionID string
	DeviceID  string
	TaskID    string
	Stream    bool
	// CaptureOutput 是否记录输出（文本格式 true；图片/音频/realtime 等二进制大响应 false）
	CaptureOutput bool

	Input     []byte
	Output    strings.Builder
	OutputSeg int64 // 已写入的段数（>0 表示输出非空）
}

// GetClientIP 获取客户端真实 IP（gin 默认信任代理，会读 X-Forwarded-For）
func GetClientIP(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return c.ClientIP()
}

// --- 文件写入（异步，仿 prompt_logger） ---

func writeTaskLogLine(prefix string, line []byte) {
	taskLogWriteMu.Lock()
	defer taskLogWriteMu.Unlock()

	dateStr := time.Now().Format("20060102")
	filePath := filepath.Join(taskLogDir, fmt.Sprintf("%s_%s.jsonl", prefix, dateStr))
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		SysLog(fmt.Sprintf("failed to open task log file: %v", err))
		return
	}
	defer f.Close()

	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		SysLog(fmt.Sprintf("failed to write task log: %v", err))
	}
}

// extractSessionMeta 从请求体 metadata.user_id（JSON 字符串，内含 JSON 对象）解析 session_id / device_id
func extractSessionMeta(body []byte) (sessionID, deviceID string) {
	if len(body) == 0 {
		return "", ""
	}
	v := gjson.GetBytes(body, "metadata.user_id")
	if !v.Exists() || v.Type != gjson.String {
		return "", ""
	}

	type metaT struct {
		DeviceID  string `json:"device_id"`
		SessionID string `json:"session_id"`
	}

	raw := strings.TrimSpace(v.String())
	// 客户端通常直接把 JSON 对象作为字符串值；少数会再包一层字符串
	tryParse := func(s string) bool {
		var m metaT
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return false
		}
		sessionID, deviceID = m.SessionID, m.DeviceID
		return sessionID != "" || deviceID != ""
	}

	if strings.HasPrefix(raw, "{") {
		tryParse(raw)
		return
	}
	// 尝试二次反序列化（字符串里套字符串）
	var inner string
	if err := json.Unmarshal([]byte(raw), &inner); err == nil && strings.HasPrefix(strings.TrimSpace(inner), "{") {
		tryParse(inner)
	}
	return
}

// --- 任务 ID：优先 session_id，否则按空闲窗口切分 ---

type taskWindowEntry struct {
	TaskID string
	LastTS int64
}

var (
	taskWindowMu sync.Mutex
	taskWindow   = map[int]taskWindowEntry{} // userID -> 最近一次时间窗任务
	taskSeq      uint64
)

func getOrCreateTaskID(userID int, sessionID string, now time.Time) string {
	if sessionID != "" {
		return sessionID
	}
	taskWindowMu.Lock()
	defer taskWindowMu.Unlock()
	ent, ok := taskWindow[userID]
	if ok && now.Sub(time.Unix(ent.LastTS, 0)) < TaskIdleTimeout {
		ent.LastTS = now.Unix()
		taskWindow[userID] = ent
		return ent.TaskID
	}
	taskSeq++
	newID := fmt.Sprintf("task_%d_%d_%s", userID, now.Unix(), GetRandomString(8))
	taskWindow[userID] = taskWindowEntry{TaskID: newID, LastTS: now.Unix()}
	return newID
}

// isTextOutputFormat 判断请求是否为文本格式（记录输出）：
// 图片生成 / 语音合成 / 转写 / realtime 等二进制大响应不记录输出。
func isTextOutputFormat(c *gin.Context) bool {
	path := ""
	if c != nil && c.Request != nil {
		path = c.Request.URL.Path
	}
	if strings.Contains(path, "/images/") ||
		strings.Contains(path, "/audio/") ||
		strings.Contains(path, "/realtime") ||
		strings.Contains(path, "/v1/audio/") {
		return false
	}
	return true
}

// InitTaskLog 请求侧初始化：解析 IP / session / device / task 并挂到 context
// 在每次请求（含重试）进入转发前调用；重试会覆盖为最后一次尝试的状态
func InitTaskLog(c *gin.Context, bodyBytes []byte) {
	if c == nil || !taskLogEnabled {
		return
	}
	// 黑名单用户：整体跳过，不初始化日志状态（后续所有写文件调用自动跳过）。
	if taskLogBlocked(c.GetInt("id"), c.GetString("username")) {
		return
	}
	sessionID, deviceID := extractSessionMeta(bodyBytes)
	now := time.Now()
	userID := c.GetInt("id")

	st := &TaskLogState{
		StartTime:     now,
		IP:            GetClientIP(c),
		UserID:        userID,
		Username:      c.GetString("username"),
		Model:         c.GetString("original_model"),
		TokenName:     c.GetString("token_name"),
		Path:          c.Request.URL.Path,
		RequestID:     c.GetString(RequestIdKey),
		SessionID:     sessionID,
		DeviceID:      deviceID,
		TaskID:        getOrCreateTaskID(userID, sessionID, now),
		Stream:        c.GetBool("is_stream"),
		CaptureOutput: isTextOutputFormat(c),
		Input:         bodyBytes,
	}
	c.Set(TaskLogStateContextKey, st)

	ensureTaskLogFlusher()
}

// AppendTaskLogOutput 累计输出（写回客户端前调用）。
// 非流式：完整响应体一次追加；流式：每个 SSE 块追加一次，换行分隔。
// 仅当该请求格式允许记录输出（文本）时生效。
func AppendTaskLogOutput(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 || !taskLogEnabled {
		return
	}
	val, ok := c.Get(TaskLogStateContextKey)
	if !ok {
		return
	}
	st, ok := val.(*TaskLogState)
	if !ok || st == nil || !st.CaptureOutput {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.OutputSeg > 0 {
		st.Output.WriteByte('\n')
	}
	st.Output.Write(data)
	st.OutputSeg++
}

// FinalizeTaskLog 请求结束（defer）时落盘调用记录，并更新任务级聚合
func FinalizeTaskLog(c *gin.Context, status, errMsg string) {
	if c == nil || !taskLogEnabled {
		return
	}
	val, ok := c.Get(TaskLogStateContextKey)
	if !ok {
		return
	}
	st, ok := val.(*TaskLogState)
	if !ok || st == nil {
		return
	}
	c.Set(TaskLogStateContextKey, nil)

	// 记录完整输出（含二进制未捕获时为空串）
	output := ""
	if st.CaptureOutput {
		st.mu.Lock()
		output = st.Output.String()
		st.mu.Unlock()
	}
	input := string(st.Input)

	promptT, compT, totalT := extractUsageFromOutput(output)

	entry := taskLogCallEntry{
		Timestamp:        st.StartTime.Unix(),
		UserID:           st.UserID,
		Username:         st.Username,
		IP:               st.IP,
		Model:            st.Model,
		TokenName:        st.TokenName,
		Path:             st.Path,
		RequestID:        st.RequestID,
		TaskID:           st.TaskID,
		SessionID:        st.SessionID,
		DeviceID:         st.DeviceID,
		Stream:           st.Stream,
		Status:           status,
		Error:            errMsg,
		LatencyMs:        time.Since(st.StartTime).Milliseconds(),
		PromptTokens:     promptT,
		CompletionTokens: compT,
		TotalTokens:      totalT,
		Input:            input,
		Output:           output,
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	go writeTaskLogLine("tasks", line)

	updateTaskAggregate(st, status, promptT, compT, totalT)
}

type taskLogCallEntry struct {
	Timestamp        int64  `json:"ts"`
	UserID           int    `json:"uid"`
	Username         string `json:"user"`
	IP               string `json:"ip"`
	Model            string `json:"model"`
	TokenName        string `json:"token"`
	Path             string `json:"path"`
	RequestID        string `json:"rid"`
	TaskID           string `json:"task_id"`
	SessionID        string `json:"session_id,omitempty"`
	DeviceID         string `json:"device_id,omitempty"`
	Stream           bool   `json:"stream"`
	Status           string `json:"status"`
	Error            string `json:"error,omitempty"`
	LatencyMs        int64  `json:"latency_ms"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	Input            string `json:"input"`
	Output           string `json:"output"`
}

// extractUsageFromOutput 从输出里提取 token 用量。
// 兼容 OpenAI（usage.prompt_tokens/completion_tokens/total_tokens）与
// Claude（usage.input_tokens/output_tokens）两种格式。流式取最后一段（通常含 usage）。
func extractUsageFromOutput(output string) (prompt, completion, total int64) {
	if output == "" {
		return 0, 0, 0
	}
	// 取最后一个非空行（流式最后一段通常带 usage）
	lines := strings.Split(output, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			last = t
			break
		}
	}
	if last == "" {
		return 0, 0, 0
	}
	u := gjson.GetBytes([]byte(last), "usage")
	if !u.Exists() {
		return 0, 0, 0
	}
	prompt = u.Get("prompt_tokens").Int()
	completion = u.Get("completion_tokens").Int()
	total = u.Get("total_tokens").Int()
	if prompt == 0 && completion == 0 && total == 0 {
		// Claude 格式
		inT := u.Get("input_tokens").Int()
		outT := u.Get("output_tokens").Int()
		if inT > 0 || outT > 0 {
			prompt, completion = inT, outT
			if total == 0 {
				total = inT + outT
			}
		}
	}
	return
}

// --- 任务级聚合（task_index_YYYYMMDD.jsonl） ---

type taskIndexEntry struct {
	TaskID           string         `json:"task_id"`
	UserID           int            `json:"uid"`
	Username         string         `json:"user"`
	SessionID        string         `json:"session_id,omitempty"`
	DeviceID         string         `json:"device_id,omitempty"`
	FirstTS          int64          `json:"first_ts"`
	LastTS           int64          `json:"last_ts"`
	CallCount        int            `json:"calls"`
	PromptTokens     int64          `json:"prompt_tokens"`
	CompletionTokens int64          `json:"completion_tokens"`
	TotalTokens      int64          `json:"total_tokens"`
	Models           map[string]int `json:"models"`
	Statuses         map[string]int `json:"statuses"`
}

var (
	taskAggMu   sync.Mutex
	taskAggs    = map[string]*taskIndexEntry{}
	taskFlusher sync.Once
)

func ensureTaskLogFlusher() {
	taskFlusher.Do(func() {
		go func() {
			ticker := time.NewTicker(taskIndexFlushInterval)
			defer ticker.Stop()
			for range ticker.C {
				flushIdleTasks()
			}
		}()
	})
}

func updateTaskAggregate(st *TaskLogState, status string, promptT, compT, totalT int64) {
	if st == nil {
		return
	}
	now := st.StartTime.Unix()
	taskAggMu.Lock()
	defer taskAggMu.Unlock()
	agg, ok := taskAggs[st.TaskID]
	if !ok {
		agg = &taskIndexEntry{
			TaskID:    st.TaskID,
			UserID:    st.UserID,
			Username:  st.Username,
			SessionID: st.SessionID,
			DeviceID:  st.DeviceID,
			FirstTS:   now,
			Models:    map[string]int{},
			Statuses:  map[string]int{},
		}
		taskAggs[st.TaskID] = agg
	}
	if agg.Username == "" && st.Username != "" {
		agg.Username = st.Username
	}
	agg.LastTS = now
	agg.CallCount++
	agg.PromptTokens += promptT
	agg.CompletionTokens += compT
	agg.TotalTokens += totalT
	if st.Model != "" {
		agg.Models[st.Model]++
	}
	agg.Statuses[status]++
}

func flushIdleTasks() {
	now := time.Now()
	taskAggMu.Lock()
	var toFlush []*taskIndexEntry
	for id, agg := range taskAggs {
		if now.Sub(time.Unix(agg.LastTS, 0)) > TaskIdleTimeout {
			toFlush = append(toFlush, agg)
			delete(taskAggs, id)
		}
	}
	taskAggMu.Unlock()

	for _, agg := range toFlush {
		line, err := json.Marshal(agg)
		if err != nil {
			continue
		}
		writeTaskLogLine("task_index", line)
	}
}
