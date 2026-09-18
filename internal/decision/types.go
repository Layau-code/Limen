// Package decision 根据能力和运行快照生成可解释的模型执行计划。
package decision

import (
	"time"

	"github.com/huz/limen/internal/catalog"
)

const (
	// SchemaVersionV1 是第一版决策输入格式。
	SchemaVersionV1 = "decision-input.v1"
	// AlgorithmVersionV1 是第一版决策算法。
	AlgorithmVersionV1 = "decision.v1"
	// AlgorithmVersionV2 是采用集合规范化的决策算法。
	AlgorithmVersionV2 = "decision.v2"
)

// Contract 描述一次请求需要的能力和治理约束。
type Contract struct {
	Active                bool     `json:"active"`
	RequiredCapabilities  []string `json:"required_capabilities"`
	MinimumQualityTier    int      `json:"minimum_quality_tier"`
	RequiredContextTokens int64    `json:"required_context_tokens"`
	DataClass             string   `json:"data_class"`
	EstimatedInputTokens  int64    `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64    `json:"estimated_output_tokens"`
	Strategy              string   `json:"strategy"`
}

// Request 保存参与模型决策的请求字段。
type Request struct {
	Model    string   `json:"model"`
	Stream   bool     `json:"stream"`
	Contract Contract `json:"contract"`
}

// RunSnapshot 保存决策时的 Run 预算和截止时间快照。
type RunSnapshot struct {
	Governed                bool          `json:"governed"`
	SettledCostNanoUSD      int64         `json:"settled_cost_nano_usd"`
	SoftBudgetNanoUSD       int64         `json:"soft_budget_nano_usd"`
	EconomyThresholdPercent int           `json:"economy_threshold_percent"`
	RemainingDeadline       time.Duration `json:"remaining_deadline_nanos"` // 为零表示没有截止时间。
	MinimumAttemptWindow    time.Duration `json:"minimum_attempt_window_nanos"`
}

// HealthSnapshot 保存目标在决策时观察到的熔断状态。
type HealthSnapshot struct {
	State          string `json:"state"`
	ProbeAvailable bool   `json:"probe_available"`
}

// Candidate 将目录目标和决策时的外部状态组合起来。
type Candidate struct {
	ModelID         string         `json:"model_id"`
	Compatibility   bool           `json:"compatibility,omitempty"`
	Target          catalog.Target `json:"target"`
	Enabled         bool           `json:"enabled"`
	SecurityAllowed bool           `json:"security_allowed"`
	Health          HealthSnapshot `json:"health"`
}

// Input 是 Decision Engine 的完整、可持久化输入快照。
type Input struct {
	SchemaVersion     string      `json:"schema_version"`
	AlgorithmVersion  string      `json:"algorithm_version"`
	ConfigVersion     string      `json:"config_version,omitempty"`
	EvaluatedAtUnixMS int64       `json:"evaluated_at_unix_ms"`
	Request           Request     `json:"request"`
	Run               RunSnapshot `json:"run"`
	Candidates        []Candidate `json:"candidates"`
}

// CandidateResult 说明一个目标为何被接受或淘汰。
type CandidateResult struct {
	ModelID  string `json:"model_id"`
	TargetID string `json:"target_id"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
}

// PlanTarget 描述 ExecutionPlan 中一个可执行目标。
type PlanTarget struct {
	ModelID       string         `json:"model_id"`
	Compatibility bool           `json:"compatibility,omitempty"`
	Target        catalog.Target `json:"target"`
}

// ExecutionPlan 是经过筛选和排序的不可变执行计划。
type ExecutionPlan struct {
	SchemaVersion     string            `json:"schema_version"`
	AlgorithmVersion  string            `json:"algorithm_version"`
	ConfigVersion     string            `json:"config_version,omitempty"`
	InputHash         string            `json:"input_hash"`
	EffectiveStrategy string            `json:"effective_strategy"`
	Reasons           []string          `json:"reasons"`
	Candidates        []CandidateResult `json:"candidates"`
	Targets           []PlanTarget      `json:"targets"`
	PlanHash          string            `json:"plan_hash"`
}
