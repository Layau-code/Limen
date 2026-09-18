package gateway

import (
	"context"

	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/provider"
)

// Executor 只消费 ExecutionPlan，负责 Provider 调用、超时、熔断和 Fallback。
type Executor struct {
	providers  map[string]provider.Provider
	policy     func() Policy
	breakerFor func(string) *circuitBreaker
	acquire    func(decision.ExecutionPlan, Policy) func()
}

// newExecutor 创建与 Router 状态读取边界相连的计划执行器。
func newExecutor(providers map[string]provider.Provider, policy func() Policy, breakerFor func(string) *circuitBreaker, acquire func(decision.ExecutionPlan, Policy) func()) *Executor {
	cloned := make(map[string]provider.Provider, len(providers))
	for name, upstream := range providers {
		cloned[name] = upstream
	}
	return &Executor{providers: cloned, policy: policy, breakerFor: breakerFor, acquire: acquire}
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
	releaseBreakers := func() {}
	if executor.acquire != nil {
		releaseBreakers = executor.acquire(plan, policy)
	}
	defer releaseBreakers()
	budget, cancelBudget := context.WithTimeout(parent, policy.RequestTimeout)
	state := newExecutionState(len(plan.Targets))

	for _, planned := range plan.Targets {
		target := planned.Target
		model := Model{ID: planned.ModelID, Compatibility: planned.Compatibility}
		if err := parent.Err(); err != nil {
			state.closePending(parent, budget)
			cancelBudget()
			return state.withPlan(plan, Result{Decision: state.decision, Settlement: state.settlement}), &RouteError{Decision: state.decision, Err: err}
		}
		breaker := executor.breakerFor(targetKey(model, target))
		if breaker == nil {
			state.decision.Steps = append(state.decision.Steps, DecisionStep{Provider: target.Provider, Outcome: "target_unavailable"})
			continue
		}
		if !breaker.allowWithCooldown(policy.Cooldown) {
			outcome := "circuit_open"
			if breaker.observeWithCooldown(policy.Cooldown).state == "half_open" {
				outcome = "skipped_due_to_race"
			}
			state.decision.Steps = append(state.decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
			continue
		}
		if err := budget.Err(); err != nil {
			breaker.recordNeutral()
			if state.hasPendingResponse {
				return state.withPlan(plan, state.pendingResult(cancelBudget)), nil
			}
			cancelBudget()
			return state.withPlan(plan, Result{Decision: state.decision, Settlement: state.settlement}), &RouteError{Decision: state.decision, Err: err}
		}

		attempt, cancelAttempt := context.WithTimeout(budget, policy.AttemptTimeout)
		upstream := executor.providers[target.Provider]
		if upstream == nil {
			cancelAttempt()
			breaker.recordNeutral()
			state.decision.Steps = append(state.decision.Steps, DecisionStep{Provider: target.Provider, Outcome: "provider_unavailable"})
			if state.hasPendingResponse {
				return state.withPlan(plan, state.pendingResult(cancelBudget)), nil
			}
			cancelBudget()
			return state.withPlan(plan, Result{Decision: state.decision, Settlement: state.settlement}), &ProviderUnavailableError{Name: target.Provider, Decision: state.decision}
		}
		state.closePending(parent, budget)
		if beforeAttempt != nil {
			if err := beforeAttempt(planned); err != nil {
				cancelAttempt()
				cancelBudget()
				return state.withPlan(plan, Result{Decision: state.decision, Settlement: state.settlement}), &AttemptStartError{Err: err}
			}
		}
		upstreamRequest := request
		upstreamRequest.Model = target.UpstreamModel
		upstreamRequest.EndpointID = target.EndpointID
		response, err := traceProviderChat(attempt, upstream, upstreamRequest, planned)
		state.decision.Attempts++
		state.decision.Provider = target.Provider
		if err != nil {
			outcome := state.handleProviderError(parent, budget, planned, breaker, response, err, cancelAttempt, cancelBudget, policy.FailureThreshold)
			if outcome.stop {
				return state.withPlan(plan, outcome.result), outcome.err
			}
			continue
		}
		response, accepted := state.recordResponse(planned, response, breaker, cancelAttempt, policy.FailureThreshold)
		if !accepted {
			continue
		}
		breaker.recordSuccess()
		return state.withPlan(plan, resultWithCancel(response, state.decision, state.settlement, cancelAttempt, cancelBudget)), nil
	}

	if state.hasPendingResponse {
		if err := parent.Err(); err != nil {
			state.closePending(parent, budget)
			cancelBudget()
			return state.withPlan(plan, Result{Decision: state.decision, Settlement: state.settlement}), &RouteError{Decision: state.decision, Err: err}
		}
		return state.withPlan(plan, state.pendingResult(cancelBudget)), nil
	}
	cancelBudget()
	if state.decision.Attempts == 0 {
		return state.withPlan(plan, Result{Decision: state.decision, Settlement: state.settlement}), &NoAvailableTargetError{Decision: state.decision}
	}
	return state.withPlan(plan, Result{Decision: state.decision, Settlement: state.settlement}), &RouteError{Decision: state.decision, Err: state.lastErr}
}
