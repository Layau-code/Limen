package gateway

import (
	"strings"
	"time"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/provider"
)

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
	return router.planWithRegistryAndRunPolicy(request, contract, registry, configVersion, runSnapshot, router.Policy())
}

// planWithInputAndRunPolicy 使用固定路由策略生成受治理请求的决策计划。
func (router *Router) planWithInputAndRunPolicy(request provider.ChatRequest, contract decision.Contract, runSnapshot decision.RunSnapshot, policy Policy) (decision.Input, decision.ExecutionPlan, error) {
	registry, configVersion := router.registrySnapshot()
	return router.planWithRegistryAndRunPolicy(request, contract, registry, configVersion, runSnapshot, policy)
}

// planWithRegistry 将指定目录和当前熔断快照组装为可持久化的 DecisionInput。
func (router *Router) planWithRegistry(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAndRun(request, contract, registry, configVersion, decision.RunSnapshot{})
}

// planWithRegistryAndRun 使用指定目录和 Run 快照生成决策计划。
func (router *Router) planWithRegistryAndRun(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, runSnapshot decision.RunSnapshot) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAndRunPolicy(request, contract, registry, configVersion, runSnapshot, router.Policy())
}

// planWithRegistryAndRunPolicy 使用指定目录和路由策略生成决策计划。
func (router *Router) planWithRegistryAndRunPolicy(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, runSnapshot decision.RunSnapshot, policy Policy) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAtAndRunPolicy(request, contract, registry, configVersion, router.now(), runSnapshot, policy)
}

// planWithRegistryAt 将指定目录和评估时间组装为可持久化的决策输入。
func (router *Router) planWithRegistryAt(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, evaluatedAt time.Time) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAtAndRun(request, contract, registry, configVersion, evaluatedAt, decision.RunSnapshot{})
}

// planWithRegistryAtAndRun 使用指定时间和 Run 快照生成可复现计划。
func (router *Router) planWithRegistryAtAndRun(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, evaluatedAt time.Time, runSnapshot decision.RunSnapshot) (decision.Input, decision.ExecutionPlan, error) {
	return router.planWithRegistryAtAndRunPolicy(request, contract, registry, configVersion, evaluatedAt, runSnapshot, router.Policy())
}

// planWithRegistryAtAndRunPolicy 使用指定时间、Run 快照和路由策略生成可复现计划。
func (router *Router) planWithRegistryAtAndRunPolicy(request provider.ChatRequest, contract decision.Contract, registry *ModelRegistry, configVersion string, evaluatedAt time.Time, runSnapshot decision.RunSnapshot, policy Policy) (decision.Input, decision.ExecutionPlan, error) {
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
				observation = breaker.observeWithCooldown(policy.Cooldown)
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
