package decision

import (
	"testing"
	"time"
)

func TestAlgorithmRegistryDoesNotFallbackAcrossVersions(t *testing.T) {
	registry := NewAlgorithmRegistry()
	if _, ok := registry.Resolve(AlgorithmVersionV1); !ok {
		t.Fatal("current algorithm is not registered")
	}
	if _, ok := registry.Resolve(AlgorithmVersionV2); !ok {
		t.Fatal("canonical algorithm is not registered")
	}
	if _, ok := registry.Resolve("decision.v0"); ok {
		t.Fatal("unknown algorithm unexpectedly resolved")
	}
	if err := registry.Register(AlgorithmVersionV1, NewEngine()); err == nil {
		t.Fatal("expected duplicate algorithm version error")
	}
}

func TestAlgorithmRegistryRejectsExpiredReplayVersion(t *testing.T) {
	registry := &AlgorithmRegistry{}
	deadline := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := registry.RegisterWithRetention("decision.legacy", NewEngine(), deadline); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.ResolveAt("decision.legacy", deadline.Add(-time.Nanosecond)); !ok {
		t.Fatal("algorithm should resolve before retention deadline")
	}
	if _, ok := registry.ResolveAt("decision.legacy", deadline); ok {
		t.Fatal("expired algorithm unexpectedly resolved")
	}
}

func TestAlgorithmRegistryRegisterWithRetentionRejectsDuplicates(t *testing.T) {
	registry := NewAlgorithmRegistry()
	if err := registry.RegisterWithRetention(AlgorithmVersionV2, NewEngine(), time.Time{}); err == nil {
		t.Fatal("expected duplicate algorithm version error")
	}
}
