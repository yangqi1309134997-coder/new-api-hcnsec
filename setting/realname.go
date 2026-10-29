package setting

import (
	"crypto/sha256"
	"encoding/hex"
)

// ============================================================
// 实名认证配置（默认全部关闭，后台可全量热切换）
// 方式 RealNameMethod：
//
//	phone    A路线：手机号短信验证绑定（深度合成规定§9 认可的实名方式）
//	id2      B路线：身份证二要素（接第三方核验接口）
//	tiered   C路线：分级组合——日常手机号即可，触发条件（充值/用量）后要求二要素
//	manual   D路线：人工审核（用户提交，管理员后台审核）
//	national 预留：国家网络身份认证（网证/网号），当前仅预留接口位
//
// 拦截策略：RealNameBlockRelay / RealNameBlockTopup 可分别控制
// 未实名是否禁止 API 调用 / 充值；均可关闭（只提醒不拦截）。
// PIPL 合规：身份证号明文不落库，只存加盐哈希（一证一号）与掩码。
// ============================================================
var (
	RealNameEnabled              = false
	RealNameMethod               = "phone"
	RealNameApplyGroups          = ""
	RealNameUserWhitelist        = ""
	RealNameBlockRelay           = false
	RealNameBlockTopup           = false
	RealNameBlockMessage         = "根据相关规定，使用本服务需先完成实名认证，请前往「个人资料」页完成认证后再试。"
	RealNameNoticeText           = "根据国家相关规定，使用 AI 服务需完成实名认证。请在下方绑定手机号完成认证，未认证可能影响部分功能使用。"
	RealNameTieredId2Trigger     = "topup" // none | topup | quota | always
	RealNameTieredQuotaThreshold = 0       // quota 单位（美元*500000），used_quota 超过则要求二要素
	RealNameOneIdOneAccount      = true
	RealNameMinAge               = 0               // 0=不校验；>0 时按身份证出生年月校验最低年龄
	RealNameId2Provider          = "aliyun-market" // aliyun-market | tencent | generic
	RealNameId2AppCode           = ""              // APPCODE（阿里云市场）或 generic 的 Authorization 头
	RealNameId2SecretKey         = ""              // 腾讯云 SecretKey
	RealNameId2Endpoint          = ""              // 支持 {name} {idno} 占位符
	RealNameId2BodyTemplate      = ""              // generic POST body 模板（JSON），支持 {name} {idno}
	RealNameId2SuccessRule       = ""              // 可选 JSON：{"code_field":"code","code_ok":["0","200"],"msg_field":"msg"}
	RealNameSmsProvider          = "aliyun"        // aliyun | tencent | generic
	RealNameSmsAccessKey         = ""
	RealNameSmsSecretKey         = ""
	RealNameSmsSignName          = ""
	RealNameSmsTemplateCode      = "" // 阿里云模板Code / 腾讯云模板ID
	RealNameSmsAppId             = "" // 腾讯云 SmsSdkAppId
	RealNameSmsEndpoint          = "" // generic 发送地址
	RealNameSmsBodyTemplate      = "" // generic body：{"phone":"{phone}","code":"{code}"}
	RealNameSmsAuthHeader        = "" // generic 完整 Authorization 头（可选）
	RealNameSmsCodeTTL           = 300
	RealNamePhonePattern         = `^1[3-9]\d{9}$`
	RealNameHashPepper           = ""
	RealNameProviderTimeoutSec   = 10
)

// realNameDefaultPepper 固定默认盐；修改 RealNameHashPepper 选项即可整体换盐
// （换盐后已存哈希全部失效，需重新认证）。
const realNameDefaultPepper = "newapi-realname-static-pepper-v1"

// RealNameIdHash 身份证号加盐 SHA256（用于一证一号的唯一性判断，不存明文）。
func RealNameIdHash(idNo string) string {
	pepper := RealNameHashPepper
	if pepper == "" {
		pepper = realNameDefaultPepper
	}
	sum := sha256.Sum256([]byte(pepper + "|" + idNo))
	return hex.EncodeToString(sum[:])
}

// RealNameMethodLevel 各认证方式的等级：手机号=1，二要素/人工/国家=2。
func RealNameMethodLevel(method string) int {
	switch method {
	case "phone":
		return 1
	case "id2", "manual", "national":
		return 2
	}
	return 1
}
