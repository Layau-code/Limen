package gateway

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/provider"
)

// executionState 保存一次计划执行期间的决策、Attempt 和待返回响应。
type executionState struct {
	decision           Decision
	attemptReports     []AttemptReport
	settlement         *Settlement
	lastErr            error
	pendingResponse    provider.Response
	pendingCancel      context.CancelFunc
	hasPendingResponse bool
}

// newExecutionState 创建容量与计划目标数匹配的执行状态。
func newExecutionState(targetCount int) *executionState {
	return &executionState{
		attemptReports: make([]AttemptReport, 0, targetCount),
		settlement:     NewSettlement(),
	}
}

// withPlan 将不可变计划和当前执行摘要附加到结果中。
func (state *executionState) withPlan(plan decision.ExecutionPlan, result Result) Result {
	result.Plan = plan
	result.Attempts = append([]AttemptReport(nil), state.attemptReports...)
	return result
}

// closePending 关闭尚未交给客户端的响应，并释放对应尝试上下文。
func (state *executionState) closePending(parent, budget context.Context) {
	if !state.hasPendingResponse {
		return
	}
	if parent.Err() == nil && budget.Err() == nil && state.pendingResponse.Usage != nil {
		_, _ = io.CopyN(io.Discard, state.pendingResponse.Body, maxSettlementDrainBytes+1)
	}
	_ = state.pendingResponse.Body.Close()
	state.pendingCancel()
	state.hasPendingResponse = false
}

// pendingResult 返回仍可交给客户端的最后一个响应，并把取消动作绑定到响应关闭。
func (state *executionState) pendingResult(cancelBudget context.CancelFunc) Result {
	return resultWithCancel(state.pendingResponse, state.decision, state.settlement, state.pendingCancel, cancelBudget)
}

// recordResponse 记录成功响应或保存可用于最终返回的瞬时错误响应。
func (state *executionState) recordResponse(planned decision.PlanTarget, response provider.Response, breaker *circuitBreaker, cancelAttempt context.CancelFunc, failureThreshold int) (provider.Response, bool) {
	if response.Body == nil {
		response.Body = io.NopCloser(strings.NewReader(""))
	}
	state.settlement.AddAttempt(AttemptSettlement{Provider: planned.Target.Provider, UpstreamModel: planned.Target.UpstreamModel, StatusCode: response.StatusCode, Pricing: planned.Target.Pricing, Usage: response.Usage})

	outcome := strconv.Itoa(response.StatusCode)
	state.decision.Steps = append(state.decision.Steps, DecisionStep{Provider: planned.Target.Provider, Outcome: outcome})
	state.attemptReports = append(state.attemptReports, AttemptReport{TargetID: planned.Target.ID, Provider: planned.Target.Provider, UpstreamModel: planned.Target.UpstreamModel, Outcome: outcome, StatusCode: response.StatusCode, ErrorClass: response.ErrorClass, ProviderRequestID: response.ProviderRequestID})
	if provider.IsRetryableResponse(response) {
		breaker.recordFailureWith(failureThreshold)
		state.lastErr = nil
		state.pendingResponse = response
		state.pendingCancel = cancelAttempt
		state.hasPendingResponse = true
		return response, false
	}
	return response, true
}

// handleProviderError 将 Provider 错误分类为终止、Fallback 或可返回的上下文结果。
func (state *executionState) handleProviderError(parent, budget context.Context, planned decision.PlanTarget, breaker *circuitBreaker, response provider.Response, err error, cancelAttempt, cancelBudget context.CancelFunc, failureThreshold int) executionOutcome {
	if response.Body != nil {
		// Provider 出错时不再消费响应，防御性关闭可能已创建的上游连接。
		_ = response.Body.Close()
	}
	cancelAttempt()
	errorClass := provider.ClassifyError(err)
	if budget.Err() != nil || parent.Err() != nil || errors.Is(err, context.Canceled) {
		outcome := "timeout"
		if errors.Is(parent.Err(), context.Canceled) {
			outcome = "canceled"
		}
		state.decision.Steps = append(state.decision.Steps, DecisionStep{Provider: planned.Target.Provider, Outcome: outcome})
		breaker.recordNeutral()
		state.attemptReports = append(state.attemptReports, AttemptReport{TargetID: planned.Target.ID, Provider: planned.Target.Provider, UpstreamModel: planned.Target.UpstreamModel, Outcome: outcome})
		if errors.Is(parent.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			state.closePending(parent, budget)
			cancelBudget()
			cause := firstContextError(parent, budget)
			if cause == nil {
				cause = context.Canceled
			}
			return executionOutcome{stop: true, result: Result{Decision: state.decision, Settlement: state.settlement}, err: &RouteError{Decision: state.decision, Err: cause}}
		}
		if state.hasPendingResponse {
			return executionOutcome{stop: true, result: state.pendingResult(cancelBudget)}
		}
		cancelBudget()
		cause := firstContextError(parent, budget)
		if cause == nil {
			cause = context.Canceled
		}
		return executionOutcome{stop: true, result: Result{Decision: state.decision, Settlement: state.settlement}, err: &RouteError{Decision: state.decision, Err: cause}}
	}
	if errorClass != provider.ErrorClassRetryableTransient && !errors.Is(err, context.DeadlineExceeded) {
		outcome := "internal_error"
		if errorClass == provider.ErrorClassDeterministicRequest {
			outcome = "request_error"
		}
		state.decision.Steps = append(state.decision.Steps, DecisionStep{Provider: planned.Target.Provider, Outcome: outcome})
		state.attemptReports = append(state.attemptReports, AttemptReport{TargetID: planned.Target.ID, Provider: planned.Target.Provider, UpstreamModel: planned.Target.UpstreamModel, Outcome: outcome})
		breaker.recordNeutral()
		cancelBudget()
		return executionOutcome{stop: true, result: Result{Decision: state.decision, Settlement: state.settlement}, err: &RouteError{Decision: state.decision, Err: err}}
	}
	outcome := "transport_error"
	if errors.Is(err, context.DeadlineExceeded) {
		outcome = "timeout"
	}
	state.decision.Steps = append(state.decision.Steps, DecisionStep{Provider: planned.Target.Provider, Outcome: outcome})
	state.attemptReports = append(state.attemptReports, AttemptReport{TargetID: planned.Target.ID, Provider: planned.Target.Provider, UpstreamModel: planned.Target.UpstreamModel, Outcome: outcome})
	breaker.recordFailureWith(failureThreshold)
	state.lastErr = err
	return executionOutcome{}
}

// executionOutcome 描述一次 Provider 错误处理后的循环控制结果。
type executionOutcome struct {
	stop   bool
	result Result
	err    error
}
