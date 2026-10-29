package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withLogArchiveSettings(t *testing.T, mutate func(*operation_setting.LogArchiveSetting)) {
	t.Helper()
	setting := operation_setting.GetLogArchiveSetting()
	previous := *setting
	mutate(setting)
	t.Cleanup(func() { *setting = previous })
}

func TestArchiveWindowToFileVerifyAndReuse(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "logs-2026-01-01.ndjson.zst")
	exportCalls := 0
	export := func(ctx context.Context, path string, total int64, report archiveReportFunc) (int64, error) {
		exportCalls++
		w, err := openNDJSONZst(path)
		require.NoError(t, err)
		for i := range int(total) {
			require.NoError(t, w.writeLine([]byte(fmt.Sprintf(`{"id":%d}`, i+1))))
		}
		require.NoError(t, w.close())
		return int64(total), nil
	}
	report := func(processed, total int64) bool { return true }

	written, err := archiveWindowToFile(context.Background(), finalPath, 3, export, report)
	require.NoError(t, err)
	assert.EqualValues(t, 3, written)
	assert.FileExists(t, finalPath)
	assert.NoFileExists(t, finalPath+".tmp")
	assert.Equal(t, 1, exportCalls, "首次导出落盘")

	// 同名归档行数一致或覆盖窗口（删除中断后续跑）时直接复用，不重复导出。
	written, err = archiveWindowToFile(context.Background(), finalPath, 3, export, report)
	require.NoError(t, err)
	assert.EqualValues(t, 3, written)
	assert.Equal(t, 1, exportCalls, "行数一致应复用已有归档")
	written, err = archiveWindowToFile(context.Background(), finalPath, 2, export, report)
	require.NoError(t, err)
	assert.EqualValues(t, 2, written)
	assert.Equal(t, 1, exportCalls, "归档覆盖窗口（部分行已删）应复用而非重导出")

	// 行数少于窗口（窗口内容变化）时重新导出覆盖。
	written, err = archiveWindowToFile(context.Background(), finalPath, 5, export, report)
	require.NoError(t, err)
	assert.EqualValues(t, 5, written)
	assert.Equal(t, 2, exportCalls, "归档覆盖不足应重新导出")
	lines, err := countNDJSONZstLines(finalPath)
	require.NoError(t, err)
	assert.EqualValues(t, 5, lines)

	// 导出行数与计数不符时报错且不落最终文件。
	broken := func(ctx context.Context, path string, total int64, report archiveReportFunc) (int64, error) {
		w, err := openNDJSONZst(path)
		require.NoError(t, err)
		require.NoError(t, w.writeLine([]byte(`{"id":1}`)))
		require.NoError(t, w.close())
		return 1, nil
	}
	freshPath := filepath.Join(dir, "logs-2026-01-02.ndjson.zst")
	_, err = archiveWindowToFile(context.Background(), freshPath, 4, broken, report)
	require.Error(t, err)
	assert.NoFileExists(t, freshPath)
	assert.NoFileExists(t, freshPath+".tmp")
}

func TestExportNDJSONZstAndPurgeExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	path := filepath.Join(dir, "logs-"+now.AddDate(0, 0, -1).Format("2006-01-02")+".ndjson.zst")
	report := func(processed, total int64) bool { return true }
	// 每批 2 行，共 5 行，验证游标分批与进度回调。
	fetchCalls := 0
	written, err := exportNDJSONZst(context.Background(), path, 5, report, func(afterID int64) ([]any, int64, error) {
		fetchCalls++
		switch afterID {
		case 0:
			return []any{map[string]any{"id": 1}, map[string]any{"id": 2}}, int64(2), nil
		case 2:
			return []any{map[string]any{"id": 3}, map[string]any{"id": 4}, map[string]any{"id": 5}}, int64(5), nil
		case 5:
			return []any{}, int64(0), nil
		default:
			return nil, 0, fmt.Errorf("unexpected cursor %d", afterID)
		}
	})
	require.NoError(t, err)
	assert.EqualValues(t, 5, written)
	assert.Equal(t, 3, fetchCalls)
	lines, err := countNDJSONZstLines(path)
	require.NoError(t, err)
	assert.EqualValues(t, 5, lines)

	// 过期文件清理：旧归档删除、新归档保留、遗留 tmp 清除。
	oldName := filepath.Join(dir, fmt.Sprintf("logs-%s.ndjson.zst", now.AddDate(0, 0, -200).Format("2006-01-02")))
	require.NoError(t, os.WriteFile(oldName, []byte("x"), 0o644))
	staleTmp := filepath.Join(dir, "vguard-events-2020-01-01.ndjson.zst.tmp")
	require.NoError(t, os.WriteFile(staleTmp, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(staleTmp, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))

	purged, err := purgeExpiredArchiveFiles(dir, now, 180)
	require.NoError(t, err)
	assert.Equal(t, 2, purged)
	assert.NoFileExists(t, oldName)
	assert.NoFileExists(t, staleTmp)
	assert.FileExists(t, path)
}

func TestArchiveWindowsEligibilityAndRoundtrip(t *testing.T) {
	withLogArchiveSettings(t, func(setting *operation_setting.LogArchiveSetting) {
		setting.Enabled = true
		setting.LogHotDays = 7
		setting.VGuardHotDays = 30
		setting.KeepDays = 180
		setting.Dir = t.TempDir()
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.VGuardEvent{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	ctx := context.Background()
	now := time.Now()
	mkTime := func(daysAgo int) int64 {
		d := now.AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, d.Location()).Unix()
	}
	logsOld := []*model.Log{{UserId: 1, CreatedAt: mkTime(9), Type: 2, Content: "old-a"}, {UserId: 1, CreatedAt: mkTime(8), Type: 2, Content: "old-b"}}
	logsHot := []*model.Log{{UserId: 1, CreatedAt: mkTime(1), Type: 2, Content: "hot"}}
	eventsOld := []*model.VGuardEvent{{UserId: 1, Username: "u", Kind: "weak", CreatedAt: mkTime(40)}}
	eventsHot := []*model.VGuardEvent{{UserId: 1, Username: "u", Kind: "weak", CreatedAt: mkTime(2)}}
	require.NoError(t, db.Create(&logsOld).Error)
	require.NoError(t, db.Create(&logsHot).Error)
	require.NoError(t, db.Create(&eventsOld).Error)
	require.NoError(t, db.Create(&eventsHot).Error)

	windows := logArchiveWindows(ctx, "", operation_setting.GetLogArchiveSetting())
	require.Len(t, windows, 2, "logs 与 vguard-events 各取一个最旧待归档日")
	byTable := map[string]archiveTableWindow{}
	for _, w := range windows {
		byTable[w.table] = w
	}

	logCount, err := byTable["logs"].count(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, logCount, "logs 每次只归档最旧的待归档日（9 天前那天）")
	eventCount, err := byTable["vguard-events"].count(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, eventCount, "vguard 30 天热保留，只归档 40 天前那天")

	for _, w := range windows {
		count, err := w.count(ctx)
		require.NoError(t, err)
		finalPath := filepath.Join(t.TempDir(), w.table+"-"+w.dayStr+".ndjson.zst")
		exported, err := archiveWindowToFile(ctx, finalPath, count, w.export, nil)
		require.NoError(t, err)
		lines, err := countNDJSONZstLines(finalPath)
		require.NoError(t, err)
		assert.Equal(t, count, lines, "归档行数与导出行数一致")
		deleted, err := deleteArchiveWindow(ctx, w.remove, nil)
		require.NoError(t, err)
		assert.Equal(t, exported, deleted)
	}

	var logRest int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logRest).Error)
	assert.EqualValues(t, 2, logRest, "仅删除已归档日；8 天前那天与热保留行留下")
	var eventRest int64
	require.NoError(t, db.Model(&model.VGuardEvent{}).Count(&eventRest).Error)
	assert.EqualValues(t, 1, eventRest, "30 天内违规事件保留")
}
