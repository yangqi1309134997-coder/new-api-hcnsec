package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// PromptLogEntry stores a single prompt log entry
type PromptLogEntry struct {
	Timestamp     int64  `json:"ts"`
	UserID        int    `json:"uid"`
	Username      string `json:"user"`
	IP            string `json:"ip,omitempty"`
	Model         string `json:"model"`
	TokenName     string `json:"token"`
	Path          string `json:"path"`
	RequestID     string `json:"rid"`
	TaskID        string `json:"task_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	Body          string `json:"body,omitempty"`
	BodySizeBytes int64  `json:"size"`
}

var (
	promptLogFile *os.File
	promptLogMu   sync.Mutex
	promptLogDir  = "/data/prompt_logs"
)

func initPromptLogger() {
	if err := os.MkdirAll(promptLogDir, 0755); err != nil {
		common.SysLog(fmt.Sprintf("failed to create prompt log dir: %v", err))
		return
	}
}

// LogRequestBody asynchronously logs the request body to a file
func LogRequestBody(c *gin.Context, bodyBytes []byte) {
	// 环境变量开关: 默认关闭(prompts 数据与 tasks 重复), 设 PROMPT_LOG_ENABLED=true 恢复
	if os.Getenv("PROMPT_LOG_ENABLED") != "true" {
		return
	}

	if len(bodyBytes) == 0 {
		return
	}

	// Ensure directory exists
	initPromptLogger()

	// 从任务级日志状态里带出 IP / session / device / task（若已初始化）
	var ip, sessionID, deviceID, taskID string
	if val, ok := c.Get(common.TaskLogStateContextKey); ok {
		if st, sok := val.(*common.TaskLogState); sok && st != nil {
			ip = st.IP
			sessionID = st.SessionID
			deviceID = st.DeviceID
			taskID = st.TaskID
		}
	}

	entry := PromptLogEntry{
		Timestamp:     time.Now().Unix(),
		UserID:        c.GetInt("id"),
		Username:      c.GetString("username"),
		IP:            ip,
		Model:         c.GetString("original_model"),
		TokenName:     c.GetString("token_name"),
		Path:          c.Request.URL.Path,
		RequestID:     c.GetString(common.RequestIdKey),
		TaskID:        taskID,
		SessionID:     sessionID,
		DeviceID:      deviceID,
		BodySizeBytes: int64(len(bodyBytes)),
	}

	// Truncate body if too large (>64KB) to save space
	if len(bodyBytes) > 65536 {
		entry.Body = string(bodyBytes[:65536]) + "...[truncated]"
	} else {
		entry.Body = string(bodyBytes)
	}

	// Async write
	go func() {
		promptLogMu.Lock()
		defer promptLogMu.Unlock()

		// Open file in append mode, create if not exists
		dateStr := time.Now().Format("20060102")
		filePath := filepath.Join(promptLogDir, fmt.Sprintf("prompts_%s.jsonl", dateStr))

		f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			common.SysLog(fmt.Sprintf("failed to open prompt log file: %v", err))
			return
		}
		defer f.Close()

		line, err := json.Marshal(entry)
		if err != nil {
			return
		}
		line = append(line, '\n')
		f.Write(line)
	}()
}
