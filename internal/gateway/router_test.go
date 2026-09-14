package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/huz/limen/internal/provider"
)

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

	router := NewRouter(provider.NewOpenAI(openAI.Client(), openAI.URL, "openai-secret", 0), provider.NewAnthropic(anthropic.Client(), anthropic.URL, "anthropic-secret", 0), NewCompatibilityRegistry())
	for _, model := range []string{"gpt-test", "o1-test", "o3-test", "claude-test"} {
		response, err := router.Chat(context.Background(), provider.ChatRequest{Model: model, Messages: []provider.Message{{Role: "user", Content: "hello"}}})
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		_ = response.Body.Close()
	}
	if openAIRequests != 3 || anthropicRequests != 1 {
		t.Fatalf("openai=%d anthropic=%d", openAIRequests, anthropicRequests)
	}
}

func TestRouterRejectsUnknownModel(t *testing.T) {
	router := NewRouter(nil, nil, NewCompatibilityRegistry())
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

	registry, err := NewModelRegistry([]Model{{ID: "fast-model", Provider: "openai", UpstreamModel: "gpt-real", DisplayName: "Fast Model"}})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(provider.NewOpenAI(openAI.Client(), openAI.URL, "openai-secret", 0), nil, registry)
	response, err := router.Chat(context.Background(), provider.ChatRequest{Model: "fast-model", Messages: []provider.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
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

	registry, err := NewModelRegistry([]Model{{ID: "smart-model", Provider: "anthropic", UpstreamModel: "claude-real"}})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil, provider.NewAnthropic(anthropic.Client(), anthropic.URL, "anthropic-secret", 0), registry)
	response, err := router.Chat(context.Background(), provider.ChatRequest{Model: "smart-model", Messages: []provider.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
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
