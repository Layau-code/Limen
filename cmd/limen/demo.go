package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
	"github.com/huz/limen/internal/run"
)

// demoProvider 是只在本地演示中使用的确定性 Provider，不访问网络。
type demoProvider func(context.Context, provider.ChatRequest) (provider.Response, error)

// Chat 返回演示用的固定 Provider 响应。
func (provider demoProvider) Chat(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
	return provider(ctx, request)
}

// demoResult 是离线演示输出，刻意不包含 Prompt 和真实上游模型名。
type demoResult struct {
	Scenario       string        `json:"scenario"`
	Route          string        `json:"route"`
	Attempts       int           `json:"attempts"`
	DraftProvider  string        `json:"draft_provider"`
	ImpactDetected bool          `json:"impact_detected"`
	ProviderCalls  int           `json:"provider_calls"`
	Selection      demoSelection `json:"selection"`
	Run            demoRun       `json:"run"`
}

// demoSelection 是不暴露上游映射的能力契约选模摘要。
type demoSelection struct {
	RequestedModel string   `json:"requested_model"`
	SelectedModel  string   `json:"selected_model"`
	DataClass      string   `json:"data_class"`
	MinimumQuality int      `json:"minimum_quality_tier"`
	Rejected       []string `json:"rejected,omitempty"`
}

// demoRun 是不暴露租户和幂等细节的 Run 结算摘要。
type demoRun struct {
	State              string `json:"state"`
	SettlementStatus   string `json:"settlement_status"`
	InFlight           int    `json:"in_flight"`
	SettledCostNanoUSD int64  `json:"settled_cost_nano_usd"`
}

// runDemo 展示一次 Fallback 和配置草稿影响分析，不读取配置或访问外部网络。
func runDemo(stdout io.Writer) error {
	providerCalls := 0
	registry, err := gateway.NewModelRegistry([]gateway.Model{{
		ID: "smart-model",
		Targets: []gateway.Target{
			{ID: "primary", Provider: "openai", UpstreamModel: "gpt-demo-primary", Capabilities: []string{"text"}, QualityTier: 4, CostTier: 1, DataClasses: []string{"internal"}},
			{ID: "fallback", Provider: "anthropic", UpstreamModel: "claude-demo-fallback", Capabilities: []string{"text"}, QualityTier: 4, CostTier: 2, DataClasses: []string{"internal"}},
		},
	}, {
		ID:      "basic-model",
		Targets: []gateway.Target{{ID: "basic", Provider: "openai", UpstreamModel: "gpt-demo-basic", Capabilities: []string{"text"}, QualityTier: 1, DataClasses: []string{"public"}}},
	}})
	if err != nil {
		return fmt.Errorf("create demo registry: %w", err)
	}
	router := gateway.NewRouter(map[string]provider.Provider{
		"openai": demoProvider(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			providerCalls++
			return provider.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("demo transient failure"))}, nil
		}),
		"anthropic": demoProvider(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			providerCalls++
			return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
		}),
	}, registry, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 3, Cooldown: time.Second})
	request := provider.ChatRequest{Model: "auto", Messages: []provider.Message{{Role: "user", Content: "demo"}}}
	contract := decision.Contract{RequiredCapabilities: []string{"text"}, MinimumQualityTier: 3, DataClass: "internal", Strategy: decision.StrategyBalanced}
	input, originalPlan, err := router.Explain(request, contract)
	if err != nil {
		return fmt.Errorf("explain demo request: %w", err)
	}
	result, err := router.ChatWithContract(context.Background(), request, contract)
	if err != nil {
		return fmt.Errorf("execute demo request: %w", err)
	}
	if result.Response.Body != nil {
		_ = result.Response.Body.Close()
	}
	draft, err := gateway.NewModelRegistry([]gateway.Model{{
		ID:      "smart-model",
		Targets: []gateway.Target{{ID: "primary", Provider: "anthropic", UpstreamModel: "claude-demo-draft", Capabilities: []string{"text"}, QualityTier: 4, DataClasses: []string{"internal"}}},
	}})
	if err != nil {
		return fmt.Errorf("create demo draft: %w", err)
	}
	draftPlan, err := router.ReplayWithRegistry(input, draft, "demo-draft")
	if err != nil {
		return fmt.Errorf("replay demo draft: %w", err)
	}
	runSummary, err := demoRunSummary(result.Plan.PlanHash)
	if err != nil {
		return fmt.Errorf("settle demo run: %w", err)
	}
	output := demoResult{
		Scenario:       "draft-impact",
		Route:          result.Decision.String(),
		Attempts:       len(result.Attempts),
		DraftProvider:  draftPlan.Targets[0].Target.Provider,
		ImpactDetected: originalPlan.PlanHash != draftPlan.PlanHash,
		ProviderCalls:  providerCalls,
		Selection: demoSelection{
			RequestedModel: request.Model,
			SelectedModel:  originalPlan.Targets[0].ModelID,
			DataClass:      contract.DataClass,
			MinimumQuality: contract.MinimumQualityTier,
			Rejected:       rejectedCandidates(originalPlan),
		},
		Run: runSummary,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(output)
}

// demoRunSummary 演示一次 Run 准入、决策绑定和已知成本结算。
func demoRunSummary(decisionID string) (demoRun, error) {
	store := run.NewMemoryStore()
	if err := store.CreateRun(run.Run{ID: "run-demo", TenantID: "demo", State: run.StateActive, SoftBudgetNanoUSD: 1_000_000, MaxParallelism: 1, ConfigVersion: "demo-config"}); err != nil {
		return demoRun{}, err
	}
	now := time.Unix(0, 0).UTC()
	request, err := store.AdmitRequest("demo", "run-demo", run.Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "demo-request", RequestHash: "sha256:demo"}, now)
	if err != nil {
		return demoRun{}, err
	}
	if err := store.SetRequestDecisionID("demo", request.ID, decisionID); err != nil {
		return demoRun{}, err
	}
	if _, err := store.BeginSettlement("demo", request.ID, now.Add(time.Second)); err != nil {
		return demoRun{}, err
	}
	costNanoUSD := int64(250_000)
	settled, err := store.SettleRequest("demo", request.ID, &costNanoUSD, now.Add(2*time.Second))
	if err != nil {
		return demoRun{}, err
	}
	item, ok := store.GetRun("demo", "run-demo")
	if !ok {
		return demoRun{}, errors.New("demo run not found")
	}
	return demoRun{State: string(item.State), SettlementStatus: settled.SettlementStatus, InFlight: item.InFlight, SettledCostNanoUSD: item.SettledCostNanoUSD}, nil
}

// rejectedCandidates 返回逻辑模型及稳定原因码，不暴露目标内部映射。
func rejectedCandidates(plan decision.ExecutionPlan) []string {
	rejected := make([]string, 0)
	for _, candidate := range plan.Candidates {
		if !candidate.Accepted {
			rejected = append(rejected, candidate.ModelID+":"+candidate.Reason)
		}
	}
	return rejected
}

var _ provider.Provider = demoProvider(nil)
