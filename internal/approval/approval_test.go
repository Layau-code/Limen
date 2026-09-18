package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreRequiresDistinctApproverAndConsumesOnce(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	created, err := store.Create(context.Background(), "tenant-a", "v1", "publish-1", "request-hash", "actor-a", Mutation{Key: "create-1", Hash: "create-hash"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := store.Approve(context.Background(), "tenant-a", "v1", created.ID, "actor-a", Mutation{Key: "approve-1", Hash: "approve-hash"}); !errors.Is(err, ErrActorNotDistinct) {
		t.Fatalf("self approval error = %v, want %v", err, ErrActorNotDistinct)
	}
	approved, err := store.Approve(context.Background(), "tenant-a", "v1", created.ID, "actor-b", Mutation{Key: "approve-1", Hash: "approve-hash"})
	if err != nil || approved.State != StateApproved || approved.ApprovedBy != "actor-b" {
		t.Fatalf("Approve() = %#v, %v", approved, err)
	}
	consumed, err := store.Consume(context.Background(), Binding{TenantID: "tenant-a", ConfigVersion: "v1", ApprovalID: created.ID, PublishIdempotencyKey: "publish-1", RequestHash: "request-hash", Publisher: "actor-c"})
	if err != nil || consumed.State != StateConsumed {
		t.Fatalf("Consume() = %#v, %v", consumed, err)
	}
	repeated, err := store.Consume(context.Background(), Binding{TenantID: "tenant-a", ConfigVersion: "v1", ApprovalID: created.ID, PublishIdempotencyKey: "publish-1", RequestHash: "request-hash", Publisher: "actor-c"})
	if err != nil || repeated.State != StateConsumed {
		t.Fatalf("repeated Consume() = %#v, %v", repeated, err)
	}
}

func TestMemoryStoreBindsApprovalAndRejectsConflictingIdempotency(t *testing.T) {
	store := newMemoryStore(time.Now)
	first, err := store.Create(context.Background(), "tenant-a", "v1", "publish-1", "hash-1", "actor-a", Mutation{Key: "create-1", Hash: "create-hash"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	repeated, err := store.Create(context.Background(), "tenant-a", "v1", "publish-1", "hash-1", "actor-a", Mutation{Key: "create-1", Hash: "create-hash"})
	if err != nil || repeated.ID != first.ID {
		t.Fatalf("repeated Create() = %#v, %v", repeated, err)
	}
	if _, err := store.Create(context.Background(), "tenant-a", "v1", "publish-1", "hash-2", "actor-a", Mutation{Key: "create-2", Hash: "create-hash-2"}); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("binding conflict = %v, want %v", err, ErrBindingConflict)
	}
	if _, err := store.Create(context.Background(), "tenant-a", "v1", "publish-2", "hash-2", "actor-a", Mutation{Key: "create-1", Hash: "different"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v, want %v", err, ErrIdempotencyConflict)
	}
}

func TestMemoryStoreExpiresPendingApproval(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	created, err := store.Create(context.Background(), "tenant-a", "v1", "publish-1", "hash", "actor-a", Mutation{Key: "create", Hash: "hash"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	now = created.ExpiresAt.Add(time.Nanosecond)
	record, err := store.Get(context.Background(), "tenant-a", "v1", created.ID)
	if err != nil || record.State != StateExpired {
		t.Fatalf("Get() = %#v, %v", record, err)
	}
	if _, err := store.Approve(context.Background(), "tenant-a", "v1", created.ID, "actor-b", Mutation{Key: "approve", Hash: "hash"}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired approval error = %v, want %v", err, ErrExpired)
	}
}

func TestMemoryStoreConcurrentApprovalIsIdempotent(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), "tenant-a", "v1", "publish", "publish-hash", "actor-a", Mutation{Key: "create", Hash: "create-hash"})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan Record, workers)
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			record, err := store.Approve(context.Background(), "tenant-a", "v1", created.ID, "actor-b", Mutation{Key: "approve", Hash: "approve-hash"})
			results <- record
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent approval error = %v", err)
		}
	}
	for record := range results {
		if record.State != StateApproved || record.ID != created.ID {
			t.Fatalf("concurrent approval record = %+v", record)
		}
	}
}

func TestMemoryStoreRejectsPendingApproval(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), "tenant-a", "v1", "publish", "publish-hash", "actor-a", Mutation{Key: "create", Hash: "create-hash"})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := store.Reject(context.Background(), "tenant-a", "v1", created.ID, "actor-b", Mutation{Key: "reject", Hash: "reject-hash"})
	if err != nil || rejected.State != StateRejected {
		t.Fatalf("Reject() = %+v, %v", rejected, err)
	}
	if _, err := store.Consume(context.Background(), Binding{TenantID: "tenant-a", ConfigVersion: "v1", ApprovalID: created.ID, PublishIdempotencyKey: "publish", RequestHash: "publish-hash", Publisher: "actor-c"}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("consume rejected approval error = %v, want %v", err, ErrStateConflict)
	}
}
