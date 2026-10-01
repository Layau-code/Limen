package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/journal"
	"github.com/huz/limen/internal/provider"
	"github.com/huz/limen/internal/semantic"
)

func semanticAcceptanceConfig() config.SemanticRouting {
	cfg := config.DefaultRouting().Semantic
	cfg.Mode, cfg.ExternalEnabled, cfg.AllowedDataClasses = "active", true, []string{"public"}
	cfg.Rules = []config.SemanticRule{{TaskType: "code", Complexity: "complex", Language: "en", MinimumTaskConfidence: .8, MinimumComplexityConfidence: .8, MinimumProbabilityMargin: .2, MinimumQualityTier: 4, ThresholdProfile: "test", EvaluationReport: "test-only"}}
	return cfg
}

func semanticAcceptanceResult() semantic.Assessment {
	return semantic.Assessment{ModelVersion: "jev-1.13.0", TaskType: "code", TaskConfidence: 1, TaskProbabilities: map[string]float64{"extraction": 0, "transformation": 0, "writing": 0, "code": 1, "analysis": 0, "other": 0}, Complexity: "complex", ComplexityConfidence: 1, ComplexityProbabilities: map[string]float64{"simple": 0, "standard": 0, "complex": 1}}
}

func semanticAcceptanceRegistry(t *testing.T, target string) *gateway.ModelRegistry {
	t.Helper()
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "smart", Targets: []gateway.Target{{ID: target, Provider: "openai", UpstreamModel: "test", QualityTier: 4, Capabilities: []string{"text"}, DataClasses: []string{"public", "internal"}, SupportsStreaming: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func semanticAcceptanceRequest(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

const semanticAcceptanceBody = `{"model":"auto","messages":[{"role":"user","content":"write a private regression test"}],"limen":{"data_class":"public"}}`

func TestSemanticHTTPPinsConfigAndReplaysOffline(t *testing.T) {
	providerCalls, assessorCalls := 0, 0
	upstream := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		providerCalls++
		return provider.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"response"}`))}, nil
	})
	registry := semanticAcceptanceRegistry(t, "old-target")
	router := newTestRouter(upstream, nil, registry)
	if err := router.ReplaceRegistryWithSemanticPolicy(registry, "old-config", router.Policy(), semanticAcceptanceConfig()); err != nil {
		t.Fatal(err)
	}
	assessor := semanticAssessorFunc(func(context.Context, string, semantic.State, string, string) (semantic.Assessment, error) {
		assessorCalls++
		if err := router.ReplaceRegistryWithSemanticPolicy(semanticAcceptanceRegistry(t, "new-target"), "new-config", router.Policy(), config.SemanticRouting{Mode: "off"}); err != nil {
			t.Fatal(err)
		}
		return semanticAcceptanceResult(), nil
	})
	store := journal.NewMemoryStore()
	h := NewWithOptions(HandlerOptions{Authenticator: auth.NewStaticAuthenticator("test-key", "local", nil), Router: router, Decisions: store, SemanticAssessor: assessor})
	w := semanticAcceptanceRequest(h, "/v1/chat/completions", semanticAcceptanceBody)
	if w.Code != 200 || w.Header().Get("X-Limen-Config-Version") != "old-config" {
		t.Fatalf("config changed during assessment: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	id := w.Header().Get("X-Limen-Decision-ID")
	record, err := store.Get(context.Background(), "local", id)
	if err != nil {
		t.Fatal(err)
	}
	if record.Plan.Targets[0].Target.ID != "old-target" || record.Input.AlgorithmVersion != decision.AlgorithmVersionV3 {
		t.Fatal("mixed configuration snapshot")
	}
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), "write a private regression test") {
		t.Fatal("journal retained prompt")
	}
	w = semanticAcceptanceRequest(h, "/v1/limen/decisions/"+id+"/replay", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"match":true`) {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}
	if err := router.ReplaceRegistryWithSemanticPolicy(registry, "old-config", router.Policy(), semanticAcceptanceConfig()); err != nil {
		t.Fatal(err)
	}
	w = semanticAcceptanceRequest(h, "/v1/limen/decisions/dry-run", semanticAcceptanceBody)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"semantic_status":"not_evaluated"`) {
		t.Fatalf("dry-run: %d %s", w.Code, w.Body.String())
	}
	if providerCalls != 1 || assessorCalls != 1 {
		t.Fatalf("offline operations called networks: provider=%d jev=%d", providerCalls, assessorCalls)
	}
}

func TestSemanticHTTPGatesAndNoEligibleReplay(t *testing.T) {
	for _, tc := range []struct {
		name, body, mode string
		scopes           []auth.Scope
		expected         int
		status           string
	}{
		{name: "bypass", body: strings.Replace(semanticAcceptanceBody, `"data_class":"public"`, `"data_class":"public","semantic_routing":false`, 1), mode: "active", expected: 200, status: "bypassed"},
		{name: "explicit", body: strings.Replace(semanticAcceptanceBody, `"model":"auto"`, `"model":"smart"`, 1), mode: "active", expected: 200},
		{name: "private", body: strings.Replace(semanticAcceptanceBody, `"public"`, `"internal"`, 1), mode: "active", expected: 200, status: "data_class_denied"},
		{name: "admin_without_scope", body: semanticAcceptanceBody, mode: "active", scopes: []auth.Scope{auth.ScopeAdmin}, expected: 200, status: "not_authorized"},
		{name: "off", body: semanticAcceptanceBody, mode: "off", expected: 200},
		{name: "hard_filter", body: strings.Replace(semanticAcceptanceBody, `"data_class":"public"`, `"data_class":"public","semantic_routing":false,"required_capabilities":["vision"]`, 1), mode: "active", expected: 503, status: "bypassed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			router := newTestRouter(testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
				return provider.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}), nil, semanticAcceptanceRegistry(t, "target"))
			cfg := semanticAcceptanceConfig()
			cfg.Mode = tc.mode
			router.SetSemanticRouting(cfg)
			store := journal.NewMemoryStore()
			h := NewWithOptions(HandlerOptions{Authenticator: auth.NewStaticAuthenticator("test-key", "local", tc.scopes), Router: router, Decisions: store, SemanticAssessor: semanticAssessorFunc(func(context.Context, string, semantic.State, string, string) (semantic.Assessment, error) {
				calls++
				return semanticAcceptanceResult(), nil
			})})
			w := semanticAcceptanceRequest(h, "/v1/chat/completions", tc.body)
			if w.Code != tc.expected || calls != 0 {
				t.Fatalf("gate status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
			if tc.status != "" {
				id := w.Header().Get("X-Limen-Decision-ID")
				record, err := store.Get(context.Background(), "local", id)
				if err != nil {
					t.Fatal(err)
				}
				if record.Input.SemanticAssessment == nil || record.Input.SemanticAssessment.Status != tc.status {
					t.Fatal("skip metadata missing")
				}
				w = semanticAcceptanceRequest(h, "/v1/limen/decisions/"+id+"/replay", "")
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"match":true`) {
					t.Fatalf("replay: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestSemanticTimeoutPreservesOuterDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	outer, _ := ctx.Deadline()
	assessorCalls, providerCalls := 0, 0
	router := newTestRouter(testProviderFunc(func(ctx context.Context, _ provider.ChatRequest) (provider.Response, error) {
		providerCalls++
		deadline, _ := ctx.Deadline()
		if !deadline.Equal(outer) {
			t.Errorf("provider deadline reset: got %s want %s", deadline, outer)
		}
		return provider.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}), nil, semanticAcceptanceRegistry(t, "target"))
	router.SetSemanticRouting(semanticAcceptanceConfig())
	h := NewWithOptions(HandlerOptions{Authenticator: auth.NewStaticAuthenticator("test-key", "local", nil), Router: router, SemanticAssessor: semanticAssessorFunc(func(ctx context.Context, _ string, _ semantic.State, _, _ string) (semantic.Assessment, error) {
		assessorCalls++
		deadline, _ := ctx.Deadline()
		if time.Until(deadline) > 50*time.Millisecond {
			t.Error("assessment exceeded 10% budget")
		}
		<-ctx.Done()
		return semantic.Assessment{}, ctx.Err()
	})})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(semanticAcceptanceBody)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || assessorCalls != 1 || providerCalls != 1 {
		t.Fatalf("fallback failed: status=%d assessments=%d providers=%d", w.Code, assessorCalls, providerCalls)
	}
}
