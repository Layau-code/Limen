package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCredentialChangeListenerRequiresDatabaseURL(t *testing.T) {
	if _, err := NewCredentialChangeListener(" "); err == nil {
		t.Fatal("expected database URL validation error")
	}
}

func TestParseCredentialChangeRejectsIncompletePayload(t *testing.T) {
	if _, err := parseCredentialChange(`{"tenant_id":"tenant-a","provider":"openai"}`); err == nil {
		t.Fatal("expected incomplete credential change error")
	}
	change, err := parseCredentialChange(`{"tenant_id":"tenant-a","provider":"openai","endpoint_id":"endpoint-a","revoked":true}`)
	if err != nil || !change.Revoked {
		t.Fatalf("change=%+v err=%v", change, err)
	}
}

func TestCredentialChangePayloadDoesNotContainSecret(t *testing.T) {
	encoded, err := json.Marshal(CredentialChange{TenantID: "tenant-a", Provider: "openai", EndpointID: "endpoint-a"})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	if strings.Contains(payload, "secret") {
		t.Fatal("credential notification contains secret field")
	}
}
