package telemetry

import (
	"fmt"
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

func TestRegistryCapsSeriesCount(t *testing.T) {
	registry := NewRegistry()
	for i := 0; i < maxSeriesPerMetric+1; i++ {
		registry.Inc(RequestsTotal, Labels{Model: fmt.Sprintf("model-%d", i)})
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if got := len(registry.samples[RequestsTotal]); got != maxSeriesPerMetric {
		t.Fatalf("series = %d, want %d", got, maxSeriesPerMetric)
	}
}

func TestRegistryWritesBoundedHistogram(t *testing.T) {
	registry := NewRegistry()
	registry.Observe(RequestDurationSeconds, Labels{Endpoint: "chat", Model: "smart"}, 0.12)
	registry.Observe(RequestDurationSeconds, Labels{Endpoint: "chat", Model: "smart"}, 0.7)
	registry.Observe(RequestDurationSeconds, Labels{Endpoint: "chat", Model: "smart"}, -1)
	var output strings.Builder
	if _, err := registry.Write(&output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, part := range []string{
		"# TYPE limen_chat_request_duration_seconds histogram",
		`limen_chat_request_duration_seconds_bucket{endpoint="chat",status="",model="smart",provider="",target="",result="",reason="",le="0.25"} 1`,
		`limen_chat_request_duration_seconds_bucket{endpoint="chat",status="",model="smart",provider="",target="",result="",reason="",le="+Inf"} 2`,
		`limen_chat_request_duration_seconds_count{endpoint="chat",status="",model="smart",provider="",target="",result="",reason=""} 2`,
		`limen_chat_request_duration_seconds_sum{endpoint="chat",status="",model="smart",provider="",target="",result="",reason=""} 0.82`,
	} {
		if !strings.Contains(text, part) {
			t.Fatalf("metrics missing %q:\n%s", part, text)
		}
	}
}

func TestRegistryCapsHistogramSeriesCount(t *testing.T) {
	registry := NewRegistry()
	for i := 0; i < maxSeriesPerMetric+1; i++ {
		registry.Observe(TimeToFirstByteSeconds, Labels{Model: fmt.Sprintf("model-%d", i)}, 0.01)
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if got := len(registry.histograms[TimeToFirstByteSeconds]); got != maxSeriesPerMetric {
		t.Fatalf("histogram series = %d, want %d", got, maxSeriesPerMetric)
	}
}
