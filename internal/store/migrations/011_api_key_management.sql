CREATE TABLE api_key_operations (
    tenant_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    public_prefix TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, endpoint, idempotency_key),
    FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id)
);

CREATE INDEX api_key_operations_tenant_created_idx ON api_key_operations (tenant_id, created_at DESC);

ALTER TABLE api_key_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_key_operations FORCE ROW LEVEL SECURITY;

CREATE POLICY api_key_operations_tenant_isolation ON api_key_operations
    USING (tenant_id = current_setting('limen.tenant_id', TRUE));

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys NO FORCE ROW LEVEL SECURITY;

CREATE POLICY api_keys_tenant_isolation ON api_keys
    USING (tenant_id = current_setting('limen.tenant_id', TRUE));

CREATE OR REPLACE FUNCTION public.limen_lookup_api_key(key_prefix TEXT)
RETURNS TABLE (tenant_id TEXT, digest BYTEA, scopes JSONB, active BOOLEAN, expires_at TIMESTAMPTZ)
LANGUAGE SQL
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT keys.tenant_id, keys.digest, keys.scopes, keys.active, keys.expires_at
    FROM public.api_keys AS keys
    WHERE keys.public_prefix = key_prefix
$$;
