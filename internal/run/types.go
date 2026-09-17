// Package run 保存受治理 Agent Run 的领域状态和结算边界。
package run

import (
	"context"
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
	// ErrInvalidAccountingResolution 表示结算处置既未确认金额，也未明确接受未知费用。
	ErrInvalidAccountingResolution = errors.New("invalid accounting resolution")
	// ErrInvalidRunTransition 表示生命周期状态转换不合法。
	ErrInvalidRunTransition = errors.New("invalid run state transition")
	// ErrRequestNotSettleable 表示请求当前不能进入结算。
	ErrRequestNotSettleable = errors.New("request is not settleable")
	// ErrResourceNotFound 表示指定租户下资源不存在。
	ErrResourceNotFound = errors.New("run resource not found")
	// ErrLeaseUnavailable 表示请求仍被其他执行实例持有。
	ErrLeaseUnavailable = errors.New("request lease unavailable")
	// ErrLeaseLost 表示当前执行实例已失去请求租约。
	ErrLeaseLost = errors.New("request lease lost")
	// ErrRunCancelled 表示请求因 Run 取消而提前结束。
	ErrRunCancelled = errors.New("run cancelled")
)

const (
	// RequestLeaseDuration 是一次执行租约的最长无续租时间。
	RequestLeaseDuration = 30 * time.Second
	// RequestLeaseRenewInterval 是执行实例续租的固定间隔。
	RequestLeaseRenewInterval = 10 * time.Second
)

// AdmissionInput 描述已完成规范哈希的请求准入参数。
type AdmissionInput struct {
	Request    Request
	Now        time.Time
	LeaseOwner string
	LeaseTTL   time.Duration
}

// Mutation 保存控制面幂等键和规范请求哈希。
type Mutation struct {
	Key  string
	Hash string
}

// Service 定义 Run Coordinator 对 HTTP 和 Gateway 暴露的统一状态接口。
type Service interface {
	CreateRun(context.Context, string, Run) error
	AdmitRequest(context.Context, string, string, AdmissionInput) (Request, error)
	RecordAttemptStarted(context.Context, string, Attempt) error
	UpdateAttemptProviderRequestID(context.Context, string, string, string) error
	FinishAttempt(context.Context, string, string, AttemptState, time.Time) error
	BeginSettlement(context.Context, string, string, time.Time) (Request, error)
	SettleRequest(context.Context, string, string, *int64, time.Time) (Request, error)
	ResolveAccounting(context.Context, string, string, AccountingResolution, time.Time) (Request, error)
	GetRun(context.Context, string, string) (Run, error)
	GetRequest(context.Context, string, string) (Request, error)
}

// AccountingService 定义未知费用的管理员处置边界。
type AccountingService interface {
	Service
	ResolveAccountingWithMutation(context.Context, string, string, string, AccountingResolution, Mutation, time.Time) (Request, error)
}

// LeaseService 定义跨进程执行租约和过期恢复边界。
type LeaseService interface {
	Service
	AcquireRequestLease(context.Context, string, string, string, time.Time, time.Duration) (Request, error)
	RenewRequestLease(context.Context, string, string, string, time.Time, time.Duration) (Request, error)
	ReleaseRequestLease(context.Context, string, string, string, time.Time) error
	RecoverExpiredRequests(context.Context, string, time.Time, int) ([]Request, error)
}

// SettlementRecoveryService 定义结算任务的持久化领取和恢复边界。
type SettlementRecoveryService interface {
	Service
	QueueSettlement(context.Context, string, string, *int64, time.Time) error
	ClaimSettlementJobs(context.Context, string, string, time.Time, time.Duration, int) ([]SettlementJob, error)
	CompleteSettlementJob(context.Context, string, string, string) error
	FailSettlementJob(context.Context, string, string, string, time.Time, string) error
}

// CancellationService 定义跨执行实例传播 Run 取消事件的边界。
type CancellationService interface {
	PollCancellationEvents(context.Context, string, int64, int) ([]CancellationEvent, error)
}

// CancellationEvent 是一次租户内 Run 取消通知。
type CancellationEvent struct {
	ID        int64     `json:"id"`
	TenantID  string    `json:"tenant_id"`
	RunID     string    `json:"run_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ControlService 在 Service 之上提供带幂等控制操作的 Run 生命周期管理。
type ControlService interface {
	Service
	CreateRunWithMutation(context.Context, string, Run, Mutation) (Run, error)
	CompleteRunWithMutation(context.Context, string, string, Mutation) (Run, error)
	CancelRunWithMutation(context.Context, string, string, Mutation) (Run, error)
}

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

// SettlementJob 保存一次尚未完成的结算及其执行租约。
type SettlementJob struct {
	TenantID       string    `json:"tenant_id"`
	RequestID      string    `json:"request_id"`
	CostNanoUSD    *int64    `json:"cost_nano_usd,omitempty"`
	Attempts       int       `json:"attempts"`
	NextAttemptAt  time.Time `json:"next_attempt_at"`
	LeaseOwner     string    `json:"lease_owner,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
}

// AccountingResolution 描述管理员对未知费用的最终处置。
type AccountingResolution struct {
	CostNanoUSD   *int64
	AcceptUnknown bool
}

// Validate 确认处置只能选择补记金额或接受未知费用中的一种。
func (resolution AccountingResolution) Validate() error {
	if resolution.CostNanoUSD == nil && !resolution.AcceptUnknown {
		return ErrInvalidAccountingResolution
	}
	if resolution.CostNanoUSD != nil && (*resolution.CostNanoUSD < 0 || resolution.AcceptUnknown) {
		return ErrInvalidAccountingResolution
	}
	return nil
}
