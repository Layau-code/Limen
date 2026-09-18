package catalog

import "testing"

func TestOpaqueTargetIDIsStableAndDoesNotContainSource(t *testing.T) {
	first := OpaqueTargetID("openai:gpt-secret")
	second := OpaqueTargetID("openai:gpt-secret")
	if first != second || first == "openai:gpt-secret" || len(first) != len("target-")+12 {
		t.Fatalf("opaque target id first=%q second=%q", first, second)
	}
}

func TestRegistryClonesCapabilityMetadata(t *testing.T) {
	registry, err := NewRegistry([]Model{{
		ID: "coding",
		Targets: []Target{{
			ID: "openai-code", Provider: "openai", UpstreamModel: "gpt-test",
			Capabilities: []string{"text"}, SupportsStreaming: true,
			QualityTier: 3, CostTier: 1, ContextWindow: 128000,
			DataClasses: []string{"public", "internal"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := registry.List()
	got[0].Targets[0].Capabilities[0] = "changed"
	got[0].Targets[0].DataClasses[0] = "changed"
	if registry.List()[0].Targets[0].Capabilities[0] != "text" {
		t.Fatal("registry capabilities were mutable")
	}
	if registry.List()[0].Targets[0].DataClasses[0] != "public" {
		t.Fatal("registry data classes were mutable")
	}
}
