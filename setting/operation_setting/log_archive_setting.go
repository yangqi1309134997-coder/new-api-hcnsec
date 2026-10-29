package operation_setting

import (
	"github.com/QuantumNous/new-api/setting/config"
)

// LogArchiveSetting 历史日志归档：按天导出 zstd 压缩的 NDJSON 归档后从数据库删除。
// 消费日志热保留 LogHotDays 个自然天，违规事件热保留 VGuardHotDays 个自然天，
// 归档文件统一保留 KeepDays 天。通过 GlobalConfig 注册，保存选项后热生效；
// 调度器每小时检查一次，每次最多推进每个表一个自然天。
type LogArchiveSetting struct {
	Enabled bool `json:"enabled"`
	// LogHotDays 消费日志（logs 表）数据库热保留天数。
	LogHotDays int `json:"log_hot_days"`
	// VGuardHotDays 违规事件（v_guard_events 表）数据库热保留天数。
	VGuardHotDays int `json:"vguard_hot_days"`
	// KeepDays 归档文件保留天数，超期文件在任务收尾时自动删除。
	KeepDays int `json:"keep_days"`
	// Dir 归档输出目录；为空时使用工作目录下的 logs/archive。
	Dir string `json:"dir"`
}

const (
	defaultLogArchiveLogHotDays    = 7
	defaultLogArchiveVGuardHotDays = 30
	defaultLogArchiveKeepDays      = 180
)

var logArchiveSetting = LogArchiveSetting{
	LogHotDays:    defaultLogArchiveLogHotDays,
	VGuardHotDays: defaultLogArchiveVGuardHotDays,
	KeepDays:      defaultLogArchiveKeepDays,
}

func init() {
	config.GlobalConfig.Register("log_archive_setting", &logArchiveSetting)
}

// GetLogArchiveSetting 返回归一化后的日志归档设置。
func GetLogArchiveSetting() *LogArchiveSetting {
	logArchiveSetting.LogHotDays = clampInt(logArchiveSetting.LogHotDays, 1, 365, defaultLogArchiveLogHotDays)
	logArchiveSetting.VGuardHotDays = clampInt(logArchiveSetting.VGuardHotDays, 1, 3650, defaultLogArchiveVGuardHotDays)
	logArchiveSetting.KeepDays = clampInt(logArchiveSetting.KeepDays, 1, 3650, defaultLogArchiveKeepDays)
	return &logArchiveSetting
}
