package httpapi

import (
	"context"
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

type fakeAPIKeyManager struct {
	record     auth.APIKeyRecord
	plaintext  string
	created    int
	revoked    bool
	rotated    bool
	rotateKey  string
	rotateHash string
	oldPrefix  string
}

func (manager *fakeAPIKeyManager) Create(_ context.Context, tenantID string, scopes []auth.Scope, expiresAt *time.Time, mutation auth.APIKeyMutation) (auth.APIKeyRecord, string, error) {
	if mutation.Key == "" || mutation.Hash == "" {
		return auth.APIKeyRecord{}, "", auth.ErrInvalidAPIKeyOperation
	}
	if manager.created > 0 {
		return manager.record, "", nil
	}
	manager.created++
	manager.record = auth.APIKeyRecord{PublicPrefix: "0123456789abcdef", TenantID: tenantID, Scopes: append([]auth.Scope(nil), scopes...), Active: true, ExpiresAt: expiresAt, CreatedAt: time.Now().UTC()}
	manager.plaintext = "lmn_live_0123456789abcdef_secret456"
	return manager.record, manager.plaintext, nil
}

func (manager *fakeAPIKeyManager) List(_ context.Context, tenantID string) ([]auth.APIKeyRecord, error) {
	if manager.record.TenantID != tenantID {
		return nil, nil
	}
	return []auth.APIKeyRecord{manager.record}, nil
}

func (manager *fakeAPIKeyManager) Revoke(_ context.Context, tenantID, prefix string, mutation auth.APIKeyMutation) error {
	if tenantID != manager.record.TenantID || prefix != manager.record.PublicPrefix {
		return auth.ErrAPIKeyNotFound
	}
	if mutation.Key == "" || mutation.Hash == "" {
		return auth.ErrInvalidAPIKeyOperation
	}
	manager.revoked = true
	manager.record.Active = false
	return nil
}

func (manager *fakeAPIKeyManager) Rotate(_ context.Context, tenantID, prefix string, scopes []auth.Scope, expiresAt *time.Time, mutation auth.APIKeyMutation) (auth.APIKeyRecord, auth.APIKeyRecord, string, error) {
	if tenantID != manager.record.TenantID || (prefix != manager.record.PublicPrefix && prefix != manager.oldPrefix) {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", auth.ErrAPIKeyNotFound
	}
	if mutation.Key == "" || mutation.Hash == "" || auth.ValidateAPIKeyScopes(scopes) != nil {
		return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", auth.ErrInvalidAPIKeyOperation
	}
	if manager.rotated {
		if mutation.Key != manager.rotateKey || mutation.Hash != manager.rotateHash {
			return auth.APIKeyRecord{}, auth.APIKeyRecord{}, "", auth.ErrAPIKeyConflict
		}
		return auth.APIKeyRecord{PublicPrefix: prefix, TenantID: tenantID, Active: false}, manager.record, "", nil
	}
	manager.rotated = true
	manager.rotateKey, manager.rotateHash = mutation.Key, mutation.Hash
	old := manager.record
	manager.oldPrefix = old.PublicPrefix
	manager.record = auth.APIKeyRecord{PublicPrefix: "fedcba9876543210", TenantID: tenantID, Scopes: append([]auth.Scope(nil), scopes...), Active: true, ExpiresAt: expiresAt, CreatedAt: time.Now().UTC()}
	return old, manager.record, "lmn_live_fedcba9876543210_rotatedsecret", nil
}

func TestAPIKeyControlCreatesOnceListsMetadataAndRevokes(t *testing.T) {
	manager := &fakeAPIKeyManager{}
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentialsAndAuditAndAPIKeys(
		auth.NewStaticAuthenticator("admin", "tenant-a", []auth.Scope{auth.ScopeAdmin}),
		gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configstore.NewMemoryStore(), nil, nil, nil, nil, nil, manager,
	)
	create := httptest.NewRequest(http.MethodPost, "/v1/limen/keys", strings.NewReader(`{"scopes":["inference"]}`))
	create.Header.Set("Authorization", "Bearer admin")
	create.Header.Set("Idempotency-Key", "key-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, create)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "lmn_live_0123456789abcdef_secret456") {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	createRepeat := httptest.NewRequest(http.MethodPost, "/v1/limen/keys", strings.NewReader(`{"scopes":["inference"]}`))
	createRepeat.Header.Set("Authorization", "Bearer admin")
	createRepeat.Header.Set("Idempotency-Key", "key-1")
	repeatResponse := httptest.NewRecorder()
	handler.ServeHTTP(repeatResponse, createRepeat)
	if repeatResponse.Code != http.StatusCreated || strings.Contains(repeatResponse.Body.String(), "secret456") {
		t.Fatalf("repeat status=%d body=%s", repeatResponse.Code, repeatResponse.Body.String())
	}
	list := httptest.NewRequest(http.MethodGet, "/v1/limen/keys", nil)
	list.Header.Set("Authorization", "Bearer admin")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || strings.Contains(listResponse.Body.String(), "secret456") || !strings.Contains(listResponse.Body.String(), "0123456789abcdef") {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	revoke := httptest.NewRequest(http.MethodPost, "/v1/limen/keys/0123456789abcdef/revoke", nil)
	revoke.Header.Set("Authorization", "Bearer admin")
	revoke.Header.Set("Idempotency-Key", "revoke-1")
	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusOK || !manager.revoked {
		t.Fatalf("revoke status=%d body=%s", revokeResponse.Code, revokeResponse.Body.String())
	}
}

func TestAPIKeyControlRequiresAdmin(t *testing.T) {
	manager := &fakeAPIKeyManager{}
	registry, _ := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentialsAndAuditAndAPIKeys(
		auth.NewStaticAuthenticator("user", "tenant-a", []auth.Scope{auth.ScopeInference}),
		gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configstore.NewMemoryStore(), nil, nil, nil, nil, nil, manager,
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/limen/keys", nil)
	request.Header.Set("Authorization", "Bearer user")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "insufficient_scope") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAPIKeyControlRotatesAtomically(t *testing.T) {
	manager := &fakeAPIKeyManager{record: auth.APIKeyRecord{PublicPrefix: "0123456789abcdef", TenantID: "tenant-a", Active: true}}
	registry, _ := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentialsAndAuditAndAPIKeys(
		auth.NewStaticAuthenticator("admin", "tenant-a", []auth.Scope{auth.ScopeAdmin}),
		gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configstore.NewMemoryStore(), nil, nil, nil, nil, nil, manager,
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/limen/keys/0123456789abcdef/rotate", strings.NewReader(`{"scopes":["inference"]}`))
	request.Header.Set("Authorization", "Bearer admin")
	request.Header.Set("Idempotency-Key", "rotate-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !manager.rotated || !strings.Contains(response.Body.String(), "fedcba9876543210") || !strings.Contains(response.Body.String(), "rotatedsecret") {
		t.Fatalf("rotate status=%d body=%s rotated=%v", response.Code, response.Body.String(), manager.rotated)
	}
	repeat := httptest.NewRequest(http.MethodPost, "/v1/limen/keys/0123456789abcdef/rotate", strings.NewReader(`{"scopes":["inference"]}`))
	repeat.Header.Set("Authorization", "Bearer admin")
	repeat.Header.Set("Idempotency-Key", "rotate-1")
	repeatResponse := httptest.NewRecorder()
	handler.ServeHTTP(repeatResponse, repeat)
	if repeatResponse.Code != http.StatusCreated || strings.Contains(repeatResponse.Body.String(), "rotatedsecret") {
		t.Fatalf("repeat rotate status=%d body=%s", repeatResponse.Code, repeatResponse.Body.String())
	}
	conflict := httptest.NewRequest(http.MethodPost, "/v1/limen/keys/0123456789abcdef/rotate", strings.NewReader(`{"scopes":["runs:read"]}`))
	conflict.Header.Set("Authorization", "Bearer admin")
	conflict.Header.Set("Idempotency-Key", "rotate-1")
	conflictResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict || !strings.Contains(conflictResponse.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict rotate status=%d body=%s", conflictResponse.Code, conflictResponse.Body.String())
	}
}

var _ auth.APIKeyManager = (*fakeAPIKeyManager)(nil)
