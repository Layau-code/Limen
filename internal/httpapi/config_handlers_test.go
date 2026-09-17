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

func TestConfigControlPublishesAndReplacesRouter(t *testing.T) {
	initial, err := gateway.NewModelRegistry([]gateway.Model{{ID: "old", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	router := gateway.NewRouter(nil, initial, gateway.Policy{RequestTimeout: time.Second, AttemptTimeout: time.Second, FailureThreshold: 1, Cooldown: time.Second})
	configs := configstore.NewMemoryStore()
	authenticator := auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeConfigsRead, auth.ScopeConfigsWrite})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(authenticator, router, nil, "tenant-a", journal.NewMemoryStore(), configs, nil)

	create := httptest.NewRequest(http.MethodPost, "/v1/limen/configs", strings.NewReader(`{"models":[{"id":"new","targets":[{"id":"new-target","provider":"anthropic","upstream_model":"claude-test"}]}]}`))
	create.Header.Set("Authorization", "Bearer secret")
	createdResponse := httptest.NewRecorder()
	handler.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created configSummary
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	publish := httptest.NewRequest(http.MethodPost, "/v1/limen/configs/"+created.Version+"/publish", nil)
	publish.Header.Set("Authorization", "Bearer secret")
	publishedResponse := httptest.NewRecorder()
	handler.ServeHTTP(publishedResponse, publish)
	if publishedResponse.Code != http.StatusOK || router.ConfigVersion() != created.Version {
		t.Fatalf("publish status=%d version=%s body=%s", publishedResponse.Code, router.ConfigVersion(), publishedResponse.Body.String())
	}
	models := router.Models()
	if len(models) != 1 || models[0].ID != "new" {
		t.Fatalf("models = %+v", models)
	}
}

func TestConfigDiffIsTenantScopedAndValueFree(t *testing.T) {
	configs := configstore.NewMemoryStore()
	before, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"id":"target","provider":"openai","upstream_model":"gpt-old"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := configs.Create(nil, "tenant-a", []byte(`{"models":[{"id":"model","targets":[{"id":"target","provider":"anthropic","upstream_model":"claude-new"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := gateway.NewModelRegistry([]gateway.Model{{ID: "model", Targets: []gateway.Target{{Provider: "openai", UpstreamModel: "gpt-test"}}}})
	handler := NewWithHealthAndRunsForTenantAuthenticatorJournalAndConfig(
		auth.NewStaticAuthenticator("secret", "tenant-a", []auth.Scope{auth.ScopeConfigsRead}),
		gateway.NewRouter(nil, registry, gateway.Policy{}), nil, "tenant-a", journal.NewMemoryStore(), configs, nil,
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/limen/configs/"+after.Version+"/diff/"+before.Version, nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "upstream_model") || strings.Contains(body, "gpt-old") || strings.Contains(body, "claude-new") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}
