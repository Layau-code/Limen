package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingOmitsSecretsAndBody(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := WithLogging(logger, New("limen-secret", nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"content":"private prompt"}]}`))
	request.Header.Set("Authorization", "Bearer limen-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	logged := output.String()
	if strings.Contains(logged, "limen-secret") || strings.Contains(logged, "private prompt") {
		t.Fatalf("sensitive data logged: %s", logged)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
}

func TestLoggingIncludesSafeRouteFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Limen-Provider", "anthropic")
		w.Header().Set("X-Limen-Attempts", "2")
		w.Header().Set("X-Limen-Route", "openai:503>anthropic:200")
		w.WriteHeader(http.StatusOK)
	})
	WithLogging(logger, next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	logged := output.String()
	for _, value := range []string{"anthropic", "2", "openai:503>anthropic:200"} {
		if !strings.Contains(logged, value) {
			t.Fatalf("missing route value %q in %s", value, logged)
		}
	}
}
