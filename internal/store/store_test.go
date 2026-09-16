package store

import (
	"os"
	"strings"
	"testing"
)

// TestRunLedgerMigrationDefinesTenantIsolation 验证迁移中的高风险租户隔离约束。
func TestRunLedgerMigrationDefinesTenantIsolation(t *testing.T) {
	contents, err := os.ReadFile("migrations/001_run_ledger.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, table := range []string{"runs", "run_requests", "attempts", "ledger_entries", "cancellation_events"} {
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
