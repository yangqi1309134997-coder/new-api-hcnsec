package controller

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

// ============================================================================
// 自定义维护任务（原宿主机 cron 定时任务的内置化）
//
// 通过 rc.40 的 system_tasks 框架周期执行（DB 租约去重、后台可见、多实例安全），
// 替代以下宿主机 cron：
//   - billingPrefFixHandler   <- /usr/local/bin/subscription_pref_auto_fix.py (*/5)
//                               <- /usr/local/bin/cron_subscription_pref.sh (每小时+3:30)
//   - subscriptionSweepHandler <- sub_expire_downgrade.sh (每天 0 点)
//   - channelScheduleHandler   <- scripts/disable_night_channels.sh (9:00)
//                               <- scripts/enable_night_channels.sh (23:00)
//
// 配置（option 表，热生效，可用 /api/option/ 或 SQL 设置）：
//   SubscriptionMaintenanceExcludedUsers : 逗号分隔用户ID白名单，默认 "1,70"
//   SubscriptionSweepPaidGroups          : 逗号分隔付费分组，默认 "svip,ultra,vip,pub"
//   SubscriptionSweepDefaultGroup        : 兜底回退分组，默认 "default"
//   NightChannelSchedule                 : JSON
//       {"enabled":true,"start_id":179,"end_id":194,"disable_hour":9,"enable_hour":23}
//
// 环境变量开关（默认均开启）：BILLING_PREF_FIX_TASK_ENABLED / SUBSCRIPTION_SWEEP_TASK_ENABLED
// ============================================================================

// ---- helpers ----

func maintenanceOptionValue(key, fallback string) string {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	if v, ok := common.OptionMap[key]; ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func parseMaintenanceIdList(raw string) []int {
	ids := make([]int, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if id, err := strconv.Atoi(p); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func parseMaintenanceList(raw string) []string {
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// finishCustomSystemTask 统一收尾（成功/失败都写任务记录）
func finishCustomSystemTask(task *model.SystemTask, runnerID string, err error, summary any) {
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// ---- 1) billing_pref_fix：过期订阅偏好回切（替代两份订阅偏好 cron 脚本） ----

type billingPrefFixHandler struct{}

func (billingPrefFixHandler) Type() string { return model.SystemTaskTypeBillingPrefFix }

func (billingPrefFixHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("BILLING_PREF_FIX_TASK_ENABLED", true)
}

func (billingPrefFixHandler) Interval() time.Duration { return 10 * time.Minute }

func (billingPrefFixHandler) NewPayload() any { return nil }

func (billingPrefFixHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	excluded := parseMaintenanceIdList(maintenanceOptionValue("SubscriptionMaintenanceExcludedUsers", "1,70"))
	fixed, err := model.FixStaleBillingPreferences(excluded)
	if err != nil {
		finishCustomSystemTask(task, runnerID, err, nil)
		return
	}
	common.SysLog("[billing_pref_fix] reset stale subscription_only preferences: " + strconv.Itoa(fixed))
	finishCustomSystemTask(task, runnerID, nil, map[string]any{"fixed": fixed})
}

// ---- 2) subscription_sweep：到期订阅置 expired + 分组降级兜底（替代 sub_expire_downgrade.sh） ----

type subscriptionSweepHandler struct{}

func (subscriptionSweepHandler) Type() string { return model.SystemTaskTypeSubscriptionSweep }

func (subscriptionSweepHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("SUBSCRIPTION_SWEEP_TASK_ENABLED", true)
}

func (subscriptionSweepHandler) Interval() time.Duration { return time.Hour }

func (subscriptionSweepHandler) NewPayload() any { return nil }

func (subscriptionSweepHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	excluded := parseMaintenanceIdList(maintenanceOptionValue("SubscriptionMaintenanceExcludedUsers", "1,70"))
	paidGroups := parseMaintenanceList(maintenanceOptionValue("SubscriptionSweepPaidGroups", "svip,ultra,vip,pub"))
	defaultGroup := maintenanceOptionValue("SubscriptionSweepDefaultGroup", "default")
	result, err := model.SweepExpiredUserSubscriptions(excluded, paidGroups, defaultGroup)
	if err != nil {
		finishCustomSystemTask(task, runnerID, err, nil)
		return
	}
	common.SysLog("[subscription_sweep] expired=" + strconv.Itoa(result.ExpiredCount) +
		" group_restored=" + strconv.Itoa(result.GroupRestored))
	finishCustomSystemTask(task, runnerID, nil, map[string]any{
		"expired_count":  result.ExpiredCount,
		"group_restored": result.GroupRestored,
	})
}

// ---- 3) channel_schedule：夜间渠道定时启停（替代 disable/enable_night_channels.sh） ----

// NightChannelScheduleConfig NightChannelSchedule option 的 JSON 结构
type NightChannelScheduleConfig struct {
	Enabled     bool `json:"enabled"`
	StartID     int  `json:"start_id"`
	EndID       int  `json:"end_id"`
	DisableHour int  `json:"disable_hour"` // 该时刻起禁用（status=2），如 9
	EnableHour  int  `json:"enable_hour"`  // 该时刻起启用（status=1），如 23
}

func (c *NightChannelScheduleConfig) normalized() *NightChannelScheduleConfig {
	if c.StartID <= 0 {
		c.StartID = 179
	}
	if c.EndID < c.StartID {
		c.EndID = c.StartID
	}
	if c.DisableHour < 0 || c.DisableHour > 23 {
		c.DisableHour = 9
	}
	if c.EnableHour < 0 || c.EnableHour > 23 {
		c.EnableHour = 23
	}
	return c
}

// desiredEnabled 按当前小时判断渠道应处于启用还是禁用状态。
// 支持 disable_hour < enable_hour（白天禁用）与跨零点窗口两种配置。
func (c *NightChannelScheduleConfig) desiredEnabled(hour int) bool {
	if c.DisableHour == c.EnableHour {
		return true
	}
	if c.DisableHour < c.EnableHour {
		// 窗口 [disable, enable) 内禁用，例如 9~23 禁用
		return hour < c.DisableHour || hour >= c.EnableHour
	}
	// 跨零点：例如 23 启用、9 禁用（禁用窗口 9~23，启用窗口 23~次日9）
	return hour >= c.EnableHour && hour < c.DisableHour
}

type channelScheduleHandler struct{}

func (channelScheduleHandler) Type() string { return model.SystemTaskTypeChannelSchedule }

func (channelScheduleHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("CHANNEL_SCHEDULE_TASK_ENABLED", true)
}

func (channelScheduleHandler) Interval() time.Duration { return time.Hour }

func (channelScheduleHandler) NewPayload() any { return nil }

func (channelScheduleHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	cfg := &NightChannelScheduleConfig{}
	if raw := maintenanceOptionValue("NightChannelSchedule", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), cfg); err != nil {
			finishCustomSystemTask(task, runnerID, err, nil)
			return
		}
	}
	cfg.normalized()
	if !cfg.Enabled {
		finishCustomSystemTask(task, runnerID, nil, map[string]any{"skipped": "disabled"})
		return
	}

	hour := time.Now().In(time.Local).Hour()
	desiredEnabled := cfg.desiredEnabled(hour)

	changed := 0
	for id := cfg.StartID; id <= cfg.EndID; id++ {
		channel, err := model.GetChannelById(id, true)
		if err != nil || channel == nil {
			continue
		}
		if desiredEnabled {
			if channel.Status == common.ChannelStatusEnabled {
				continue
			}
			// 只恢复「夜间任务自己禁用（status=2）」的渠道，不干扰 auto_ban(status=3) 等状态
			if channel.Status != common.ChannelStatusManuallyDisabled {
				continue
			}
			if model.UpdateChannelStatus(id, "", common.ChannelStatusEnabled, "night channel schedule") {
				changed++
			}
		} else {
			if channel.Status == common.ChannelStatusManuallyDisabled {
				continue
			}
			if model.UpdateChannelStatus(id, "", common.ChannelStatusManuallyDisabled, "night channel schedule") {
				changed++
			}
		}
	}
	if changed > 0 {
		common.SysLog("[channel_schedule] hour=" + strconv.Itoa(hour) +
			" desired_enabled=" + strconv.FormatBool(desiredEnabled) + " changed=" + strconv.Itoa(changed))
	}
	finishCustomSystemTask(task, runnerID, nil, map[string]any{
		"hour":            hour,
		"desired_enabled": desiredEnabled,
		"changed":         changed,
	})
}

// RegisterCustomMaintenanceSystemTasks 把三个自定义维护任务挂进 system_tasks 框架。
// 在 RegisterScheduledSystemTasks 中调用。
func RegisterCustomMaintenanceSystemTasks() {
	service.RegisterSystemTaskHandler(billingPrefFixHandler{})
	service.RegisterSystemTaskHandler(subscriptionSweepHandler{})
	service.RegisterSystemTaskHandler(channelScheduleHandler{})
}
