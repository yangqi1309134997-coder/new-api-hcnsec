package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting"
)

// ============================================================
// 身份证二要素核验（B 路线）
// 服务商：
//   aliyun-market  阿里云市场 APPCODE 商品（个人可购、即买即用，推荐起步）
//                  GET {endpoint}?name={name}&idNo={idno}
//                  头 Authorization: APPCODE {AppCode}
//   tencent        腾讯云人脸核身 IdCardVerification（TC3 签名，企业实名）
//                  AppCode=SecretId，SecretKey=SecretKey
//   generic        任意 HTTP 接口：endpoint 支持 {name}/{idno} 占位；
//                  配了 BodyTemplate 走 POST(JSON)，否则 GET；
//                  可选 AppCode 作为 Authorization 头原样携带。
// 成功判定：默认启发式（code/status/errcode...∈{0,200,ok}、
//   data.isVerify==1、Response.Result==0），可用 SuccessRule 覆盖：
//   {"code_field":"code","code_ok":["0","200"],"msg_field":"msg"}
// ============================================================

type id2Outcome struct {
	OK       bool
	Msg      string
	Provider string
}

func id2HTTPClient() *http.Client {
	timeout := setting.RealNameProviderTimeoutSec
	if timeout <= 0 || timeout > 60 {
		timeout = 10
	}
	return &http.Client{Timeout: time.Duration(timeout) * time.Second}
}

func CheckId2(realName, idNo string) (*id2Outcome, error) {
	provider := setting.RealNameId2Provider
	switch provider {
	case "aliyun-market", "generic":
		return id2CheckHTTP(realName, idNo, provider)
	case "tencent":
		return id2CheckTencent(realName, idNo)
	case "national":
		return nil, errors.New("国家网络身份认证接口尚未开通（预留）")
	default:
		return nil, fmt.Errorf("未知的二要素服务商: %s", provider)
	}
}

func id2CheckHTTP(realName, idNo, provider string) (*id2Outcome, error) {
	endpoint := strings.TrimSpace(setting.RealNameId2Endpoint)
	if endpoint == "" {
		return nil, errors.New("未配置二要素接口地址（Endpoint）")
	}
	endpoint = strings.ReplaceAll(endpoint, "{name}", url.QueryEscape(realName))
	endpoint = strings.ReplaceAll(endpoint, "{idno}", url.QueryEscape(idNo))
	endpoint = strings.ReplaceAll(endpoint, "{idNo}", url.QueryEscape(idNo))

	var req *http.Request
	var err error
	if bodyTpl := strings.TrimSpace(setting.RealNameId2BodyTemplate); bodyTpl != "" {
		body := strings.ReplaceAll(bodyTpl, "{name}", realName)
		body = strings.ReplaceAll(body, "{idno}", idNo)
		body = strings.ReplaceAll(body, "{idNo}", idNo)
		req, err = http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
	}
	if code := strings.TrimSpace(setting.RealNameId2AppCode); code != "" {
		if provider == "aliyun-market" {
			req.Header.Set("Authorization", "APPCODE "+code)
		} else {
			req.Header.Set("Authorization", code)
		}
	}
	resp, err := id2HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return &id2Outcome{OK: false, Provider: provider,
			Msg: fmt.Sprintf("接口返回 HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 160))}, nil
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		// 非 JSON 响应：正文包含"一致/通过/success"则视为通过
		low := strings.ToLower(string(body))
		if strings.Contains(low, "一致") || strings.Contains(low, "通过") || strings.Contains(low, "success") {
			return &id2Outcome{OK: true, Provider: provider, Msg: "ok"}, nil
		}
		return &id2Outcome{OK: false, Provider: provider, Msg: "接口响应无法解析: " + truncateStr(string(body), 160)}, nil
	}
	ok, msg := id2JudgeSuccess(data)
	return &id2Outcome{OK: ok, Provider: provider, Msg: msg}, nil
}

// id2JudgeSuccess 按可配置规则或默认启发式判定 JSON 响应。
func id2JudgeSuccess(data map[string]any) (bool, string) {
	msg := pickStr(data, "msg", "message", "info", "description", "errmsg")
	if rule := strings.TrimSpace(setting.RealNameId2SuccessRule); rule != "" {
		var r struct {
			CodeField string   `json:"code_field"`
			CodeOK    []string `json:"code_ok"`
			MsgField  string   `json:"msg_field"`
		}
		if err := json.Unmarshal([]byte(rule), &r); err == nil && r.CodeField != "" {
			val := digPath(data, r.CodeField)
			for _, want := range r.CodeOK {
				if fmt.Sprint(val) == want {
					return true, pickStr(data, r.MsgField)
				}
			}
			return false, pickStr(data, r.MsgField)
		}
	}
	// 默认启发式
	for _, key := range []string{"code", "status", "errcode", "err_code", "error_code"} {
		if v, ok := data[key]; ok {
			s := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
			if s == "0" || s == "200" || s == "ok" || s == "success" {
				return true, msg
			}
			if s != "" {
				return false, msg
			}
		}
	}
	if resp, ok := data["Response"].(map[string]any); ok { // 腾讯风格
		if r, ok := resp["Result"]; ok {
			return fmt.Sprint(r) == "0", pickStr(resp, "Description")
		}
	}
	if d, ok := data["data"].(map[string]any); ok { // 阿里云市场常见
		if v, ok := d["isVerify"]; ok {
			s := fmt.Sprint(v)
			return s == "1" || strings.EqualFold(s, "true"), msg
		}
	}
	return false, msg
}

func digPath(data map[string]any, path string) any {
	cur := any(data)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

func pickStr(data map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := data[k]; ok {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------- 腾讯云 TC3 ----------------

func id2CheckTencent(realName, idNo string) (*id2Outcome, error) {
	secretId := strings.TrimSpace(setting.RealNameId2AppCode)
	secretKey := strings.TrimSpace(setting.RealNameId2SecretKey)
	if secretId == "" || secretKey == "" {
		return nil, errors.New("腾讯云二要素未配置 SecretId/SecretKey")
	}
	payload, _ := json.Marshal(map[string]string{"IdCard": idNo, "Name": realName})
	body, err := tc3Request("faceid.tencentcloudapi.com", "faceid", "IdCardVerification",
		"2023-03-15", "", payload, secretId, secretKey)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, errors.New("腾讯云响应解析失败: " + truncateStr(string(body), 160))
	}
	if resp, ok := data["Response"].(map[string]any); ok {
		if e, ok := resp["Error"].(map[string]any); ok {
			return nil, fmt.Errorf("腾讯云错误: %v", e["Message"])
		}
		result := -1
		if r, ok := resp["Result"].(float64); ok {
			result = int(r)
		}
		desc := pickStr(resp, "Description")
		// Result: 0=通过 1=身份证与姓名不匹配 其他=异常
		return &id2Outcome{OK: result == 0, Provider: "tencent", Msg: desc}, nil
	}
	return &id2Outcome{OK: false, Provider: "tencent", Msg: "响应格式异常"}, nil
}

// tc3Request 腾讯云 TC3 签名请求（faceid 二要素 / sms 共用）。
func tc3Request(host, service, action, version, region string, payload []byte, secretId, secretKey string) ([]byte, error) {
	timeout := setting.RealNameProviderTimeoutSec
	if timeout <= 0 || timeout > 60 {
		timeout = 10
	}
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	ts := time.Now().Unix()
	date := time.Unix(ts, 0).UTC().Format("2006-01-02")

	hashedPayload := sha256Hex(payload)
	canonicalReq := strings.Join([]string{
		"POST", "/", "",
		"content-type:application/json; charset=utf-8",
		"host:" + host, "",
		"content-type;host",
		hashedPayload,
	}, "\n")
	stringToSign := strings.Join([]string{
		"TC3-HMAC-SHA256",
		strconv.FormatInt(ts, 10),
		date + "/" + service + "/tc3_request",
		sha256Hex([]byte(canonicalReq)),
	}, "\n")
	sig := hex.EncodeToString(hmacSHA256(
		hmacSHA256(hmacSHA256(hmacSHA256([]byte("TC3-HMAC-SHA256"), []byte(date)),
			[]byte(service)), []byte("tc3_request")),
		[]byte(stringToSign)))

	endpoint := "https://" + host + "/"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-TC-Version", version)
	if region != "" {
		req.Header.Set("X-TC-Region", region)
	}
	req.Header.Set("Authorization",
		"TC3-HMAC-SHA256 Credential="+secretId+"/"+date+"/"+service+"/tc3_request, SignedHeaders=content-type;host, Signature="+sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<16))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
