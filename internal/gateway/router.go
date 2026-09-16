package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huz/limen/internal/provider"
)

// Policy 定义完整请求预算、单次尝试和熔断参数。
type Policy struct {
	RequestTimeout   time.Duration
	AttemptTimeout   time.Duration
	FailureThreshold int
	Cooldown         time.Duration
}

// Router 按模型注册表执行预算感知的 Provider 路由。
type Router struct {
	providers map[string]provider.Provider
	registry  *ModelRegistry
	policy    Policy
	breakers  map[string]*circuitBreaker
}

// Result 同时返回上游响应和不含业务正文的路由决策。
type Result struct {
	Response   provider.Response
	Decision   Decision
	Settlement *Settlement
}

// Decision 描述一次请求实际经过的安全路由路径。
type Decision struct {
	Provider string
	Attempts int
	Steps    []DecisionStep
}

// DecisionStep 描述一个目标的 Provider 和结果类别。
type DecisionStep struct {
	Provider string
	Outcome  string
}

// String 将路由步骤编码为长度有界的紧凑路径。
func (decision Decision) String() string {
	steps := make([]string, 0, len(decision.Steps))
	for _, step := range decision.Steps {
		steps = append(steps, step.Provider+":"+step.Outcome)
	}
	return strings.Join(steps, ">")
}

// NewRouter 创建使用指定 Provider、注册表和可靠性策略的路由器。
func NewRouter(providers map[string]provider.Provider, registry *ModelRegistry, policy Policy) *Router {
	return newRouter(providers, registry, policy, time.Now)
}

// newRouter 使用可替换时钟创建路由器，便于确定性验证熔断行为。
func newRouter(providers map[string]provider.Provider, registry *ModelRegistry, policy Policy, now func() time.Time) *Router {
	router := &Router{
		providers: make(map[string]provider.Provider, len(providers)),
		registry:  registry,
		policy:    policy,
		breakers:  make(map[string]*circuitBreaker),
	}
	for name, upstream := range providers {
		router.providers[name] = upstream
	}
	for _, model := range registry.List() {
		for _, target := range model.Targets {
			key := targetKey(model, target)
			if _, exists := router.breakers[key]; !exists {
				router.breakers[key] = newCircuitBreaker(policy.FailureThreshold, policy.Cooldown, now)
			}
		}
	}
	return router
}

// Chat 在共享总预算内依次尝试逻辑模型的上游目标。
func (router *Router) Chat(parent context.Context, request provider.ChatRequest) (Result, error) {
	model, found := router.registry.Resolve(request.Model)
	if !found {
		return Result{}, &UnsupportedModelError{Model: request.Model}
	}
	budget, cancelBudget := context.WithTimeout(parent, router.policy.RequestTimeout)
	decision := Decision{}
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

	for _, target := range model.Targets {
		if err := parent.Err(); err != nil {
			closePending()
			cancelBudget()
			return Result{}, &RouteError{Decision: decision, Err: err}
		}
		breaker := router.breakers[targetKey(model, target)]
		if !breaker.allow() {
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: "circuit_open"})
			continue
		}
		if err := budget.Err(); err != nil {
			breaker.recordNeutral()
			if hasPendingResponse {
				return resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget), nil
			}
			cancelBudget()
			return Result{}, &RouteError{Decision: decision, Err: err}
		}

		attempt, cancelAttempt := context.WithTimeout(budget, router.policy.AttemptTimeout)
		upstream := router.providers[target.Provider]
		if upstream == nil {
			cancelAttempt()
			breaker.recordNeutral()
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: "provider_unavailable"})
			if hasPendingResponse {
				return resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget), nil
			}
			cancelBudget()
			return Result{}, &ProviderUnavailableError{Name: target.Provider, Decision: decision}
		}
		closePending()
		upstreamRequest := request
		upstreamRequest.Model = target.UpstreamModel
		response, err := upstream.Chat(attempt, upstreamRequest)
		decision.Attempts++
		decision.Provider = target.Provider
		if err != nil {
			cancelAttempt()
			if budget.Err() != nil || parent.Err() != nil || errors.Is(err, context.Canceled) {
				outcome := "timeout"
				if errors.Is(parent.Err(), context.Canceled) {
					outcome = "canceled"
				}
				decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
				breaker.recordNeutral()
				if errors.Is(parent.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
					closePending()
					cancelBudget()
					cause := firstContextError(parent, budget)
					if cause == nil {
						cause = context.Canceled
					}
					return Result{}, &RouteError{Decision: decision, Err: cause}
				}
				if hasPendingResponse {
					return resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget), nil
				}
				cancelBudget()
				cause := firstContextError(parent, budget)
				if cause == nil {
					cause = context.Canceled
				}
				return Result{}, &RouteError{Decision: decision, Err: cause}
			}
			var transportError *provider.TransportError
			if !errors.As(err, &transportError) {
				outcome := "internal_error"
				var requestError *provider.RequestError
				if errors.As(err, &requestError) {
					outcome = "request_error"
				}
				decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
				breaker.recordNeutral()
				cancelBudget()
				return Result{}, &RouteError{Decision: decision, Err: err}
			}
			outcome := "transport_error"
			if errors.Is(err, context.DeadlineExceeded) {
				outcome = "timeout"
			}
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: outcome})
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
		if isTransientStatus(response.StatusCode) {
			breaker.recordFailure()
			lastErr = nil
			pendingResponse = response
			pendingCancel = cancelAttempt
			hasPendingResponse = true
			continue
		}
		breaker.recordSuccess()
		return resultWithCancel(response, decision, settlement, cancelAttempt, cancelBudget), nil
	}

	if hasPendingResponse {
		if err := parent.Err(); err != nil {
			closePending()
			cancelBudget()
			return Result{}, &RouteError{Decision: decision, Err: err}
		}
		return resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget), nil
	}
	cancelBudget()
	if decision.Attempts == 0 {
		return Result{}, &NoAvailableTargetError{Decision: decision}
	}
	return Result{}, &RouteError{Decision: decision, Err: lastErr}
}

const maxSettlementDrainBytes = 64 << 10

// Models 返回 Router 当前公开的模型列表。
func (router *Router) Models() []Model {
	return router.registry.List()
}

// targetKey 返回显式目标或兼容模式前缀使用的稳定熔断标识。
func targetKey(model Model, target Target) string {
	upstreamModel := target.UpstreamModel
	if model.Compatibility {
		upstreamModel = model.ID
	}
	return model.ID + "\x00" + target.Provider + "\x00" + upstreamModel
}

// isTransientStatus 判断上游状态是否允许切换到下一个目标。
func isTransientStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 529:
		return true
	default:
		return false
	}
}

// firstContextError 优先返回调用方取消原因，否则返回总预算原因。
func firstContextError(parent, budget context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	return budget.Err()
}

// resultWithCancel 将尝试和总预算的释放绑定到响应体关闭。
func resultWithCancel(response provider.Response, decision Decision, settlement *Settlement, cancels ...context.CancelFunc) Result {
	response.Body = &cancelBody{ReadCloser: response.Body, cancel: func() {
		for _, cancel := range cancels {
			cancel()
		}
	}}
	return Result{Response: response, Decision: decision, Settlement: settlement}
}

type cancelBody struct {
	io.ReadCloser
	once     sync.Once
	cancel   func()
	closeErr error
}

// Close 关闭上游响应，并且只释放一次关联 Context。
func (body *cancelBody) Close() error {
	body.once.Do(func() {
		body.cancel()
		body.closeErr = body.ReadCloser.Close()
	})
	return body.closeErr
}

// ProviderUnavailableError 表示目标 Provider 尚未配置。
type ProviderUnavailableError struct {
	Name     string
	Decision Decision
}

// Error 返回面向日志和 API 层的 Provider 错误描述。
func (e *ProviderUnavailableError) Error() string {
	return fmt.Sprintf("provider unavailable: %s", e.Name)
}

// UnsupportedModelError 表示请求的模型没有注册。
type UnsupportedModelError struct {
	Model string
}

// Error 返回面向日志和 API 层的模型错误描述。
func (e *UnsupportedModelError) Error() string {
	return fmt.Sprintf("unsupported model: %s", e.Model)
}

// NoAvailableTargetError 表示所有目标均被熔断器暂时拒绝。
type NoAvailableTargetError struct {
	Decision Decision
}

// Error 返回无可用目标的稳定错误描述。
func (e *NoAvailableTargetError) Error() string {
	return "no available model target"
}

// RouteError 表示路由在获得可转发响应前失败。
type RouteError struct {
	Decision Decision
	Err      error
}

// Error 返回包含底层原因的路由错误描述。
func (e *RouteError) Error() string {
	return fmt.Sprintf("route failed: %v", e.Err)
}

// Unwrap 返回底层错误，便于 API 层识别取消和超时。
func (e *RouteError) Unwrap() error {
	return e.Err
}
