CREATE TABLE api_keys (
    public_prefix TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    digest BYTEA NOT NULL,
    scopes JSONB NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id)
);

CREATE INDEX api_keys_tenant_idx ON api_keys (tenant_id);
