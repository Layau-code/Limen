package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestMigrationHelpersRequireDatabase(t *testing.T) {
	if err := ApplyMigrations(context.Background(), nil); err != ErrDatabaseRequired {
		t.Fatalf("migration error = %v", err)
	}
	if err := EnsureTenant(context.Background(), nil, "tenant"); err != ErrDatabaseRequired {
		t.Fatalf("tenant error = %v", err)
	}
}

// TestRunLedgerMigrationDefinesTenantIsolation 验证迁移中的高风险租户隔离约束。
func TestRunLedgerMigrationDefinesTenantIsolation(t *testing.T) {
	contents, err := os.ReadFile("migrations/001_run_ledger.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, table := range []string{"runs", "run_requests", "attempts", "ledger_entries", "cancellation_events", "control_operations"} {
		if !strings.Contains(source, "CREATE TABLE "+table) || !strings.Contains(source, "tenant_id TEXT NOT NULL") {
			t.Fatalf("tenant key missing for %s", table)
		}
	}
	for _, required := range []string{
		"UNIQUE (tenant_id, endpoint, idempotency_key)",
		"UNIQUE (tenant_id, request_id)",
		"ENABLE ROW LEVEL SECURITY",
		"current_setting('limen.tenant_id'",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestDecisionJournalMigrationDefinesTenantIsolation(t *testing.T) {
	contents, err := os.ReadFile("migrations/002_decision_journal.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE decision_journal", "JSONB NOT NULL", "PRIMARY KEY (tenant_id, decision_id)", "ENABLE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestAPIKeyMigrationStoresOnlyDigestAndScopes(t *testing.T) {
	contents, err := os.ReadFile("migrations/003_api_keys.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE api_keys", "digest BYTEA NOT NULL", "scopes JSONB NOT NULL", "tenant_id TEXT NOT NULL"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestConfigVersionMigrationDefinesTenantIsolation(t *testing.T) {
	contents, err := os.ReadFile("migrations/004_config_versions.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE config_versions", "document JSONB NOT NULL", "PRIMARY KEY (tenant_id, version)", "CREATE UNIQUE INDEX config_versions_one_published", "ENABLE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}
