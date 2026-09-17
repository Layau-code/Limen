CREATE TABLE accounting_operations (
    tenant_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    request_id TEXT NOT NULL,
    resolution TEXT NOT NULL CHECK (resolution IN ('cost', 'accept_unknown')),
    cost_nano_usd BIGINT CHECK (cost_nano_usd IS NULL OR cost_nano_usd >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, endpoint, idempotency_key),
    FOREIGN KEY (tenant_id, request_id) REFERENCES run_requests (tenant_id, id)
);

ALTER TABLE accounting_operations ENABLE ROW LEVEL SECURITY;

CREATE POLICY accounting_operations_tenant_isolation ON accounting_operations
    USING (tenant_id = current_setting('limen.tenant_id', TRUE));
