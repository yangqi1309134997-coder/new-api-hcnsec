package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// ============================================================
// 实名认证：作用域 / 等级 / 调用与充值闸门
// ============================================================

func realnameGroupApplies(group string) bool {
	list := setting.RealNameApplyGroups
	if strings.TrimSpace(list) == "" {
		return true
	}
	g := strings.ToLower(strings.TrimSpace(group))
	for _, part := range strings.Split(list, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == g {
			return true
		}
	}
	return false
}

func realnameUserWhitelisted(uid int, username string) bool {
	list := setting.RealNameUserWhitelist
	if strings.TrimSpace(list) == "" {
		return false
	}
	un := strings.ToLower(strings.TrimSpace(username))
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil {
			if n == uid {
				return true
			}
		} else if un != "" && strings.ToLower(p) == un {
			return true
		}
	}
	return false
}

// RealNameUserInScope 该用户是否在实名管控范围内（管理员与白名单豁免）。
func RealNameUserInScope(c *gin.Context, uid int, username, group string) bool {
	if c != nil && c.GetInt("role") >= common.RoleAdminUser {
		return false
	}
	if !realnameGroupApplies(group) {
		return false
	}
	if realnameUserWhitelisted(uid, username) {
		return false
	}
	return true
}

// RealNameAchievedLevel 用户已达到的实名等级：
// 0=未认证；1=手机号已验证；2=二要素/人工/国家认证通过。
func RealNameAchievedLevel(v *model.UserVerification) int {
	if v == nil {
		return 0
	}
	if v.Status == "verified" {
		switch v.Method {
		case "id2", "manual", "national":
			return 2
		case "phone":
			return 1
		}
	}
	if v.PhoneVerifiedAt > 0 {
		return 1
	}
	return 0
}

func realnameHasToppedUp(uid int) bool {
	var cnt int64
	model.DB.Raw("SELECT COUNT(1) FROM top_ups WHERE user_id = ? AND status = 'success'", uid).Scan(&cnt)
	return cnt > 0
}

func realnameUsedQuota(uid int) int64 {
	var q int64
	model.DB.Raw("SELECT used_quota FROM users WHERE id = ?", uid).Scan(&q)
	return q
}

// RealNameRequiredLevel 当前策略下该用户需要的实名等级。
func RealNameRequiredLevel(uid int) int {
	switch setting.RealNameMethod {
	case "phone":
		return 1
	case "id2", "manual", "national":
		return 2
	case "tiered":
		switch setting.RealNameTieredId2Trigger {
		case "always":
			return 2
		case "topup":
			if realnameHasToppedUp(uid) {
				return 2
			}
		case "quota":
			if setting.RealNameTieredQuotaThreshold > 0 && realnameUsedQuota(uid) >= int64(setting.RealNameTieredQuotaThreshold) {
				return 2
			}
		}
		return 1
	}
	return 1
}

// RealNameUserPasses 综合判定：用户是否满足当前实名要求。返回 (是否通过, 原因)。
func RealNameUserPasses(c *gin.Context, uid int) (bool, string) {
	if !setting.RealNameEnabled || uid <= 0 {
		return true, ""
	}
	username := ""
	group := ""
	if c != nil {
		username = c.GetString("username")
		group = c.GetString("group")
	}
	if !RealNameUserInScope(c, uid, username, group) {
		return true, ""
	}
	v, err := model.GetUserVerification(uid)
	if err != nil {
		return true, "" // 查询异常不拦截业务
	}
	if RealNameAchievedLevel(v) >= RealNameRequiredLevel(uid) {
		return true, ""
	}
	msg := setting.RealNameBlockMessage
	if strings.TrimSpace(msg) == "" {
		msg = "根据相关规定，使用本服务需先完成实名认证。"
	}
	return false, msg
}

// RealNameTopUpAllowed 充值闸门（controller.TopUp 入口调用）。
func RealNameTopUpAllowed(c *gin.Context, uid int) (bool, string) {
	if !setting.RealNameEnabled || !setting.RealNameBlockTopup {
		return true, ""
	}
	return RealNameUserPasses(c, uid)
}

// RealNameGate 中继调用闸门（PrepareRequestBilling 最前调用）。
func RealNameGate(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if !setting.RealNameEnabled || !setting.RealNameBlockRelay {
		return nil
	}
	uid := 0
	if info != nil {
		uid = info.UserId
	}
	if uid == 0 && c != nil {
		uid = c.GetInt("id")
	}
	if uid == 0 {
		return nil
	}
	if ok, reason := RealNameUserPasses(c, uid); !ok {
		return types.NewError(errors.New(reason), types.ErrorCodeSensitiveWordsDetected)
	}
	return nil
}

// RealNameSelfStatus 前台个人资料页状态。
type RealNameSelfStatus struct {
	Enabled        bool   `json:"enabled"`
	Method         string `json:"method"`
	NeedLevel      int    `json:"need_level"`
	AchievedLevel  int    `json:"achieved_level"`
	Verified       bool   `json:"verified"`
	Status         string `json:"status"`
	PhoneBound     bool   `json:"phone_bound"`
	PhoneMasked    string `json:"phone_masked"`
	NameMasked     string `json:"name_masked"`
	IdNoMasked     string `json:"id_no_masked"`
	Provider       string `json:"provider"`
	VerifiedAt     int64  `json:"verified_at"`
	ReviewNote     string `json:"review_note"`
	NoticeText     string `json:"notice_text"`
	BlockRelay     bool   `json:"block_relay"`
	BlockTopup     bool   `json:"block_topup"`
}

func RealNameSelfStatusOf(c *gin.Context, uid int) *RealNameSelfStatus {
	st := &RealNameSelfStatus{
		Enabled:  setting.RealNameEnabled,
		Method:   setting.RealNameMethod,
		NoticeText: setting.RealNameNoticeText,
		BlockRelay: setting.RealNameBlockRelay,
		BlockTopup: setting.RealNameBlockTopup,
	}
	if !setting.RealNameEnabled {
		return st
	}
	v, err := model.GetUserVerification(uid)
	if err == nil && v != nil {
		st.Status = v.Status
		if st.Status == "" {
			st.Status = "none"
		}
		st.PhoneBound = v.Phone != "" && v.PhoneVerifiedAt > 0
		if len(v.Phone) == 11 {
			st.PhoneMasked = v.Phone[:3] + "****" + v.Phone[7:]
		} else if v.Phone != "" {
			st.PhoneMasked = v.Phone[:1] + "***"
		}
		st.NameMasked = v.NameMasked
		st.IdNoMasked = v.IdNoMasked
		st.Provider = v.Provider
		st.VerifiedAt = v.VerifiedAt
		st.ReviewNote = v.ReviewNote
		st.AchievedLevel = RealNameAchievedLevel(v)
	} else {
		st.Status = "none"
	}
	st.NeedLevel = RealNameRequiredLevel(uid)
	st.Verified = st.AchievedLevel >= st.NeedLevel
	return st
}

// ---------------- 身份证工具 ----------------

// ValidateChineseIdNo 校验 18 位身份证（含校验位）。
func ValidateChineseIdNo(id string) error {
	if len(id) != 18 {
		return errors.New("身份证号必须为 18 位")
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	codes := "10X98765432"
	sum := 0
	for i := 0; i < 17; i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return errors.New("身份证号前 17 位必须为数字")
		}
		sum += int(c-'0') * weights[i]
	}
	last := id[17]
	if last == 'x' {
		last = 'X'
	}
	if codes[sum%11] != last {
		return errors.New("身份证号校验位不正确")
	}
	year, err := strconv.Atoi(id[6:10])
	if err != nil || year < 1900 || year > 2100 {
		return errors.New("身份证号出生年份无效")
	}
	return nil
}

// IdBirthYear / IdMinAgeCheck：从身份证取出生年，结合月日校验最低年龄。
func IdBirthYear(id string) int {
	y, _ := strconv.Atoi(id[6:10])
	return y
}

func IdMeetsMinAge(id string, minAge int) bool {
	if minAge <= 0 {
		return true
	}
	year, _ := strconv.Atoi(id[6:10])
	month, _ := strconv.Atoi(id[10:12])
	day, _ := strconv.Atoi(id[12:14])
	now := common.GetTimestamp()
	// 粗略按当前时间比较：用 unix 秒换算年月日
	t := timeUnixToYMD(now)
	age := t.year - year
	if t.month < month || (t.month == month && t.day < day) {
		age--
	}
	return age >= minAge
}

type ymd struct{ year, month, day int }

func timeUnixToYMD(ts int64) ymd {
	t := time.Unix(ts, 0)
	return ymd{t.Year(), int(t.Month()), t.Day()}
}

// MaskRealName 姓名掩码：首字保留，其余 *（少数民族长名保留首字）。
func MaskRealName(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) <= 1 {
		return string(r)
	}
	return string(r[0]) + strings.Repeat("*", len(r)-1)
}

// MaskIdNo 身份证掩码：前 6 后 4。
func MaskIdNo(id string) string {
	if len(id) != 18 {
		return "***"
	}
	return id[:6] + "********" + id[14:]
}

func RealNameApplyIdVerification(uid int, realName, idNo, method, provider string) error {
	idHash := setting.RealNameIdHash(idNo)
	if setting.RealNameOneIdOneAccount {
		if used, err := model.IdHashUsedByOther(idHash, uid); err == nil && used {
			return errors.New("该身份证号已绑定其他账号（一证一号）")
		}
	}
	if setting.RealNameMinAge > 0 && !IdMeetsMinAge(idNo, setting.RealNameMinAge) {
		return fmt.Errorf("未满 %d 周岁，无法完成实名认证", setting.RealNameMinAge)
	}
	v, err := model.GetUserVerification(uid)
	if err != nil {
		return err
	}
	v.NameMasked = MaskRealName(realName)
	v.IdNoMasked = MaskIdNo(idNo)
	v.IdHash = idHash
	v.BirthYear = IdBirthYear(idNo)
	v.Method = method
	v.Provider = provider
	v.Status = "verified"
	v.VerifiedAt = common.GetTimestamp()
	v.ReviewNote = ""
	return model.SaveUserVerification(v)
}
