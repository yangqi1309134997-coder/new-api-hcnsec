package model

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// ============================================================================
// 订阅维护任务（原服务器 cron 脚本的内置化）
//
// 后台系统任务（controller/system_task_custom.go）周期性调用本文件的函数，
// 替代原先部署在宿主机上的 shell/python cron：
//   - FixStaleBillingPreferences   <- subscription_pref_auto_fix.py + cron_subscription_pref.sh
//   - SweepExpiredUserSubscriptions <- sub_expire_downgrade.sh
//
// 注意：UserSubscription.EndTime 为秒级 Unix 时间戳（与 common.GetTimestamp 一致），
// 原 python 脚本误用毫秒比较，导致「存在有效订阅则跳过」的判断永不成立、
// 有效订阅用户也会被误切偏好；本实现统一使用秒级比较修正该问题。
// ============================================================================

// FixStaleBillingPreferences 将「计费偏好仍锁定 subscription_only，但已无任何
// 活跃有效订阅」的用户回切为 wallet_first，避免订阅过期后所有请求报
// no active subscription。excludedUserIds 中的用户（管理员/白名单）不处理。
// 返回成功回切的用户数。
func FixStaleBillingPreferences(excludedUserIds []int) (int, error) {
	excluded := make(map[int]bool, len(excludedUserIds))
	for _, id := range excludedUserIds {
		excluded[id] = true
	}

	// LIKE 预筛选（与原脚本一致），精确判断在内存中基于解析后的 setting 进行
	var candidateIds []int
	if err := DB.Model(&User{}).
		Where("setting LIKE ?", `%subscription_only%`).
		Pluck("id", &candidateIds).Error; err != nil {
		return 0, err
	}
	if len(candidateIds) == 0 {
		return 0, nil
	}

	fixed := 0
	for _, id := range candidateIds {
		if excluded[id] {
			continue
		}
		hasSub, err := HasActiveUserSubscription(id)
		if err != nil {
			continue
		}
		if hasSub {
			continue
		}
		user, err := GetUserById(id, true)
		if err != nil || user == nil {
			continue
		}
		setting := user.GetSetting()
		if common.NormalizeBillingPreference(setting.BillingPreference) != "subscription_only" {
			continue
		}
		setting.BillingPreference = "wallet_first"
		if err := UpdateUserSetting(id, setting); err != nil {
			common.SysError(fmt.Sprintf("[subscription_maintenance] failed to reset billing preference for user %d: %s", id, err.Error()))
			continue
		}
		fixed++
	}
	return fixed, nil
}

// SubscriptionSweepResult 汇总一次过期订阅清扫的结果
type SubscriptionSweepResult struct {
	ExpiredCount  int   `json:"expired_count"`
	GroupRestored int   `json:"group_restored"`
	RestoredUsers []int `json:"restored_users,omitempty"`
}

// SweepExpiredUserSubscriptions 做两件事：
//  1. 把 status=active 且已到期（end_time <= now）的订阅置为 expired，
//     并走 downgradeUserGroupForSubscriptionTx 的正规降级规则回退用户分组；
//  2. 兜底：把「无任何活跃有效订阅、但仍停留在付费分组」的用户回退到
//     defaultGroup（覆盖历史遗留与管理员删除订阅等场景）。
//
// excludedUserIds 为永久白名单（不降组、不切偏好）。
func SweepExpiredUserSubscriptions(excludedUserIds []int, paidGroups []string, defaultGroup string) (*SubscriptionSweepResult, error) {
	now := common.GetTimestamp()
	excluded := make(map[int]bool, len(excludedUserIds))
	for _, id := range excludedUserIds {
		excluded[id] = true
	}
	result := &SubscriptionSweepResult{}
	touchedUsers := make(map[int]bool)

	// ---- 1) 到期订阅：置 expired + 正规降级 ----
	var expiredSubs []UserSubscription
	if err := DB.Where("status = ? AND end_time > 0 AND end_time <= ?", "active", now).
		Order("id asc").Limit(1000).Find(&expiredSubs).Error; err != nil {
		return nil, err
	}
	for _, sub := range expiredSubs {
		if excluded[sub.UserId] {
			continue
		}
		err := DB.Transaction(func(tx *gorm.DB) error {
			var fresh UserSubscription
			if err := lockForUpdate(tx).
				Where("id = ? AND status = ?", sub.Id, "active").First(&fresh).Error; err != nil {
				// 已被并发处理/管理员取消，跳过
				return err
			}
			if fresh.EndTime > now {
				// 到期前恰好被续期，跳过
				return nil
			}
			if err := tx.Model(&fresh).Updates(map[string]any{
				"status":     "expired",
				"updated_at": now,
			}).Error; err != nil {
				return err
			}
			_, err := downgradeUserGroupForSubscriptionTx(tx, &fresh, now)
			return err
		})
		if err == nil {
			result.ExpiredCount++
			touchedUsers[sub.UserId] = true
		}
	}

	// ---- 2) 兜底：付费分组但无任何活跃订阅的用户回退 default ----
	if len(paidGroups) > 0 && defaultGroup != "" {
		query := DB.Model(&User{}).
			Select("id", "username", "`group`").
			Where("`group` IN ?", paidGroups)
		if len(excludedUserIds) > 0 {
			query = query.Where("id NOT IN ?", excludedUserIds)
		}
		var users []User
		if err := query.Limit(2000).Find(&users).Error; err != nil {
			return result, err
		}
		for _, u := range users {
			if excluded[u.Id] || u.Group == defaultGroup {
				continue
			}
			hasSub, err := HasActiveUserSubscription(u.Id)
			if err != nil || hasSub {
				continue
			}
			if err := DB.Model(&User{}).Where("id = ?", u.Id).
				Update("group", defaultGroup).Error; err != nil {
				common.SysError(fmt.Sprintf("[subscription_maintenance] failed to downgrade group for user %d: %s", u.Id, err.Error()))
				continue
			}
			result.GroupRestored++
			result.RestoredUsers = append(result.RestoredUsers, u.Id)
			touchedUsers[u.Id] = true
		}
	}

	for uid := range touchedUsers {
		if err := RefreshUserGroupCache(uid); err != nil {
			common.SysError(fmt.Sprintf("[subscription_maintenance] failed to refresh group cache for user %d: %s", uid, err.Error()))
		}
	}
	return result, nil
}
