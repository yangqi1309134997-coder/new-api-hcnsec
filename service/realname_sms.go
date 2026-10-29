package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting"
)

// ============================================================
// 实名短信验证码发送
//   aliyun   阿里云短信（dysmsapi，个人/企业均可，新人有免费包）
//            AccessKey / SecretKey / 签名 / 模板Code（模板变量 code）
//   tencent  腾讯云短信（个人 100 条/企业 200 条免费额度）
//            AccessKey=SecretId / SecretKey / SmsSdkAppId / 签名 / 模板ID
//   generic  任意 HTTP 短信接口：Endpoint + BodyTemplate
//            （{"phone":"{phone}","code":"{code}"}），可选 AuthHeader
// ============================================================

func SendRealNameSms(phone, code string) error {
	switch setting.RealNameSmsProvider {
	case "aliyun":
		return sendSmsAliyun(phone, code)
	case "tencent":
		return sendSmsTencent(phone, code)
	case "generic":
		return sendSmsGeneric(phone, code)
	default:
		return fmt.Errorf("未知的短信服务商: %s", setting.RealNameSmsProvider)
	}
}

func smsHTTPClient() *http.Client {
	timeout := setting.RealNameProviderTimeoutSec
	if timeout <= 0 || timeout > 60 {
		timeout = 10
	}
	return &http.Client{Timeout: time.Duration(timeout) * time.Second}
}

// ---------------- 阿里云短信（RPC 签名 HMAC-SHA1） ----------------

func sendSmsAliyun(phone, code string) error {
	ak := strings.TrimSpace(setting.RealNameSmsAccessKey)
	sk := strings.TrimSpace(setting.RealNameSmsSecretKey)
	sign := strings.TrimSpace(setting.RealNameSmsSignName)
	tpl := strings.TrimSpace(setting.RealNameSmsTemplateCode)
	if ak == "" || sk == "" || sign == "" || tpl == "" {
		return errors.New("阿里云短信配置不完整（AccessKey/SecretKey/签名/模板Code）")
	}
	params := map[string]string{
		"AccessKeyId":      ak,
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     phone,
		"RegionId":         "cn-hangzhou",
		"SignName":         sign,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   fmt.Sprintf("%d%d", time.Now().UnixNano(), time.Now().Unix()%1000),
		"SignatureVersion": "1.0",
		"TemplateCode":     tpl,
		"TemplateParam":    `{"code":"` + code + `"}`,
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2017-05-25",
	}
	query := canonicalAliyunQuery(params)
	stringToSign := "GET&" + pctEncodeAliyun("/") + "&" + pctEncodeAliyun(query)
	mac := hmac.New(sha1.New, []byte(sk+"&"))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	full := "https://dysmsapi.aliyuncs.com/?" + query + "&Signature=" + url.QueryEscape(signature)

	resp, err := smsHTTPClient().Get(full)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return errors.New("阿里云短信响应解析失败: " + truncateStr(string(body), 120))
	}
	if code, _ := out["Code"].(string); strings.EqualFold(code, "OK") {
		return nil
	}
	return errors.New("阿里云短信发送失败: " + truncateStr(string(body), 200))
}

func canonicalAliyunQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, pctEncodeAliyun(k)+"="+pctEncodeAliyun(params[k]))
	}
	return strings.Join(parts, "&")
}

func pctEncodeAliyun(s string) string {
	s = url.QueryEscape(s)
	s = strings.ReplaceAll(s, "+", "%20")
	s = strings.ReplaceAll(s, "*", "%2A")
	s = strings.ReplaceAll(s, "%7E", "~")
	return s
}

// ---------------- 腾讯云短信（TC3） ----------------

func sendSmsTencent(phone, code string) error {
	secretId := strings.TrimSpace(setting.RealNameSmsAccessKey)
	secretKey := strings.TrimSpace(setting.RealNameSmsSecretKey)
	appId := strings.TrimSpace(setting.RealNameSmsAppId)
	sign := strings.TrimSpace(setting.RealNameSmsSignName)
	tpl := strings.TrimSpace(setting.RealNameSmsTemplateCode)
	if secretId == "" || secretKey == "" || appId == "" || sign == "" || tpl == "" {
		return errors.New("腾讯云短信配置不完整（SecretId/SecretKey/SmsSdkAppId/签名/模板ID）")
	}
	payload, _ := json.Marshal(map[string]any{
		"PhoneNumber":      "+86" + phone,
		"SmsSdkAppId":      appId,
		"SignName":         sign,
		"TemplateId":       tpl,
		"TemplateParamSet": []string{code},
	})
	body, err := tc3Request("sms.tencentcloudapi.com", "sms", "SendSms",
		"2021-01-11", "ap-guangzhou", payload, secretId, secretKey)
	if err != nil {
		return err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return errors.New("腾讯云短信响应解析失败")
	}
	if resp, ok := data["Response"].(map[string]any); ok {
		if sendSet, ok := resp["SendStatusSet"].([]any); ok && len(sendSet) > 0 {
			if item, ok := sendSet[0].(map[string]any); ok {
				if c, _ := item["Code"].(string); strings.EqualFold(c, "Ok") {
					return nil
				}
				return errors.New("腾讯云短信发送失败: " + pickStr(item, "Message"))
			}
		}
		if e, ok := resp["Error"].(map[string]any); ok {
			return fmt.Errorf("腾讯云短信错误: %v", e["Message"])
		}
	}
	return errors.New("腾讯云短信响应格式异常")
}

// ---------------- 通用 HTTP ----------------

func sendSmsGeneric(phone, code string) error {
	endpoint := strings.TrimSpace(setting.RealNameSmsEndpoint)
	if endpoint == "" {
		return errors.New("未配置短信接口地址（Endpoint）")
	}
	bodyTpl := strings.TrimSpace(setting.RealNameSmsBodyTemplate)
	if bodyTpl == "" {
		bodyTpl = `{"phone":"{phone}","code":"{code}"}`
	}
	body := strings.ReplaceAll(bodyTpl, "{phone}", phone)
	body = strings.ReplaceAll(body, "{code}", code)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h := strings.TrimSpace(setting.RealNameSmsAuthHeader); h != "" {
		req.Header.Set("Authorization", h)
	}
	resp, err := smsHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		low := strings.ToLower(string(rb))
		if strings.Contains(low, "fail") || strings.Contains(low, "error") || strings.Contains(low, "\"success\":false") {
			return errors.New("短信接口返回失败: " + truncateStr(string(rb), 160))
		}
		return nil
	}
	return fmt.Errorf("短信接口 HTTP %d: %s", resp.StatusCode, truncateStr(string(rb), 160))
}
