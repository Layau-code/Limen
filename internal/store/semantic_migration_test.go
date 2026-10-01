package store

import (
	"os"
	"strings"
	"testing"
)

func TestSemanticShadowMigrationStoresNoRequestTextAndEnforcesTenantRLS(t *testing.T) {
	contents, err := os.ReadFile("migrations/014_semantic_shadow_evaluations.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"CREATE TABLE semantic_shadow_evaluations", "state_hash TEXT NOT NULL", "assessment_json JSONB NOT NULL", "FOREIGN KEY (tenant_id, decision_id)", "FORCE ROW LEVEL SECURITY", "current_setting('limen.tenant_id', TRUE)"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	for _, forbidden := range []string{"prompt TEXT", "state TEXT NOT NULL", "request_body"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration must not persist request text: found %q", forbidden)
		}
	}
}
