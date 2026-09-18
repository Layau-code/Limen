package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
)

//go:embed migrations/001_run_ledger.sql
var runLedgerMigration string

//go:embed migrations/002_decision_journal.sql
var decisionJournalMigration string

//go:embed migrations/003_api_keys.sql
var apiKeysMigration string

//go:embed migrations/004_config_versions.sql
var configVersionsMigration string

//go:embed migrations/005_provider_credentials.sql
var providerCredentialsMigration string

//go:embed migrations/006_settlement_jobs.sql
var settlementJobsMigration string

//go:embed migrations/007_accounting_operations.sql
var accountingOperationsMigration string

//go:embed migrations/008_force_rls.sql
var forceRLSMigration string

//go:embed migrations/009_config_operations.sql
var configOperationsMigration string

//go:embed migrations/010_audit_events.sql
var auditEventsMigration string

//go:embed migrations/011_api_key_management.sql
var apiKeyManagementMigration string

//go:embed migrations/012_audit_actor.sql
var auditActorMigration string

// ApplyMigrations 以版本表和单事务方式执行内置 PostgreSQL 迁移。
func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrDatabaseRequired
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS limen_schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL)`); err != nil {
		return err
	}
	for _, migration := range []struct {
		version string
		source  string
	}{
		{version: "001_run_ledger", source: runLedgerMigration},
		{version: "002_decision_journal", source: decisionJournalMigration},
		{version: "003_api_keys", source: apiKeysMigration},
		{version: "004_config_versions", source: configVersionsMigration},
		{version: "005_provider_credentials", source: providerCredentialsMigration},
		{version: "006_settlement_jobs", source: settlementJobsMigration},
		{version: "007_accounting_operations", source: accountingOperationsMigration},
		{version: "008_force_rls", source: forceRLSMigration},
		{version: "009_config_operations", source: configOperationsMigration},
		{version: "010_audit_events", source: auditEventsMigration},
		{version: "011_api_key_management", source: apiKeyManagementMigration},
		{version: "012_audit_actor", source: auditActorMigration},
	} {
		var applied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM limen_schema_migrations WHERE version=$1)`, migration.version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration.source); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO limen_schema_migrations (version, applied_at) VALUES ($1, CURRENT_TIMESTAMP)`, migration.version); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EnsureTenant 创建启动配置绑定的开发租户，重复执行保持幂等。
func EnsureTenant(ctx context.Context, db *sql.DB, tenantID string) error {
	if db == nil {
		return ErrDatabaseRequired
	}
	if tenantID == "" {
		return errors.New("tenant id is required")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO tenants (tenant_id, created_at) VALUES ($1, CURRENT_TIMESTAMP) ON CONFLICT (tenant_id) DO NOTHING`, tenantID)
	return err
}
