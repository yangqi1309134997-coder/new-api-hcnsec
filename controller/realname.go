package controller

import (
	"errors"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// ============================================================
// 实名认证：用户端接口 + 管理端审核
// ============================================================

var realnameId2AttemptMu sync.Mutex
var realnameId2Attempts = map[int]int64{} // uid -> 尝试次数（当日, 内存计数, 重启清零）

func realnameId2TooManyAttempts(uid int) bool {
	realnameId2AttemptMu.Lock()
	defer realnameId2AttemptMu.Unlock()
	day := time.Now().Unix() / 86400
	key := uid*100000 + int(day%100000)
	if n, ok := realnameId2Attempts[key]; ok && n >= 10 {
		return true
	}
	return false
}

func realnameId2RecordAttempt(uid int) {
	realnameId2AttemptMu.Lock()
	defer realnameId2AttemptMu.Unlock()
	day := time.Now().Unix() / 86400
	key := uid*100000 + int(day%100000)
	realnameId2Attempts[key]++
}

// GetRealNameStatus 个人资料页认证状态。
func GetRealNameStatus(c *gin.Context) {
	uid := c.GetInt("id")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": service.RealNameSelfStatusOf(c, uid)})
}

// SendRealNamePhoneCode 发送短信验证码。
func SendRealNamePhoneCode(c *gin.Context) {
	if !setting.RealNameEnabled {
		common.ApiError(c, errors.New("实名认证功能未开启"))
		return
	}
	var req struct {
		Phone string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	phone := strings.TrimSpace(req.Phone)
	pattern := setting.RealNamePhonePattern
	if pattern == "" {
		pattern = `^1[3-9]\d{9}$`
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		re = regexp.MustCompile(`^1[3-9]\d{9}$`)
	}
	if !re.MatchString(phone) {
		common.ApiError(c, errors.New("手机号格式不正确"))
		return
	}
	uid := c.GetInt("id")
	m1, dPhone, dUser := model.PhoneCodeRateStats(phone, uid)
	if m1 >= 1 {
		common.ApiError(c, errors.New("发送过于频繁，请 1 分钟后再试"))
		return
	}
	if dPhone >= 5 {
		common.ApiError(c, errors.New("该手机号今日发送次数已达上限"))
		return
	}
	if dUser >= 10 {
		common.ApiError(c, errors.New("您今日发送次数已达上限"))
		return
	}
	code := strconv.Itoa(100000 + rand.Intn(900000))
	ttl := int64(setting.RealNameSmsCodeTTL)
	if ttl < 60 {
		ttl = 300
	}
	if err := model.CreatePhoneCode(phone, code, uid, ttl); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := service.SendRealNameSms(phone, code); err != nil {
		common.SysError("实名短信发送失败 phone=" + phone[:3] + "****: " + err.Error())
		common.ApiError(c, errors.New("短信发送失败："+err.Error()))
		return
	}
	common.ApiSuccess(c, gin.H{"ttl": ttl})
}

// BindRealNamePhone 绑定手机号（校验验证码；A 路线完成即实名通过）。
func BindRealNamePhone(c *gin.Context) {
	if !setting.RealNameEnabled {
		common.ApiError(c, errors.New("实名认证功能未开启"))
		return
	}
	var req struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	phone := strings.TrimSpace(req.Phone)
	code := strings.TrimSpace(req.Code)
	if phone == "" || code == "" {
		common.ApiError(c, errors.New("手机号与验证码不能为空"))
		return
	}
	if !model.ConsumePhoneCode(phone, code) {
		common.ApiError(c, errors.New("验证码错误或已过期"))
		return
	}
	uid := c.GetInt("id")
	v, err := model.GetUserVerification(uid)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	v.Phone = phone
	v.PhoneVerifiedAt = common.GetTimestamp()
	// 手机号路线：绑定即视为实名通过（不覆盖已有的更高级认证）
	if v.Status != "verified" || v.Method == "" || v.Method == "phone" {
		if setting.RealNameMethod == "phone" || setting.RealNameMethod == "tiered" {
			v.Status = "verified"
			v.Method = "phone"
			v.Provider = "sms"
			v.VerifiedAt = common.GetTimestamp()
		}
	}
	if err := model.SaveUserVerification(v); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, service.RealNameSelfStatusOf(c, uid))
}

// SubmitRealNameId2 B 路线：提交姓名+身份证号，实时调二要素接口。
func SubmitRealNameId2(c *gin.Context) {
	if !setting.RealNameEnabled {
		common.ApiError(c, errors.New("实名认证功能未开启"))
		return
	}
	uid := c.GetInt("id")
	if realnameId2TooManyAttempts(uid) {
		common.ApiError(c, errors.New("今日尝试次数已达上限（10 次），请明日再试或联系管理员"))
		return
	}
	var req struct {
		RealName string `json:"real_name"`
		IdNo     string `json:"id_no"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	name := strings.TrimSpace(req.RealName)
	idNo := strings.ToUpper(strings.TrimSpace(req.IdNo))
	if len([]rune(name)) < 2 || len([]rune(name)) > 30 {
		common.ApiError(c, errors.New("请输入真实姓名"))
		return
	}
	if err := service.ValidateChineseIdNo(idNo); err != nil {
		common.ApiError(c, err)
		return
	}
	realnameId2RecordAttempt(uid)
	outcome, err := service.CheckId2(name, idNo)
	if err != nil {
		common.SysError("实名二要素调用失败 uid=" + strconv.Itoa(uid) + ": " + err.Error())
		common.ApiError(c, errors.New("核验接口调用失败："+err.Error()))
		return
	}
	if !outcome.OK {
		msg := outcome.Msg
		if msg == "" {
			msg = "姓名与身份证号不一致或核验未通过"
		}
		common.ApiError(c, errors.New(msg))
		return
	}
	if err := service.RealNameApplyIdVerification(uid, name, idNo, "id2", outcome.Provider); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, service.RealNameSelfStatusOf(c, uid))
}

// SubmitRealNameManual D 路线：提交人工审核。
func SubmitRealNameManual(c *gin.Context) {
	if !setting.RealNameEnabled {
		common.ApiError(c, errors.New("实名认证功能未开启"))
		return
	}
	var req struct {
		RealName string `json:"real_name"`
		IdNo     string `json:"id_no"`
		Note     string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	uid := c.GetInt("id")
	name := strings.TrimSpace(req.RealName)
	idNo := strings.ToUpper(strings.TrimSpace(req.IdNo))
	if len([]rune(name)) < 2 {
		common.ApiError(c, errors.New("请输入真实姓名"))
		return
	}
	if err := service.ValidateChineseIdNo(idNo); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := service.RealNameApplyIdVerification(uid, name, idNo, "manual", "manual"); err != nil {
		common.ApiError(c, err)
		return
	}
	// 覆写为待审核状态
	v, err := model.GetUserVerification(uid)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	v.Status = "pending"
	v.SubmitNote = strings.TrimSpace(req.Note)
	if err := model.SaveUserVerification(v); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, service.RealNameSelfStatusOf(c, uid))
}

// ---------------- 管理端 ----------------

// GetRealNameAdminList 认证记录列表（可按状态筛选）。
func GetRealNameAdminList(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("p"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	status := c.Query("status")
	items, total, err := model.ListUserVerifications(status, page, pageSize)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"items": items, "total": total,
			"verified_total": model.CountVerifiedUsers(),
		},
	})
}

// PostRealNameReview 人工审核（通过/驳回）。
func PostRealNameReview(c *gin.Context) {
	var req struct {
		UserId  int    `json:"user_id"`
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.UserId <= 0 {
		common.ApiError(c, errors.New("参数错误"))
		return
	}
	v, err := model.GetUserVerification(req.UserId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if v.ID == 0 {
		common.ApiError(c, errors.New("该用户没有认证记录"))
		return
	}
	v.ReviewedBy = c.GetString("username")
	v.ReviewNote = strings.TrimSpace(req.Note)
	if req.Approve {
		v.Status = "verified"
		v.Method = "manual"
		v.VerifiedAt = common.GetTimestamp()
	} else {
		v.Status = "rejected"
	}
	if err := model.SaveUserVerification(v); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"ok": true})
}
