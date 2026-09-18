package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/provider"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// Policy 定义完整请求预算、单次尝试和熔断参数。
type Policy struct {
	RequestTimeout          time.Duration
	AttemptTimeout          time.Duration
	FailureThreshold        int
	Cooldown                time.Duration
	EconomyThresholdPercent int
	MinimumAttemptWindow    time.Duration
}

// Router 按模型注册表执行预算感知的 Provider 路由。
type Router struct {
	registryMu    sync.RWMutex
	registry      *ModelRegistry
	configVersion string
	policy        Policy
	breakers      map[string]*circuitBreaker
	engine        decision.Engine
	algorithms    *decision.AlgorithmRegistry
	now           func() time.Time
	executor      *Executor
}

// Result 同时返回上游响应和不含业务正文的路由决策。
type Result struct {
	Response   provider.Response
	Decision   Decision
	Settlement *Settlement
	Attempts   []AttemptReport
	Input      decision.Input
	Plan       decision.ExecutionPlan
}

// AttemptReport 描述一次真实 Provider 调用的安全结果摘要。
type AttemptReport struct {
	TargetID          string
	Provider          string
	UpstreamModel     string
	Outcome           string
	StatusCode        int
	ErrorClass        provider.ErrorClass
	ProviderRequestID string
}

// AttemptStartHook 在 Provider 调用前持久化 Attempt，失败时不会发起调用。
type AttemptStartHook func(decision.PlanTarget) error

// ChatOptions 描述一次 Chat 执行所需的可选治理快照和审计钩子。
type ChatOptions struct {
	Run           decision.RunSnapshot
	Registry      *ModelRegistry
	ConfigVersion string
	Policy        *Policy
	BeforeExecute func(decision.Input, decision.ExecutionPlan) error
	BeforeAttempt AttemptStartHook
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
	policy = normalizePolicy(policy)
	router := &Router{
		registry:   registry,
		policy:     policy,
		breakers:   make(map[string]*circuitBreaker),
		now:        now,
		algorithms: decision.NewAlgorithmRegistry(),
	}
	providerSet := make(map[string]provider.Provider, len(providers))
	for name, upstream := range providers {
		providerSet[name] = upstream
	}
	for _, model := range registry.List() {
		for _, target := range model.Targets {
			key := targetKey(model, target)
			if _, exists := router.breakers[key]; !exists {
				router.breakers[key] = newCircuitBreaker(policy.FailureThreshold, policy.Cooldown, now)
			}
		}
	}
	router.executor = newExecutor(providerSet, router.Policy, router.breakerFor)
	return router
}

// normalizePolicy 补齐决策所需的稳定路由默认值。
func normalizePolicy(policy Policy) Policy {
	if policy.EconomyThresholdPercent == 0 {
		policy.EconomyThresholdPercent = 20
	}
	if policy.MinimumAttemptWindow == 0 {
		policy.MinimumAttemptWindow = 250 * time.Millisecond
	}
	return policy
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
	policy = normalizePolicy(policy)
	router.registryMu.Lock()
	defer router.registryMu.Unlock()
	nextBreakers := make(map[string]*circuitBreaker, len(router.breakers)+len(registry.List()))
	// 保留历史目标的熔断器，供仍在执行旧配置版本的 Run 使用。
	for key, breaker := range router.breakers {
		nextBreakers[key] = breaker
	}
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

// ObservableModelID 将请求模型归一为来自当前目录的安全低基数标识。
func (router *Router) ObservableModelID(requested string) string {
	if requested == "auto" {
		return "auto"
	}
	registry, _ := router.registrySnapshot()
	model, found := registry.Resolve(requested)
	if !found {
		return "unsupported"
	}
	return model.ID
}

// Chat 使用默认契约处理一次聊天请求，保留 OpenAI 兼容调用方式。
func (router *Router) Chat(parent context.Context, request provider.ChatRequest) (Result, error) {
	return router.ChatWithContract(parent, request, decision.Contract{Active: request.Model == "auto"})
}

// ChatWithContract 根据能力契约生成计划，再在共享总预算内执行目标。
func (router *Router) ChatWithContract(parent context.Context, request provider.ChatRequest, contract decision.Contract) (Result, error) {
	return router.ChatWithOptions(parent, request, contract, ChatOptions{})
}

// ChatWithContractAndRun 根据固定 Run 快照生成计划并执行一次请求。
func (router *Router) ChatWithContractAndRun(parent context.Context, request provider.ChatRequest, contract decision.Contract, runSnapshot decision.RunSnapshot) (Result, error) {
	return router.ChatWithOptions(parent, request, contract, ChatOptions{Run: runSnapshot})
}

// ChatWithContractHooks 允许调用方同时记录决策和每次 Provider 尝试。
func (router *Router) ChatWithContractHooks(parent context.Context, request provider.ChatRequest, contract decision.Contract, beforeExecute func(decision.Input, decision.ExecutionPlan) error, beforeAttempt AttemptStartHook) (Result, error) {
	return router.ChatWithOptions(parent, request, contract, ChatOptions{BeforeExecute: beforeExecute, BeforeAttempt: beforeAttempt})
}

// ChatWithContractHooksAndRun 在持久化计划前注入 Run 快照和 Attempt 钩子。
func (router *Router) ChatWithContractHooksAndRun(parent context.Context, request provider.ChatRequest, contract decision.Contract, runSnapshot decision.RunSnapshot, beforeExecute func(decision.Input, decision.ExecutionPlan) error, beforeAttempt AttemptStartHook) (Result, error) {
	return router.ChatWithOptions(parent, request, contract, ChatOptions{Run: runSnapshot, BeforeExecute: beforeExecute, BeforeAttempt: beforeAttempt})
}

// ChatWithOptions 使用显式治理快照和配置目录执行一次 Chat 请求。
func (router *Router) ChatWithOptions(parent context.Context, request provider.ChatRequest, contract decision.Contract, options ChatOptions) (Result, error) {
	return router.chatWithContract(parent, request, contract, options)
}

// chatWithContract 统一处理计划生成、审计回调和计划执行。
func (router *Router) chatWithContract(parent context.Context, request provider.ChatRequest, contract decision.Contract, options ChatOptions) (Result, error) {
	ctx, span := otel.Tracer("github.com/huz/limen/internal/gateway").Start(parent, "limen.decision")
	defer span.End()
	var input decision.Input
	var plan decision.ExecutionPlan
	var err error
	if options.Registry == nil {
		input, plan, err = router.planWithInputAndRun(request, contract, options.Run)
	} else {
		input, plan, err = router.planWithRegistryAndRun(request, contract, options.Registry, options.ConfigVersion, options.Run)
	}
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("limen.config.version", plan.ConfigVersion),
			attribute.String("limen.algorithm.version", plan.AlgorithmVersion),
			attribute.String("limen.decision.input_hash", plan.InputHash),
			attribute.String("limen.decision.plan_hash", plan.PlanHash),
			attribute.Int("limen.decision.target_count", len(plan.Targets)),
		)
		if err == nil {
			if modelID := traceModelID(request.Model, plan); modelID != "" {
				span.SetAttributes(attribute.String("limen.model.id", modelID))
			}
		}
	}
	if err != nil {
		span.SetStatus(codes.Error, "")
		var decisionErr *decision.DecisionError
		if errors.As(err, &decisionErr) && decisionErr.Code == "no_eligible_target" {
			return Result{Input: input, Plan: plan}, &NoEligibleTargetError{Plan: plan}
		}
		return Result{Input: input, Plan: plan}, err
	}
	if options.BeforeExecute != nil {
		if err := options.BeforeExecute(input, plan); err != nil {
			span.SetStatus(codes.Error, "")
			return Result{Input: input, Plan: plan}, err
		}
	}
	result, err := router.executePlanWithPolicy(ctx, request, plan, options.Policy, options.BeforeAttempt)
	if err != nil {
		span.SetStatus(codes.Error, "")
	}
	result.Input = input
	return result, err
}

// traceModelID 返回由执行计划确认的稳定模型标识，避免记录任意请求输入。
func traceModelID(requested string, plan decision.ExecutionPlan) string {
	if requested == "auto" {
		return "auto"
	}
	if len(plan.Targets) == 0 {
		return ""
	}
	return plan.Targets[0].ModelID
}

// ChatWithContractHook 在 Provider 调用前执行一次决策审计回调。
func (router *Router) ChatWithContractHook(parent context.Context, request provider.ChatRequest, contract decision.Contract, beforeExecute func(decision.Input, decision.ExecutionPlan) error) (Result, error) {
	return router.ChatWithOptions(parent, request, contract, ChatOptions{BeforeExecute: beforeExecute})
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

// ExplainWithRun 使用固定 Run 快照生成可审计的决策输入和执行计划。
func (router *Router) ExplainWithRun(request provider.ChatRequest, contract decision.Contract, runSnapshot decision.RunSnapshot) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithInputAndRun(request, contract, runSnapshot)
}

// ExplainAt 使用指定评估时间生成可复现的决策计划，供离线解释和验收使用。
func (router *Router) ExplainAt(evaluatedAt time.Time, request provider.ChatRequest, contract decision.Contract) (decision.Input, decision.ExecutionPlan, error) {
	registry, configVersion := router.registrySnapshot()
	return router.planWithRegistryAt(request, contract, registry, configVersion, evaluatedAt)
}

// ExplainWithRegistry 使用指定配置版本生成只读决策计划，不访问 Provider 或修改当前目录。
func (router *Router) ExplainWithRegistry(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistry(request, contract, registry, configVersion)
}

// Replay 使用历史输入重算计划，不读取当前熔断状态，也不访问 Provider。
func (router *Router) Replay(input decision.Input) (decision.ExecutionPlan, error) {
	engine, ok := router.algorithms.Resolve(input.AlgorithmVersion)
	if !ok {
		return decision.ExecutionPlan{}, &decision.DecisionError{Code: "algorithm_version_unavailable"}
	}
	return engine.Decide(input)
}

// ReplayWithRegistry 将历史决策输入应用到指定目录，供发布前影响分析使用。
func (router *Router) ReplayWithRegistry(input decision.Input, registry *ModelRegistry, configVersion string) (decision.ExecutionPlan, error) {
	if registry == nil {
		return decision.ExecutionPlan{}, &decision.DecisionError{Code: "model_registry_unavailable"}
	}
	engine, ok := router.algorithms.Resolve(input.AlgorithmVersion)
	if !ok {
		return decision.ExecutionPlan{}, &decision.DecisionError{Code: "algorithm_version_unavailable"}
	}
	replayInput, err := router.replayInputWithRegistry(input, registry, configVersion)
	if err != nil {
		return decision.ExecutionPlan{}, err
	}
	return engine.Decide(replayInput)
}

// plan 将注册表和熔断器快照组装为确定性的 DecisionInput。
func (router *Router) plan(request provider.ChatRequest, contract decision.Contract) (decision.ExecutionPlan, error) {
	_, plan, err := router.planWithInput(request, contract)
	return plan, err
}

// planWithInput 将注册表和熔断器快照组装为可持久化的 DecisionInput。
func (router *Router) planWithInput(request provider.ChatRequest, contract decision.Contract) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithInputAndRun(request, contract, decision.RunSnapshot{})
}

// planWithInputAndRun 将注册表和固定 Run 快照组装为决策输入。
func (router *Router) planWithInputAndRun(request provider.ChatRequest, contract decision.Contract, runSnapshot decision.RunSnapshot) (decision.Input, decision.ExecutionPlan, error) {
	registry, configVersion := router.registrySnapshot()
	return router.planWithRegistryAndRun(request, contract, registry, configVersion, runSnapshot)
}

// planWithRegistry 将指定目录和当前熔断快照组装为可持久化的 DecisionInput。
func (router *Router) planWithRegistry(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAndRun(request, contract, registry, configVersion, decision.RunSnapshot{})
}

// planWithRegistryAndRun 使用指定目录和 Run 快照生成决策计划。
func (router *Router) planWithRegistryAndRun(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, runSnapshot decision.RunSnapshot) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAtAndRun(request, contract, registry, configVersion, router.now(), runSnapshot)
}

// planWithRegistryAt 将指定目录和评估时间组装为可持久化的决策输入。
func (router *Router) planWithRegistryAt(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, evaluatedAt time.Time) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAtAndRun(request, contract, registry, configVersion, evaluatedAt, decision.RunSnapshot{})
}

// planWithRegistryAtAndRun 使用指定时间和 Run 快照生成可复现计划。
func (router *Router) planWithRegistryAtAndRun(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, evaluatedAt time.Time, runSnapshot decision.RunSnapshot) (decision.Input, decision.ExecutionPlan, error) {
	if registry == nil {
		return decision.Input{}, decision.ExecutionPlan{}, &decision.DecisionError{Code: "model_registry_unavailable"}
	}
	if evaluatedAt.IsZero() {
		evaluatedAt = router.now()
	}
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
				Compatibility:   model.Compatibility,
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
		AlgorithmVersion:  decision.AlgorithmVersionV2,
		ConfigVersion:     configVersion,
		EvaluatedAtUnixMS: evaluatedAt.UnixMilli(),
		Request:           decision.Request{Model: request.Model, Stream: request.Stream, Contract: contract},
		Run:               runSnapshot,
		Candidates:        candidates,
	}
	plan, err := router.engine.Decide(input)
	return input, plan, err
}

// replayInputWithRegistry 保留历史请求与运行快照，只替换配置候选目标。
func (router *Router) replayInputWithRegistry(input decision.Input, registry *ModelRegistry, configVersion string) (decision.Input, error) {
	models := registry.List()
	if input.Request.Model != "auto" {
		model, found := registry.Resolve(input.Request.Model)
		if !found {
			return decision.Input{}, &UnsupportedModelError{Model: input.Request.Model}
		}
		models = []Model{model}
	} else if registry.IsCompatibility() {
		return decision.Input{}, &UnsupportedModelError{Model: input.Request.Model}
	}
	candidates := make([]decision.Candidate, 0)
	for _, model := range models {
		for _, target := range model.Targets {
			candidates = append(candidates, decision.Candidate{
				ModelID:         model.ID,
				Compatibility:   model.Compatibility,
				Target:          target,
				Enabled:         true,
				SecurityAllowed: true,
				Health:          historicalHealth(input.Candidates, model.ID, target),
			})
		}
	}
	input.ConfigVersion = strings.TrimSpace(configVersion)
	input.Candidates = candidates
	return input, nil
}

// historicalHealth 只沿用草稿中仍然存在的同一目标健康快照，避免影响分析读取当前熔断状态。
func historicalHealth(candidates []decision.Candidate, modelID string, target catalog.Target) decision.HealthSnapshot {
	for _, candidate := range candidates {
		if candidate.ModelID == modelID && candidate.Target.ID == target.ID && candidate.Target.Provider == target.Provider && candidate.Target.UpstreamModel == target.UpstreamModel {
			return candidate.Health
		}
	}
	return decision.HealthSnapshot{State: "closed"}
}

// executePlan 交给只消费 ExecutionPlan 的 Gateway Executor。
func (router *Router) executePlan(parent context.Context, request provider.ChatRequest, plan decision.ExecutionPlan, beforeAttempt AttemptStartHook) (Result, error) {
	return router.executor.Execute(parent, request, plan, beforeAttempt)
}

// executePlanWithPolicy 使用可选的 Run 固定执行策略消费计划。
func (router *Router) executePlanWithPolicy(parent context.Context, request provider.ChatRequest, plan decision.ExecutionPlan, policy *Policy, beforeAttempt AttemptStartHook) (Result, error) {
	if policy == nil {
		return router.executePlan(parent, request, plan, beforeAttempt)
	}
	return router.executor.ExecuteWithPolicy(parent, request, plan, *policy, beforeAttempt)
}

// traceProviderChat 为一次真实上游调用记录不含业务正文和上游模型名的 Attempt Span。
func traceProviderChat(ctx context.Context, upstream provider.Provider, request provider.ChatRequest, planned decision.PlanTarget) (provider.Response, error) {
	ctx, span := otel.Tracer("github.com/huz/limen/internal/gateway").Start(ctx, "limen.provider.attempt")
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("limen.target.id", catalog.OpaqueTargetID(planned.Target.ID)),
			attribute.String("limen.provider.name", planned.Target.Provider),
		)
	}
	response, err := upstream.Chat(ctx, request)
	if err != nil {
		if span.IsRecording() {
			span.SetAttributes(
				attribute.String("limen.attempt.result", attemptErrorResult(ctx, err)),
				attribute.String("limen.error.class", string(provider.ClassifyError(err))),
			)
		}
		span.SetStatus(codes.Error, "")
		return response, err
	}
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("limen.attempt.result", "response"),
			attribute.Int("http.response.status_code", response.StatusCode),
			attribute.String("limen.error.class", string(provider.ClassifyHTTPStatus(response.StatusCode))),
		)
	}
	if response.StatusCode >= 400 {
		span.SetStatus(codes.Error, "")
	}
	return response, nil
}

// attemptErrorResult 将 Context 状态归并为有限的 Attempt 结果类别。
func attemptErrorResult(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return "transport_error"
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

// AttemptStartError 表示调用 Provider 前无法持久化 Attempt。
type AttemptStartError struct {
	Err error
}

// Error 返回安全的 Attempt 持久化错误描述。
func (e *AttemptStartError) Error() string {
	return fmt.Sprintf("attempt start failed: %v", e.Err)
}

// Unwrap 返回底层 Store 错误，供 HTTP 层映射服务不可用。
func (e *AttemptStartError) Unwrap() error {
	return e.Err
}
