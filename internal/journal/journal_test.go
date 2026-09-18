package journal

import (
	"context"
	"testing"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/decision"
)

func TestMemoryStoreIsTenantScopedAndIdempotent(t *testing.T) {
	store := NewMemoryStore()
	input := decision.Input{SchemaVersion: decision.SchemaVersionV1, AlgorithmVersion: decision.AlgorithmVersionV1, Request: decision.Request{Model: "model"}}
	plan, err := decision.NewEngine().Decide(input)
	if err == nil {
		t.Fatal("expected empty candidate error")
	}
	input.Candidates = []decision.Candidate{{ModelID: "model", Enabled: true, SecurityAllowed: true, Target: testTarget()}}
	plan, err = decision.NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{ID: "decision-1", TenantID: "tenant-1", Input: input, Plan: plan}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "tenant-2", record.ID); err != ErrNotFound {
		t.Fatalf("cross-tenant error = %v", err)
	}
}

func TestMemoryStoreRejectsTamperedPlanOnRead(t *testing.T) {
	input := decision.Input{
		SchemaVersion:    decision.SchemaVersionV1,
		AlgorithmVersion: decision.AlgorithmVersionV1,
		Request:          decision.Request{Model: "model"},
		Candidates:       []decision.Candidate{{ModelID: "model", Enabled: true, SecurityAllowed: true, Target: testTarget()}},
	}
	plan, err := decision.NewEngine().Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	record := Record{ID: "decision-1", TenantID: "tenant-1", Input: input, Plan: plan}
	record.Plan.Reasons = []string{"tampered"}
	store.records["tenant-1\x00decision-1"] = record
	if _, err := store.Get(context.Background(), "tenant-1", "decision-1"); err == nil {
		t.Fatal("tampered plan was returned")
	}
}

func testTarget() catalog.Target {
	return catalog.Target{ID: "target", Provider: "openai", UpstreamModel: "gpt-test", SupportsStreaming: true}
}
