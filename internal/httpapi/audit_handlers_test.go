package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/journal"
)

func TestAuditEndpointRequiresAdminAndHidesConfigValues(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeAdmin}),
		gateway.NewRouter(nil, registry, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second}), nil, "tenant-a", journal.NewMemoryStore(), configstore.NewMemoryStore(), nil,
	)
	create := httptest.NewRequest(http.MethodPost, "/v1/limen/configs", strings.NewReader(`{"models":[{"id":"new","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-secret-upstream"}]}]}`))
	create.Header.Set("Authorization", "Bearer secret")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var summary configSummary
	if err := json.Unmarshal(created.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	publish := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+summary.Version+"/publish", nil)
	publish.Header.Set("Authorization", "Bearer secret")
	publish.Header.Set("Idempotency-Key", "publish-1")
	published := httptest.NewRecorder()
	handler.ServeHTTP(published, publish)
	if published.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body.String())
	}
	auditRequest := httptest.NewRequest(http.MethodGet, "/v1/limen/audit?limit=10", nil)
	auditRequest.Header.Set("Authorization", "Bearer secret")
	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, auditRequest)
	body := auditResponse.Body.String()
	if auditResponse.Code != http.StatusOK || !strings.Contains(body, "config.publish") || strings.Contains(body, "gpt-secret-upstream") {
		t.Fatalf("audit status=%d body=%s", auditResponse.Code, body)
	}

	nonAdmin := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("inference-secret", "tenant-a", []auth.Scope{auth.ScopeInference}),
		gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configstore.NewMemoryStore(), nil,
	)
	denied := httptest.NewRequest(http.MethodGet, "/v1/limen/audit", nil)
	denied.Header.Set("Authorization", "Bearer inference-secret")
	deniedResponse := httptest.NewRecorder()
	nonAdmin.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("non-admin audit status=%d body=%s", deniedResponse.Code, deniedResponse.Body.String())
	}
}
