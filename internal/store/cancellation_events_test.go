package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCancellationEventListenerRequiresDatabaseURL(t *testing.T) {
	if _, err := NewCancellationEventListener(" "); err == nil {
		t.Fatal("expected database URL validation error")
	}
}

func TestParseCancellationEventValidatesMetadata(t *testing.T) {
	if _, err := parseCancellationEvent(`{"tenant_id":"tenant-a","run_id":"run-a"}`); err == nil {
		t.Fatal("expected incomplete cancellation event error")
	}
	event, err := parseCancellationEvent(`{"id":7,"tenant_id":"tenant-a","run_id":"run-a"}`)
	if err != nil || event.ID != 7 {
		t.Fatalf("event=%+v err=%v", event, err)
	}
}

func TestCancellationEventPayloadContainsOnlyMetadata(t *testing.T) {
	encoded, err := json.Marshal(map[string]any{"id": 7, "tenant_id": "tenant-a", "run_id": "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "prompt") {
		t.Fatal("cancellation notification contains sensitive data")
	}
}
