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

	"github.com/huz/limen/internal/decision"
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
	providers     map[string]provider.Provider
	registryMu    sync.RWMutex
	registry      *ModelRegistry
	configVersion string
	policy        Policy
	breakers      map[string]*circuitBreaker
	engine        decision.Engine
	now           func() time.Time
}

// Result 同时返回上游响应和不含业务正文的路由决策。
type Result struct {
	Response   provider.Response
	Decision   Decision
	Settlement *Settlement
	Input      decision.Input
	Plan       decision.ExecutionPlan
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
		now:       now,
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

// ReplaceRegistry 原子替换模型目录，并保留仍然存在目标的熔断状态。
func (router *Router) ReplaceRegistry(registry *ModelRegistry, version string) error {
	return router.ReplaceRegistryWithPolicy(registry, version, router.Policy())
}

// ReplaceRegistryWithPolicy 替换模型目录和路由策略，供配置版本发布使用。
func (router *Router) ReplaceRegistryWithPolicy(registry *ModelRegistry, version string, policy Policy) error {
	if registry == nil || len(registry.List()) == 0 {
		return errors.New("model registry is required")
	}
	router.registryMu.Lock()
	defer router.registryMu.Unlock()
	nextBreakers := make(map[string]*circuitBreaker)
	for _, model := range registry.List() {
		for _, target := range model.Targets {
			key := targetKey(model, target)
			if breaker, ok := router.breakers[key]; ok {
				nextBreakers[key] = breaker
				continue
			}
			nextBreakers[key] = newCircuitBreaker(policy.FailureThreshold, policy.Cooldown, router.now)
		}
	}
	router.registry = registry
	router.breakers = nextBreakers
	router.policy = policy
	router.configVersion = strings.TrimSpace(version)
	return nil
}

// Policy 返回当前路由策略的副本，避免配置发布覆盖请求总预算。
func (router *Router) Policy() Policy {
	router.registryMu.RLock()
	defer router.registryMu.RUnlock()
	return router.policy
}

// SetConfigVersion 设置当前 Router 使用的配置版本标识。
func (router *Router) SetConfigVersion(version string) {
	router.registryMu.Lock()
	router.configVersion = strings.TrimSpace(version)
	router.registryMu.Unlock()
}

// ConfigVersion 返回当前 Router 使用的配置版本标识。
func (router *Router) ConfigVersion() string {
	router.registryMu.RLock()
	defer router.registryMu.RUnlock()
	return router.configVersion
}

// Chat 使用默认契约处理一次聊天请求，保留 OpenAI 兼容调用方式。
func (router *Router) Chat(parent context.Context, request provider.ChatRequest) (Result, error) {
	return router.ChatWithContract(parent, request, decision.Contract{Active: request.Model == "auto"})
}

// ChatWithContract 根据能力契约生成计划，再在共享总预算内执行目标。
func (router *Router) ChatWithContract(parent context.Context, request provider.ChatRequest, contract decision.Contract) (Result, error) {
	input, plan, err := router.planWithInput(request, contract)
	if err != nil {
		var decisionErr *decision.DecisionError
		if errors.As(err, &decisionErr) && decisionErr.Code == "no_eligible_target" {
			return Result{Input: input, Plan: plan}, &NoEligibleTargetError{Plan: plan}
		}
		return Result{Input: input, Plan: plan}, err
	}
	result, err := router.executePlan(parent, request, plan)
	result.Input = input
	return result, err
}

// ChatWithContractHook 在 Provider 调用前执行一次决策审计回调。
func (router *Router) ChatWithContractHook(parent context.Context, request provider.ChatRequest, contract decision.Contract, beforeExecute func(decision.Input, decision.ExecutionPlan) error) (Result, error) {
	input, plan, err := router.planWithInput(request, contract)
	if err != nil {
		var decisionErr *decision.DecisionError
		if errors.As(err, &decisionErr) && decisionErr.Code == "no_eligible_target" {
			return Result{Input: input, Plan: plan}, &NoEligibleTargetError{Plan: plan}
		}
		return Result{Input: input, Plan: plan}, err
	}
	if beforeExecute != nil {
		if err := beforeExecute(input, plan); err != nil {
			return Result{Input: input, Plan: plan}, err
		}
	}
	result, err := router.executePlan(parent, request, plan)
	result.Input = input
	return result, err
}

// DryRun 只生成决策计划，不访问 Provider 或改变熔断、结算状态。
func (router *Router) DryRun(request provider.ChatRequest, contract decision.Contract) (decision.ExecutionPlan, error) {
	_, plan, err := router.planWithInput(request, contract)
	return plan, err
}

// Explain 生成决策输入和执行计划，供审计与 Replay 使用。
func (router *Router) Explain(request provider.ChatRequest, contract decision.Contract) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithInput(request, contract)
}

// Replay 使用历史输入重算计划，不读取当前熔断状态，也不访问 Provider。
func (router *Router) Replay(input decision.Input) (decision.ExecutionPlan, error) {
	return router.engine.Decide(input)
}

// plan 将注册表和熔断器快照组装为确定性的 DecisionInput。
func (router *Router) plan(request provider.ChatRequest, contract decision.Contract) (decision.ExecutionPlan, error) {
	_, plan, err := router.planWithInput(request, contract)
	return plan, err
}

// planWithInput 将注册表和熔断器快照组装为可持久化的 DecisionInput。
func (router *Router) planWithInput(request provider.ChatRequest, contract decision.Contract) (decision.Input, decision.ExecutionPlan, error) {
	registry, configVersion := router.registrySnapshot()
	models := registry.List()
	if request.Model != "auto" {
		model, found := registry.Resolve(request.Model)
		if !found {
			return decision.Input{}, decision.ExecutionPlan{}, &UnsupportedModelError{Model: request.Model}
		}
		models = []Model{model}
	} else if registry.IsCompatibility() {
		return decision.Input{}, decision.ExecutionPlan{}, &UnsupportedModelError{Model: request.Model}
	}
	if request.Model == "auto" {
		contract.Active = true
	}
	candidates := make([]decision.Candidate, 0)
	for _, model := range models {
		for _, target := range model.Targets {
			breaker := router.breakerFor(targetKey(model, target))
			observation := breakerObservation{}
			if breaker != nil {
				observation = breaker.observe()
			}
			candidates = append(candidates, decision.Candidate{
				ModelID:         model.ID,
				Target:          target,
				Enabled:         true,
				SecurityAllowed: true,
				Health: decision.HealthSnapshot{
					State:          observation.state,
					ProbeAvailable: observation.probeAvailable,
				},
			})
		}
	}
	input := decision.Input{
		SchemaVersion:     decision.SchemaVersionV1,
		AlgorithmVersion:  decision.AlgorithmVersionV1,
		ConfigVersion:     configVersion,
		EvaluatedAtUnixMS: time.Now().UnixMilli(),
		Request:           decision.Request{Model: request.Model, Stream: request.Stream, Contract: contract},
		Candidates:        candidates,
	}
	plan, err := router.engine.Decide(input)
	return input, plan, err
}

// executePlan 按计划顺序执行 Provider，并保留 Fallback、超时和结算语义。
func (router *Router) executePlan(parent context.Context, request provider.ChatRequest, plan decision.ExecutionPlan) (Result, error) {
	policy := router.Policy()
	budget, cancelBudget := context.WithTimeout(parent, policy.RequestTimeout)
	withPlan := func(result Result) Result {
		result.Plan = plan
		return result
	}
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

	for _, planned := range plan.Targets {
		target := planned.Target
		registry, _ := router.registrySnapshot()
		model := Model{ID: planned.ModelID, Compatibility: registry.IsCompatibility()}
		if err := parent.Err(); err != nil {
			closePending()
			cancelBudget()
			return Result{Plan: plan}, &RouteError{Decision: decision, Err: err}
		}
		breaker := router.breakerFor(targetKey(model, target))
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
			return Result{Plan: plan}, &RouteError{Decision: decision, Err: err}
		}

		attempt, cancelAttempt := context.WithTimeout(budget, policy.AttemptTimeout)
		upstream := router.providers[target.Provider]
		if upstream == nil {
			cancelAttempt()
			breaker.recordNeutral()
			decision.Steps = append(decision.Steps, DecisionStep{Provider: target.Provider, Outcome: "provider_unavailable"})
			if hasPendingResponse {
				return withPlan(resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget)), nil
			}
			cancelBudget()
			return Result{Plan: plan}, &ProviderUnavailableError{Name: target.Provider, Decision: decision}
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
					return Result{Plan: plan}, &RouteError{Decision: decision, Err: cause}
				}
				if hasPendingResponse {
					return withPlan(resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget)), nil
				}
				cancelBudget()
				cause := firstContextError(parent, budget)
				if cause == nil {
					cause = context.Canceled
				}
				return Result{Plan: plan}, &RouteError{Decision: decision, Err: cause}
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
				return Result{Plan: plan}, &RouteError{Decision: decision, Err: err}
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
		return withPlan(resultWithCancel(response, decision, settlement, cancelAttempt, cancelBudget)), nil
	}

	if hasPendingResponse {
		if err := parent.Err(); err != nil {
			closePending()
			cancelBudget()
			return Result{Plan: plan}, &RouteError{Decision: decision, Err: err}
		}
		return withPlan(resultWithCancel(pendingResponse, decision, settlement, pendingCancel, cancelBudget)), nil
	}
	cancelBudget()
	if decision.Attempts == 0 {
		return Result{Plan: plan}, &NoAvailableTargetError{Decision: decision}
	}
	return Result{Plan: plan}, &RouteError{Decision: decision, Err: lastErr}
}

const maxSettlementDrainBytes = 64 << 10

// Models 返回 Router 当前公开的模型列表。
func (router *Router) Models() []Model {
	registry, _ := router.registrySnapshot()
	return registry.List()
}

// registrySnapshot 获取目录和版本号的一致快照。
func (router *Router) registrySnapshot() (*ModelRegistry, string) {
	router.registryMu.RLock()
	defer router.registryMu.RUnlock()
	return router.registry, router.configVersion
}

// breakerFor 在锁保护下读取目标熔断器。
func (router *Router) breakerFor(key string) *circuitBreaker {
	router.registryMu.RLock()
	defer router.registryMu.RUnlock()
	return router.breakers[key]
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

// NoEligibleTargetError 表示能力契约筛选后没有可执行目标。
type NoEligibleTargetError struct {
	Plan decision.ExecutionPlan
}

// Error 返回不泄露请求内容的能力筛选错误描述。
func (e *NoEligibleTargetError) Error() string {
	return "no eligible model target"
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
