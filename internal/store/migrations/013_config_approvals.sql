CREATE TABLE config_approvals (
    tenant_id TEXT NOT NULL,
    approval_id TEXT NOT NULL,
    config_version TEXT NOT NULL,
    publish_idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    requested_by TEXT NOT NULL,
    approved_by TEXT,
    state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'consumed', 'rejected', 'expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, approval_id),
    UNIQUE (tenant_id, config_version, publish_idempotency_key),
    FOREIGN KEY (tenant_id, config_version) REFERENCES config_versions (tenant_id, version)
);

CREATE INDEX config_approvals_tenant_created_idx ON config_approvals (tenant_id, created_at DESC, approval_id DESC);

CREATE TABLE config_approval_operations (
    tenant_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    approval_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, endpoint, idempotency_key),
    FOREIGN KEY (tenant_id, approval_id) REFERENCES config_approvals (tenant_id, approval_id)
);

CREATE INDEX config_approval_operations_tenant_created_idx ON config_approval_operations (tenant_id, created_at DESC);

ALTER TABLE config_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE config_approvals FORCE ROW LEVEL SECURITY;
ALTER TABLE config_approval_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE config_approval_operations FORCE ROW LEVEL SECURITY;

CREATE POLICY config_approvals_tenant_isolation ON config_approvals
    USING (tenant_id = current_setting('limen.tenant_id', TRUE));
CREATE POLICY config_approval_operations_tenant_isolation ON config_approval_operations
    USING (tenant_id = current_setting('limen.tenant_id', TRUE));
