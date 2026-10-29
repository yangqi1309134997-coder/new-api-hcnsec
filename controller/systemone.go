package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// RelaySystemone /v1/systemone 决策模型中继。
//
// 结构化判定接口（AMD Radeon 风格）：
//
//	请求  {"model": "...", "state": {...}, "questions": {"q1": {"type": "choice"|"noul"|"score", ...}}}
//	响应  {"answers": {"q1": {"choice": "...", "confidence": 0.99, ...}}, "usage": {"input_tokens": N, "output_tokens": 0}}
//
// 请求体原样透传到所选渠道的 {base_url}/v1/systemone（渠道类型用 OpenAI，
// 渠道分组的路由/负载均衡/多 key 轮询全部复用），按 usage.input_tokens
// 走与文本一致的倍率计费（决策模型 output_tokens 恒为 0，只收输入）。
func RelaySystemone(c *gin.Context) {
	bodyBytes, err := common.GetBodyBytesFromContext(c)
	if err != nil {
		common.FinalizeTaskLog(c, "error", "读取请求体失败: "+err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "无法读取请求体: " + err.Error(), "type": "new_api_error",
		}})
		return
	}
	common.InitTaskLog(c, bodyBytes)

	info := relaycommon.GenRelayInfoSystemone(c)
	info.InitChannelMeta(c)

	// 计费价目：与文本计费同一套 model_ratio × group_ratio 体系
	if _, perr := helper.ModelPriceHelper(c, info, 0, &types.TokenCountMeta{}); perr != nil {
		common.FinalizeTaskLog(c, "error", perr.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": perr.Error(), "type": "new_api_error",
		}})
		return
	}

	base := strings.TrimRight(info.ChannelBaseUrl, "/")
	if base == "" {
		common.FinalizeTaskLog(c, "error", "所选渠道未配置上游地址")
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"message": "所选渠道未配置上游地址", "type": "new_api_error",
		}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 300*time.Second)
	defer cancel()
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/systemone", bytes.NewReader(bodyBytes))
	if rerr != nil {
		common.FinalizeTaskLog(c, "error", rerr.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"message": "构造上游请求失败: " + rerr.Error(), "type": "new_api_error",
		}})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if info.ApiKey != "" {
		req.Header.Set("Authorization", "Bearer "+info.ApiKey)
	}

	client, cerr := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if cerr != nil {
		common.FinalizeTaskLog(c, "error", "构造HTTP客户端失败: "+cerr.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"message": "构造HTTP客户端失败: " + cerr.Error(), "type": "new_api_error",
		}})
		return
	}

	resp, derr := client.Do(req)
	if derr != nil {
		logger.LogError(c, common.LogText("systemone relay upstream error: %s", derr.Error()))
		common.FinalizeTaskLog(c, "error", derr.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"message": "上游请求失败: " + derr.Error(), "type": "new_api_error",
		}})
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode != http.StatusOK {
		errPreview := string(respBytes)
		if len(errPreview) > 200 {
			errPreview = errPreview[:200]
		}
		logger.LogWarn(c, common.LogText("systemone relay upstream HTTP %d: %s", resp.StatusCode, errPreview))
		common.FinalizeTaskLog(c, "error", fmt.Sprintf("upstream HTTP %d", resp.StatusCode))
		// 上游错误原样透传，保留其错误结构供调用方判断
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		c.Data(resp.StatusCode, ct, respBytes)
		return
	}

	var out struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(respBytes, &out)
	usage := &dto.Usage{
		PromptTokens:     out.Usage.InputTokens,
		CompletionTokens: out.Usage.OutputTokens,
		TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
	}

	// 计费 + 消费日志（tokens × model_ratio × group_ratio，output 恒为 0）
	service.PostTextConsumeQuota(c, info, usage, nil)

	common.FinalizeTaskLog(c, "success", fmt.Sprintf("systemone ok useTime=%ds tokens=%d", int(time.Since(info.StartTime).Seconds()), usage.TotalTokens))
	c.Data(http.StatusOK, "application/json", respBytes)
}
