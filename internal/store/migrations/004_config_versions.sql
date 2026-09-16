CREATE TABLE config_versions (
    tenant_id TEXT NOT NULL,
    version TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('draft', 'published', 'superseded')),
    document JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, version),
    FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id)
);

CREATE INDEX config_versions_tenant_created_idx ON config_versions (tenant_id, created_at DESC);
CREATE UNIQUE INDEX config_versions_one_published ON config_versions (tenant_id) WHERE state = 'published';

ALTER TABLE config_versions ENABLE ROW LEVEL SECURITY;

CREATE POLICY config_versions_tenant_isolation ON config_versions USING (tenant_id = current_setting('limen.tenant_id', TRUE));
