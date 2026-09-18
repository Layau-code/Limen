package httpapi

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// WithTracing 为每个 HTTP 请求建立隐私安全的根 Span，并提取 W3C Trace Context。
func WithTracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		started := time.Now()
		ctx, span := otel.Tracer("github.com/huz/limen/internal/httpapi").Start(ctx, "limen.http.request",
			trace.WithSpanKind(trace.SpanKindServer),
		)
		if span.IsRecording() {
			span.SetAttributes(
				attribute.String("http.request.method", r.Method),
			)
		}
		defer span.End()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		tracedRequest := r.WithContext(ctx)
		next.ServeHTTP(recorder, tracedRequest)
		if span.IsRecording() {
			span.SetAttributes(
				attribute.Int("http.response.status_code", recorder.status),
				attribute.String("limen.request.id", r.Header.Get("X-Request-ID")),
			)
			if tracedRequest.Pattern != "" {
				span.SetAttributes(attribute.String("http.route", tracedRequest.Pattern))
			}
			if firstByte := recorder.FirstByteAt(); !firstByte.IsZero() {
				span.SetAttributes(attribute.Int64("limen.ttfb_ms", firstByte.Sub(started).Milliseconds()))
			}
			setTraceResponseAttributes(span, recorder.Header())
		}
		if recorder.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "")
		}
	})
}

// setTraceResponseAttributes 只从固定响应头白名单写入治理结果。
func setTraceResponseAttributes(span trace.Span, header http.Header) {
	for name, key := range map[string]string{
		"X-Limen-Request-ID":     "limen.run.request_id",
		"X-Limen-Decision-ID":    "limen.decision.id",
		"X-Limen-Config-Version": "limen.config.version",
		"X-Limen-Provider":       "limen.provider.name",
		"X-Limen-Attempts":       "limen.attempt.count",
	} {
		if value := header.Get(name); value != "" {
			span.SetAttributes(attribute.String(key, value))
		}
	}
}
