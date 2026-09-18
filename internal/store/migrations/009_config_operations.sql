CREATE TABLE config_operations (
    tenant_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    version TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, endpoint, idempotency_key),
    FOREIGN KEY (tenant_id, version) REFERENCES config_versions (tenant_id, version)
);

CREATE INDEX config_operations_tenant_created_idx ON config_operations (tenant_id, created_at DESC);

ALTER TABLE config_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE config_operations FORCE ROW LEVEL SECURITY;

CREATE POLICY config_operations_tenant_isolation ON config_operations
    USING (tenant_id = current_setting('limen.tenant_id', TRUE));
