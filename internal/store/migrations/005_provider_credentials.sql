CREATE TABLE provider_credentials (
    tenant_id TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    endpoint_id TEXT NOT NULL,
    key_version TEXT NOT NULL,
    ciphertext BYTEA NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, credential_id),
    FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id)
);

CREATE UNIQUE INDEX provider_credentials_one_active ON provider_credentials (tenant_id, provider, endpoint_id) WHERE active = TRUE;
CREATE INDEX provider_credentials_tenant_idx ON provider_credentials (tenant_id, provider, endpoint_id);

ALTER TABLE provider_credentials ENABLE ROW LEVEL SECURITY;

CREATE POLICY provider_credentials_tenant_isolation ON provider_credentials USING (tenant_id = current_setting('limen.tenant_id', TRUE));
