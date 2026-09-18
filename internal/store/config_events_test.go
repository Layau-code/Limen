package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigChangeListenerRequiresDatabaseURL(t *testing.T) {
	if _, err := NewConfigChangeListener(" "); err == nil {
		t.Fatal("expected database URL validation error")
	}
}

func TestParseConfigChangeValidatesMetadata(t *testing.T) {
	if _, err := parseConfigChange(`{"tenant_id":"tenant-a"}`); err == nil {
		t.Fatal("expected incomplete config change error")
	}
	change, err := parseConfigChange(`{"tenant_id":"tenant-a","version":"sha256:abc"}`)
	if err != nil || change.Version != "sha256:abc" {
		t.Fatalf("change=%+v err=%v", change, err)
	}
	if _, err := parseConfigChange(`{"tenant_id":"tenant-a","version":"sha256:abc","document":"secret"}`); err == nil {
		t.Fatal("config notification accepted document field")
	}
}

func TestConfigChangePayloadContainsOnlyMetadata(t *testing.T) {
	encoded, err := json.Marshal(ConfigChange{TenantID: "tenant-a", Version: "sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "prompt") {
		t.Fatal("config notification contains sensitive data")
	}
}
