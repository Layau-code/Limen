CREATE TABLE decision_journal (
    tenant_id TEXT NOT NULL,
    decision_id TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    plan_hash TEXT NOT NULL,
    input_json JSONB NOT NULL,
    plan_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, decision_id),
    FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id)
);

ALTER TABLE decision_journal ENABLE ROW LEVEL SECURITY;

CREATE POLICY decision_journal_tenant_isolation ON decision_journal USING (tenant_id = current_setting('limen.tenant_id', TRUE));
