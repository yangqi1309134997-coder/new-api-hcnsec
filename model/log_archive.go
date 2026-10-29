package model

import (
	"context"
	"database/sql"
)

// 归档用的时间窗口查询：供日志按天导出后删除，见 service/log_archive.go。

// CountLogsBetween 统计 [start, end) 区间内的日志行数。
func CountLogsBetween(ctx context.Context, start, end int64) (int64, error) {
	var total int64
	err := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("created_at >= ? AND created_at < ?", start, end).Count(&total).Error
	return total, err
}

// FindLogsBetweenBatch 以 id 游标分批读取区间内的完整日志行，供导出使用。
func FindLogsBetweenBatch(ctx context.Context, start, end, afterID int64, limit int) ([]*Log, error) {
	var logs []*Log
	err := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("created_at >= ? AND created_at < ? AND id > ?", start, end, afterID).
		Order("id ASC").Limit(limit).Find(&logs).Error
	return logs, err
}

// DeleteLogsBetweenBatch 删除区间内至多 limit 行，返回实际删除行数。
// 先取主键再按主键删除，避免 DELETE ... LIMIT 的数据库方言差异。
func DeleteLogsBetweenBatch(ctx context.Context, start, end int64, limit int) (int64, error) {
	var ids []int
	err := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("created_at >= ? AND created_at < ?", start, end).
		Order("id ASC").Limit(limit).Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	res := LOG_DB.WithContext(ctx).Where("id IN ?", ids).Delete(&Log{})
	return res.RowsAffected, res.Error
}

// MinLogCreatedAt 返回最早的日志时间戳；无数据返回 0。
func MinLogCreatedAt(ctx context.Context) (int64, error) {
	var mn sql.NullInt64
	err := LOG_DB.WithContext(ctx).Model(&Log{}).Select("MIN(created_at)").Scan(&mn).Error
	if err != nil || !mn.Valid {
		return 0, err
	}
	return mn.Int64, nil
}
