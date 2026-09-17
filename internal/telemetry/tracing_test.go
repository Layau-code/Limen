package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestNewTracingStaysDisabledWithoutOTLPEndpoint(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	tracing, err := NewTracing(context.Background(), "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if tracing.Enabled() {
		t.Fatal("tracing should be disabled without an explicit OTLP endpoint")
	}
	tracing.Install()
	if err := tracing.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
