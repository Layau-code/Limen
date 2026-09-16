// Package run 保存受治理 Agent Run 的领域状态和结算边界。
package run

import (
	"errors"
	"time"
)

// RunState 是 Run 生命周期中的稳定状态。
type RunState string

const (
	StateActive              RunState = "active"
	StateCompleting          RunState = "completing"
	StateCompleted           RunState = "completed"
	StateCancelled           RunState = "cancelled"
	StateDeadlineExceeded    RunState = "deadline_exceeded"
	StateSoftBudgetExhausted RunState = "soft_budget_exhausted"
	StateSuspendedAccounting RunState = "suspended_accounting"
)

// RequestState 是单个 Run 请求的结算状态。
type RequestState string

const (
	RequestAdmitted          RequestState = "admitted"
	RequestDecisionReady     RequestState = "decision_ready"
	RequestExecuting         RequestState = "executing"
	RequestSettlementPending RequestState = "settlement_pending"
	RequestSettled           RequestState = "settled"
	RequestFailed            RequestState = "failed"
	RequestCancelled         RequestState = "cancelled"
	RequestAbandoned         RequestState = "abandoned"
)

// AttemptState 是一次真实 Provider 调用的执行结果。
type AttemptState string

const (
	AttemptStarted           AttemptState = "started"
	AttemptSucceeded         AttemptState = "succeeded"
	AttemptTransientFailed   AttemptState = "transient_failed"
	AttemptDeterministicFail AttemptState = "deterministic_failed"
	AttemptCancelled         AttemptState = "cancelled"
	AttemptAbandoned         AttemptState = "abandoned"
)

var (
	// ErrRunNotActive 表示 Run 当前不接受新请求。
	ErrRunNotActive = errors.New("run is not active")
	// ErrRunDeadlineExceeded 表示 Run 已超过截止时间。
	ErrRunDeadlineExceeded = errors.New("run deadline exceeded")
	// ErrRunBudgetExhausted 表示已结算金额达到软预算阈值。
	ErrRunBudgetExhausted = errors.New("run soft budget exhausted")
	// ErrRunConcurrencyExceeded 表示在途请求已达到并发上限。
	ErrRunConcurrencyExceeded = errors.New("run concurrency exceeded")
	// ErrAccountingSuspended 表示账本状态不确定，必须先恢复对账。
	ErrAccountingSuspended = errors.New("run accounting is suspended")
	// ErrInvalidRunTransition 表示生命周期状态转换不合法。
	ErrInvalidRunTransition = errors.New("invalid run state transition")
	// ErrRequestNotSettleable 表示请求当前不能进入结算。
	ErrRequestNotSettleable = errors.New("request is not settleable")
)

// Run 保存一次 Agent 工作流的预算、截止时间和并发快照。
type Run struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id"`
	State              RunState  `json:"state"`
	SoftBudgetNanoUSD  int64     `json:"soft_budget_nano_usd"`
	SettledCostNanoUSD int64     `json:"settled_cost_nano_usd"`
	Deadline           time.Time `json:"deadline"`
	MaxParallelism     int       `json:"max_parallelism"`
	InFlight           int       `json:"in_flight"`
	Strategy           string    `json:"strategy"`
	ConfigVersion      string    `json:"config_version"`
	CompleteRequested  bool      `json:"complete_requested"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Request 保存幂等键、决策关联和结算占用信息，不包含请求正文。
type Request struct {
	ID               string       `json:"id"`
	TenantID         string       `json:"tenant_id"`
	RunID            string       `json:"run_id"`
	Endpoint         string       `json:"endpoint"`
	IdempotencyKey   string       `json:"idempotency_key"`
	RequestHash      string       `json:"request_hash"`
	State            RequestState `json:"state"`
	SettlementStatus string       `json:"settlement_status"`
	DecisionID       string       `json:"decision_id,omitempty"`
	LedgerRecorded   bool         `json:"ledger_recorded"`
	LeaseOwner       string       `json:"lease_owner,omitempty"`
	LeaseExpiresAt   time.Time    `json:"lease_expires_at,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// Attempt 保存调用前持久化的目标和 Provider 结果摘要。
type Attempt struct {
	ID                string       `json:"id"`
	TenantID          string       `json:"tenant_id"`
	RequestID         string       `json:"request_id"`
	TargetID          string       `json:"target_id"`
	Provider          string       `json:"provider"`
	UpstreamModel     string       `json:"upstream_model"`
	State             AttemptState `json:"state"`
	ProviderRequestID string       `json:"provider_request_id,omitempty"`
	StartedAt         time.Time    `json:"started_at"`
	FinishedAt        time.Time    `json:"finished_at,omitempty"`
}
