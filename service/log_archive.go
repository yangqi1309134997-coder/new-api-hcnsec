package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/klauspost/compress/zstd"
)

// 历史日志按天归档：把超过热保留天数的完整自然天导出为 zstd 压缩的
// NDJSON 文件，核对行数一致后再从数据库删除；归档文件超过 KeepDays 天自动清除。
// 消费日志（logs）与违规事件（v_guard_events）各自按独立的热保留天数轮转。
// 调度器每小时触发一次，每次最多为每个表推进一个自然天，避免单次任务过长。

const (
	logArchiveLogBatchSize   = 5000
	logArchiveEventBatchSize = 500
	logArchiveDeleteBatch    = 20000
)

var logArchiveFileNameRe = regexp.MustCompile(`^(logs|vguard-events)-(\d{4}-\d{2}-\d{2})\.ndjson\.zst$`)

type LogArchivePayload struct {
	// Day 目标自然天（本地时区 YYYY-MM-DD）；为空时两个表各自自动选择最旧的待归档日。
	Day string `json:"day"`
}

type LogArchiveState struct {
	Day       string `json:"day"`
	Phase     string `json:"phase"`
	Total     int64  `json:"total"`
	Processed int64  `json:"processed"`
	Progress  int    `json:"progress"`
}

type LogArchiveResult struct {
	LogDay         string   `json:"log_day,omitempty"`
	EventDay       string   `json:"event_day,omitempty"`
	ArchiveFiles   []string `json:"archive_files,omitempty"`
	ArchivedLogs   int64    `json:"archived_logs"`
	ArchivedEvents int64    `json:"archived_events"`
	PurgedFiles    int      `json:"purged_files"`
}

type logArchiveHandler struct{}

func (logArchiveHandler) Type() string { return model.SystemTaskTypeLogArchive }

func (logArchiveHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	runLogArchiveTask(ctx, task, runnerID)
}

func (logArchiveHandler) Enabled() bool {
	return operation_setting.GetLogArchiveSetting().Enabled
}

func (logArchiveHandler) Interval() time.Duration { return time.Hour }

func (logArchiveHandler) NewPayload() any { return LogArchivePayload{} }

func init() {
	RegisterSystemTaskHandler(logArchiveHandler{})
}

// archiveReportFunc 推进任务状态；返回 false 表示任务锁已丢失，应中止。
type archiveReportFunc func(processed, total int64) bool

// archiveTableWindow 一个表的一个待归档自然天：计数、导出与分批删除的绑定。
type archiveTableWindow struct {
	table  string
	dayStr string
	count  func(ctx context.Context) (int64, error)
	export func(ctx context.Context, path string, total int64, report archiveReportFunc) (int64, error)
	remove func(ctx context.Context, limit int) (int64, error)
}

func runLogArchiveTask(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := LogArchivePayload{}
	if err := task.DecodePayload(&payload); err != nil {
		failSystemTask(task, runnerID, err)
		return
	}
	setting := operation_setting.GetLogArchiveSetting()
	if !setting.Enabled {
		// 手动创建的任务在开关关闭后直接结束，不做任何删除。
		_ = model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, LogArchiveResult{}, "")
		return
	}
	dir := logArchiveDir(setting.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		failSystemTask(task, runnerID, fmt.Errorf("create archive dir: %w", err))
		return
	}

	state := LogArchiveState{}
	if err := task.DecodeState(&state); err != nil {
		failSystemTask(task, runnerID, err)
		return
	}
	saveState := func(day string, phase string, processed, total int64) bool {
		state.Day = day
		state.Phase = phase
		state.Processed = processed
		state.Total = total
		if total > 0 {
			state.Progress = int(processed * 100 / total)
		} else {
			state.Progress = 100
		}
		if err := model.UpdateSystemTaskState(task.TaskID, runnerID, state); err != nil {
			logSystemTaskLockError(ctx, task, err)
			return false
		}
		return true
	}

	result := LogArchiveResult{ArchiveFiles: []string{}}
	for _, w := range logArchiveWindows(ctx, payload.Day, setting) {
		day := w.dayStr
		count, err := w.count(ctx)
		if err != nil {
			failSystemTask(task, runnerID, fmt.Errorf("count %s %s: %w", w.table, day, err))
			return
		}
		if count == 0 {
			continue
		}
		if !saveState(day, "export_"+w.table, 0, count) {
			return
		}
		finalPath := filepath.Join(dir, w.table+"-"+day+".ndjson.zst")
		exported, err := archiveWindowToFile(ctx, finalPath, count, w.export, func(p, t int64) bool {
			return saveState(day, "export_"+w.table, p, t)
		})
		if err != nil {
			failSystemTask(task, runnerID, fmt.Errorf("archive %s %s: %w", w.table, day, err))
			return
		}
		result.ArchiveFiles = append(result.ArchiveFiles, finalPath)
		if w.table == "logs" {
			result.LogDay = day
			result.ArchivedLogs = exported
		} else {
			result.EventDay = day
			result.ArchivedEvents = exported
		}
		if !saveState(day, "delete_"+w.table, 0, exported) {
			return
		}
		deleted, err := deleteArchiveWindow(ctx, w.remove, func(processed int64) bool {
			return saveState(day, "delete_"+w.table, processed, exported)
		})
		if err != nil {
			failSystemTask(task, runnerID, fmt.Errorf("delete %s %s: %w", w.table, day, err))
			return
		}
		logger.LogInfo(ctx, common.LogText("log archive: removed %d rows from %s for day %s, archive %s", deleted, w.table, day, finalPath))
	}

	purged, err := purgeExpiredArchiveFiles(dir, time.Now(), setting.KeepDays)
	if err != nil {
		logger.LogWarn(ctx, common.LogText("log archive: purge expired files failed: %s", err.Error()))
	}
	result.PurgedFiles = purged
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, result, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
}

// logArchiveWindows 组装本次要处理的表窗口：每个表最多一个最旧的待归档日。
func logArchiveWindows(ctx context.Context, dayStr string, setting *operation_setting.LogArchiveSetting) []archiveTableWindow {
	now := time.Now()
	today0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	requested := time.Time{}
	if dayStr != "" {
		d, err := time.ParseInLocation("2006-01-02", dayStr, now.Location())
		if err != nil {
			return nil
		}
		requested = d
	}
	dayEligible := func(d time.Time, hotDays int) bool {
		return !d.IsZero() && !d.After(today0.AddDate(0, 0, -hotDays))
	}

	logDay, eventDay := time.Time{}, time.Time{}
	if !requested.IsZero() {
		if dayEligible(requested, setting.LogHotDays) {
			logDay = requested
		}
		if dayEligible(requested, setting.VGuardHotDays) {
			eventDay = requested
		}
	} else {
		if d, ok := oldestArchivableDay(ctx, setting.LogHotDays, model.MinLogCreatedAt); ok {
			logDay = d
		}
		if d, ok := oldestArchivableDay(ctx, setting.VGuardHotDays, model.MinVGuardEventCreatedAt); ok {
			eventDay = d
		}
	}

	windows := make([]archiveTableWindow, 0, 2)
	if !logDay.IsZero() {
		start, end := logDay.Unix(), logDay.AddDate(0, 0, 1).Unix()
		windows = append(windows, archiveTableWindow{
			table: "logs", dayStr: logDay.Format("2006-01-02"),
			count: func(ctx context.Context) (int64, error) {
				return model.CountLogsBetween(ctx, start, end)
			},
			export: func(ctx context.Context, path string, total int64, report archiveReportFunc) (int64, error) {
				return exportLogsNDJSONZst(ctx, path, start, end, total, report)
			},
			remove: func(ctx context.Context, limit int) (int64, error) {
				return model.DeleteLogsBetweenBatch(ctx, start, end, limit)
			},
		})
	}
	if !eventDay.IsZero() {
		start, end := eventDay.Unix(), eventDay.AddDate(0, 0, 1).Unix()
		windows = append(windows, archiveTableWindow{
			table: "vguard-events", dayStr: eventDay.Format("2006-01-02"),
			count: func(ctx context.Context) (int64, error) {
				return model.CountVGuardEventsBetween(ctx, start, end)
			},
			export: func(ctx context.Context, path string, total int64, report archiveReportFunc) (int64, error) {
				return exportVGuardEventsNDJSONZst(ctx, path, start, end, total, report)
			},
			remove: func(ctx context.Context, limit int) (int64, error) {
				return model.DeleteVGuardEventsBetweenBatch(ctx, start, end, limit)
			},
		})
	}
	return windows
}

// oldestArchivableDay 返回该表最早数据所在自然天；仅当它早于热保留截止线时才可归档。
func oldestArchivableDay(ctx context.Context, hotDays int, minFn func(ctx context.Context) (int64, error)) (time.Time, bool) {
	mn, err := minFn(ctx)
	if err != nil || mn <= 0 {
		return time.Time{}, false
	}
	t := time.Unix(mn, 0)
	day0 := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	now := time.Now()
	today0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if day0.After(today0.AddDate(0, 0, -hotDays)) {
		return time.Time{}, false
	}
	return day0, true
}

// archiveWindowToFile 把窗口数据落到 finalPath：已有同名归档且覆盖行数不少于
// 当前窗口行数时直接复用（导出后删除中断的续跑场景，文件内容是窗口的超集，
// 覆盖会导致已删除行从归档中丢失），否则导出到临时文件、核对行数后原子改名。
func archiveWindowToFile(ctx context.Context, finalPath string, expected int64, export func(ctx context.Context, path string, total int64, report archiveReportFunc) (int64, error), report archiveReportFunc) (int64, error) {
	if n, err := countNDJSONZstLines(finalPath); err == nil && n >= expected {
		return expected, nil
	}
	tmpPath := finalPath + ".tmp"
	_ = os.Remove(tmpPath)
	written, err := export(ctx, tmpPath, expected, report)
	if err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	if written != expected {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("exported %d rows but counted %d before export", written, expected)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	return written, nil
}

func exportLogsNDJSONZst(ctx context.Context, path string, start, end, total int64, report archiveReportFunc) (int64, error) {
	return exportNDJSONZst(ctx, path, total, report, func(afterID int64) ([]any, int64, error) {
		rows, err := model.FindLogsBetweenBatch(ctx, start, end, afterID, logArchiveLogBatchSize)
		if err != nil {
			return nil, 0, err
		}
		out := make([]any, len(rows))
		var lastID int64
		for i, row := range rows {
			out[i] = row
			lastID = int64(row.Id)
		}
		return out, lastID, nil
	})
}

func exportVGuardEventsNDJSONZst(ctx context.Context, path string, start, end, total int64, report archiveReportFunc) (int64, error) {
	return exportNDJSONZst(ctx, path, total, report, func(afterID int64) ([]any, int64, error) {
		rows, err := model.FindVGuardEventsBetweenBatch(ctx, start, end, afterID, logArchiveEventBatchSize)
		if err != nil {
			return nil, 0, err
		}
		out := make([]any, len(rows))
		var lastID int64
		for i, row := range rows {
			out[i] = row
			lastID = row.ID
		}
		return out, lastID, nil
	})
}

// exportNDJSONZst 以 id 游标分批读取行、逐行写 NDJSON 并流式 zstd 压缩。
func exportNDJSONZst(ctx context.Context, path string, total int64, report archiveReportFunc, fetch func(afterID int64) ([]any, int64, error)) (written int64, err error) {
	w, err := openNDJSONZst(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := w.close(); err == nil {
			err = closeErr
		}
	}()
	var afterID int64
	for {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		rows, lastID, err := fetch(afterID)
		if err != nil {
			return written, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			line, err := common.Marshal(row)
			if err != nil {
				return written, err
			}
			if err := w.writeLine(line); err != nil {
				return written, err
			}
			written++
		}
		afterID = lastID
		if report != nil && !report(written, total) {
			return written, fmt.Errorf("archive task state update failed")
		}
	}
	return written, nil
}

// deleteArchiveWindow 反复批量删除直到窗口清空。
func deleteArchiveWindow(ctx context.Context, remove func(ctx context.Context, limit int) (int64, error), report func(processed int64) bool) (int64, error) {
	var deleted int64
	for {
		n, err := remove(ctx, logArchiveDeleteBatch)
		if err != nil {
			return deleted, err
		}
		if n == 0 {
			return deleted, nil
		}
		deleted += n
		if report != nil && !report(deleted) {
			return deleted, fmt.Errorf("archive task state update failed")
		}
	}
}

func logArchiveDir(dir string) string {
	if strings.TrimSpace(dir) != "" {
		return dir
	}
	return filepath.Join("logs", "archive")
}

// purgeExpiredArchiveFiles 删除超过保留期的归档文件与遗留的临时文件。
func purgeExpiredArchiveFiles(dir string, now time.Time, keepDays int) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	today0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	cutoff := today0.AddDate(0, 0, -keepDays)
	purged := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".tmp") {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) > 24*time.Hour {
				if os.Remove(filepath.Join(dir, name)) == nil {
					purged++
				}
			}
			continue
		}
		m := logArchiveFileNameRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", m[2], now.Location())
		if err != nil || day.After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			purged++
		}
	}
	return purged, nil
}

// ---------------- NDJSON zstd 文件 ----------------

type ndjsonZstFile struct {
	f       *os.File
	zw      *zstd.Encoder
	bw      *bufio.Writer
	closeEr error
}

func openNDJSONZst(path string) (*ndjsonZstFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &ndjsonZstFile{f: f, zw: zw, bw: bufio.NewWriter(zw)}, nil
}

func (w *ndjsonZstFile) writeLine(line []byte) error {
	if _, err := w.bw.Write(line); err != nil {
		return err
	}
	return w.bw.WriteByte('\n')
}

func (w *ndjsonZstFile) close() error {
	if err := w.bw.Flush(); err != nil {
		w.closeEr = err
	}
	if err := w.zw.Close(); err != nil && w.closeEr == nil {
		w.closeEr = err
	}
	if err := w.f.Sync(); err != nil && w.closeEr == nil {
		w.closeEr = err
	}
	if err := w.f.Close(); err != nil && w.closeEr == nil {
		w.closeEr = err
	}
	return w.closeEr
}

// countNDJSONZstLines 流式解压统计行数，用于归档文件与导出行数的核对。
func countNDJSONZstLines(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	scanner := bufio.NewScanner(io.Reader(zr))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var n int64
	for scanner.Scan() {
		n++
	}
	return n, scanner.Err()
}
