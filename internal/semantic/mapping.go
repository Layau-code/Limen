package semantic

import (
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/decision"
)

// Snapshot 创建不保留评估文本的冻结决策记录。
func Snapshot(mode, status, reason string, state State, cfg config.SemanticRouting) *decision.SemanticAssessment {
	return &decision.SemanticAssessment{Mode: mode, Status: status, Reason: reason, StateHash: state.Hash, StateLength: state.Length, Truncated: state.Truncated, StateBuilderVersion: cfg.StateBuilderVersion, QuestionTemplateVersion: cfg.QuestionTemplateVersion, MappingVersion: cfg.MappingVersion, Language: state.Language}
}

// CostNanoUSD 仅在配置单价时估算网关承担的 Jev 费用。
func CostNanoUSD(cfg config.SemanticRouting, result Assessment) (int64, bool) {
	if cfg.AssessmentPricing == nil {
		return 0, false
	}
	value, err := cfg.AssessmentPricing.Cost(result.InputTokens, result.OutputTokens)
	if err != nil {
		return 0, false
	}
	return value, true
}

// Map 将已校验的 Jev 结果转换为确定且受限的路由信号。
func Map(cfg config.SemanticRouting, state State, result Assessment) *decision.SemanticAssessment {
	snapshot := Snapshot("active", "assessed", "", state, cfg)
	snapshot.ModelVersion = result.ModelVersion
	snapshot.TaskType, snapshot.TaskProbabilities, snapshot.TaskConfidence = result.TaskType, cloneProbabilities(result.TaskProbabilities), result.TaskConfidence
	snapshot.Complexity, snapshot.ComplexityProbabilities, snapshot.ComplexityConfidence = result.Complexity, cloneProbabilities(result.ComplexityProbabilities), result.ComplexityConfidence
	if result.TaskType == "other" || state.Truncated || state.Language != "zh" && state.Language != "en" {
		return snapshot
	}
	rule := findRule(cfg.Rules, result.TaskType, result.Complexity, state.Language)
	if rule == nil {
		return snapshot
	}
	snapshot.ThresholdProfile = rule.ThresholdProfile
	if result.TaskConfidence < rule.MinimumTaskConfidence || result.ComplexityConfidence < rule.MinimumComplexityConfidence || probabilityMargin(result.TaskProbabilities) < rule.MinimumProbabilityMargin || probabilityMargin(result.ComplexityProbabilities) < rule.MinimumProbabilityMargin {
		snapshot.Status, snapshot.Reason = "low_confidence", "low_confidence"
		return snapshot
	}
	snapshot.MinimumQualityTier = rule.MinimumQualityTier
	snapshot.PreferredTargetIDs = append([]string(nil), rule.PreferredTargetIDs...)
	snapshot.Applied = snapshot.MinimumQualityTier > 0 || len(snapshot.PreferredTargetIDs) > 0
	return snapshot
}

// findRule 按任务、复杂度和语言查找最具体的路由规则。
func findRule(rules []config.SemanticRule, task, complexity, language string) *config.SemanticRule {
	var general *config.SemanticRule
	for index := range rules {
		rule := &rules[index]
		if rule.TaskType != task || rule.Complexity != complexity {
			continue
		}
		if rule.Language == language {
			return rule
		}
		if rule.Language == "" {
			general = rule
		}
	}
	return general
}

// probabilityMargin 返回最高和次高分类概率之间的差值。
func probabilityMargin(values map[string]float64) float64 {
	first, second := -1.0, -1.0
	for _, value := range values {
		if value > first {
			second, first = first, value
		} else if value > second {
			second = value
		}
	}
	if second < 0 {
		return 1
	}
	return first - second
}
