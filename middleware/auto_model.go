package middleware

import (
	"math/rand"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// resolveAutoSmartModel 处理 auto 智能模型：
// 请求模型名为 auto 时，在候选池（auto_model_setting.pool）∩ 目标分组可用模型中，
// 按渠道健康分选出最优真实模型并返回；同时把计费模型名固定为 auto
// （BillingModelName 机制），用户侧日志与价格保持原样。
// 不满足改写条件时返回空串，调用方维持原模型走既有选择逻辑。
func resolveAutoSmartModel(c *gin.Context, requestedModel string) string {
	autoSetting := operation_setting.GetAutoModelSetting()
	if !autoSetting.Enabled || requestedModel != operation_setting.AutoSmartModelName {
		return ""
	}
	groups := autoSmartModelGroups(c)
	if len(groups) == 0 {
		return ""
	}
	candidates := autoSmartModelCandidates(c, autoSetting.Pool)
	if len(candidates) == 0 {
		return ""
	}
	scores := model.SmartModelScores(groups, candidates)
	if len(scores) == 0 {
		return ""
	}
	best := ""
	// 小概率在未被封禁的候选内均匀探索，让新加入池子的模型能积累健康样本
	if explore := operation_setting.GetLoadBalanceSetting().SmartExplorePercent; explore > 0 && rand.Intn(100) < explore {
		picks := make([]string, 0, len(scores))
		for candidate := range scores {
			picks = append(picks, candidate)
		}
		best = picks[rand.Intn(len(picks))]
	} else {
		for _, candidate := range candidates {
			if score, ok := scores[candidate]; ok && (best == "" || score > scores[best]) {
				best = candidate
			}
		}
	}
	if best == "" {
		return ""
	}
	common.SetContextKey(c, constant.ContextKeyBillingModelOverride, operation_setting.AutoSmartModelName)
	logger.LogInfo(c, common.LogText("auto smart model routed: %s -> %s (groups=%v)", requestedModel, best, groups))
	return best
}

// autoSmartModelGroups 解析 auto 请求的目标分组：
// 普通分组即其本身；令牌分组为 auto（自动分组）时展开为用户可用的分组序列。
func autoSmartModelGroups(c *gin.Context) []string {
	usingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if usingGroup == "" {
		return nil
	}
	if usingGroup != "auto" {
		return []string{usingGroup}
	}
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	groups := service.GetRequestAutoGroups(c, userGroup)
	if len(groups) == 0 {
		return nil
	}
	return groups
}

// autoSmartModelCandidates 返回通过令牌模型限制校验的候选模型。
// 外层已校验 auto 本身；这里保证改写后的真实模型同样被允许。
func autoSmartModelCandidates(c *gin.Context, pool []string) []string {
	if len(pool) == 0 {
		return nil
	}
	if !common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
		return pool
	}
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenModelLimit)
	if !ok || value == nil {
		return nil
	}
	limit, _ := value.(map[string]bool)
	candidates := make([]string, 0, len(pool))
	for _, modelName := range pool {
		if TokenModelLimitAllows(limit, modelName) {
			candidates = append(candidates, modelName)
		}
	}
	return candidates
}
