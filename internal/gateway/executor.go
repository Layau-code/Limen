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

// Executor 只消费 ExecutionPlan，负责 Provider 调用、超时、熔断和 Fallback。
type Executor struct {
	providers  map[string]provider.Provider
	policy     func() Policy
	breakerFor func(string) *circuitBreaker
}

// newExecutor 创建与 Router 状态读取边界相连的计划执行器。
func newExecutor(providers map[string]provider.Provider, policy func() Policy, breakerFor func(string) *circuitBreaker) *Executor {
	cloned := make(map[string]provider.Provider, len(providers))
	for name, upstream := range providers {
		cloned[name] = upstream
	}
	return &Executor{providers: cloned, policy: policy, breakerFor: breakerFor}
}

// Execute 按计划顺序调用 Provider，不解析逻辑模型或修改决策策略。
func (executor *Executor) Execute(parent context.Context, request provider.ChatRequest, plan decision.ExecutionPlan, beforeAttempt AttemptStartHook) (Result, error) {
	return executor.execute(parent, request, plan, executor.policy(), beforeAttempt)
}

// ExecuteWithPolicy 使用固定配置版本的执行参数运行计划。
func (executor *Executor) ExecuteWithPolicy(parent context.Context, request provider.ChatRequest, plan decision.ExecutionPlan, policy Policy, beforeAttempt AttemptStartHook) (Result, error) {
	return executor.execute(parent, request, plan, policy, beforeAttempt)
}

// execute 按给定执行策略顺序调用 Provider，并保持总预算不重置。
func (executor *Executor) execute(parent context.Context, request provider.ChatRequest, plan decision.ExecutionPlan, policy Policy, beforeAttempt AttemptStartHook) (Result, error) {
	budget, cancelBudget := context.WithTimeout(parent, policy.RequestTimeout)
	decision := Decision{}
	attemptReports := make([]AttemptReport, 0, len(plan.Targets))
	withPlan := func(result Result) Result {
		result.Plan = plan
		result.Attempts = append([]AttemptReport(nil), attemptReports...)
		return result
	}
	settlement := NewSettlement()
	var lastErr error
	var pendingResponse provider.Response
	var pendingCancel context.CancelFunc
	hasPendingResponse := false
	closePending := func() {
		if !hasPendingResponse {
			return
		}
		if parent.Err() == nil && budget.Err() == nil && pendingResponse.Usage != nil {
			_, _ = io.CopyN(io.Discard, pendingResponse.Body, maxSettlementDrainBytes+1)
		}
		_ = pendingResponse.Body.Close()
		pendingCancel()
		hasPendingResponse = false
	}

	for _, planned := range plan.Targets {
		target := planned.Target
		model := Model{ID: planned.ModelID, Compatibility: planned.Compatibility}
		if err := parent.Err(); err != nil {
			closePending()
			cancelBudget()
			return withPlan(Result{Decision: decision, Settlement: settlement}), &RouteError{Decision: decision, Err: err}
		}
		breaker := executor.breakerFor(targetKey(model, target))
		if breaker == nil {
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: "target_unavailable"})
			continue
		}
		if !breaker.allow() {
			outcome := "circuit_open"
			if breaker.observe().state == "half_open" {
				outcome = "skipped_due_to_race"
			}
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
			continue
		}
		if err := budget.Err(); err != nil {
			breaker.recordNeutral()
			if hasPendingResponse {
				return withPlan(resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget)), nil
			}
			cancelBudget()
			return withPlan(Result{Decision: decision, Settlement: settlement}), &RouteError{Decision: decision, Err: err}
		}

		attempt, cancelAttempt := context.WithTimeout(budget, policy.AttemptTimeout)
		upstream := executor.providers[target.Provider]
		if upstream == nil {
			cancelAttempt()
			breaker.recordNeutral()
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: "provider_unavailable"})
			if hasPendingResponse {
				return withPlan(resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget)), nil
			}
			cancelBudget()
			return withPlan(Result{Decision: decision, Settlement: settlement}), &ProviderUnavailableError{Name: target.Provider, Decision: decision}
		}
		closePending()
		if beforeAttempt != nil {
			if err := beforeAttempt(planned); err != nil {
				cancelAttempt()
				cancelBudget()
				return withPlan(Result{Decision: decision, Settlement: settlement}), &AttemptStartError{Err: err}
			}
		}
		upstreamRequest := request
		upstreamRequest.Model = target.UpstreamModel
		upstreamRequest.EndpointID = target.EndpointID
		response, err := traceProviderChat(attempt, upstream, upstreamRequest, planned)
		decision.Attempts++
		decision.Provider = target.Provider
		if err != nil {
			cancelAttempt()
			errorClass := provider.ClassifyError(err)
			if budget.Err() != nil || parent.Err() != nil || errors.Is(err, context.Canceled) {
				outcome := "timeout"
				if errors.Is(parent.Err(), context.Canceled) {
					outcome = "canceled"
				}
				decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
				breaker.recordNeutral()
				if errors.Is(parent.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
					attemptReports = append(attemptReports, AttemptReport{TargetID: target.ID, Provider: target.Provider, UpstreamModel: target.UpstreamModel, Outcome: outcome})
					closePending()
					cancelBudget()
					cause := firstContextError(parent, budget)
					if cause == nil {
						cause = context.Canceled
					}
					return withPlan(Result{Decision: decision, Settlement: settlement}), &RouteError{Decision: decision, Err: cause}
				}
				attemptReports = append(attemptReports, AttemptReport{TargetID: target.ID, Provider: target.Provider, UpstreamModel: target.UpstreamModel, Outcome: outcome})
				if hasPendingResponse {
					return withPlan(resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget)), nil
				}
				cancelBudget()
				cause := firstContextError(parent, budget)
				if cause == nil {
					cause = context.Canceled
				}
				return withPlan(Result{Decision: decision, Settlement: settlement}), &RouteError{Decision: decision, Err: cause}
			}
			if errorClass != provider.ErrorClassRetryableTransient && !errors.Is(err, context.DeadlineExceeded) {
				outcome := "internal_error"
				if errorClass == provider.ErrorClassDeterministicRequest {
					outcome = "request_error"
				}
				decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
				attemptReports = append(attemptReports, AttemptReport{TargetID: target.ID, Provider: target.Provider, UpstreamModel: target.UpstreamModel, Outcome: outcome})
				breaker.recordNeutral()
				cancelBudget()
				return withPlan(Result{Decision: decision, Settlement: settlement}), &RouteError{Decision: decision, Err: err}
			}
			outcome := "transport_error"
			if errors.Is(err, context.DeadlineExceeded) {
				outcome = "timeout"
			}
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
			attemptReports = append(attemptReports, AttemptReport{TargetID: target.ID, Provider: target.Provider, UpstreamModel: target.UpstreamModel, Outcome: outcome})
			breaker.recordFailure()
			lastErr = err
			continue
		}
		if response.Body == nil {
			response.Body = io.NopCloser(strings.NewReader(""))
		}
		settlement.AddAttempt(AttemptSettlement{Provider: target.Provider, UpstreamModel: target.UpstreamModel, StatusCode: response.StatusCode, Pricing: target.Pricing, Usage: response.Usage})

		outcome := strconv.Itoa(response.StatusCode)
		decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
		attemptReports = append(attemptReports, AttemptReport{TargetID: target.ID, Provider: target.Provider, UpstreamModel: target.UpstreamModel, Outcome: outcome, StatusCode: response.StatusCode, ErrorClass: response.ErrorClass, ProviderRequestID: response.ProviderRequestID})
		if provider.IsRetryableResponse(response) {
			breaker.recordFailure()
			lastErr = nil
			pendingResponse = response
			pendingCancel = cancelAttempt
			hasPendingResponse = true
			continue
		}
		breaker.recordSuccess()
		return withPlan(resultWithCancel(response, decision, settlement, cancelAttempt, cancelBudget)), nil
	}

	if hasPendingResponse {
		if err := parent.Err(); err != nil {
			closePending()
			cancelBudget()
			return withPlan(Result{Decision: decision, Settlement: settlement}), &RouteError{Decision: decision, Err: err}
		}
		return withPlan(resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget)), nil
	}
	cancelBudget()
	if decision.Attempts == 0 {
		return withPlan(Result{Decision: decision, Settlement: settlement}), &NoAvailableTargetError{Decision: decision}
	}
	return withPlan(Result{Decision: decision, Settlement: settlement}), &RouteError{Decision: decision, Err: lastErr}
}
