package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

func newTestRouter(openAI, anthropic provider.Provider, registry *gateway.ModelRegistry) *gateway.Router {
	providers := make(map[string]provider.Provider)
	if openAI != nil {
		providers["openai"] = openAI
	}
	if anthropic != nil {
		providers["anthropic"] = anthropic
	}
	return gateway.NewRouter(providers, registry, gateway.Policy{
		RequestTimeout:   time.Second,
		AttemptTimeout:   time.Second,
		FailureThreshold: 3,
		Cooldown:         time.Second,
	})
}

func TestModelsRequiresAuthentication(t *testing.T) {
	handler := New("limen-secret", nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestValidBearerTokenRejectsEmptyExpectedKey(t *testing.T) {
	if validBearerToken("Bearer ", "") {
		t.Fatal("empty expected key was accepted")
	}
}

func TestModelsReturnsConfiguredModelsSortedByID(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{
		{ID: "smart-model", Targets: []gateway.Target{{Provider: "anthropic", UpstreamModel: "claude-real"}}},
		{ID: "fast-model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-real"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(nil, nil, registry))
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Object != "list" {
		t.Fatalf("response = %d %#v", response.Code, body)
	}
	if got := []string{body.Data[0].ID, body.Data[1].ID}; !slices.Equal(got, []string{"fast-model", "smart-model"}) {
		t.Fatalf("model IDs = %v", got)
	}
	if body.Data[0].Object != "model" || body.Data[0].Created != 0 || body.Data[0].OwnedBy != "limen" {
		t.Fatalf("first model = %#v", body.Data[0])
	}
}

func TestModelsReturnsCompatibilityPatterns(t *testing.T) {
	handler := New("limen-secret", newTestRouter(nil, nil, gateway.NewCompatibilityRegistry()))
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(body.Data))
	for _, model := range body.Data {
		ids = append(ids, model.ID)
	}
	if !slices.Equal(ids, []string{"claude-*", "gpt-*", "o1-*", "o3-*"}) {
		t.Fatalf("model IDs = %v", ids)
	}
}

func TestChatRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		body   string
		status int
	}{
		{"missing token", "", `{"model":"gpt-test"}`, http.StatusUnauthorized},
		{"wrong token", "wrong", `{"model":"gpt-test"}`, http.StatusUnauthorized},
		{"invalid json", "limen-secret", `{`, http.StatusBadRequest},
		{"missing model", "limen-secret", `{}`, http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := New("limen-secret", nil)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("content type = %q", got)
			}
		})
	}
}

func TestChatRelaysProviderResponse(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"chat-1"}`))
	}))
	defer providerServer.Close()

	openAI := provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret")
	handler := New("limen-secret", newTestRouter(openAI, nil, gateway.NewCompatibilityRegistry()))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || response.Body.String() != `{"id":"chat-1"}` {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestChatPublishesCompleteSettlementTrailers(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-1","usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer providerServer.Close()
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{
		Provider:      "openai",
		UpstreamModel: "gpt-test",
		Pricing:       &cost.Pricing{InputPerMillionNanoUSD: 1_000_000, OutputPerMillionNanoUSD: 2_000_000},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret"), nil, registry))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Header().Get("X-Limen-Settlement-Status") != "complete" || response.Header().Get("X-Limen-Input-Tokens") != "10" || response.Header().Get("X-Limen-Output-Tokens") != "5" || response.Header().Get("X-Limen-Total-Tokens") != "15" || response.Header().Get("X-Limen-Cost-USD") != "0.00000002" {
		t.Fatalf("settlement headers = %v", response.Header())
	}
}

func TestChatKeepsSettlementMetadataOutOfSSEBody(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer providerServer.Close()
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test", Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1_000_000, OutputPerMillionNanoUSD: 1_000_000}}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret"), nil, registry))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if strings.Contains(response.Body.String(), "X-Limen-") || response.Header().Get("X-Limen-Settlement-Status") != "complete" {
		t.Fatalf("stream=%q headers=%v", response.Body.String(), response.Header())
	}
}

func TestChatLeavesCostEmptyWithoutPricing(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer providerServer.Close()
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret"), nil, registry))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Header().Get("X-Limen-Settlement-Status") != "partial" || response.Header().Get("X-Limen-Cost-USD") != "" {
		t.Fatalf("settlement headers = %v", response.Header())
	}
}

func TestChatRelaysSSE(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	openAI := provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret")
	handler := New("limen-secret", newTestRouter(openAI, nil, gateway.NewCompatibilityRegistry()))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Body.String(); got != "data: first\n\ndata: [DONE]\n\n" {
		t.Fatalf("stream = %q", got)
	}
}

func TestChatRelaysProviderError(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
	}))
	defer providerServer.Close()

	openAI := provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret")
	handler := New("limen-secret", newTestRouter(openAI, nil, gateway.NewCompatibilityRegistry()))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests || response.Body.String() != `{"error":{"message":"busy"}}` {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestChatExposesSafeRoutingHeaders(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"busy"}`)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-real","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`)
	}))
	defer backup.Close()
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "smart-model", Targets: []gateway.Target{
		{Provider: "openai", UpstreamModel: "gpt-real"},
		{Provider: "anthropic", UpstreamModel: "claude-real"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(
		provider.NewOpenAI(primary.Client(), primary.URL, "openai-secret"),
		provider.NewAnthropic(backup.Client(), backup.URL, "anthropic-secret"),
		registry,
	)
	handler := New("limen-secret", router)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"smart-model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("X-Limen-Provider") != "anthropic" || response.Header().Get("X-Limen-Attempts") != "2" || response.Header().Get("X-Limen-Route") != "openai:503>anthropic:200" {
		t.Fatalf("status=%d headers=%v", response.Code, response.Header())
	}
}

func TestChatRoutesClaudeAndRejectsUnknownModel(t *testing.T) {
	var claudeRequests int
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-1","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer providerServer.Close()

	router := newTestRouter(nil, provider.NewAnthropic(providerServer.Client(), providerServer.URL, "anthropic-secret"), gateway.NewCompatibilityRegistry())
	handler := New("limen-secret", router)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || claudeRequests != 1 {
		t.Fatalf("status=%d claude_requests=%d", response.Code, claudeRequests)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"unknown","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown model status = %d", response.Code)
	}
}

func TestChatRejectsUnsupportedContent(t *testing.T) {
	handler := New("limen-secret", nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported content status = %d", response.Code)
	}
}
