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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/decision"
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

type settlementProbe struct {
	*run.MemoryService
	mu        sync.Mutex
	failKnown int
	costs     []*int64
}

type rejectingLeaseService struct {
	*run.MemoryService
}

// AcquireRequestLease 模拟多实例竞争下租约已被其他执行实例持有。
func (service *rejectingLeaseService) AcquireRequestLease(context.Context, string, string, string, time.Time, time.Duration) (run.Request, error) {
	return run.Request{}, run.ErrLeaseUnavailable
}

// SettleRequest 记录结算输入并模拟已知成本的暂时性存储失败。
func (probe *settlementProbe) SettleRequest(ctx context.Context, tenantID, requestID string, costNanoUSD *int64, now time.Time) (run.Request, error) {
	probe.mu.Lock()
	var snapshot *int64
	if costNanoUSD != nil {
		value := *costNanoUSD
		snapshot = &value
	}
	probe.costs = append(probe.costs, snapshot)
	shouldFail := costNanoUSD != nil && probe.failKnown > 0
	if shouldFail {
		probe.failKnown--
	}
	probe.mu.Unlock()
	if shouldFail {
		return run.Request{}, errors.New("temporary settlement failure")
	}
	return probe.MemoryService.SettleRequest(ctx, tenantID, requestID, costNanoUSD, now)
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
	if metricsResponse.Code != http.StatusOK || !strings.Contains(metricsResponse.Body.String(), "limen_chat_requests_total") || !strings.Contains(metricsResponse.Body.String(), "limen_chat_request_duration_seconds") {
		t.Fatalf("metrics = %d %s", metricsResponse.Code, metricsResponse.Body.String())
	}
}

func TestMetricsExposeStableMetadataWithoutChat(t *testing.T) {
	handler := New("secret", nil)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, want := range []string{
		"# HELP limen_chat_requests_total",
		"# TYPE limen_chat_request_duration_seconds histogram",
		"# TYPE limen_chat_ttfb_seconds histogram",
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("metrics missing %q: %s", want, response.Body.String())
		}
	}
}

func TestMetricsUseTrustedModelAndStableErrorLabels(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "known-model", Targets: []gateway.Target{{ID: "primary", Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("secret", newTestRouter(nil, nil, registry))
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"private-model-input","messages":[{"role":"user","content":"hello"}]}`))
	chat.Header.Set("Authorization", "Bearer secret")
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chat)
	if strings.Contains(chatResponse.Body.String(), "private-model-input") {
		t.Fatalf("error response echoes rejected model: %s", chatResponse.Body.String())
	}

	metrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metrics.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, metrics)
	output := response.Body.String()
	if strings.Contains(output, "private-model-input") {
		t.Fatalf("metrics contain rejected model: %s", output)
	}
	for _, field := range []string{`model="unsupported"`, `status="4xx"`, `reason="unsupported_model"`} {
		if !strings.Contains(output, field) {
			t.Fatalf("metrics missing %s: %s", field, output)
		}
	}
}

func TestAttemptMetricsCountOnlyRealProviderCalls(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{
		{ID: "primary", Provider: "openai", UpstreamModel: "gpt-primary", QualityTier: 2, Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}},
		{ID: "backup", Provider: "anthropic", UpstreamModel: "claude-backup", QualityTier: 1, Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	openAI := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("busy"))}, nil
	})
	anthropic := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`))}, nil
	})
	router := gateway.NewRouter(map[string]provider.Provider{"openai": openAI, "anthropic": anthropic}, registry, gateway.Policy{
		RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Hour,
	})
	handler := New("secret", router)
	for range 2 {
		chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
		chat.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, chat)
		if response.Code != http.StatusOK {
			t.Fatalf("chat status=%d body=%s", response.Code, response.Body.String())
		}
	}
	metrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metrics.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, metrics)
	output := response.Body.String()
	for _, target := range []string{`target="` + catalog.OpaqueTargetID("primary") + `"`, `target="` + catalog.OpaqueTargetID("backup") + `"`} {
		if !strings.Contains(output, target) {
			t.Fatalf("metrics missing %s: %s", target, output)
		}
	}
	for _, result := range []string{`result="retryable_transient"`, `result="success"`} {
		if !strings.Contains(output, result) {
			t.Fatalf("metrics missing %s: %s", result, output)
		}
	}
	if strings.Contains(output, `result="circuit_open"`) {
		t.Fatalf("skipped route step counted as provider attempt: %s", output)
	}
}

func TestChatPassesAuthenticatedTenantToProvider(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test", Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var tenantID string
	upstream := testProviderFunc(func(_ context.Context, request provider.ChatRequest) (provider.Response, error) {
		tenantID = request.TenantID
		return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`))}, nil
	})
	handler := NewWithHealthAndRunsForTenantScopes("secret", newTestRouter(upstream, nil, registry), nil, "tenant-b", []auth.Scope{auth.ScopeInference}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || tenantID != "tenant-b" {
		t.Fatalf("status=%d tenant=%q body=%s", response.Code, tenantID, response.Body.String())
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
	if strings.Contains(response.Body.String(), "tenant_id") {
		t.Fatalf("run response exposes tenant internals: %s", response.Body.String())
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
	if chatResponse.Code != http.StatusOK || chatResponse.Header().Get("X-Limen-Request-ID") == "" || chatResponse.Header().Get("X-Limen-Decision-ID") == "" || chatResponse.Header().Get("X-Limen-Plan-Hash") == "" || chatResponse.Header().Get("X-Limen-Settlement-Status") != "complete" {
		t.Fatalf("chat = %d headers=%v body=%s", chatResponse.Code, chatResponse.Header(), chatResponse.Body.String())
	}
	decisionRequest := httptest.NewRequest(http.MethodGet, "/v1/limen/decisions/"+chatResponse.Header().Get("X-Limen-Decision-ID"), nil)
	decisionRequest.Header.Set("Authorization", "Bearer limen-secret")
	decisionResponse := httptest.NewRecorder()
	handler.ServeHTTP(decisionResponse, decisionRequest)
	var decisionRecord struct {
		Input struct {
			Run decision.RunSnapshot `json:"run"`
		} `json:"input"`
	}
	if err := json.Unmarshal(decisionResponse.Body.Bytes(), &decisionRecord); err != nil {
		t.Fatal(err)
	}
	if decisionResponse.Code != http.StatusOK || !decisionRecord.Input.Run.Governed || decisionRecord.Input.Run.SoftBudgetNanoUSD != 1_000_000_000 {
		t.Fatalf("decision = %d %+v body=%s", decisionResponse.Code, decisionRecord.Input.Run, decisionResponse.Body.String())
	}
	requestID := chatResponse.Header().Get("X-Limen-Request-ID")
	request, err := runs.GetRequest(context.Background(), runTenantID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if request.State != run.RequestSettled || !request.LedgerRecorded || request.LeaseOwner != "" || !request.LeaseExpiresAt.IsZero() {
		t.Fatalf("request = %+v", request)
	}
	lookup := httptest.NewRequest(http.MethodGet, "/v1/limen/runs/"+created.ID+"/requests/"+requestID, nil)
	lookup.Header.Set("Authorization", "Bearer limen-secret")
	lookupResponse := httptest.NewRecorder()
	handler.ServeHTTP(lookupResponse, lookup)
	if lookupResponse.Code != http.StatusOK {
		t.Fatalf("request lookup = %d %s", lookupResponse.Code, lookupResponse.Body.String())
	}
	for _, secret := range []string{"tenant_id", "request-1", "request_hash", "lease_owner"} {
		if strings.Contains(lookupResponse.Body.String(), secret) {
			t.Fatalf("request lookup exposes %s: %s", secret, lookupResponse.Body.String())
		}
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

func TestGovernedChatIdempotencyProvidesRetryContract(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test", Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var calls atomic.Int32
	upstream := testProviderFunc(func(ctx context.Context, _ provider.ChatRequest) (provider.Response, error) {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			return provider.Response{StatusCode: http.StatusOK, ContentType: "application/json", Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`)), Usage: staticUsage{InputTokens: 1, OutputTokens: 1, Complete: true}}, nil
		case <-ctx.Done():
			return provider.Response{}, ctx.Err()
		}
	})
	runs := run.NewMemoryService(nil)
	handler := NewWithRuns("limen-secret", newTestRouter(upstream, nil, registry), runs)
	create := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1}`))
	create.Header.Set("Authorization", "Bearer limen-secret")
	create.Header.Set("Idempotency-Key", "idempotency-run")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, create)
	var created run.Run
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", createdResponse.Code, createdResponse.Body.String())
	}

	newChat := func(body string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer limen-secret")
		request.Header.Set("X-Limen-Run-ID", created.ID)
		request.Header.Set("Idempotency-Key", "same-request")
		return request
	}
	body := `{"model":"model","messages":[{"role":"user","content":"hello"}]}`
	firstResponse := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		handler.ServeHTTP(firstResponse, newChat(body))
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}

	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, newChat(body))
	requestID := duplicateResponse.Header().Get("X-Limen-Request-ID")
	if duplicateResponse.Code != http.StatusConflict || duplicateResponse.Header().Get("Retry-After") != "1" || requestID == "" || !strings.Contains(duplicateResponse.Body.String(), "request_in_progress") {
		t.Fatalf("in-progress duplicate = %d headers=%v body=%s", duplicateResponse.Code, duplicateResponse.Header(), duplicateResponse.Body.String())
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	if firstResponse.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("first response = %d calls=%d body=%s", firstResponse.Code, calls.Load(), firstResponse.Body.String())
	}

	processedResponse := httptest.NewRecorder()
	handler.ServeHTTP(processedResponse, newChat(body))
	var processedEnvelope errorEnvelope
	if err := json.Unmarshal(processedResponse.Body.Bytes(), &processedEnvelope); err != nil {
		t.Fatal(err)
	}
	if processedResponse.Code != http.StatusConflict || processedResponse.Header().Get("X-Limen-Request-ID") != requestID || processedResponse.Header().Get("Retry-After") != "" || processedEnvelope.Error.Code != "request_already_processed" || processedEnvelope.Error.RequestID != requestID || processedEnvelope.Error.DecisionID == "" || processedEnvelope.Error.SettlementStatus != "complete" {
		t.Fatalf("processed duplicate = %d headers=%v body=%s", processedResponse.Code, processedResponse.Header(), processedResponse.Body.String())
	}

	conflictResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictResponse, newChat(`{"model":"model","messages":[{"role":"user","content":"changed"}]}`))
	if conflictResponse.Code != http.StatusConflict || conflictResponse.Header().Get("X-Limen-Request-ID") != requestID || !strings.Contains(conflictResponse.Body.String(), "idempotency_conflict") {
		t.Fatalf("hash conflict = %d headers=%v body=%s", conflictResponse.Code, conflictResponse.Header(), conflictResponse.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
}

// TestGovernedChatLeaseFailureSettlesZeroCost 验证未调用 Provider 的租约失败不会暂停 Run 账本。
func TestGovernedChatLeaseFailureSettlesZeroCost(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	base := run.NewMemoryService(nil)
	created := run.Run{ID: "run-lease-rejected", State: run.StateActive, MaxParallelism: 1}
	if err := base.CreateRun(context.Background(), "local", created); err != nil {
		t.Fatal(err)
	}
	runs := &rejectingLeaseService{MemoryService: base}
	handler := NewWithRuns("limen-secret", newTestRouter(nil, nil, registry), runs)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	request.Header.Set("X-Limen-Run-ID", created.ID)
	request.Header.Set("Idempotency-Key", "lease-rejected")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "request_in_progress") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	requestID := response.Header().Get("X-Limen-Request-ID")
	storedRequest, err := base.GetRequest(context.Background(), "local", requestID)
	if err != nil || storedRequest.State != run.RequestSettled || storedRequest.SettlementStatus != "complete" || !storedRequest.LedgerRecorded {
		t.Fatalf("request = %+v err=%v", storedRequest, err)
	}
	storedRun, err := base.GetRun(context.Background(), "local", created.ID)
	if err != nil || storedRun.State != run.StateActive || storedRun.InFlight != 0 || storedRun.SettledCostNanoUSD != 0 {
		t.Fatalf("run = %+v err=%v", storedRun, err)
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
		{ID: "primary", Provider: "openai", UpstreamModel: "gpt-primary", QualityTier: 2, Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}},
		{ID: "backup", Provider: "anthropic", UpstreamModel: "claude-backup", QualityTier: 1, Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	openAI := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("busy")), ProviderRequestID: "req-primary"}, nil
	})
	anthropic := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`)), ProviderRequestID: "req-backup", Usage: staticUsage{InputTokens: 1, OutputTokens: 1, Complete: true}}, nil
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
	if len(attempts) != 2 || attempts[0].State != run.AttemptTransientFailed || attempts[0].ProviderRequestID != "req-primary" || attempts[1].State != run.AttemptSucceeded || attempts[1].ProviderRequestID != "req-backup" {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRunCancellationStopsInFlightChat(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test", Pricing: &cost.Pricing{InputPerMillionNanoUSD: 1, OutputPerMillionNanoUSD: 1}}}}})
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
	if response.Code != http.StatusOK || plan.PlanHash == "" || response.Header().Get("X-Limen-Plan-Hash") != plan.PlanHash || len(plan.Targets) != 1 || plan.Targets[0].ModelID != "smart" || called {
		t.Fatalf("status=%d plan=%+v called=%v", response.Code, plan, called)
	}
}

func TestChatErrorExposesPlanHashForNoEligibleTarget(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{
		ID:      "basic",
		Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-basic", QualityTier: 1, DataClasses: []string{"public"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(nil, nil, registry))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"limen":{"minimum_quality_tier":5}}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("X-Limen-Plan-Hash") == "" || !strings.Contains(response.Body.String(), "no_eligible_target") {
		t.Fatalf("status=%d plan_hash=%q body=%s", response.Code, response.Header().Get("X-Limen-Plan-Hash"), response.Body.String())
	}
}

func TestDryRunErrorExposesPlanHashForNoEligibleTarget(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{
		ID:      "basic",
		Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-basic", QualityTier: 1, DataClasses: []string{"public"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := New("limen-secret", newTestRouter(nil, nil, registry))
	request := httptest.NewRequest(http.MethodPost, "/v1/limen/decisions/dry-run", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"limen":{"minimum_quality_tier":5}}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("X-Limen-Plan-Hash") == "" || !strings.Contains(response.Body.String(), "no_eligible_target") {
		t.Fatalf("status=%d plan_hash=%q body=%s", response.Code, response.Header().Get("X-Limen-Plan-Hash"), response.Body.String())
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
	if dryResponse.Code != http.StatusOK || decisionID == "" || dryResponse.Header().Get("X-Limen-Plan-Hash") == "" {
		t.Fatalf("dry run = %d decision=%q body=%s", dryResponse.Code, decisionID, dryResponse.Body.String())
	}
	if strings.Contains(dryResponse.Body.String(), "gpt-test") {
		t.Fatalf("dry run leaked upstream model: %s", dryResponse.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/limen/decisions/"+decisionID, nil)
	get.Header.Set("Authorization", "Bearer limen-secret")
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), decisionID) || strings.Contains(getResponse.Body.String(), "gpt-test") {
		t.Fatalf("get decision = %d body=%s", getResponse.Code, getResponse.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost, "/v1/limen/decisions/"+decisionID+"/replay", nil)
	replay.Header.Set("Authorization", "Bearer limen-secret")
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK || !strings.Contains(replayResponse.Body.String(), `"match":true`) || strings.Contains(replayResponse.Body.String(), "gpt-test") {
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

func TestGovernedChatKeepsKnownSettlementForDeferredRetry(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
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
	runs := &settlementProbe{MemoryService: run.NewMemoryService(nil), failKnown: 3}
	if err := runs.CreateRun(context.Background(), "local", run.Run{ID: "run-settlement", State: run.StateActive, MaxParallelism: 1}); err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenant("secret", newTestRouter(provider.NewOpenAI(providerServer.Client(), providerServer.URL, "provider-secret"), nil, registry), nil, "local", runs)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Limen-Run-ID", "run-settlement")
	request.Header.Set("Idempotency-Key", "request-settlement")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	runs.mu.Lock()
	costs := append([]*int64(nil), runs.costs...)
	runs.mu.Unlock()
	if len(costs) < 4 {
		t.Fatalf("settlement calls = %d, want deferred retry; response=%d %s headers=%v", len(costs), response.Code, response.Body.String(), response.Header())
	}
	for index, value := range costs {
		if value == nil {
			t.Fatalf("settlement call %d used unknown cost after known response", index)
		}
	}
}

func TestGovernedChatSettlesZeroWhenNoProviderAttempt(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "known", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	runs := run.NewMemoryService(nil)
	if err := runs.CreateRun(context.Background(), "local", run.Run{ID: "run-no-attempt", State: run.StateActive, MaxParallelism: 1}); err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenant("secret", newTestRouter(nil, nil, registry), nil, "local", runs)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Limen-Run-ID", "run-no-attempt")
	request.Header.Set("Idempotency-Key", "request-no-attempt")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unsupported_model") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	item, err := runs.GetRequest(context.Background(), "local", response.Header().Get("X-Limen-Request-ID"))
	if err != nil {
		t.Fatal(err)
	}
	if item.State != run.RequestSettled || item.SettlementStatus != "complete" || !item.LedgerRecorded {
		t.Fatalf("request settlement = %+v", item)
	}
	runItem, err := runs.GetRun(context.Background(), "local", "run-no-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if runItem.State != run.StateActive || runItem.InFlight != 0 || runItem.SettledCostNanoUSD != 0 {
		t.Fatalf("run state = %+v", runItem)
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

func TestParseChatRequestReportsUnknownFieldExplicitly(t *testing.T) {
	_, err := parseChatRequestEnvelope([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"unknown":true}`))
	var unsupported *unsupportedFieldError
	if !errors.As(err, &unsupported) || unsupported.Field != "unknown" {
		t.Fatalf("err=%v", err)
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

func TestParseChatRequestExtractsStreamUsageOption(t *testing.T) {
	envelope, err := parseChatRequestEnvelope([]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Request.StreamIncludeUsage == nil || *envelope.Request.StreamIncludeUsage {
		t.Fatalf("stream usage option = %v", envelope.Request.StreamIncludeUsage)
	}
}

func TestParseChatRequestAcceptsModernCompletionTokenLimit(t *testing.T) {
	request, err := parseChatRequest([]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":32}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.MaxTokens != 0 || request.MaxCompletionTokens == nil || *request.MaxCompletionTokens != 32 {
		t.Fatalf("request = %+v", request)
	}
}

func TestParseChatRequestAcceptsDeveloperMessage(t *testing.T) {
	request, err := parseChatRequest([]byte(`{"model":"o1-test","messages":[{"role":"developer","content":"be precise"},{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 2 || request.Messages[0].Role != "developer" {
		t.Fatalf("messages = %+v", request.Messages)
	}
}

func TestParseChatRequestRejectsTwoCompletionTokenLimits(t *testing.T) {
	_, err := parseChatRequest([]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"max_completion_tokens":32}`))
	if err == nil || err.Error() != "max_tokens and max_completion_tokens are mutually exclusive" {
		t.Fatalf("err = %v", err)
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
