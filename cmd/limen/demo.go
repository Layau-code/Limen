package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

// demoProvider 是只在本地演示中使用的确定性 Provider，不访问网络。
type demoProvider func(context.Context, provider.ChatRequest) (provider.Response, error)

// Chat 返回演示用的固定 Provider 响应。
func (provider demoProvider) Chat(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
	return provider(ctx, request)
}

// demoResult 是离线演示输出，刻意不包含 Prompt 和真实上游模型名。
type demoResult struct {
	Scenario       string `json:"scenario"`
	Route          string `json:"route"`
	Attempts       int    `json:"attempts"`
	DraftProvider  string `json:"draft_provider"`
	ImpactDetected bool   `json:"impact_detected"`
	ProviderCalls  int    `json:"provider_calls"`
}

// runDemo 展示一次 Fallback 和配置草稿影响分析，不读取配置或访问外部网络。
func runDemo(stdout io.Writer) error {
	providerCalls := 0
	registry, err := gateway.NewModelRegistry([]gateway.Model{{
		ID: "smart-model",
		Targets: []gateway.Target{
			{ID: "primary", Provider: "openai", UpstreamModel: "gpt-demo-primary"},
			{ID: "fallback", Provider: "anthropic", UpstreamModel: "claude-demo-fallback"},
		},
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
	request := provider.ChatRequest{Model: "smart-model", Messages: []provider.Message{{Role: "user", Content: "demo"}}}
	input, originalPlan, err := router.Explain(request, decision.Contract{})
	if err != nil {
		return fmt.Errorf("explain demo request: %w", err)
	}
	result, err := router.Chat(context.Background(), request)
	if err != nil {
		return fmt.Errorf("execute demo request: %w", err)
	}
	if result.Response.Body != nil {
		_ = result.Response.Body.Close()
	}
	draft, err := gateway.NewModelRegistry([]gateway.Model{{
		ID:      "smart-model",
		Targets: []gateway.Target{{ID: "primary", Provider: "anthropic", UpstreamModel: "claude-demo-draft"}},
	}})
	if err != nil {
		return fmt.Errorf("create demo draft: %w", err)
	}
	draftPlan, err := router.ReplayWithRegistry(input, draft, "demo-draft")
	if err != nil {
		return fmt.Errorf("replay demo draft: %w", err)
	}
	output := demoResult{
		Scenario:       "draft-impact",
		Route:          result.Decision.String(),
		Attempts:       len(result.Attempts),
		DraftProvider:  draftPlan.Targets[0].Target.Provider,
		ImpactDetected: originalPlan.PlanHash != draftPlan.PlanHash,
		ProviderCalls:  providerCalls,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(output)
}

var _ provider.Provider = demoProvider(nil)
