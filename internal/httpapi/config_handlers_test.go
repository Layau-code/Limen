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
