package run

import (
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
