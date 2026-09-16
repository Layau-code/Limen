package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/provider"
)

type providerFunc func(context.Context, provider.ChatRequest) (provider.Response, error)

func (fn providerFunc) Chat(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
	return fn(ctx, request)
}

type closeSpy struct {
	io.Reader
	closed bool
}

func (body *closeSpy) Close() error {
	body.closed = true
	return nil
}

func newTestRouter(openAI, anthropic provider.Provider, registry *ModelRegistry) *Router {
	providers := make(map[string]provider.Provider)
	if openAI != nil {
		providers["openai"] = openAI
	}
	if anthropic != nil {
		providers["anthropic"] = anthropic
	}
	return NewRouter(providers, registry, Policy{
		RequestTimeout:   time.Second,
		AttemptTimeout:   time.Second,
		FailureThreshold: 3,
		Cooldown:         time.Second,
	})
}

func TestRouterSelectsProviderByModelPrefix(t *testing.T) {
	var openAIRequests, anthropicRequests int
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAIRequests++
		_, _ = io.WriteString(w, `{"id":"openai"}`)
	}))
	defer openAI.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicRequests++
		_, _ = io.WriteString(w, `{"id":"anthropic"}`)
	}))
	defer anthropic.Close()

	router := newTestRouter(provider.NewOpenAI(openAI.Client(), openAI.URL, "openai-secret"), provider.NewAnthropic(anthropic.Client(), anthropic.URL, "anthropic-secret"), NewCompatibilityRegistry())
	for _, model := range []string{"gpt-test", "o1-test", "o3-test", "claude-test"} {
		result, err := router.Chat(context.Background(), provider.ChatRequest{Model: model, Messages: []provider.Message{{Role: "user", Content: "hello"}}})
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		_ = result.Response.Body.Close()
	}
	if openAIRequests != 3 || anthropicRequests != 1 {
		t.Fatalf("openai=%d anthropic=%d", openAIRequests, anthropicRequests)
	}
}

func TestRouterRejectsUnknownModel(t *testing.T) {
	router := newTestRouter(nil, nil, NewCompatibilityRegistry())
	_, err := router.Chat(context.Background(), provider.ChatRequest{Model: "unknown-model"})
	if err == nil {
		t.Fatal("expected unsupported model error")
	}
	if _, ok := err.(*UnsupportedModelError); !ok {
		t.Fatalf("error type = %T", err)
	}
}

func TestRouterMapsLogicalModelToUpstreamModel(t *testing.T) {
	var upstreamModel string
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		upstreamModel = request.Model
		_, _ = io.WriteString(w, `{"id":"openai"}`)
	}))
	defer openAI.Close()

	registry, err := NewModelRegistry([]Model{{ID: "fast-model", Targets: []Target{{Provider: "openai", UpstreamModel: "gpt-real"}}, DisplayName: "Fast Model"}})
	if err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(provider.NewOpenAI(openAI.Client(), openAI.URL, "openai-secret"), nil, registry)
	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "fast-model", Messages: []provider.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Response.Body.Close()
	if upstreamModel != "gpt-real" {
		t.Fatalf("upstream model = %q", upstreamModel)
	}
	_, err = router.Chat(context.Background(), provider.ChatRequest{Model: "gpt-real"})
	if _, ok := err.(*UnsupportedModelError); !ok {
		t.Fatalf("unregistered model error = %T, want *UnsupportedModelError", err)
	}
}

func TestRouterMapsLogicalModelToAnthropic(t *testing.T) {
	var upstreamModel string
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		upstreamModel = request.Model
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-real","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`)
	}))
	defer anthropic.Close()

	registry, err := NewModelRegistry([]Model{{ID: "smart-model", Targets: []Target{{Provider: "anthropic", UpstreamModel: "claude-real"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(nil, provider.NewAnthropic(anthropic.Client(), anthropic.URL, "anthropic-secret"), registry)
	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model", Messages: []provider.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Response.Body.Close()
	if upstreamModel != "claude-real" {
		t.Fatalf("upstream model = %q", upstreamModel)
	}
}

func TestCompatibilityRegistryListsStablePatterns(t *testing.T) {
	models := NewCompatibilityRegistry().List()
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	want := []string{"claude-*", "gpt-*", "o1-*", "o3-*"}
	if !slices.Equal(ids, want) {
		t.Fatalf("models = %v, want %v", ids, want)
	}
}

func TestModelRegistryRejectsInvalidTargets(t *testing.T) {
	tests := []Model{
		{ID: "", Targets: []Target{{Provider: "openai", UpstreamModel: "gpt"}}},
		{ID: "empty-targets"},
		{ID: "bad-provider", Targets: []Target{{Provider: "other", UpstreamModel: "model"}}},
		{ID: "empty-upstream", Targets: []Target{{Provider: "openai"}}},
		{ID: "duplicate-target", Targets: []Target{{Provider: "openai", UpstreamModel: "gpt"}, {Provider: "openai", UpstreamModel: "gpt"}}},
	}
	for _, model := range tests {
		if _, err := NewModelRegistry([]Model{model}); err == nil {
			t.Fatalf("model %+v was accepted", model)
		}
	}
}

func TestModelRegistryExposesOrderedTargets(t *testing.T) {
	registry := NewCompatibilityRegistry()
	models := registry.List()
	if len(models[0].Targets) != 1 || models[0].Targets[0].Provider != "anthropic" {
		t.Fatalf("targets = %+v", models[0].Targets)
	}
	models[0].Targets[0].Provider = "changed"
	if got := registry.List()[0].Targets[0].Provider; got != "anthropic" {
		t.Fatalf("registry target changed to %q", got)
	}
}

func TestRouterFallsBackOnTransientStatus(t *testing.T) {
	statuses := []int{408, 409, 429, 500, 502, 503, 504, 529}
	for _, status := range statuses {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls []string
			primaryBody := &closeSpy{Reader: strings.NewReader(`{"error":"busy"}`)}
			providers := map[string]provider.Provider{
				"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
					calls = append(calls, "openai")
					return provider.Response{StatusCode: status, Body: primaryBody}, nil
				}),
				"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
					calls = append(calls, "anthropic")
					return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`))}, nil
				}),
			}
			registry, err := NewModelRegistry([]Model{{
				ID: "smart-model",
				Targets: []Target{
					{Provider: "openai", UpstreamModel: "gpt-real"},
					{Provider: "anthropic", UpstreamModel: "claude-real"},
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			router := NewRouter(providers, registry, Policy{
				RequestTimeout:   time.Second,
				AttemptTimeout:   100 * time.Millisecond,
				FailureThreshold: 3,
				Cooldown:         time.Second,
			})
			result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
			if err != nil {
				t.Fatal(err)
			}
			defer result.Response.Body.Close()
			if result.Response.StatusCode != http.StatusOK || !slices.Equal(calls, []string{"openai", "anthropic"}) {
				t.Fatalf("status=%d calls=%v", result.Response.StatusCode, calls)
			}
			if !primaryBody.closed {
				t.Fatal("fallback response body was not closed")
			}
			wantRoute := "openai:" + strconv.Itoa(status) + ">anthropic:200"
			if result.Decision.Attempts != 2 || result.Decision.String() != wantRoute {
				t.Fatalf("decision = %+v", result.Decision)
			}
		})
	}
}

func TestRouterFallsBackOnTransportError(t *testing.T) {
	var backupCalls int
	providers := map[string]provider.Provider{
		"openai": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			return provider.Response{}, &provider.TransportError{Operation: "send", Err: errors.New("network unavailable")}
		}),
		"anthropic": providerFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
			backupCalls++
			return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`))}, nil
		}),
	}
	registry, err := NewModelRegistry([]Model{{ID: "smart-model", Targets: []Target{
		{Provider: "openai", UpstreamModel: "gpt-real"},
		{Provider: "anthropic", UpstreamModel: "claude-real"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(providers, registry, Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 3, Cooldown: time.Second})
	result, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	if backupCalls != 1 || result.Decision.String() != "openai:transport_error>anthropic:200" {
		t.Fatalf("backup_calls=%d decision=%+v", backupCalls, result.Decision)
	}
}
