package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// GetVGuardStatus 当前违规防护状态：配置摘要 + 活跃用户（计数中/封禁中）。
func GetVGuardStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled":        setting.VGuardEnabled,
			"weak_threshold": setting.VGuardWeakThreshold,
			"window_minutes": setting.VGuardWindowMinutes,
			"block_hours":    setting.VGuardBlockHours,
			"inject_enabled": setting.VGuardInjectEnabled,
			"strong_words_n": len(setting.VGuardStrongWordsList()),
			"weak_words_n":   len(setting.VGuardWeakWordsList()),
			"apply_groups":   setting.VGuardApplyGroups,
			"user_whitelist": setting.VGuardUserWhitelist,
			"llm": gin.H{
				"enabled":             setting.VGuardLLMEnabled,
				"mode":                setting.VGuardLLMMode,
				"model":               setting.VGuardLLMModel,
				"trigger":             setting.VGuardLLMTrigger,
				"action":              setting.VGuardLLMAction,
				"protocol":            setting.VGuardLLMProtocol,
				"sustain_hours":       setting.VGuardLLMSustainHours,
				"async_enabled":       setting.VGuardLLMAsyncEnabled,
				"fast_seconds":        setting.VGuardLLMFastSeconds,
				"async_seconds":       setting.VGuardLLMAsyncSeconds,
				"models_pool":         setting.VGuardLLMModels,
				"safe_exit_streak":    setting.VGuardLLMSafeExitStreak,
				"safe_exit_floor_min": setting.VGuardLLMSafeExitFloorMin,
				"weak_reenter":        setting.VGuardLLMWeakReenter,
				"pool_health":         service.VGuardLLMPoolHealth(),
			},
			"items": service.VGuardStatusSnapshot(),
		},
	})
}

// GetVGuardEvents 违规触发详细日志（分页，支持按用户名/用户ID/类型过滤，
// 不含 Detail 全文以减小响应）。
func GetVGuardEvents(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("p"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	userId, _ := strconv.Atoi(c.Query("user_id"))
	items, total, err := model.GetVGuardEvents(page, pageSize, model.VGuardEventFilter{
		Username: strings.TrimSpace(c.Query("username")),
		UserId:   userId,
		Kind:     strings.TrimSpace(c.Query("kind")),
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items":     items,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// GetVGuardEventDetail 单条完整记录（含被拦截消息全文）。
func GetVGuardEventDetail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		common.ApiError(c, errors.New("无效的事件ID"))
		return
	}
	e, err := model.GetVGuardEventByID(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": e})
}

// ExportVGuardEvents 导出完整触发记录为 JSON 文件下载。
func ExportVGuardEvents(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := model.GetVGuardEventsForExport(limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.Header("Content-Disposition", "attachment; filename=vguard_events_"+time.Now().Format("20060102_150405")+".json")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"count":   len(items),
		"items":   items,
	})
}

// GetVGuardDaily 每日违规摘要（按日期聚合）。
func GetVGuardDaily(c *gin.Context) {
	stats, err := model.GetVGuardDailyStats(c.Query("date"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": stats})
}

// PostVGuardUnblock 管理员手动解除某用户的封禁并清零计数。
func PostVGuardUnblock(c *gin.Context) {
	var req struct {
		UserId int `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.UserId <= 0 {
		msg := "参数错误"
		if err != nil {
			msg = err.Error()
		}
		common.ApiError(c, errors.New(msg))
		return
	}
	cleared := service.VGuardUnblock(req.UserId)
	model.RecordVGuardEvent(&model.VGuardEvent{
		UserId:   req.UserId,
		Username: c.GetString("username"),
		Kind:     "unblocked",
		Words:    "manual",
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    gin.H{"cleared": cleared},
	})
}
