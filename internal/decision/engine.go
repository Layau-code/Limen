package decision

import (
	"math"
	"sort"

	"github.com/huz/limen/internal/catalog"
)

const (
	// StrategyBalanced 按稳定性、质量、成本排序。
	StrategyBalanced = "balanced"
	// StrategyEconomy 按成本、稳定性、质量排序。
	StrategyEconomy = "economy"
)

// DecisionError 表示能力契约或候选目标无法满足决策要求。
type DecisionError struct {
	Code string
}

// Error 返回不包含敏感信息的决策错误。
func (err *DecisionError) Error() string {
	return err.Code
}

// Engine 生成不依赖外部状态的确定性执行计划。
type Engine struct{}

// NewEngine 创建无状态的 Decision Engine。
func NewEngine() Engine {
	return Engine{}
}

// Decide 按固定硬过滤和策略排序生成 ExecutionPlan。
func (Engine) Decide(input Input) (ExecutionPlan, error) {
	if input.AlgorithmVersion == AlgorithmVersionV2 || input.AlgorithmVersion == AlgorithmVersionV3 {
		input = canonicalizeInput(input)
	}
	if err := validateInput(input); err != nil {
		return ExecutionPlan{}, err
	}
	active := input.Request.Contract.Active || input.Request.Model == "auto"
	strategy, reasons := effectiveStrategy(input)
	selectionInput := input
	semanticQuality := input.AlgorithmVersion == AlgorithmVersionV3 && input.SemanticAssessment != nil && input.SemanticAssessment.Applied && input.SemanticAssessment.MinimumQualityTier > input.Request.Contract.MinimumQualityTier
	if semanticQuality {
		selectionInput.Request.Contract.MinimumQualityTier = input.SemanticAssessment.MinimumQualityTier
	}
	results, accepted := eligibleCandidates(selectionInput, active)
	if len(accepted) == 0 && semanticQuality {
		semanticQuality = false
		selectionInput = input
		results, accepted = eligibleCandidates(selectionInput, active)
		reasons = append(reasons, "semantic_quality_fallback")
	}
	if len(accepted) == 0 {
		plan := ExecutionPlan{SchemaVersion: input.SchemaVersion, AlgorithmVersion: input.AlgorithmVersion, ConfigVersion: input.ConfigVersion, Candidates: results, EffectiveStrategy: strategy, Reasons: reasons, SemanticStatus: semanticStatus(input)}
		inputHash, err := HashInput(input)
		if err != nil {
			return ExecutionPlan{}, err
		}
		plan.InputHash = inputHash
		plan.PlanHash, err = HashPlan(plan)
		if err != nil {
			return ExecutionPlan{}, err
		}
		return plan, &DecisionError{Code: "no_eligible_target"}
	}
	if active {
		preferred := semanticPreferredTargets(input, accepted)
		if input.SemanticAssessment != nil && input.SemanticAssessment.Applied && len(input.SemanticAssessment.PreferredTargetIDs) > 0 && len(preferred) == 0 {
			reasons = append(reasons, "semantic_preference_unavailable")
		}
		sortCandidatesWithPreferences(accepted, input, strategy, preferred, semanticQuality || len(preferred) > 0)
	}
	plan := ExecutionPlan{
		SchemaVersion:     input.SchemaVersion,
		AlgorithmVersion:  input.AlgorithmVersion,
		ConfigVersion:     input.ConfigVersion,
		EffectiveStrategy: strategy,
		Reasons:           reasons,
		SemanticStatus:    semanticStatus(input),
		Candidates:        results,
		Targets:           make([]PlanTarget, 0, len(accepted)),
	}
	for _, candidate := range accepted {
		plan.Targets = append(plan.Targets, PlanTarget{ModelID: candidate.ModelID, Compatibility: candidate.Compatibility, Target: cloneTarget(candidate.Target)})
	}
	inputHash, err := HashInput(input)
	if err != nil {
		return ExecutionPlan{}, err
	}
	plan.InputHash = inputHash
	plan.PlanHash, err = HashPlan(plan)
	if err != nil {
		return ExecutionPlan{}, err
	}
	return plan, nil
}

// semanticStatus 返回快照中冻结的语义评估状态。
func semanticStatus(input Input) string {
	if input.SemanticAssessment == nil {
		return ""
	}
	return input.SemanticAssessment.Status
}

// eligibleCandidates 按硬约束生成候选结果和可执行目标。
func eligibleCandidates(input Input, active bool) ([]CandidateResult, []Candidate) {
	results := make([]CandidateResult, 0, len(input.Candidates))
	accepted := make([]Candidate, 0, len(input.Candidates))
	for _, candidate := range input.Candidates {
		result := CandidateResult{ModelID: candidate.ModelID, TargetID: candidate.Target.ID}
		if active {
			result.Reason = rejectReason(input, candidate)
		}
		if result.Reason == "" {
			result.Accepted, result.Reason = true, "eligible"
			accepted = append(accepted, candidate)
		}
		results = append(results, result)
	}
	return results, accepted
}

// validateInput 校验快照版本、契约边界和外部状态枚举。
func validateInput(input Input) error {
	if input.SchemaVersion != SchemaVersionV1 && input.SchemaVersion != SchemaVersionV2 {
		return &DecisionError{Code: "unsupported_decision_schema"}
	}
	if input.AlgorithmVersion != AlgorithmVersionV1 && input.AlgorithmVersion != AlgorithmVersionV2 && input.AlgorithmVersion != AlgorithmVersionV3 {
		return &DecisionError{Code: "unsupported_decision_algorithm"}
	}
	if (input.AlgorithmVersion == AlgorithmVersionV3 && (input.SchemaVersion != SchemaVersionV2 || input.SemanticAssessment == nil)) || (input.AlgorithmVersion != AlgorithmVersionV3 && (input.SchemaVersion != SchemaVersionV1 || input.SemanticAssessment != nil)) {
		return &DecisionError{Code: "invalid_decision_snapshot"}
	}
	if input.Request.Model == "" || input.EvaluatedAtUnixMS < 0 {
		return &DecisionError{Code: "invalid_decision_snapshot"}
	}
	contract := input.Request.Contract
	if contract.MinimumQualityTier < 0 || contract.MinimumQualityTier > 5 ||
		contract.RequiredContextTokens < 0 ||
		contract.EstimatedInputTokens < 0 ||
		contract.EstimatedOutputTokens < 0 ||
		contract.DataClass != "" && !validDataClass(contract.DataClass) {
		return &DecisionError{Code: "invalid_capability_contract"}
	}
	if contract.Strategy != "" && !validStrategy(contract.Strategy) {
		return &DecisionError{Code: "strategy_conflict"}
	}
	run := input.Run
	if run.SettledCostNanoUSD < 0 || run.SoftBudgetNanoUSD < 0 ||
		run.EconomyThresholdPercent < 0 || run.EconomyThresholdPercent > 100 ||
		run.RemainingDeadline < 0 || run.MinimumAttemptWindow < 0 {
		return &DecisionError{Code: "invalid_decision_snapshot"}
	}
	for _, candidate := range input.Candidates {
		switch candidate.Health.State {
		case "", "closed", "open", "half_open":
		default:
			return &DecisionError{Code: "invalid_decision_snapshot"}
		}
	}
	if input.SemanticAssessment != nil {
		if err := validateSemanticAssessment(*input.SemanticAssessment); err != nil {
			return err
		}
	}
	return nil
}

// validateSemanticAssessment 校验冻结语义信号的枚举和概率分布。
func validateSemanticAssessment(value SemanticAssessment) error {
	validStatus := value.Status == "assessed" || value.Status == "not_enabled" || value.Status == "not_authorized" || value.Status == "data_class_denied" || value.Status == "no_usable_state" || value.Status == "low_confidence" || value.Status == "provider_timeout" || value.Status == "provider_error" || value.Status == "invalid_response" || value.Status == "version_mismatch" || value.Status == "bypassed"
	if value.Mode != "active" || !validStatus || value.StateLength < 0 || value.MinimumQualityTier < 0 || value.MinimumQualityTier > 5 || !validProbability(value.TaskConfidence) || !validProbability(value.ComplexityConfidence) {
		return &DecisionError{Code: "invalid_semantic_snapshot"}
	}
	if value.Language != "" && value.Language != "zh" && value.Language != "en" && value.Language != "mixed" && value.Language != "unknown" {
		return &DecisionError{Code: "invalid_semantic_snapshot"}
	}
	if value.Status == "assessed" || value.Status == "low_confidence" {
		if value.TaskType != "extraction" && value.TaskType != "transformation" && value.TaskType != "writing" && value.TaskType != "code" && value.TaskType != "analysis" && value.TaskType != "other" {
			return &DecisionError{Code: "invalid_semantic_snapshot"}
		}
		if value.Complexity != "simple" && value.Complexity != "standard" && value.Complexity != "complex" {
			return &DecisionError{Code: "invalid_semantic_snapshot"}
		}
		if !validDistribution(value.TaskProbabilities, []string{"extraction", "transformation", "writing", "code", "analysis", "other"}) || !validDistribution(value.ComplexityProbabilities, []string{"simple", "standard", "complex"}) {
			return &DecisionError{Code: "invalid_semantic_snapshot"}
		}
	} else if value.Applied {
		return &DecisionError{Code: "invalid_semantic_snapshot"}
	}
	if value.Applied && (value.Status != "assessed" || value.Truncated || value.MappingVersion == "" || value.ThresholdProfile == "" || value.MinimumQualityTier == 0 && len(value.PreferredTargetIDs) == 0) {
		return &DecisionError{Code: "invalid_semantic_snapshot"}
	}
	for _, id := range value.PreferredTargetIDs {
		if id == "" {
			return &DecisionError{Code: "invalid_semantic_snapshot"}
		}
	}
	return nil
}

// validProbability 判断值是否为有限的 [0,1] 概率。
func validProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// validDistribution 校验分类概率的键和值及总和。
func validDistribution(values map[string]float64, allowed []string) bool {
	if len(values) == 0 {
		return false
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, item := range allowed {
		allowedSet[item] = true
	}
	sum := 0.0
	for key, value := range values {
		if !allowedSet[key] || !validProbability(value) {
			return false
		}
		sum += value
	}
	return sum >= 0.95 && sum <= 1.05
}

// rejectReason 按固定顺序检查候选目标是否违反硬约束。
func rejectReason(input Input, candidate Candidate) string {
	target := candidate.Target
	if !candidate.Enabled {
		return "target_disabled"
	}
	if !candidate.SecurityAllowed {
		return "security_policy_denied"
	}
	contract := input.Request.Contract
	if missing := missingCapability(target.Capabilities, contract.RequiredCapabilities); missing != "" {
		return "missing_capability:" + missing
	}
	if contract.MinimumQualityTier > target.QualityTier {
		return "quality_tier_too_low"
	}
	if input.Request.Stream && !target.SupportsStreaming {
		return "streaming_unsupported"
	}
	if contract.RequiredContextTokens > target.ContextWindow {
		return "context_window_too_small"
	}
	if contract.DataClass != "" && !contains(target.DataClasses, contract.DataClass) {
		return "data_policy_denied"
	}
	if input.Run.Governed && target.Pricing == nil {
		return "pricing_missing"
	}
	switch candidate.Health.State {
	case "open":
		return "circuit_open"
	case "half_open":
		if !candidate.Health.ProbeAvailable {
			return "circuit_probe_busy"
		}
	}
	if input.Run.MinimumAttemptWindow > 0 && input.Run.RemainingDeadline > 0 &&
		input.Run.RemainingDeadline < input.Run.MinimumAttemptWindow {
		return "deadline_insufficient"
	}
	return ""
}

// effectiveStrategy 根据 Run 软预算快照确定本次排序策略。
func effectiveStrategy(input Input) (string, []string) {
	strategy := input.Request.Contract.Strategy
	if strategy == "" {
		strategy = StrategyBalanced
	}
	if !input.Run.Governed || strategy != StrategyBalanced || input.Run.SoftBudgetNanoUSD == 0 {
		return strategy, nil
	}
	remaining := input.Run.SoftBudgetNanoUSD - input.Run.SettledCostNanoUSD
	if remaining < 0 {
		remaining = 0
	}
	threshold := percentage(input.Run.SoftBudgetNanoUSD, input.Run.EconomyThresholdPercent)
	if remaining <= threshold {
		return StrategyEconomy, []string{"economy_threshold_reached"}
	}
	return strategy, nil
}

// sortCandidates 使用稳定排序生成可解释且可复现的目标顺序。
func sortCandidates(candidates []Candidate, input Input, strategy string) {
	sortCandidatesWithPreferences(candidates, input, strategy, nil, false)
}

// sortCandidatesWithPreferences 在既有策略中应用受限的语义偏好排序。
func sortCandidatesWithPreferences(candidates []Candidate, input Input, strategy string, preferred map[string]bool, semanticOrder bool) {
	useEstimatedCost := hasEstimatedCost(input) && allPriced(candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if semanticOrder {
			if healthRank(left.Health) != healthRank(right.Health) {
				return healthRank(left.Health) < healthRank(right.Health)
			}
			if len(preferred) > 0 && preferred[left.Target.ID] != preferred[right.Target.ID] {
				return preferred[left.Target.ID]
			}
		}
		if strategy == StrategyEconomy {
			if compareCost(left, right, input, useEstimatedCost) != 0 {
				return compareCost(left, right, input, useEstimatedCost) < 0
			}
			if healthRank(left.Health) != healthRank(right.Health) {
				return healthRank(left.Health) < healthRank(right.Health)
			}
		} else {
			if healthRank(left.Health) != healthRank(right.Health) {
				return healthRank(left.Health) < healthRank(right.Health)
			}
		}
		if left.Target.QualityTier != right.Target.QualityTier {
			return left.Target.QualityTier > right.Target.QualityTier
		}
		if strategy == StrategyBalanced && compareCost(left, right, input, useEstimatedCost) != 0 {
			return compareCost(left, right, input, useEstimatedCost) < 0
		}
		leftID := left.ModelID + "\x00" + left.Target.ID
		rightID := right.ModelID + "\x00" + right.Target.ID
		return leftID < rightID
	})
}

// semanticPreferredTargets 仅保留当前硬约束后仍合格的偏好目标。
func semanticPreferredTargets(input Input, candidates []Candidate) map[string]bool {
	if input.AlgorithmVersion != AlgorithmVersionV3 || input.SemanticAssessment == nil || !input.SemanticAssessment.Applied || len(input.SemanticAssessment.PreferredTargetIDs) == 0 {
		return nil
	}
	wanted := make(map[string]bool, len(input.SemanticAssessment.PreferredTargetIDs))
	for _, id := range input.SemanticAssessment.PreferredTargetIDs {
		wanted[id] = true
	}
	available := make(map[string]bool)
	for _, candidate := range candidates {
		if wanted[candidate.Target.ID] {
			available[candidate.Target.ID] = true
		}
	}
	return available
}

// hasEstimatedCost 判断请求是否提供了完整的输入和输出 Token 估算。
func hasEstimatedCost(input Input) bool {
	return input.Request.Contract.EstimatedInputTokens > 0 && input.Request.Contract.EstimatedOutputTokens > 0
}

// allPriced 判断候选目标是否都配置了可计算价格。
func allPriced(candidates []Candidate) bool {
	for _, candidate := range candidates {
		if candidate.Target.Pricing == nil {
			return false
		}
		if _, err := candidate.Target.Pricing.Cost(0, 0); err != nil {
			return false
		}
	}
	return true
}

// compareCost 比较两个目标的预计定点成本或配置成本等级。
func compareCost(left, right Candidate, input Input, useEstimated bool) int {
	leftCost, rightCost := int64(left.Target.CostTier), int64(right.Target.CostTier)
	if useEstimated {
		contract := input.Request.Contract
		leftValue, leftErr := left.Target.Pricing.Cost(contract.EstimatedInputTokens, contract.EstimatedOutputTokens)
		rightValue, rightErr := right.Target.Pricing.Cost(contract.EstimatedInputTokens, contract.EstimatedOutputTokens)
		if leftErr == nil && rightErr == nil {
			leftCost, rightCost = leftValue, rightValue
		}
	}
	switch {
	case leftCost < rightCost:
		return -1
	case leftCost > rightCost:
		return 1
	default:
		return 0
	}
}

// healthRank 将健康观察映射为稳定排序等级。
func healthRank(health HealthSnapshot) int {
	switch health.State {
	case "half_open":
		return 1
	default:
		return 0
	}
}

// percentage 以整数运算计算软预算百分比，避免浮点误差。
func percentage(value int64, percent int) int64 {
	quotient, remainder := value/100, value%100
	return quotient*int64(percent) + remainder*int64(percent)/100
}

// validStrategy 判断策略是否属于当前版本的受控集合。
func validStrategy(strategy string) bool {
	return strategy == StrategyBalanced || strategy == StrategyEconomy
}

// validDataClass 判断数据等级是否属于当前版本的受控集合。
func validDataClass(dataClass string) bool {
	switch dataClass {
	case "public", "internal", "confidential", "restricted":
		return true
	default:
		return false
	}
}

// missingCapability 返回候选目标缺少的第一个必需能力。
func missingCapability(have, required []string) string {
	for _, want := range required {
		if !contains(have, want) {
			return want
		}
	}
	return ""
}

// contains 判断字符串切片是否包含目标值。
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// cloneTarget 深复制目标元数据，避免计划和注册表共享可变切片。
func cloneTarget(target catalog.Target) catalog.Target {
	target.Capabilities = append([]string(nil), target.Capabilities...)
	target.DataClasses = append([]string(nil), target.DataClasses...)
	if target.Pricing != nil {
		pricing := *target.Pricing
		target.Pricing = &pricing
	}
	return target
}
