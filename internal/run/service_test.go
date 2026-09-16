package run

import (
	"context"
	"testing"
	"time"
)

func TestMemoryServiceImplementsRunBoundary(t *testing.T) {
	service := NewMemoryService(nil)
	item := testRun()
	if err := service.CreateRun(context.Background(), "tenant-2", item); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetRun(context.Background(), "tenant-2", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetRun(context.Background(), "tenant-1", item.ID); err != ErrResourceNotFound {
		t.Fatalf("cross-tenant error = %v", err)
	}
	requestHash, err := HashRequest("tenant-2", "/v1/chat/completions", "key", []byte(`{"model":"auto"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdmitRequest(context.Background(), "tenant-2", item.ID, AdmissionInput{Request: Request{Endpoint: "/v1/chat/completions", IdempotencyKey: "key", RequestHash: requestHash}, Now: time.Unix(100, 0)}); err != nil {
		t.Fatal(err)
	}
}
