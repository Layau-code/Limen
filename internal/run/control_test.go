package run

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryControlMutationsAreIdempotent(t *testing.T) {
	service := NewMemoryService(nil)
	created, err := service.CreateRunWithMutation(context.Background(), "tenant", Run{ID: "run", State: StateActive}, Mutation{Key: "create-1", Hash: "sha256:create"})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := service.CreateRunWithMutation(context.Background(), "tenant", Run{ID: "different", State: StateActive}, Mutation{Key: "create-1", Hash: "sha256:create"})
	if err != nil || repeated.ID != created.ID {
		t.Fatalf("repeated=%+v err=%v", repeated, err)
	}
	if _, err := service.CreateRunWithMutation(context.Background(), "tenant", Run{ID: "other", State: StateActive}, Mutation{Key: "create-1", Hash: "sha256:other"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	completed, err := service.CompleteRunWithMutation(context.Background(), "tenant", "run", Mutation{Key: "complete-1", Hash: "sha256:complete"})
	if err != nil || completed.State != StateCompleted {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	again, err := service.CompleteRunWithMutation(context.Background(), "tenant", "run", Mutation{Key: "complete-1", Hash: "sha256:complete"})
	if err != nil || again.State != StateCompleted {
		t.Fatalf("again=%+v err=%v", again, err)
	}
}

func TestMemoryCancelPublishesTenantScopedEvent(t *testing.T) {
	service := NewMemoryService(nil)
	if _, err := service.CreateRunWithMutation(context.Background(), "tenant-1", Run{ID: "run", State: StateActive}, Mutation{Key: "create", Hash: "hash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelRunWithMutation(context.Background(), "tenant-1", "run", Mutation{Key: "cancel", Hash: "hash"}); err != nil {
		t.Fatal(err)
	}
	events, err := service.PollCancellationEvents(context.Background(), "tenant-1", 0, 10)
	if err != nil || len(events) != 1 || events[0].RunID != "run" || events[0].TenantID != "tenant-1" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	other, err := service.PollCancellationEvents(context.Background(), "tenant-2", 0, 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-tenant events=%+v err=%v", other, err)
	}
	if events[0].CreatedAt.IsZero() || events[0].CreatedAt.After(time.Now().UTC()) {
		t.Fatalf("event time = %v", events[0].CreatedAt)
	}
}
