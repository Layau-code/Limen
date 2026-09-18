package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
	"github.com/huz/limen/internal/run"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestGovernedFallbackTraceFormsPrivacySafeEvidenceChain(t *testing.T) {
	recorder := installRecordingTracer(t)
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "private-model", Targets: []gateway.Target{
		{ID: "openai:secret-upstream-primary", Provider: "openai", UpstreamModel: "secret-upstream-primary", QualityTier: 2},
		{ID: "anthropic:secret-upstream-backup", Provider: "anthropic", UpstreamModel: "secret-upstream-backup", QualityTier: 1, Pricing: &cost.Pricing{}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	openAI := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("private upstream failure body")), ProviderRequestID: "provider-request-primary"}, nil
	})
	anthropic := testProviderFunc(func(context.Context, provider.ChatRequest) (provider.Response, error) {
		return provider.Response{StatusCode: http.StatusOK, ContentType: "application/json", Body: io.NopCloser(strings.NewReader(`{"answer":"private response"}`)), ProviderRequestID: "provider-request-backup", Usage: staticUsage{InputTokens: 1, OutputTokens: 1, Complete: true}}, nil
	})
	runs := run.NewMemoryService(nil)
	base := NewWithRuns("private-limen-api-key", newTestRouter(openAI, anthropic, registry), runs)
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	handler := WithLogging(logger, WithTracing(base))

	create := httptest.NewRequest(http.MethodPost, "/v1/limen/runs", strings.NewReader(`{"soft_budget_usd":"1","max_parallelism":1}`))
	create.Header.Set("Authorization", "Bearer private-limen-api-key")
	create.Header.Set("Idempotency-Key", "trace-run-create")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	var created run.Run
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	recorder.Reset()

	chat := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"private-model","messages":[{"role":"user","content":"private prompt"}]}`))
	chat.Header.Set("Authorization", "Bearer private-limen-api-key")
	chat.Header.Set("X-Limen-Run-ID", created.ID)
	chat.Header.Set("Idempotency-Key", "trace-chat-request")
	chat.Header.Set("X-Request-ID", "privateprompt")
	chat.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chat)
	if chatResponse.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", chatResponse.Code, chatResponse.Body.String())
	}

	spans := recorder.Ended()
	wantNames := map[string]int{
		"limen.http.request":     1,
		"limen.run.admission":    1,
		"limen.decision":         1,
		"limen.provider.attempt": 2,
		"limen.settlement":       1,
	}
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	var rendered bytes.Buffer
	for _, span := range spans {
		wantNames[span.Name()]--
		if got := span.SpanContext().TraceID().String(); got != traceID {
			t.Errorf("span %q trace ID=%s, want %s", span.Name(), got, traceID)
		}
		fmt.Fprintf(&rendered, "%s %v %v", span.Name(), span.Attributes(), span.Events())
	}
	rendered.Write(logOutput.Bytes())
	for name, remaining := range wantNames {
		if remaining != 0 {
			t.Errorf("span %q count delta=%d, spans=%d", name, remaining, len(spans))
		}
	}
	root := findSpan(t, spans, "limen.http.request")
	if root.Parent().SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("root parent=%s", root.Parent().SpanID())
	}
	if got := spanAttribute(root, "limen.request.id"); got == "" || got != chatResponse.Header().Get("X-Request-ID") {
		t.Fatalf("request ID attribute=%q header=%q", got, chatResponse.Header().Get("X-Request-ID"))
	}
	if !hasSpanAttribute(root, "limen.ttfb_ms") {
		t.Fatal("trace is missing TTFB attribute")
	}
	for _, secret := range []string{"private prompt", "privateprompt", "private response", "private-limen-api-key", "secret-upstream-primary", "secret-upstream-backup", "private upstream failure body"} {
		if strings.Contains(rendered.String(), secret) {
			t.Errorf("trace contains private value %q", secret)
		}
	}
}

func TestTraceOmitsRejectedModelAndUnmatchedPath(t *testing.T) {
	recorder := installRecordingTracer(t)
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "known", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := WithLogging(slog.New(slog.NewJSONHandler(io.Discard, nil)), WithTracing(New("secret", newTestRouter(nil, nil, registry))))

	rejected := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"private-model-secret","messages":[]}`))
	rejected.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(httptest.NewRecorder(), rejected)
	unmatched := httptest.NewRequest(http.MethodGet, "/private-path-secret", nil)
	handler.ServeHTTP(httptest.NewRecorder(), unmatched)

	var rendered strings.Builder
	for _, span := range recorder.Ended() {
		fmt.Fprintf(&rendered, "%s %v", span.Name(), span.Attributes())
	}
	for _, secret := range []string{"private-model-secret", "private-path-secret"} {
		if strings.Contains(rendered.String(), secret) {
			t.Errorf("trace contains rejected input %q", secret)
		}
	}
}

// installRecordingTracer 为测试安装内存 Span 记录器，并在结束时恢复全局状态。
func installRecordingTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	return recorder
}

// findSpan 按名称返回唯一 Span，缺失时结束测试。
func findSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("span %q not found", name)
	return nil
}

// spanAttribute 读取测试关心的字符串属性。
func spanAttribute(span sdktrace.ReadOnlySpan, key string) string {
	for _, item := range span.Attributes() {
		if string(item.Key) == key {
			return item.Value.AsString()
		}
	}
	return ""
}

// hasSpanAttribute 判断测试 Span 是否记录了指定字段。
func hasSpanAttribute(span sdktrace.ReadOnlySpan, key string) bool {
	for _, item := range span.Attributes() {
		if string(item.Key) == key {
			return true
		}
	}
	return false
}
