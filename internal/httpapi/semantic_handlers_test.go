package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
	"github.com/huz/limen/internal/semantic"
	"github.com/huz/limen/internal/telemetry"
)

type semanticAssessorFunc func(context.Context, string, semantic.State, string, string) (semantic.Assessment, error)

func (function semanticAssessorFunc) Assess(ctx context.Context, tenantID string, state semantic.State, model, template string) (semantic.Assessment, error) {
	return function(ctx, tenantID, state, model, template)
}

func TestPrepareSemanticRequiresExplicitScopeAndPublicData(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "smart", Targets: []gateway.Target{{ID: "target", Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, registry, gateway.Policy{RequestTimeout: time.Second})
	calls := 0
	assessor := semanticAssessorFunc(func(context.Context, string, semantic.State, string, string) (semantic.Assessment, error) {
		calls++
		return semantic.Assessment{}, nil
	})
	handler := &Handler{router: router, semanticAssessor: assessor, metrics: telemetry.NewRegistry()}
	envelope := parsedChatRequest{Request: provider.ChatRequest{Model: "auto", Messages: []provider.Message{{Role: "user", Content: "请帮我写一个函数"}}}, Contract: decision.Contract{DataClass: "public"}, SemanticRouting: config.SemanticRouting{Mode: "active", ExternalEnabled: true, AllowedDataClasses: []string{"public"}, ModelVersion: "jev-1.13.0", StateBuilderVersion: "recent-user.v1", QuestionTemplateVersion: "task-complexity.zh.v1", MappingVersion: "task-target.v1", Timeout: 200 * time.Millisecond}}

	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, auth.Principal{TenantID: "tenant", Scopes: map[auth.Scope]struct{}{auth.ScopeAdmin: {}}}))
	assessment, _, _ := handler.prepareSemantic(request, envelope)
	if assessment == nil || assessment.Status != "not_authorized" || calls != 0 {
		t.Fatalf("assessment=%#v calls=%d", assessment, calls)
	}

	envelope.Contract.DataClass = "internal"
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, auth.Principal{TenantID: "tenant", Scopes: map[auth.Scope]struct{}{auth.ScopeSemanticExternal: {}}}))
	assessment, _, _ = handler.prepareSemantic(request, envelope)
	if assessment == nil || assessment.Status != "data_class_denied" || calls != 0 {
		t.Fatalf("assessment=%#v calls=%d", assessment, calls)
	}
}

func TestPrepareSemanticAssessesAuthorizedPublicText(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "smart", Targets: []gateway.Target{{ID: "target", Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, registry, gateway.Policy{RequestTimeout: time.Second})
	calls := 0
	sawText := false
	assessor := semanticAssessorFunc(func(_ context.Context, tenantID string, state semantic.State, model, template string) (semantic.Assessment, error) {
		calls++
		sawText = state.Text == "user: 请帮我写一个函数"
		if tenantID != "tenant" || model != "jev-1.13.0" || template != "task-complexity.zh.v1" || state.Language != "zh" {
			t.Fatalf("assessment input tenant=%q model=%q template=%q language=%q", tenantID, model, template, state.Language)
		}
		return semantic.Assessment{ModelVersion: model, TaskType: "code", TaskConfidence: .95, TaskProbabilities: map[string]float64{"extraction": 0, "transformation": 0, "writing": 0, "code": 1, "analysis": 0, "other": 0}, Complexity: "complex", ComplexityConfidence: .95, ComplexityProbabilities: map[string]float64{"simple": 0, "standard": 0, "complex": 1}}, nil
	})
	handler := &Handler{router: router, semanticAssessor: assessor, metrics: telemetry.NewRegistry()}
	envelope := parsedChatRequest{Request: provider.ChatRequest{Model: "auto", Messages: []provider.Message{{Role: "user", Content: "请帮我写一个函数"}}}, Contract: decision.Contract{DataClass: "public"}, SemanticRouting: config.SemanticRouting{Mode: "active", ExternalEnabled: true, AllowedDataClasses: []string{"public"}, ModelVersion: "jev-1.13.0", StateBuilderVersion: "recent-user.v1", QuestionTemplateVersion: "task-complexity.zh.v1", MappingVersion: "task-target.v1", Timeout: 200 * time.Millisecond, Rules: []config.SemanticRule{{TaskType: "code", Complexity: "complex", Language: "zh", MinimumTaskConfidence: .8, MinimumComplexityConfidence: .8, MinimumProbabilityMargin: .2, MinimumQualityTier: 4, ThresholdProfile: "code-complex-zh.v1"}}}}
	principal := auth.Principal{TenantID: "tenant", Scopes: map[auth.Scope]struct{}{auth.ScopeSemanticExternal: {}}}
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(context.WithValue(context.Background(), principalContextKey{}, principal))
	assessment, state, shadow := handler.prepareSemantic(request, envelope)
	if calls != 1 || !sawText || shadow || state.Text != "" || assessment == nil || !assessment.Applied || assessment.MinimumQualityTier != 4 {
		t.Fatalf("assessment=%#v state=%#v shadow=%v calls=%d", assessment, state, shadow, calls)
	}
}
