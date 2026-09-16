package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpointsTrackReadiness(t *testing.T) {
	health := NewHealth()
	handler := NewWithHealth("unused", nil, health)

	response := requestHealth(t, handler, "/livez")
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("live response = %d %q", response.Code, response.Body.String())
	}
	response = requestHealth(t, handler, "/readyz")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial ready status = %d", response.Code)
	}

	health.SetReady(true)
	response = requestHealth(t, handler, "/readyz")
	if response.Code != http.StatusOK {
		t.Fatalf("ready status = %d", response.Code)
	}
}

func requestHealth(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}
