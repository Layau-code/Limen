package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/credentialstore"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/journal"
)

type testCredentialSetter struct {
	key string
}

func (setter *testCredentialSetter) SetAPIKey(key string) error {
	setter.key = key
	return nil
}

func (setter *testCredentialSetter) ClearAPIKey() {
	setter.key = ""
}

func TestCredentialControlRotatesAndRevokesWithoutReturningSecret(t *testing.T) {
	vault, err := credentialstore.NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	setter := &testCredentialSetter{}
	credentials := credentialstore.NewMemoryStore(vault)
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentials(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeAdmin}),
		gateway.NewRouter(nil, registry, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second}),
		nil,
		"tenant-a",
		journal.NewMemoryStore(),
		configstore.NewMemoryStore(),
		credentials,
		map[string]ProviderCredentialSetter{"openai": setter},
		map[string]string{"openai": "endpoint-a"},
		nil,
	)

	rotate := httptest.NewRequest(http.MethodPost, "/v1/limen/credentials/openai", strings.NewReader(`{"endpoint_id":"endpoint-a","secret":"provider-secret"}`))
	rotate.Header.Set("Authorization", "Bearer secret")
	rotateResponse := httptest.NewRecorder()
	handler.ServeHTTP(rotateResponse, rotate)
	if rotateResponse.Code != http.StatusOK || setter.key != "provider-secret" || strings.Contains(rotateResponse.Body.String(), "provider-secret") {
		t.Fatalf("rotate status=%d key=%q body=%s", rotateResponse.Code, setter.key, rotateResponse.Body.String())
	}
	if secret, _, err := credentials.Resolve(nil, "tenant-a", "openai", "endpoint-a"); err != nil || string(secret) != "provider-secret" {
		t.Fatalf("stored credential = %q, err=%v", secret, err)
	}

	revoke := httptest.NewRequest(http.MethodPost, "/v1/limen/credentials/openai/revoke", strings.NewReader(`{"endpoint_id":"endpoint-a"}`))
	revoke.Header.Set("Authorization", "Bearer secret")
	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusOK || setter.key != "" {
		t.Fatalf("revoke status=%d key=%q body=%s", revokeResponse.Code, setter.key, revokeResponse.Body.String())
	}
	if _, _, err := credentials.Resolve(nil, "tenant-a", "openai", "endpoint-a"); err != credentialstore.ErrNotFound {
		t.Fatalf("revoked credential error = %v", err)
	}
}

func TestCredentialControlRequiresAdminAndEndpointBinding(t *testing.T) {
	vault, err := credentialstore.NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentials(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeInference}),
		gateway.NewRouter(nil, registry, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second}),
		nil,
		"tenant-a",
		journal.NewMemoryStore(),
		configstore.NewMemoryStore(),
		credentialstore.NewMemoryStore(vault),
		map[string]ProviderCredentialSetter{"openai": &testCredentialSetter{}},
		map[string]string{"openai": "endpoint-a"},
		nil,
	)

	request := httptest.NewRequest(http.MethodPost, "/v1/limen/credentials/openai", strings.NewReader(`{"endpoint_id":"endpoint-a","secret":"provider-secret"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "insufficient_scope") {
		t.Fatalf("non-admin status=%d body=%s", response.Code, response.Body.String())
	}

	admin := auth.NewStaticAuthenticator("admin-secret", "tenant-a", []auth.Scope{auth.ScopeAdmin})
	handler = NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentials(
		admin,
		gateway.NewRouter(nil, registry, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second}),
		nil,
		"tenant-a",
		journal.NewMemoryStore(),
		configstore.NewMemoryStore(),
		credentialstore.NewMemoryStore(vault),
		map[string]ProviderCredentialSetter{"openai": &testCredentialSetter{}},
		map[string]string{"openai": "endpoint-a"},
		nil,
	)
	request = httptest.NewRequest(http.MethodPost, "/v1/limen/credentials/openai", strings.NewReader(`{"endpoint_id":"endpoint-other","secret":"provider-secret"}`))
	request.Header.Set("Authorization", "Bearer admin-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "credential_endpoint_mismatch") {
		t.Fatalf("endpoint mismatch status=%d body=%s", response.Code, response.Body.String())
	}
}
