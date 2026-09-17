package decision

import "testing"

func TestAlgorithmRegistryDoesNotFallbackAcrossVersions(t *testing.T) {
	registry := NewAlgorithmRegistry()
	if _, ok := registry.Resolve(AlgorithmVersionV1); !ok {
		t.Fatal("current algorithm is not registered")
	}
	if _, ok := registry.Resolve("decision.v0"); ok {
		t.Fatal("unknown algorithm unexpectedly resolved")
	}
	if err := registry.Register(AlgorithmVersionV1, NewEngine()); err == nil {
		t.Fatal("expected duplicate algorithm version error")
	}
}
