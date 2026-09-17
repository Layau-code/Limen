CREATE TABLE settlement_jobs (
    tenant_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    cost_nano_usd BIGINT CHECK (cost_nano_usd >= 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    last_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, request_id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES run_requests (tenant_id, id)
);

CREATE INDEX settlement_jobs_due_idx ON settlement_jobs (tenant_id, next_attempt_at);

ALTER TABLE settlement_jobs ENABLE ROW LEVEL SECURITY;

CREATE POLICY settlement_jobs_tenant_isolation ON settlement_jobs USING (tenant_id = current_setting('limen.tenant_id', TRUE));
