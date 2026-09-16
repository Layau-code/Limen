package telemetry

import (
	"strings"
	"testing"
)

func TestRegistryWritesBoundedMetrics(t *testing.T) {
	registry := NewRegistry()
	registry.Inc(RequestsTotal, Labels{Endpoint: "chat", Model: strings.Repeat("m", 100)})
	registry.Inc(RequestsTotal, Labels{Endpoint: "chat", Model: strings.Repeat("m", 100)})
	registry.Inc(Metric("unbounded_metric"), Labels{Model: "ignored"})
	var output strings.Builder
	if _, err := registry.Write(&output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "limen_chat_requests_total") || !strings.Contains(output.String(), " 2\n") || strings.Contains(output.String(), "unbounded_metric") {
		t.Fatalf("metrics = %s", output.String())
	}
	if strings.Contains(output.String(), strings.Repeat("m", 65)) {
		t.Fatal("label was not bounded")
	}
}
