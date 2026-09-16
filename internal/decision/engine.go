package decision

import (
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
	if err := validateInput(input); err != nil {
		return ExecutionPlan{}, err
	}
	active := input.Request.Contract.Active || input.Request.Model == "auto"
	strategy, reasons := effectiveStrategy(input)
	results := make([]CandidateResult, 0, len(input.Candidates))
	accepted := make([]Candidate, 0, len(input.Candidates))
	for _, candidate := range input.Candidates {
		result := CandidateResult{ModelID: candidate.ModelID, TargetID: candidate.Target.ID}
		if active {
			result.Reason = rejectReason(input, candidate)
		}
		if result.Reason == "" {
			result.Accepted = true
			result.Reason = "eligible"
			accepted = append(accepted, candidate)
		}
		results = append(results, result)
	}
	if len(accepted) == 0 {
		return ExecutionPlan{Candidates: results, EffectiveStrategy: strategy, Reasons: reasons}, &DecisionError{Code: "no_eligible_target"}
	}
	if active {
		sortCandidates(accepted, input, strategy)
	}
	plan := ExecutionPlan{
		SchemaVersion:     input.SchemaVersion,
		AlgorithmVersion:  input.AlgorithmVersion,
		EffectiveStrategy: strategy,
		Reasons:           reasons,
		Candidates:        results,
		Targets:           make([]PlanTarget, 0, len(accepted)),
	}
	for _, candidate := range accepted {
		plan.Targets = append(plan.Targets, PlanTarget{ModelID: candidate.ModelID, Target: cloneTarget(candidate.Target)})
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

func validateInput(input Input) error {
	if input.SchemaVersion != SchemaVersionV1 {
		return &DecisionError{Code: "unsupported_decision_schema"}
	}
	if input.AlgorithmVersion != AlgorithmVersionV1 {
		return &DecisionError{Code: "unsupported_decision_algorithm"}
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
	if run.Governed && run.SoftBudgetNanoUSD == 0 {
		return &DecisionError{Code: "invalid_run_budget"}
	}
	for _, candidate := range input.Candidates {
		switch candidate.Health.State {
		case "", "closed", "open", "half_open":
		default:
			return &DecisionError{Code: "invalid_decision_snapshot"}
		}
	}
	return nil
}

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
	if input.Run.MinimumAttemptWindow > 0 &&
		input.Run.RemainingDeadline < input.Run.MinimumAttemptWindow {
		return "deadline_insufficient"
	}
	return ""
}

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

func sortCandidates(candidates []Candidate, input Input, strategy string) {
	useEstimatedCost := hasEstimatedCost(input) && allPriced(candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
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

func hasEstimatedCost(input Input) bool {
	return input.Request.Contract.EstimatedInputTokens > 0 && input.Request.Contract.EstimatedOutputTokens > 0
}

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

func healthRank(health HealthSnapshot) int {
	switch health.State {
	case "half_open":
		return 1
	default:
		return 0
	}
}

func percentage(value int64, percent int) int64 {
	quotient, remainder := value/100, value%100
	return quotient*int64(percent) + remainder*int64(percent)/100
}

func validStrategy(strategy string) bool {
	return strategy == StrategyBalanced || strategy == StrategyEconomy
}

func validDataClass(dataClass string) bool {
	switch dataClass {
	case "public", "internal", "confidential", "restricted":
		return true
	default:
		return false
	}
}

func missingCapability(have, required []string) string {
	for _, want := range required {
		if !contains(have, want) {
			return want
		}
	}
	return ""
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cloneTarget(target catalog.Target) catalog.Target {
	target.Capabilities = append([]string(nil), target.Capabilities...)
	target.DataClasses = append([]string(nil), target.DataClasses...)
	if target.Pricing != nil {
		pricing := *target.Pricing
		target.Pricing = &pricing
	}
	return target
}
