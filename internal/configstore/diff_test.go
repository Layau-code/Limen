package configstore

import (
	"testing"

	"github.com/huz/limen/internal/config"
)

func TestDiffReturnsStableStructuralChangesWithoutValues(t *testing.T) {
	before := Record{Models: []config.Model{{ID: "model", Targets: []config.Target{{ID: "target", Provider: "openai", UpstreamModel: "gpt-old"}}}}}
	after := Record{Models: []config.Model{{ID: "model", Targets: []config.Target{{ID: "target", Provider: "anthropic", UpstreamModel: "claude-new"}}}}}
	changes := Diff(before, after)
	if len(changes) != 2 || changes[0].Path != "models[model].targets[target].provider" || changes[1].Path != "models[model].targets[target].upstream_model" {
		t.Fatalf("changes = %+v", changes)
	}
	for _, change := range changes {
		if change.Kind != "changed" {
			t.Fatalf("change = %+v", change)
		}
	}
}

func TestDiffReportsEndpointBindingChanges(t *testing.T) {
	before := Record{Models: []config.Model{{ID: "model", Targets: []config.Target{{ID: "target", Provider: "openai", UpstreamModel: "gpt-model", EndpointID: "endpoint:0123456789abcdef01234567"}}}}}
	after := Record{Models: []config.Model{{ID: "model", Targets: []config.Target{{ID: "target", Provider: "openai", UpstreamModel: "gpt-model", EndpointID: "endpoint:fedcba987654321001234567"}}}}}
	changes := Diff(before, after)
	if len(changes) != 1 || changes[0].Path != "models[model].targets[target].endpoint_id" || changes[0].Kind != "changed" {
		t.Fatalf("changes = %+v", changes)
	}
}
