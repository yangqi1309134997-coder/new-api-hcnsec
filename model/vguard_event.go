package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// VGuardEvent 违规防护触发记录。
// kind: weak | strong | blocked | unblocked | output | upstream_strong | upstream_weak
type VGuardEvent struct {
	ID        int64  `json:"id" gorm:"primaryKey"`
	UserId    int    `json:"user_id" gorm:"index:idx_vguard_uid_time,priority:1"`
	Username  string `json:"username"`
	Kind      string `json:"kind" gorm:"index"`
	Words     string `json:"words"`
	Model     string `json:"model"`
	RequestId string `json:"request_id"`
	Context   string `json:"context" gorm:"type:text"`
	Detail    string `json:"detail" gorm:"type:longtext"`
	CreatedAt int64  `json:"created_at" gorm:"index:idx_vguard_uid_time,priority:2"`
}

// utf8Fix 用户文本可能含非法 UTF-8（多字节字符被按字节截断、二进制
// payload 等），MySQL 严格模式会以 1366 丢弃整条事件；落库前统一净化。
func utf8Fix(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

func RecordVGuardEvent(e *VGuardEvent) {
	if e == nil {
		return
	}
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}
	e.Username = utf8Fix(e.Username)
	e.Kind = utf8Fix(e.Kind)
	e.Words = utf8Fix(e.Words)
	e.Model = utf8Fix(e.Model)
	e.RequestId = utf8Fix(e.RequestId)
	e.Context = utf8Fix(e.Context)
	e.Detail = utf8Fix(e.Detail)
	if err := DB.Create(e).Error; err != nil {
		common.SysError(fmt.Sprintf("RecordVGuardEvent insert failed: %v", err))
	}
}

func GetVGuardEventByID(id int64) (*VGuardEvent, error) {
	var e VGuardEvent
	err := DB.First(&e, id).Error
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// VGuardEventFilter 违规事件列表过滤条件。
type VGuardEventFilter struct {
	Username string
	UserId   int
	Kind     string
}

func GetVGuardEvents(page int, pageSize int, filter VGuardEventFilter) ([]*VGuardEvent, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	buildQuery := func() *gorm.DB {
		query := DB.Model(&VGuardEvent{})
		if filter.UserId > 0 {
			query = query.Where("user_id = ?", filter.UserId)
		}
		if filter.Username != "" {
			pattern, err := sanitizeLikePattern(filter.Username)
			if err != nil {
				return query.Where("1 = 0")
			}
			query = query.Where("username LIKE ? ESCAPE '!'", "%"+pattern+"%")
		}
		if filter.Kind != "" {
			query = query.Where("kind = ?", filter.Kind)
		}
		return query
	}
	var total int64
	if err := buildQuery().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*VGuardEvent
	err := buildQuery().Omit("Detail").Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&items).Error
	return items, total, err
}

func GetVGuardEventsForExport(limit int) ([]*VGuardEvent, error) {
	if limit < 1 || limit > 20000 {
		limit = 5000
	}
	var items []*VGuardEvent
	err := DB.Order("id DESC").Limit(limit).Find(&items).Error
	return items, err
}

// ---------------- 归档用时间窗口查询 ----------------

// CountVGuardEventsBetween 统计 [start, end) 区间内的违规事件行数。
func CountVGuardEventsBetween(ctx context.Context, start, end int64) (int64, error) {
	var total int64
	err := DB.WithContext(ctx).Model(&VGuardEvent{}).
		Where("created_at >= ? AND created_at < ?", start, end).Count(&total).Error
	return total, err
}

// FindVGuardEventsBetweenBatch 以 id 游标分批读取区间内的完整事件行，供导出使用。
func FindVGuardEventsBetweenBatch(ctx context.Context, start, end, afterID int64, limit int) ([]*VGuardEvent, error) {
	var items []*VGuardEvent
	err := DB.WithContext(ctx).Model(&VGuardEvent{}).
		Where("created_at >= ? AND created_at < ? AND id > ?", start, end, afterID).
		Order("id ASC").Limit(limit).Find(&items).Error
	return items, err
}

// DeleteVGuardEventsBetweenBatch 删除区间内至多 limit 行，返回实际删除行数。
// 先取主键再按主键删除，避免 DELETE ... LIMIT 的数据库方言差异。
func DeleteVGuardEventsBetweenBatch(ctx context.Context, start, end int64, limit int) (int64, error) {
	var ids []int64
	err := DB.WithContext(ctx).Model(&VGuardEvent{}).
		Where("created_at >= ? AND created_at < ?", start, end).
		Order("id ASC").Limit(limit).Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	res := DB.WithContext(ctx).Where("id IN ?", ids).Delete(&VGuardEvent{})
	return res.RowsAffected, res.Error
}

// MinVGuardEventCreatedAt 返回最早的违规事件时间戳；无数据返回 0。
func MinVGuardEventCreatedAt(ctx context.Context) (int64, error) {
	var mn sql.NullInt64
	err := DB.WithContext(ctx).Model(&VGuardEvent{}).Select("MIN(created_at)").Scan(&mn).Error
	if err != nil || !mn.Valid {
		return 0, err
	}
	return mn.Int64, nil
}

// ---------------- 每日统计 ----------------

type VGuardKV struct {
	K string `json:"k"`
	V int64  `json:"v"`
}

type VGuardUserRow struct {
	UserId   int    `json:"user_id"`
	Username string `json:"username"`
	Count    int64  `json:"count"`
}

type VGuardDailyStats struct {
	Date            string           `json:"date"`
	Totals          map[string]int64 `json:"totals"`
	UniqueUsers     int64            `json:"unique_users"`
	NewBlockedUsers int64            `json:"new_blocked_users"`
	TopWords        []VGuardKV       `json:"top_words"`
	TopUsers        []VGuardUserRow  `json:"top_users"`
	ByModel         []VGuardKV       `json:"by_model"`
	Hourly          []int64          `json:"hourly"`
}

func GetVGuardDailyStats(date string) (*VGuardDailyStats, error) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	startT, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return nil, err
	}
	start := startT.Unix()
	end := start + 86400
	s := &VGuardDailyStats{Date: date, Totals: map[string]int64{}, Hourly: make([]int64, 24)}

	type kindRow struct {
		Kind string
		C    int64
	}
	var kinds []kindRow
	if err := DB.Model(&VGuardEvent{}).Select("kind, count(*) as c").
		Where("created_at >= ? AND created_at < ?", start, end).
		Group("kind").Order("c DESC").Scan(&kinds).Error; err != nil {
		return nil, err
	}
	for _, k := range kinds {
		s.Totals[k.Kind] = k.C
	}

	DB.Model(&VGuardEvent{}).Where("created_at >= ? AND created_at < ?", start, end).
		Distinct("user_id").Count(&s.UniqueUsers)

	DB.Raw(`SELECT COUNT(DISTINCT e.user_id) FROM v_guard_events e
		WHERE e.kind='blocked' AND e.created_at >= ? AND e.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM v_guard_events p WHERE p.user_id = e.user_id AND p.kind='blocked' AND p.created_at < ?)`,
		start, end, start).Scan(&s.NewBlockedUsers)

	var words []VGuardKV
	DB.Model(&VGuardEvent{}).Select("words as k, count(*) as v").
		Where("created_at >= ? AND created_at < ? AND kind IN ('weak','strong','output')", start, end).
		Group("words").Order("v DESC").Limit(10).Scan(&words)
	s.TopWords = words

	var users []VGuardUserRow
	DB.Model(&VGuardEvent{}).Select("user_id, MAX(username) as username, count(*) as count").
		Where("created_at >= ? AND created_at < ? AND kind IN ('weak','strong','output')", start, end).
		Group("user_id").Order("count DESC").Limit(10).Scan(&users)
	s.TopUsers = users

	var models []VGuardKV
	DB.Model(&VGuardEvent{}).Select("model as k, count(*) as v").
		Where("created_at >= ? AND created_at < ? AND kind IN ('weak','strong','output')", start, end).
		Group("model").Order("v DESC").Limit(5).Scan(&models)
	s.ByModel = models

	type hRow struct {
		H int64
		C int64
	}
	var hrs []hRow
	DB.Model(&VGuardEvent{}).Select("(created_at - ?)/3600 as h, count(*) as c", start).
		Where("created_at >= ? AND created_at < ?", start, end).
		Group("h").Scan(&hrs)
	for _, r := range hrs {
		if r.H >= 0 && r.H < 24 {
			s.Hourly[r.H] = r.C
		}
	}
	return s, nil
}
