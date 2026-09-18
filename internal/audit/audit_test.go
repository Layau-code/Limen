package audit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreListsTenantScopedSummaries(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(10, 0).UTC()
	if err := store.Append(context.Background(), Event{ID: "a1", TenantID: "tenant-a", Action: ActionConfigPublish, ResourceType: "config", ResourceID: "v1", Outcome: "success", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), Event{ID: "b1", TenantID: "tenant-b", Action: ActionConfigPublish, ResourceType: "config", ResourceID: "v2", Outcome: "success", CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(context.Background(), "tenant-a", 10)
	if err != nil || len(items) != 1 || items[0].ID != "a1" {
		t.Fatalf("tenant events = %#v, err=%v", items, err)
	}
}

func TestEventRejectsMissingSecurityFields(t *testing.T) {
	event := Event{ID: "a1", TenantID: "tenant-a", Action: ActionConfigPublish, ResourceType: "config", ResourceID: "v1", Outcome: "success"}
	if err := event.Validate(); err != ErrInvalidEvent {
		t.Fatalf("validation error = %v", err)
	}
}
