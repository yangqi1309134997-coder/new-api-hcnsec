package common

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestTaskAggregateFlush 验证任务级聚合：updateTaskAggregate 累计 + flushIdleTasks 落盘 task_index
func TestTaskAggregateFlush(t *testing.T) {
	dir, _ := os.MkdirTemp("", "tasklogtest")
	defer os.RemoveAll(dir)
	taskLogDir = dir

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	st := &TaskLogState{
		StartTime: time.Now(),
		IP:        "1.2.3.4",
		UserID:    42,
		Username:  "tester",
		Model:     "gpt-test",
		TokenName: "tk",
		Path:      "/v1/chat/completions",
		RequestID: "rid1",
		TaskID:    "sess-agg",
		SessionID: "sess-agg",
		DeviceID:  "dev-1",
		Stream:    true,
	}
	c.Set(TaskLogStateContextKey, st)

	updateTaskAggregate(st, "ok", 100, 50, 150)
	updateTaskAggregate(st, "ok", 200, 100, 300)

	// 强制把 LastTS 调旧，触发 flush
	taskAggMu.Lock()
	agg := taskAggs["sess-agg"]
	agg.LastTS = time.Now().Add(-TaskIdleTimeout - time.Minute).Unix()
	taskAggMu.Unlock()

	flushIdleTasks()

	// 检查 task_index 文件
	files, err := filepath.Glob(filepath.Join(dir, "task_index_*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("task_index file not created: %v", err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	line := string(data)
	if line == "" {
		t.Fatal("empty task_index line")
	}
	var got taskIndexEntry
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CallCount != 2 || got.TotalTokens != 450 || got.PromptTokens != 300 {
		t.Fatalf("aggregate mismatch: %+v", got)
	}
	if got.Models["gpt-test"] != 2 || got.Statuses["ok"] != 2 {
		t.Fatalf("maps mismatch: %+v", got)
	}
	t.Logf("aggregate OK: calls=%d pt=%d ct=%d tot=%d", got.CallCount, got.PromptTokens, got.CompletionTokens, got.TotalTokens)
}
