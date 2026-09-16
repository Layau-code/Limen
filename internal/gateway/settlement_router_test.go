package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/provider"
)

func TestRouterResultCarriesSettlement(t *testing.T) {
	pricing := &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 2}
	registry, err := NewModelRegistry([]Model{{ID: "model", Targets: []Target{{Provider: "openai", UpstreamModel: "gpt-test", Pricing: pricing}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{
				StatusCode: httpStatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"ok"}`)),
				Usage:      fixedUsage{usage: provider.Usage{InputTokens: 10, OutputTokens: 5, Complete: true}},
			}, nil
		}),
	}, registry, Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 3, Cooldown: time.Second})

	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	_, _ = io.ReadAll(result.Response.Body)
	if result.Settlement == nil || result.Settlement.Summary().Status != SettlementComplete {
		t.Fatalf("settlement = %#v", result.Settlement)
	}
}

const httpStatusOK = 200

func TestRouterSettlementIncludesFallbackResponses(t *testing.T) {
	pricing := &cost.Pricing{InputPerMillionNanoUSD: 1_000_000, OutputPerMillionNanoUSD: 1_000_000}
	registry, err := NewModelRegistry([]Model{{ID: "model", Targets: []Target{
		{Provider: "openai", UpstreamModel: "gpt-one", Pricing: pricing},
		{Provider: "anthropic", UpstreamModel: "claude-two", Pricing: pricing},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("busy")), Usage: fixedUsage{usage: provider.Usage{InputTokens: 2, OutputTokens: 1, Complete: true}}}, nil
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Usage: fixedUsage{usage: provider.Usage{InputTokens: 3, OutputTokens: 4, Complete: true}}}, nil
		}),
	}, registry, Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 3, Cooldown: time.Second})

	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	_, _ = io.ReadAll(result.Response.Body)
	summary := result.Settlement.Summary()
	if summary.Status != SettlementComplete || summary.InputTokens != 5 || summary.OutputTokens != 5 || summary.CostNanoUSD != 10 {
		t.Fatalf("summary = %+v", summary)
	}
}
