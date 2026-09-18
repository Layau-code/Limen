package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huz/limen/internal/approval"
	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/journal"
)

type approvalTestAuthenticator map[string]auth.Principal

func (authenticator approvalTestAuthenticator) AuthenticateContext(_ context.Context, header string) (auth.Principal, bool, error) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	principal, found := authenticator[token]
	if !ok || !found {
		return auth.Principal{}, false, nil
	}
	return principal, true, nil
}

func TestConfigApprovalRequiresDistinctActorAndBindsPublish(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, registry, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second})
	configs := configstore.NewMemoryStore()
	approvals := approval.NewMemoryStore()
	authenticator := approvalTestAuthenticator{
		"requester": {TenantID: "tenant-a", Subject: "actor-a", Scopes: map[auth.Scope]struct{}{auth.ScopeConfigsRead: {}, auth.ScopeConfigsWrite: {}}},
		"approver":  {TenantID: "tenant-a", Subject: "actor-b", Scopes: map[auth.Scope]struct{}{auth.ScopeConfigsRead: {}, auth.ScopeConfigsWrite: {}}},
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentialsAndAuditAndAPIKeysAndApproval(authenticator, router, nil, "tenant-a", journal.NewMemoryStore(), configs, nil, nil, nil, nil, nil, nil, approvals, true)

	create := httptest.NewRequest(http.MethodPost, "/v1/limen/configs", strings.NewReader(`{"models":[{"id":"new","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-new"}]}]}`))
	create.Header.Set("Authorization", "Bearer requester")
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var summary configSummary
	if err := json.Unmarshal(created.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}

	requestApproval := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+summary.Version+"/approvals", strings.NewReader(`{"publish_idempotency_key":"publish-1"}`))
	requestApproval.Header.Set("Authorization", "Bearer requester")
	requestApproval.Header.Set("Idempotency-Key", "approval-1")
	requested := httptest.NewRecorder()
	handler.ServeHTTP(requested, requestApproval)
	if requested.Code != http.StatusCreated {
		t.Fatalf("approval request status=%d body=%s", requested.Code, requested.Body.String())
	}
	var record approval.Record
	if err := json.Unmarshal(requested.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}

	missingApproval := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+summary.Version+"/publish", nil)
	missingApproval.Header.Set("Authorization", "Bearer approver")
	missingApproval.Header.Set("Idempotency-Key", "publish-1")
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missingApproval)
	if missingResponse.Code != http.StatusBadRequest || !strings.Contains(missingResponse.Body.String(), "approval_required") {
		t.Fatalf("missing approval status=%d body=%s", missingResponse.Code, missingResponse.Body.String())
	}

	pendingPublish := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+summary.Version+"/publish", nil)
	pendingPublish.Header.Set("Authorization", "Bearer approver")
	pendingPublish.Header.Set("Idempotency-Key", "publish-1")
	pendingPublish.Header.Set("X-Limen-Approval-ID", record.ID)
	pendingResponse := httptest.NewRecorder()
	handler.ServeHTTP(pendingResponse, pendingPublish)
	if pendingResponse.Code != http.StatusConflict || !strings.Contains(pendingResponse.Body.String(), "approval_state_conflict") {
		t.Fatalf("pending publish status=%d body=%s", pendingResponse.Code, pendingResponse.Body.String())
	}

	selfApprove := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+summary.Version+"/approvals/"+record.ID+"/approve", strings.NewReader(`{}`))
	selfApprove.Header.Set("Authorization", "Bearer requester")
	selfApprove.Header.Set("Idempotency-Key", "approve-self")
	selfApproved := httptest.NewRecorder()
	handler.ServeHTTP(selfApproved, selfApprove)
	if selfApproved.Code != http.StatusConflict || !strings.Contains(selfApproved.Body.String(), "approval_actor_not_distinct") {
		t.Fatalf("self approval status=%d body=%s", selfApproved.Code, selfApproved.Body.String())
	}

	approve := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+summary.Version+"/approvals/"+record.ID+"/approve", strings.NewReader(`{}`))
	approve.Header.Set("Authorization", "Bearer approver")
	approve.Header.Set("Idempotency-Key", "approve-1")
	approved := httptest.NewRecorder()
	handler.ServeHTTP(approved, approve)
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"state":"approved"`) {
		t.Fatalf("approve status=%d body=%s", approved.Code, approved.Body.String())
	}

	publish := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+summary.Version+"/publish", nil)
	publish.Header.Set("Authorization", "Bearer approver")
	publish.Header.Set("Idempotency-Key", "publish-1")
	publish.Header.Set("X-Limen-Approval-ID", record.ID)
	published := httptest.NewRecorder()
	handler.ServeHTTP(published, publish)
	if published.Code != http.StatusOK || router.ConfigVersion() != summary.Version {
		t.Fatalf("publish status=%d version=%s body=%s", published.Code, router.ConfigVersion(), published.Body.String())
	}

	status := httptest.NewRequest(http.MethodGet, "/v1/limen/configs/"+summary.Version+"/approvals/"+record.ID, nil)
	status.Header.Set("Authorization", "Bearer approver")
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, status)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"state":"consumed"`) {
		t.Fatalf("approval status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
}

func TestConfigApprovalFlagOffKeepsExistingPublishFlow(t *testing.T) {
	registry, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	configs := configstore.NewMemoryStore()
	record, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"new","targets":[{"provider":"openai","upstream_model":"gpt-new"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentialsAndAuditAndAPIKeysAndApproval(auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeConfigsWrite}), gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configs, nil, nil, nil, nil, nil, nil, nil, false)
	publish := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+record.Version+"/publish", nil)
	publish.Header.Set("Authorization", "Bearer secret")
	publish.Header.Set("Idempotency-Key", "publish-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, publish)
	if response.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", response.Code, response.Body.String())
	}
}
