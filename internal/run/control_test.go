package run

import (
	"context"
	"errors"
	"testing"
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
