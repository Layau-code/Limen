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
	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/journal"
	"github.com/huz/limen/internal/provider"
	"github.com/huz/limen/internal/run"
)

func TestConfigControlPublishesAndReplacesRouter(t *testing.T) {
	initial, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, initial, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second})
	configs := configstore.NewMemoryStore()
	authenticator := auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeConfigsRead, auth.ScopeConfigsWrite})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(authenticator, router, nil, "tenant-a", journal.NewMemoryStore(), configs, nil)

	create := httptest.NewRequest(http.MethodPost, "/v1/limen/configs", strings.NewReader(`{"models":[{"id":"new","targets":[{"provider":"anthropic","upstream_model":"claude-test"}]}]}`))
	create.Header.Set("Authorization", "Bearer secret")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created configSummary
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(createdResponse.Body.String(), "claude-test") || !strings.Contains(createdResponse.Body.String(), catalog.OpaqueTargetID("anthropic:claude-test")) {
		t.Fatalf("config summary leaked target: %s", createdResponse.Body.String())
	}
	publish := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+created.Version+"/publish", nil)
	publish.Header.Set("Authorization", "Bearer secret")
	publish.Header.Set("Idempotency-Key", "publish-1")
	publishedResponse := httptest.NewRecorder()
	handler.ServeHTTP(publishedResponse, publish)
	if publishedResponse.Code != http.StatusOK || router.ConfigVersion() != created.Version {
		t.Fatalf("publish status=%d version=%s body=%s", publishedResponse.Code, router.ConfigVersion(), publishedResponse.Body.String())
	}
	models := router.Models()
	if len(models) != 1 || models[0].ID != "new" {
		t.Fatalf("models = %+v", models)
	}
}

func TestGovernedChatUsesRunConfigVersionAfterPublish(t *testing.T) {
	configs := configstore.NewMemoryStore()
	oldDocument := []byte(`{"routing":{"attempt_timeout":"2s","failure_threshold":1,"cooldown":"10s","economy_threshold_percent":77,"minimum_attempt_window":"333ms"},"models":[{"id":"model","targets":[{"id":"old","provider":"openai","upstream_model":"gpt-old","pricing":{"input_per_million_usd":"1","output_per_million_usd":"1"}}]}]}`)
	oldRecord, err := configs.Create(context.Background(), "tenant-a", oldDocument)
	if err != nil {
		t.Fatal(err)
	}
	var seenModel string
	upstream := testProviderFunc(func(_ context.Context, request provider.ChatRequest) (provider.Response, error) {
		seenModel = request.Model
		return provider.Response{StatusCode: http.StatusOK, ContentType: "application/json", Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`))}, nil
	})
	oldRegistry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{ID: "old", Provider: "openai", UpstreamModel: "gpt-old", Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(upstream, nil, oldRegistry)
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", auth.AllScopes()), router, nil, "tenant-a", journal.NewMemoryStore(), configs, run.NewMemoryService(nil),
	)
	publish := func(record configstore.Record, key string) {
		request := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+record.Version+"/publish", nil)
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("publish status=%d body=%s", response.Code, response.Body.String())
		}
	}
	publish(oldRecord, "publish-old")
	createRun := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1}`))
	createRun.Header.Set("Authorization", "Bearer secret")
	createRun.Header.Set("Idempotency-Key", "run-1")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, createRun)
	var created run.Run
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if createdResponse.Code != http.StatusCreated || created.ConfigVersion != oldRecord.Version {
		t.Fatalf("run = %d %+v", createdResponse.Code, created)
	}
	newRecord, err := configs.Create(context.Background(), "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"id":"new","provider":"openai","upstream_model":"gpt-new","pricing":{"input_per_million_usd":"1","output_per_million_usd":"1"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	publish(newRecord, "publish-new")

	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	chat.Header.Set("Authorization", "Bearer secret")
	chat.Header.Set("X-Limen-Run-ID", created.ID)
	chat.Header.Set("Idempotency-Key", "request-1")
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chat)
	if chatResponse.Code != http.StatusOK || seenModel != "gpt-old" || chatResponse.Header().Get("X-Limen-Config-Version") != oldRecord.Version {
		t.Fatalf("chat status=%d model=%q body=%s", chatResponse.Code, seenModel, chatResponse.Body.String())
	}
	decisionRequest := httptest.NewRequest(http.MethodGet, "/v1/limen/decisions/"+chatResponse.Header().Get("X-Limen-Decision-ID"), nil)
	decisionRequest.Header.Set("Authorization", "Bearer secret")
	decisionResponse := httptest.NewRecorder()
	handler.ServeHTTP(decisionResponse, decisionRequest)
	var decisionRecord struct {
		Input decision.Input `json:"input"`
	}
	if err := json.Unmarshal(decisionResponse.Body.Bytes(), &decisionRecord); err != nil {
		t.Fatal(err)
	}
	if decisionRecord.Input.ConfigVersion != oldRecord.Version || decisionRecord.Input.Run.EconomyThresholdPercent != 77 || decisionRecord.Input.Run.MinimumAttemptWindow != 333*time.Millisecond {
		t.Fatalf("decision = %+v", decisionRecord.Input)
	}
}

func TestGovernedChatRejectsMissingRunConfigVersion(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-current"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		t.Fatal("provider must not be called")
		return provider.Response{}, nil
	}), nil, registry)
	router.SetConfigVersion("current")
	runs := run.NewMemoryService(nil)
	if err := runs.CreateRun(context.Background(), "tenant-a", run.Run{ID: "run-missing", State: run.StateActive, SoftBudgetNanoUSD: 1, MaxParallelism: 1, Strategy: "balanced", ConfigVersion: "missing"}); err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", auth.AllScopes()), router, nil, "tenant-a", journal.NewMemoryStore(), configstore.NewMemoryStore(), runs,
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Limen-Run-ID", "run-missing")
	request.Header.Set("Idempotency-Key", "request-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "config_version_unavailable") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConfigDryRunUsesDraftWithoutChangingRouter(t *testing.T) {
	initial, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	configs := configstore.NewMemoryStore()
	record, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"draft-model","targets":[{"provider":"openai","upstream_model":"gpt-preview"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, initial, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second})
	authenticator := auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeInference, auth.ScopeDecisions, auth.ScopeConfigsRead})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(authenticator, router, nil, "tenant-a", journal.NewMemoryStore(), configs, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+record.Version+"/dry-run", strings.NewReader(`{"model":"draft-model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("X-Limen-Config-Version") != record.Version || response.Header().Get("X-Limen-Decision-ID") == "" {
		t.Fatalf("preview status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
	if strings.Contains(body, "gpt-preview") || !strings.Contains(body, catalog.OpaqueTargetID("openai:gpt-preview")) {
		t.Fatalf("preview leaked target mapping: %s", body)
	}
	if models := router.Models(); len(models) != 1 || models[0].ID != "old" {
		t.Fatalf("preview changed active router: %+v", models)
	}
}

func TestConfigReplayComparesHistoricalDecisionWithDraft(t *testing.T) {
	initial, err := gateway.NewModelRegistry([]gateway.Model{{ID: "smart", Targets: []gateway.Target{{ID: "primary", Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, initial, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second})
	input, plan, err := router.Explain(provider.ChatRequest{Model: "smart"}, decision.Contract{})
	if err != nil {
		t.Fatal(err)
	}
	decisions := journal.NewMemoryStore()
	if err := decisions.Save(nil, journal.Record{ID: "decision-1", TenantID: "tenant-a", Input: input, Plan: plan, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	configs := configstore.NewMemoryStore()
	record, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"smart","targets":[{"id":"primary","provider":"anthropic","upstream_model":"claude-new"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeDecisions, auth.ScopeConfigsRead}),
		router, nil, "tenant-a", decisions, configs, nil,
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+record.Version+"/replay", strings.NewReader(`{"decision_id":"decision-1"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("X-Limen-Config-Version") != record.Version {
		t.Fatalf("replay status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
	if strings.Contains(body, "gpt-old") || strings.Contains(body, "claude-new") || !strings.Contains(body, `"match":false`) || !strings.Contains(body, `"provider":"anthropic"`) {
		t.Fatalf("unsafe or incomplete replay response: %s", body)
	}
	if models := router.Models(); len(models) != 1 || models[0].Targets[0].Provider != "openai" {
		t.Fatalf("draft replay changed active router: %+v", models)
	}
}

func TestConfigReplayRequiresConfigReadScope(t *testing.T) {
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeDecisions}),
		gateway.NewRouter(nil, gateway.NewCompatibilityRegistry(), gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configstore.NewMemoryStore(), nil,
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/draft/replay", strings.NewReader(`{"decision_id":"decision-1"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "insufficient_scope") {
		t.Fatalf("scope response = %d %s", response.Code, response.Body.String())
	}
}

func TestConfigPublishRejectsIdempotencyConflict(t *testing.T) {
	configs := configstore.NewMemoryStore()
	record, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-test"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	other, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"other","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-other"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	router, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeConfigsWrite}),
		gateway.NewRouter(nil, router, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configs, nil,
	)
	request := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+record.Version+"/publish", nil)
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := request("publish-1"); response.Code != http.StatusOK {
		t.Fatalf("first publish status = %d", response.Code)
	}
	if response := request("publish-1"); response.Code != http.StatusOK {
		t.Fatalf("repeat publish status = %d", response.Code)
	}
	conflictRequest := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+other.Version+"/publish", nil)
	conflictRequest.Header.Set("Authorization", "Bearer secret")
	conflictRequest.Header.Set("Idempotency-Key", "publish-1")
	conflictResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictResponse, conflictRequest)
	if conflictResponse.Code != http.StatusConflict || !strings.Contains(conflictResponse.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict response = %d %s", conflictResponse.Code, conflictResponse.Body.String())
	}
}

func TestConfigDiffIsTenantScopedAndValueFree(t *testing.T) {
	configs := configstore.NewMemoryStore()
	before, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"provider":"openai","upstream_model":"gpt-old"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"provider":"anthropic","upstream_model":"claude-new"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeConfigsRead}),
		gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configs, nil,
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/limen/configs/"+after.Version+"/diff/"+before.Version, nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "gpt-old") || strings.Contains(body, "claude-new") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	for _, targetID := range []string{catalog.OpaqueTargetID("openai:gpt-old"), catalog.OpaqueTargetID("anthropic:claude-new")} {
		if !strings.Contains(body, targetID) {
			t.Fatalf("diff missing opaque target %s: %s", targetID, body)
		}
	}
}

func TestConfigDiffReportsEndpointBindingWithoutValue(t *testing.T) {
	configs := configstore.NewMemoryStore()
	before, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-model","endpoint_id":"endpoint:0123456789abcdef01234567"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-model","endpoint_id":"endpoint:fedcba987654321001234567"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-model"}}}})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeConfigsRead}),
		gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configs, nil,
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/limen/configs/"+after.Version+"/diff/"+before.Version, nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, ".endpoint_id") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	for _, endpointID := range []string{"endpoint:0123456789abcdef01234567", "endpoint:fedcba987654321001234567"} {
		if strings.Contains(body, endpointID) {
			t.Fatalf("diff leaked endpoint %s: %s", endpointID, body)
		}
	}
}
