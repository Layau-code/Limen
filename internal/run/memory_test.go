package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreAdmitsIdempotentlyUnderConcurrency(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashRequest("tenant-1", "/v1/chat/completions", "same-key", []byte(`{"model":"auto"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	created := 0
	var firstErr error
	var wg sync.WaitGroup
	for index := 0; index < 100; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "same-key", RequestHash: hash}, time.Unix(100, 0))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case !errors.Is(err, ErrRequestInProgress) && !errors.Is(err, ErrRequestAlreadyProcessed):
				firstErr = err
			}
		}()
	}
	wg.Wait()
	if firstErr != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, firstErr)
	}
}

func TestMemoryStoreSettlementIsIdempotent(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashRequest("tenant-1", "/v1/chat/completions", "key-1", []byte(`{"model":"auto"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "key-1", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSettlement("tenant-1", request.ID, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	cost := int64(20)
	settled, err := store.SettleRequest("tenant-1", request.ID, &cost, time.Unix(102, 0))
	if err != nil || settled.State != RequestSettled {
		t.Fatalf("settled=%+v err=%v", settled, err)
	}
	repeated, err := store.SettleRequest("tenant-1", request.ID, &cost, time.Unix(103, 0))
	if err != nil || repeated.State != RequestSettled || store.LedgerCount() != 1 {
		t.Fatalf("repeated=%+v err=%v ledger=%d", repeated, err, store.LedgerCount())
	}
	run, ok := store.GetRun("tenant-1", "run-1")
	if !ok || run.InFlight != 0 || run.SettledCostNanoUSD != 20 {
		t.Fatalf("run=%+v found=%v", run, ok)
	}
}

func TestMemoryStoreRejectsConflictingIdempotency(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	firstHash, _ := HashRequest("tenant-1", "/v1/chat/completions", "key-1", []byte(`{"model":"one"}`), nil)
	secondHash, _ := HashRequest("tenant-1", "/v1/chat/completions", "key-1", []byte(`{"model":"two"}`), nil)
	if _, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "key-1", RequestHash: firstHash}, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "key-1", RequestHash: secondHash}, time.Unix(100, 0)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error = %v", err)
	}
}

// TestMemoryStoreBindsDecisionID 验证决策标识只允许绑定一次并可供幂等查询使用。
func TestMemoryStoreBindsDecisionID(t *testing.T) {
	store := NewMemoryService(nil)
	if err := store.CreateRun(context.Background(), "tenant-1", testRun()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashRequest("tenant-1", "/v1/chat/completions", "decision-key", []byte(`{"model":"auto"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.AdmitRequest(context.Background(), "tenant-1", "run-1", AdmissionInput{Request: Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "decision-key", RequestHash: hash}, Now: time.Unix(100, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRequestDecisionID(context.Background(), "tenant-1", request.ID, "decision_1"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRequestDecisionID(context.Background(), "tenant-1", request.ID, "decision_1"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRequestDecisionID(context.Background(), "tenant-1", request.ID, "decision_2"); err == nil {
		t.Fatal("expected decision binding conflict")
	}
	bound, err := store.GetRequest(context.Background(), "tenant-1", request.ID)
	if err != nil || bound.DecisionID != "decision_1" {
		t.Fatalf("bound request = %+v err=%v", bound, err)
	}
}

func TestMemoryRequestLeaseRenewsAndRecovers(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	hash, err := HashRequest("tenant-1", "/v1/chat/completions", "lease-key", []byte(`{"model":"auto"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "lease-key", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireRequestLease("tenant-1", request.ID, "worker-a", time.Unix(100, 0), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireRequestLease("tenant-1", request.ID, "worker-b", time.Unix(110, 0), 30*time.Second); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatalf("second worker error = %v", err)
	}
	if _, err := store.RenewRequestLease("tenant-1", request.ID, "worker-a", time.Unix(120, 0), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if recovered := store.RecoverExpiredRequests("tenant-1", time.Unix(151, 0), 10); len(recovered) != 1 || recovered[0].State != RequestAbandoned {
		t.Fatalf("recovered = %+v", recovered)
	}
	run, ok := store.GetRun("tenant-1", "run-1")
	if !ok || run.State != StateSuspendedAccounting || run.InFlight != 0 {
		t.Fatalf("run after recovery = %+v found=%v", run, ok)
	}
}

func TestSettlementRecoveryProcessesKnownCostOnce(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashRequest("tenant-1", "/v1/chat/completions", "settlement-key", []byte(`{"model":"auto"}`), nil)
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "settlement-key", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSettlement("tenant-1", request.ID, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	cost := int64(42)
	if err := store.QueueSettlement(context.Background(), "tenant-1", request.ID, &cost, time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	processed, err := ProcessSettlementJobs(context.Background(), NewMemoryService(store), "tenant-1", "worker-a", time.Unix(102, 0), 10)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	settled, ok := store.GetRequest("tenant-1", request.ID)
	if !ok || settled.State != RequestSettled || store.LedgerCount() != 1 {
		t.Fatalf("request=%+v found=%v ledger=%d", settled, ok, store.LedgerCount())
	}
	if jobs, err := store.ClaimSettlementJobs(context.Background(), "tenant-1", "worker-b", time.Unix(103, 0), time.Second, 10); err != nil || len(jobs) != 0 {
		t.Fatalf("remaining jobs=%+v err=%v", jobs, err)
	}
}

func TestSettlementRecoveryMarksUnknownCostSafely(t *testing.T) {
	store := NewMemoryStore()
	if err := store.CreateRun(testRun()); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashRequest("tenant-1", "/v1/chat/completions", "unknown-settlement", []byte(`{"model":"auto"}`), nil)
	request, err := store.AdmitRequest("tenant-1", "run-1", Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "unknown-settlement", RequestHash: hash}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSettlement("tenant-1", request.ID, time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.QueueSettlement(context.Background(), "tenant-1", request.ID, nil, time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	processed, err := ProcessSettlementJobs(context.Background(), NewMemoryService(store), "tenant-1", "worker-a", time.Unix(102, 0), 10)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	runItem, _ := store.GetRun("tenant-1", "run-1")
	if runItem.State != StateSuspendedAccounting || runItem.InFlight != 0 || store.LedgerCount() != 0 {
		t.Fatalf("run=%+v ledger=%d", runItem, store.LedgerCount())
	}
}
