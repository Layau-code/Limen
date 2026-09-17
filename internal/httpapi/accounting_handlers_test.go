package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/run"
)

func TestResolveAccountingRequiresAdminAndIsIdempotent(t *testing.T) {
	runs := run.NewMemoryService(nil)
	if err := runs.CreateRun(context.Background(), "tenant-a", run.Run{ID: "run-a", State: run.StateActive, MaxParallelism: 1, Strategy: "balanced", ConfigVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	hash, _ := run.HashRequest("tenant-a", "/v1/chat/completions", "request-key", []byte(`{"model":"auto"}`), nil)
	request, err := runs.AdmitRequest(context.Background(), "tenant-a", "run-a", run.AdmissionInput{Request: run.Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "request-key", RequestHash: hash}, Now: time.Unix(100, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.BeginSettlement(context.Background(), "tenant-a", request.ID, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.SettleRequest(context.Background(), "tenant-a", request.ID, nil, time.Unix(102, 0)); !errors.Is(err, run.ErrAccountingSuspended) {
		t.Fatalf("suspend error = %v", err)
	}
	handler := NewWithHealthAndRunsForTenantScopes("secret", nil, nil, "tenant-a", []auth.Scope{auth.ScopeAdmin}, runs)
	requestPath := "/v1/limen/runs/run-a/requests/" + request.ID + "/accounting"
	nonAdmin := NewWithHealthAndRunsForTenantScopes("secret", nil, nil, "tenant-a", []auth.Scope{auth.ScopeRunsWrite}, runs)
	deniedRequest := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(`{"mode":"accept_unknown"}`))
	deniedRequest.Header.Set("Authorization", "Bearer secret")
	deniedRequest.Header.Set("Idempotency-Key", "denied-key")
	deniedResponse := httptest.NewRecorder()
	nonAdmin.ServeHTTP(deniedResponse, deniedRequest)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d body=%s", deniedResponse.Code, deniedResponse.Body.String())
	}
	resolve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(`{"mode":"cost","cost_usd":"0.000000042"}`))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Idempotency-Key", "resolve-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	first := resolve()
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var resolved run.Request
	if err := json.Unmarshal(first.Body.Bytes(), &resolved); err != nil || resolved.SettlementStatus != "complete" || !resolved.LedgerRecorded {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	second := resolve()
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() {
		t.Fatalf("repeat status=%d body=%s first=%s", second.Code, second.Body.String(), first.Body.String())
	}
	item, _ := runs.GetRun(context.Background(), "tenant-a", "run-a")
	if item.SettledCostNanoUSD != 42 || item.State != run.StateActive {
		t.Fatalf("run=%+v", item)
	}
}

func TestResolveAccountingAcceptsUnknownWithoutLedger(t *testing.T) {
	runs := run.NewMemoryService(nil)
	if err := runs.CreateRun(context.Background(), "tenant-a", run.Run{ID: "run-a", State: run.StateActive, MaxParallelism: 1, Strategy: "balanced", ConfigVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	hash, _ := run.HashRequest("tenant-a", "/v1/chat/completions", "request-key", []byte(`{"model":"auto"}`), nil)
	request, err := runs.AdmitRequest(context.Background(), "tenant-a", "run-a", run.AdmissionInput{Request: run.Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "request-key", RequestHash: hash}, Now: time.Unix(100, 0)})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = runs.BeginSettlement(context.Background(), "tenant-a", request.ID, time.Unix(101, 0))
	_, _ = runs.SettleRequest(context.Background(), "tenant-a", request.ID, nil, time.Unix(102, 0))
	handler := NewWithHealthAndRunsForTenantScopes("secret", nil, nil, "tenant-a", []auth.Scope{auth.ScopeAdmin}, runs)
	wrongRun := httptest.NewRequest(http.MethodPost, "/v1/limen/runs/run-other/requests/"+request.ID+"/accounting", strings.NewReader(`{"mode":"accept_unknown"}`))
	wrongRun.Header.Set("Authorization", "Bearer secret")
	wrongRun.Header.Set("Idempotency-Key", "wrong-run-key")
	wrongRunResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongRunResponse, wrongRun)
	if wrongRunResponse.Code != http.StatusNotFound {
		t.Fatalf("wrong run status=%d body=%s", wrongRunResponse.Code, wrongRunResponse.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/limen/runs/run-a/requests/"+request.ID+"/accounting", strings.NewReader(`{"mode":"accept_unknown"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Idempotency-Key", "resolve-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"settlement_status":"unknown"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
