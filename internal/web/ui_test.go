package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesShellAndAssets(t *testing.T) {
	handler := Handler()

	shell := httptest.NewRecorder()
	handler.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "Limen") {
		t.Fatalf("shell = %d %q", shell.Code, shell.Body.String())
	}

	styles := httptest.NewRecorder()
	handler.ServeHTTP(styles, httptest.NewRequest(http.MethodGet, "/ui/styles.css", nil))
	if styles.Code != http.StatusOK || !strings.Contains(styles.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("styles = %d content-type=%q", styles.Code, styles.Header().Get("Content-Type"))
	}
}

func TestHandlerFallsBackToShellForHashRoutePaths(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ui/runs/run_8FA2", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || !strings.Contains(string(body), "data-app") {
		t.Fatalf("body = %q err=%v", body, err)
	}
}

func TestFixtureDataDoesNotContainPayloadOrProviderSecrets(t *testing.T) {
	for _, name := range []string{"fixtures.js", "index.html"} {
		response := httptest.NewRecorder()
		Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ui/"+name, nil))
		body := strings.ToLower(response.Body.String())
		for _, forbidden := range []string{"sk-", "api_key", "prompt", "tool_arguments", "provider_key"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("asset %s contains forbidden marker %q", name, forbidden)
			}
		}
	}
}

func TestDecisionUIShowsOnlyFrozenSemanticMetadata(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ui/app.js", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("app.js status = %d", response.Code)
	}
	asset := response.Body.String()
	for _, marker := range []string{"semantic_assessment", "not_evaluated", "state hash", "no request text retained", "Replay uses this frozen result"} {
		if !strings.Contains(asset, marker) {
			t.Fatalf("decision UI is missing semantic marker %q", marker)
		}
	}
}
