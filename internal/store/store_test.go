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

func TestProviderCredentialMigrationStoresCiphertextAndEndpointBinding(t *testing.T) {
	contents, err := os.ReadFile("migrations/005_provider_credentials.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE provider_credentials", "ciphertext BYTEA NOT NULL", "endpoint_id TEXT NOT NULL", "CREATE UNIQUE INDEX provider_credentials_one_active", "ENABLE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestSettlementJobMigrationDefinesLeaseAndTenantBinding(t *testing.T) {
	contents, err := os.ReadFile("migrations/006_settlement_jobs.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE settlement_jobs", "cost_nano_usd BIGINT", "lease_owner TEXT", "FOREIGN KEY (tenant_id, request_id)", "ENABLE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestAccountingOperationMigrationDefinesIdempotencyAndTenantBinding(t *testing.T) {
	contents, err := os.ReadFile("migrations/007_accounting_operations.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE accounting_operations", "PRIMARY KEY (tenant_id, endpoint, idempotency_key)", "FOREIGN KEY (tenant_id, request_id)", "resolution TEXT NOT NULL", "ENABLE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestConfigOperationMigrationDefinesIdempotencyAndTenantBinding(t *testing.T) {
	contents, err := os.ReadFile("migrations/009_config_operations.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE config_operations", "PRIMARY KEY (tenant_id, endpoint, idempotency_key)", "FOREIGN KEY (tenant_id, version)", "ENABLE ROW LEVEL SECURITY", "FORCE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestAuditMigrationDefinesTenantIsolation(t *testing.T) {
	contents, err := os.ReadFile("migrations/010_audit_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE audit_events", "PRIMARY KEY (tenant_id, event_id)", "request_hash TEXT", "FORCE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestAuditActorMigrationAddsNonSensitiveIdentity(t *testing.T) {
	contents, err := os.ReadFile("migrations/012_audit_actor.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"ALTER TABLE audit_events", "ADD COLUMN actor_id TEXT NOT NULL", "DEFAULT 'unknown'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestAPIKeyManagementMigrationDefinesSecureLookupAndRLS(t *testing.T) {
	contents, err := os.ReadFile("migrations/011_api_key_management.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE api_key_operations", "PRIMARY KEY (tenant_id, endpoint, idempotency_key)", "FORCE ROW LEVEL SECURITY", "ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY", "CREATE POLICY api_keys_tenant_isolation", "public.limen_lookup_api_key", "SECURITY DEFINER", "SET search_path = pg_catalog, pg_temp", "FROM public.api_keys"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestConfigApprovalMigrationDefinesBindingAndRLS(t *testing.T) {
	contents, err := os.ReadFile("migrations/013_config_approvals.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{"CREATE TABLE config_approvals", "UNIQUE (tenant_id, config_version, publish_idempotency_key)", "CREATE TABLE config_approval_operations", "PRIMARY KEY (tenant_id, endpoint, idempotency_key)", "FOREIGN KEY (tenant_id, config_version)", "FORCE ROW LEVEL SECURITY", "current_setting('limen.tenant_id'"} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestForceRLSPolicyMigrationCoversTenantTables(t *testing.T) {
	contents, err := os.ReadFile("migrations/008_force_rls.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, table := range []string{"runs", "run_requests", "attempts", "ledger_entries", "cancellation_events", "control_operations", "decision_journal", "api_keys", "provider_credentials", "config_versions", "settlement_jobs", "accounting_operations"} {
		if !strings.Contains(source, "ALTER TABLE "+table+" FORCE ROW LEVEL SECURITY") {
			t.Fatalf("force RLS missing for %s", table)
		}
	}
}
