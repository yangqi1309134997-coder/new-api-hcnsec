package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// UserVerification 用户实名认证记录（每人一条，user_id 唯一）。
// 身份证号明文永不落库：只存加盐哈希（一证一号）与前后掩码。
// status: none | pending(人工待审) | verified | rejected
// method: phone | id2 | manual | national
type UserVerification struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	UserId          int    `json:"user_id" gorm:"uniqueIndex"`
	Phone           string `json:"phone" gorm:"index"`
	PhoneVerifiedAt int64  `json:"phone_verified_at"`
	Status          string `json:"status" gorm:"index"`
	Method          string `json:"method"`
	NameMasked      string `json:"name_masked"`
	IdNoMasked      string `json:"id_no_masked"`
	IdHash          string `json:"-" gorm:"index"`
	BirthYear       int    `json:"birth_year"`
	Provider        string `json:"provider"`
	VerifiedAt      int64  `json:"verified_at"`
	ReviewedBy      string `json:"reviewed_by"`
	ReviewNote      string `json:"review_note"`
	SubmitNote      string `json:"submit_note"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// PhoneCode 短信验证码（重启不丢失，含频控统计用途）。
type PhoneCode struct {
	ID        int64  `json:"id" gorm:"primaryKey"`
	Phone     string `json:"phone" gorm:"index:idx_phone_code,priority:1"`
	Code      string `json:"-"`
	UserId    int    `json:"user_id"`
	Purpose   string `json:"purpose"`
	ExpiresAt int64  `json:"expires_at"`
	Used      int    `json:"used"`
	CreatedAt int64  `json:"created_at" gorm:"index:idx_phone_code,priority:2"`
}

func GetUserVerification(userId int) (*UserVerification, error) {
	if userId <= 0 {
		return nil, errors.New("invalid user id")
	}
	var v UserVerification
	err := DB.Where("user_id = ?", userId).First(&v).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &UserVerification{UserId: userId, Status: "none"}, nil
		}
		return nil, err
	}
	return &v, nil
}

func SaveUserVerification(v *UserVerification) error {
	if v == nil || v.UserId <= 0 {
		return errors.New("invalid verification")
	}
	now := time.Now().Unix()
	v.UpdatedAt = now
	existing, err := GetUserVerification(v.UserId)
	if err != nil {
		return err
	}
	if existing.ID == 0 {
		v.CreatedAt = now
		return DB.Create(v).Error
	}
	v.ID = existing.ID
	v.CreatedAt = existing.CreatedAt
	if v.CreatedAt == 0 {
		v.CreatedAt = now
	}
	return DB.Save(v).Error
}

// IdHashUsedByOther 一证一号检查：该身份证哈希是否已被其他用户使用。
func IdHashUsedByOther(idHash string, excludeUserId int) (bool, error) {
	if idHash == "" {
		return false, nil
	}
	var cnt int64
	err := DB.Model(&UserVerification{}).
		Where("id_hash = ? AND user_id <> ?", idHash, excludeUserId).
		Count(&cnt).Error
	return cnt > 0, err
}

func CountVerifiedUsers() int64 {
	var cnt int64
	DB.Model(&UserVerification{}).Where("status = ?", "verified").Count(&cnt)
	return cnt
}

func ListUserVerifications(status string, page, pageSize int) ([]*UserVerification, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := DB.Model(&UserVerification{})
	if status != "" && status != "all" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*UserVerification
	err := q.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&items).Error
	return items, total, err
}

// ---------------- 短信验证码 ----------------

func CreatePhoneCode(phone string, code string, userId int, ttl int64) error {
	if ttl < 60 {
		ttl = 300
	}
	now := time.Now().Unix()
	// 作废同手机号未用旧码
	DB.Model(&PhoneCode{}).Where("phone = ? AND used = 0", phone).Updates(map[string]any{"used": 2})
	return DB.Create(&PhoneCode{
		Phone: phone, Code: code, UserId: userId, Purpose: "bind",
		ExpiresAt: now + ttl, CreatedAt: now,
	}).Error
}

// ConsumePhoneCode 校验并消费验证码。
func ConsumePhoneCode(phone, code string) bool {
	if phone == "" || code == "" {
		return false
	}
	now := time.Now().Unix()
	res := DB.Model(&PhoneCode{}).
		Where("phone = ? AND code = ? AND used = 0 AND expires_at > ?", phone, code, now).
		Updates(map[string]any{"used": 1})
	return res.Error == nil && res.RowsAffected > 0
}

// PhoneCodeRateStats 返回 (60 秒内该手机号发送数, 24 小时内该手机号发送数, 24 小时内该用户发送数)。
func PhoneCodeRateStats(phone string, userId int) (int64, int64, int64) {
	now := time.Now().Unix()
	var m1, d1, d2 int64
	DB.Model(&PhoneCode{}).Where("phone = ? AND created_at > ?", phone, now-60).Count(&m1)
	DB.Model(&PhoneCode{}).Where("phone = ? AND created_at > ?", phone, now-86400).Count(&d1)
	DB.Model(&PhoneCode{}).Where("user_id = ? AND created_at > ?", userId, now-86400).Count(&d2)
	return m1, d1, d2
}
