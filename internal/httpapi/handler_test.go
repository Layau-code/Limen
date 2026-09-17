package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
	"github.com/huz/limen/internal/run"
)

type testProviderFunc func(context.Context, provider.ChatRequest) (provider.Response, error)

func (fn testProviderFunc) Chat(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
	return fn(ctx, request)
}

type staticUsage provider.Usage

func (usage staticUsage) Snapshot() provider.Usage {
	return provider.Usage(usage)
}

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

func TestMetricsRequiresAdminScope(t *testing.T) {
	handler := NewWithHealthAndRunsForTenantScopes("secret", nil, nil, "tenant-a", []auth.Scope{auth.ScopeInference}, nil)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	handler = New("secret", nil)
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing","messages":[{"role":"user","content":"hello"}]}`))
	chat.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(httptest.NewRecorder(), chat)
	metrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metrics.Header.Set("Authorization", "Bearer secret")
	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, metrics)
	if metricsResponse.Code != http.StatusOK || !strings.Contains(metricsResponse.Body.String(), "limen_chat_requests_total") {
		t.Fatalf("metrics = %d %s", metricsResponse.Code, metricsResponse.Body.String())
	}
}

func TestScopesRejectOperationWithoutPermission(t *testing.T) {
	handler := NewWithHealthAndRunsForTenantScopes("limen-secret", nil, nil, "tenant-1", []auth.Scope{auth.ScopeInference}, run.NewMemoryService(nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	request.Header.Set("Idempotency-Key", "run-create")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "insufficient_scope") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[]}`))
	chat.Header.Set("Authorization", "Bearer limen-secret")
	chat.Header.Set("X-Limen-Run-ID", "run-1")
	chat.Header.Set("Idempotency-Key", "request-1")
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chat)
	if chatResponse.Code != http.StatusForbidden || !strings.Contains(chatResponse.Body.String(), "insufficient_scope") {
		t.Fatalf("chat status=%d body=%s", chatResponse.Code, chatResponse.Body.String())
	}
}

func TestRetrySettlementRetriesTemporaryStoreError(t *testing.T) {
	attempts := 0
	err := retrySettlementWithDelays(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary store error")
		}
		return nil
	}, []time.Duration{0, 0, 0})
	if err != nil || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRetrySettlementStopsAccountingUnknown(t *testing.T) {
	attempts := 0
	err := retrySettlementWithDelays(context.Background(), func() error {
		attempts++
		return run.ErrAccountingSuspended
	}, []time.Duration{0, 0, 0})
	if !errors.Is(err, run.ErrAccountingSuspended) || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRunControlLifecycleAndIdempotency(t *testing.T) {
	handler := NewWithRuns("limen-secret", nil, run.NewMemoryService(nil))
	body := `{"soft_budget_usd":"1.000000000","max_parallelism":2,"strategy":"balanced"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer limen-secret")
	request.Header.Set("Idempotency-Key", "create-run-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var created run.Run
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated || created.ID == "" || created.State != run.StateActive {
		t.Fatalf("create = %d %+v", response.Code, created)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer limen-secret")
	request.Header.Set("Idempotency-Key", "create-run-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var repeated run.Run
	if err := json.Unmarshal(response.Body.Bytes(), &repeated); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated || repeated.ID != created.ID {
		t.Fatalf("repeat = %d %+v", response.Code, repeated)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/limen/runs/"+created.ID+"/complete", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	request.Header.Set("Idempotency-Key", "complete-run-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"completed"`) {
		t.Fatalf("complete = %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"2","max_parallelism":2,"strategy":"balanced"}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	request.Header.Set("Idempotency-Key", "create-run-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict = %d %s", response.Code, response.Body.String())
	}
}

func TestGovernedChatAdmitsAndSettlesRunRequest(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test", Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1_000_000, OutputPerMillionNanoUSD: 1_000_000}}}}})
	if err != nil {
		t.Fatal(err)
	}
	upstream := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusOK, ContentType: "application/json", Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`)), Usage: staticUsage{InputTokens: 3, OutputTokens: 2, Complete: true}}, nil
	})
	runs := run.NewMemoryService(nil)
	handler := NewWithRuns("limen-secret", newTestRouter(upstream, nil, registry), runs)
	create := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1}`))
	create.Header.Set("Authorization", "Bearer limen-secret")
	create.Header.Set("Idempotency-Key", "run-create")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, create)
	var created run.Run
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	chat.Header.Set("Authorization", "Bearer limen-secret")
	chat.Header.Set("X-Limen-Run-ID", created.ID)
	chat.Header.Set("Idempotency-Key", "request-1")
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chat)
	if chatResponse.Code != http.StatusOK || chatResponse.Header().Get("X-Limen-Request-ID") == "" || chatResponse.Header().Get("X-Limen-Decision-ID") == "" || chatResponse.Header().Get("X-Limen-Settlement-Status") != "complete" {
		t.Fatalf("chat = %d headers=%v body=%s", chatResponse.Code, chatResponse.Header(), chatResponse.Body.String())
	}
	requestID := chatResponse.Header().Get("X-Limen-Request-ID")
	request, err := runs.GetRequest(context.Background(), runTenantID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if request.State != run.RequestSettled || !request.LedgerRecorded || request.LeaseOwner != "" || !request.LeaseExpiresAt.IsZero() {
		t.Fatalf("request = %+v", request)
	}
	runState, err := runs.GetRun(context.Background(), runTenantID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runState.SettledCostNanoUSD != 5 || runState.InFlight != 0 {
		t.Fatalf("run = %+v", runState)
	}
	duplicate := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	duplicate.Header.Set("Authorization", "Bearer limen-secret")
	duplicate.Header.Set("X-Limen-Run-ID", created.ID)
	duplicate.Header.Set("Idempotency-Key", "request-1")
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusConflict || !strings.Contains(duplicateResponse.Body.String(), "request_already_processed") {
		t.Fatalf("duplicate = %d %s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
}

func TestRunStrategyCannotBeOverriddenByChatRequest(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	runs := run.NewMemoryService(nil)
	handler := NewWithRuns("limen-secret", newTestRouter(nil, nil, registry), runs)
	create := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1,"strategy":"economy"}`))
	create.Header.Set("Authorization", "Bearer limen-secret")
	create.Header.Set("Idempotency-Key", "strategy-run-create")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, create)
	var created run.Run
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}],"limen":{"strategy":"balanced"}}`))
	chat.Header.Set("Authorization", "Bearer limen-secret")
	chat.Header.Set("X-Limen-Run-ID", created.ID)
	chat.Header.Set("Idempotency-Key", "strategy-request")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chat)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "strategy_conflict") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestAttemptStateUsesProviderClassification(t *testing.T) {
	tests := []struct {
		name   string
		report gateway.AttemptReport
		state  run.AttemptState
	}{
		{name: "success", report: gateway.AttemptReport{StatusCode: http.StatusOK}, state: run.AttemptSucceeded},
		{name: "transient", report: gateway.AttemptReport{StatusCode: http.StatusServiceUnavailable, ErrorClass: provider.ErrorClassRetryableTransient}, state: run.AttemptTransientFailed},
		{name: "authentication", report: gateway.AttemptReport{StatusCode: http.StatusServiceUnavailable, ErrorClass: provider.ErrorClassAuthentication}, state: run.AttemptDeterministicFail},
		{name: "cancelled", report: gateway.AttemptReport{Outcome: "canceled"}, state: run.AttemptCancelled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := attemptState(test.report); got != test.state {
				t.Fatalf("state = %q, want %q", got, test.state)
			}
		})
	}
}

func TestGovernedChatPersistsFallbackAttemptsSeparately(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{
		{ID: "primary", Provider: "openai", UpstreamModel: "gpt-primary", QualityTier: 2},
		{ID: "backup", Provider: "anthropic", UpstreamModel: "claude-backup", QualityTier: 1},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	openAI := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("busy"))}, nil
	})
	anthropic := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`)), Usage: staticUsage{InputTokens: 1, OutputTokens: 1, Complete: true}}, nil
	})
	runs := run.NewMemoryService(nil)
	handler := NewWithRuns("limen-secret", newTestRouter(openAI, anthropic, registry), runs)
	create := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1}`))
	create.Header.Set("Authorization", "Bearer limen-secret")
	create.Header.Set("Idempotency-Key", "fallback-run-create")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, create)
	var created run.Run
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	chat.Header.Set("Authorization", "Bearer limen-secret")
	chat.Header.Set("X-Limen-Run-ID", created.ID)
	chat.Header.Set("Idempotency-Key", "fallback-request")
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chat)
	if chatResponse.Code != http.StatusOK {
		t.Fatalf("chat = %d %s", chatResponse.Code, chatResponse.Body.String())
	}
	attempts := runs.AttemptsForRequest(runTenantID, chatResponse.Header().Get("X-Limen-Request-ID"))
	if len(attempts) != 2 || attempts[0].State != run.AttemptTransientFailed || attempts[1].State != run.AttemptSucceeded {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRunCancellationStopsInFlightChat(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	upstream := testProviderFunc(func(ctx context.Context, _ provider.ChatRequest) (provider.Response, error) {
		close(started)
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	})
	runs := run.NewMemoryService(nil)
	router := gateway.NewRouter(map[string]provider.Provider{"openai": upstream}, registry, gateway.Policy{RequestTimeout: 5 * time.Second, AttemptTimeout: 5 * time.Second, FailureThreshold: 3, Cooldown: time.Second})
	handler := NewWithRuns("limen-secret", router, runs)
	create := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1}`))
	create.Header.Set("Authorization", "Bearer limen-secret")
	create.Header.Set("Idempotency-Key", "cancel-run-create")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	var created run.Run
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	chat.Header.Set("Authorization", "Bearer limen-secret")
	chat.Header.Set("X-Limen-Run-ID", created.ID)
	chat.Header.Set("Idempotency-Key", "cancel-request")
	chatResponse := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		handler.ServeHTTP(chatResponse, chat)
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	cancelRequest := httptest.NewRequest(http.MethodPost, "/v1/limen/runs/"+created.ID+"/cancel", strings.NewReader(`{}`))
	cancelRequest.Header.Set("Authorization", "Bearer limen-secret")
	cancelRequest.Header.Set("Idempotency-Key", "cancel-run")
	cancelResponse := httptest.NewRecorder()
	handler.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK {
		t.Fatalf("cancel = %d body=%s", cancelResponse.Code, cancelResponse.Body.String())
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight chat was not cancelled")
	}
	if chatResponse.Code != http.StatusConflict || !strings.Contains(chatResponse.Body.String(), "run_cancelled") {
		t.Fatalf("chat = %d body=%s", chatResponse.Code, chatResponse.Body.String())
	}
}

func TestDryRunReturnsPlanWithoutProviderCall(t *testing.T) {
	called := false
	registry, err := gateway.NewModelRegistry([]gateway.Model{
		{ID: "basic", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-basic", QualityTier: 1, DataClasses: []string{"public"}}}},
		{ID: "smart", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-smart", QualityTier: 4, DataClasses: []string{"public", "internal"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		called = true
		return provider.Response{}, nil
	}), nil, registry)
	handler := New("limen-secret", router)
	request := httptest.NewRequest(http.MethodPost, "/v1/limen/decisions/dry-run", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"limen":{"minimum_quality_tier":3,"data_class":"internal"}}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var plan struct {
		PlanHash string `json:"plan_hash"`
		Targets  []struct {
			ModelID string `json:"model_id"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || plan.PlanHash == "" || len(plan.Targets) != 1 || plan.Targets[0].ModelID != "smart" || called {
		t.Fatalf("status=%d plan=%+v called=%v", response.Code, plan, called)
	}
}

func TestDecisionExplainAndReplay(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(nil, nil, registry))
	dryRun := httptest.NewRequest(http.MethodPost, "/v1/limen/decisions/dry-run", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	dryRun.Header.Set("Authorization", "Bearer limen-secret")
	dryResponse := httptest.NewRecorder()
	handler.ServeHTTP(dryResponse, dryRun)
	decisionID := dryResponse.Header().Get("X-Limen-Decision-ID")
	if dryResponse.Code != http.StatusOK || decisionID == "" {
		t.Fatalf("dry run = %d decision=%q body=%s", dryResponse.Code, decisionID, dryResponse.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/limen/decisions/"+decisionID, nil)
	get.Header.Set("Authorization", "Bearer limen-secret")
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), decisionID) {
		t.Fatalf("get decision = %d body=%s", getResponse.Code, getResponse.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost, "/v1/limen/decisions/"+decisionID+"/replay", nil)
	replay.Header.Set("Authorization", "Bearer limen-secret")
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK || !strings.Contains(replayResponse.Body.String(), `"match":true`) {
		t.Fatalf("replay = %d body=%s", replayResponse.Code, replayResponse.Body.String())
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

func TestChatTrailersReachHTTPClientAfterEOF(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer providerServer.Close()
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{
		Provider:      "openai",
		UpstreamModel: "gpt-test",
		Pricing:       &cost.Pricing{InputPerMillionNanoUSD: 1_000_000, OutputPerMillionNanoUSD: 1_000_000},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	apiServer := httptest.NewServer(New("limen-secret", newTestRouter(provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret"), nil, registry)))
	defer apiServer.Close()
	request, err := http.NewRequest(http.MethodPost, apiServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer limen-secret")
	response, err := apiServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if response.Trailer.Get("X-Limen-Settlement-Status") != "complete" || response.Trailer.Get("X-Limen-Total-Tokens") != "3" {
		t.Fatalf("trailers = %v", response.Trailer)
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

func TestParseChatRequestRejectsUnsupportedFields(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[]}`,
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"unknown":true}`,
	} {
		if _, err := parseChatRequestEnvelope([]byte(body)); err == nil {
			t.Fatalf("request was accepted: %s", body)
		}
	}
}

func TestParseChatRequestExtractsLimenContract(t *testing.T) {
	envelope, err := parseChatRequestEnvelope([]byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"limen":{"required_capabilities":["text"],"minimum_quality_tier":3,"required_context_tokens":1000,"data_class":"internal","strategy":"economy"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Request.Model != "auto" || !envelope.Contract.Active || envelope.Contract.MinimumQualityTier != 3 || envelope.Contract.DataClass != "internal" || envelope.Contract.Strategy != "economy" {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestChatRejectsInvalidLimenContract(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(nil, nil, registry))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}],"limen":{"strategy":"random"}}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "strategy_conflict") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}
