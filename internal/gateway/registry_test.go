package gateway

import (
	"testing"

	"github.com/huz/limen/internal/cost"
)

func TestModelRegistryClonesTargetPricing(t *testing.T) {
	registry, err := NewModelRegistry([]Model{{
		ID: "model",
		Targets: []Target{{
			Provider:      "openai",
			UpstreamModel: "gpt-test",
			Pricing:       &cost.Pricing{InputPerMillionNanoUSD: 1},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	models := registry.List()
	models[0].Targets[0].Pricing.InputPerMillionNanoUSD = 99
	if got := registry.List()[0].Targets[0].Pricing.InputPerMillionNanoUSD; got != 1 {
		t.Fatalf("registry price = %d, want 1", got)
	}
}
