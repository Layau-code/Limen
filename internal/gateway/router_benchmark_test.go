package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/provider"
)

func BenchmarkRouterMainPath(b *testing.B) {
	router := benchmarkRouter(b, map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}),
	})
	request := provider.ChatRequest{Model: "smart-model"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := router.Chat(context.Background(), request)
		if err != nil {
			b.Fatal(err)
		}
		_ = result.Response.Body.Close()
	}
}

func BenchmarkRouterFallbackPath(b *testing.B) {
	router := benchmarkRouter(b, map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}),
	})
	request := provider.ChatRequest{Model: "smart-model"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := router.Chat(context.Background(), request)
		if err != nil {
			b.Fatal(err)
		}
		_ = result.Response.Body.Close()
	}
}

// benchmarkRouter 构造无网络依赖的固定多目标路由器。
func benchmarkRouter(b *testing.B, providers map[string]provider.Provider) *Router {
	b.Helper()
	registry, err := NewModelRegistry([]Model{{ID: "smart-model", Targets: []Target{
		{Provider: "openai", UpstreamModel: "gpt-test"},
		{Provider: "anthropic", UpstreamModel: "claude-test"},
	}}})
	if err != nil {
		b.Fatal(err)
	}
	return NewRouter(providers, registry, Policy{
		RequestTimeout: time.Second, AttemptTimeout: time.Second,
		FailureThreshold: 1000000, Cooldown: time.Minute,
	})
}
