package run

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreResolvesAccountingWithKnownCost(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashRequest("tenant-1", "/v1/chat/completions", "accounting-key", []byte(`{"model":"auto"}`), nil)
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "accounting-key", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSettlement("tenant-1", request.ID, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleRequest("tenant-1", request.ID, nil, time.Unix(102, 0)); !errors.Is(err, ErrAccountingSuspended) {
		t.Fatalf("suspend error = %v", err)
	}
	cost := int64(42)
	resolved, err := store.ResolveAccounting("tenant-1", request.ID, AccountingResolution{CostNanoUSD: &cost}, time.Unix(103, 0))
	if err != nil || resolved.State != RequestSettled || resolved.SettlementStatus != "complete" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	item, _ := store.GetRun("tenant-1", "run-1")
	if item.State != StateActive || item.InFlight != 0 || item.SettledCostNanoUSD != cost || store.LedgerCount() != 1 {
		t.Fatalf("run=%+v ledger=%d", item, store.LedgerCount())
	}
	if _, err := store.ResolveAccounting("tenant-1", request.ID, AccountingResolution{CostNanoUSD: &cost}, time.Unix(104, 0)); !errors.Is(err, ErrRequestAlreadyProcessed) {
		t.Fatalf("repeat error = %v", err)
	}
}

func TestMemoryStoreCanAcceptUnknownAccountingExplicitly(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashRequest("tenant-1", "/v1/chat/completions", "unknown-accounting", []byte(`{"model":"auto"}`), nil)
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "unknown-accounting", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSettlement("tenant-1", request.ID, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleRequest("tenant-1", request.ID, nil, time.Unix(102, 0)); !errors.Is(err, ErrAccountingSuspended) {
		t.Fatalf("suspend error = %v", err)
	}
	resolved, err := store.ResolveAccounting("tenant-1", request.ID, AccountingResolution{AcceptUnknown: true}, time.Unix(103, 0))
	if err != nil || resolved.State != RequestSettled || resolved.SettlementStatus != "unknown" || resolved.LedgerRecorded {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	item, _ := store.GetRun("tenant-1", "run-1")
	if item.State != StateActive || item.InFlight != 0 || item.SettledCostNanoUSD != 0 || store.LedgerCount() != 0 {
		t.Fatalf("run=%+v ledger=%d", item, store.LedgerCount())
	}
}

func TestAccountingResolutionRequiresExactlyOneOutcome(t *testing.T) {
	for _, resolution := range []AccountingResolution{{}, {AcceptUnknown: true, CostNanoUSD: ptrInt64(1)}} {
		if err := resolution.Validate(); !errors.Is(err, ErrInvalidAccountingResolution) {
			t.Fatalf("resolution=%+v err=%v", resolution, err)
		}
	}
}

func TestAccountingResolutionWaitsForOtherUnknownRequests(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(Run{ID: "run-1", TenantID: "tenant-1", State: StateActive, MaxParallelism: 2, Strategy: "balanced"}); err != nil {
		t.Fatal(err)
	}
	requestIDs := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		key := "unknown-" + string(rune('a'+index))
		hash, _ := HashRequest("tenant-1", "/v1/chat/completions", key, []byte(`{"model":"auto"}`), nil)
		request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: key, RequestHash: hash}, time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		requestIDs = append(requestIDs, request.ID)
	}
	for _, requestID := range requestIDs {
		_, _ = store.BeginSettlement("tenant-1", requestID, time.Unix(101, 0))
		_, _ = store.SettleRequest("tenant-1", requestID, nil, time.Unix(102, 0))
	}
	cost := int64(1)
	if _, err := store.ResolveAccounting("tenant-1", requestIDs[0], AccountingResolution{CostNanoUSD: &cost}, time.Unix(103, 0)); err != nil {
		t.Fatal(err)
	}
	item, _ := store.GetRun("tenant-1", "run-1")
	if item.State != StateSuspendedAccounting {
		t.Fatalf("run resumed with unresolved request: %+v", item)
	}
	if _, err := store.ResolveAccounting("tenant-1", requestIDs[1], AccountingResolution{AcceptUnknown: true}, time.Unix(104, 0)); err != nil {
		t.Fatal(err)
	}
	item, _ = store.GetRun("tenant-1", "run-1")
	if item.State != StateActive {
		t.Fatalf("run did not resume after all resolutions: %+v", item)
	}
}

func TestAccountingResolutionDoesNotReopenTerminalRun(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(Run{ID: "run-1", TenantID: "tenant-1", State: StateActive, MaxParallelism: 1, Strategy: "balanced"}); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashRequest("tenant-1", "/v1/chat/completions", "cancelled-accounting", []byte(`{"model":"auto"}`), nil)
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "cancelled-accounting", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSettlement("tenant-1", request.ID, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelRunWithMutation("tenant-1", "run-1", Mutation{Key: "cancel-key", Hash: "hash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleRequest("tenant-1", request.ID, nil, time.Unix(102, 0)); !errors.Is(err, ErrAccountingSuspended) {
		t.Fatalf("suspend error = %v", err)
	}
	cost := int64(7)
	resolved, err := store.ResolveAccounting("tenant-1", request.ID, AccountingResolution{CostNanoUSD: &cost}, time.Unix(103, 0))
	if err != nil || resolved.State != RequestSettled {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	item, _ := store.GetRun("tenant-1", "run-1")
	if item.State != StateCancelled || item.SettledCostNanoUSD != cost {
		t.Fatalf("run reopened or cost missing: %+v", item)
	}
}

func TestSuspendedRunCanBeMarkedCompletingBeforeAccountingResolution(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(Run{ID: "run-1", TenantID: "tenant-1", State: StateActive, MaxParallelism: 1, Strategy: "balanced"}); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashRequest("tenant-1", "/v1/chat/completions", "pending-complete", []byte(`{"model":"auto"}`), nil)
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "pending-complete", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.BeginSettlement("tenant-1", request.ID, time.Unix(101, 0))
	_, _ = store.SettleRequest("tenant-1", request.ID, nil, time.Unix(102, 0))
	if _, err := store.CompleteRunWithMutation("tenant-1", "run-1", Mutation{Key: "complete-key", Hash: "hash"}); err != nil {
		t.Fatal(err)
	}
	item, _ := store.GetRun("tenant-1", "run-1")
	if item.State != StateSuspendedAccounting || !item.CompleteRequested {
		t.Fatalf("suspended run changed unexpectedly: %+v", item)
	}
	if _, err := store.ResolveAccounting("tenant-1", request.ID, AccountingResolution{AcceptUnknown: true}, time.Unix(103, 0)); err != nil {
		t.Fatal(err)
	}
	item, _ = store.GetRun("tenant-1", "run-1")
	if item.State != StateCompleted {
		t.Fatalf("resolved run state = %s", item.State)
	}
}

func ptrInt64(value int64) *int64 { return &value }
