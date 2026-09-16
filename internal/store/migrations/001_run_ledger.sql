CREATE TABLE tenants (
    tenant_id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE runs (
    tenant_id TEXT NOT NULL,
    id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active','completing','completed','cancelled','deadline_exceeded','soft_budget_exhausted','suspended_accounting')),
    soft_budget_nano_usd BIGINT NOT NULL CHECK (soft_budget_nano_usd >= 0),
    settled_cost_nano_usd BIGINT NOT NULL CHECK (settled_cost_nano_usd >= 0),
    deadline TIMESTAMPTZ,
    max_parallelism INTEGER NOT NULL CHECK (max_parallelism >= 0),
    in_flight INTEGER NOT NULL CHECK (in_flight >= 0),
    strategy TEXT NOT NULL,
    config_version TEXT NOT NULL,
    complete_requested BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id)
);

CREATE TABLE run_requests (
    tenant_id TEXT NOT NULL,
    id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    state TEXT NOT NULL,
    settlement_status TEXT NOT NULL,
    decision_id TEXT,
    ledger_recorded BOOLEAN NOT NULL DEFAULT FALSE,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, endpoint, idempotency_key),
    FOREIGN KEY (tenant_id, run_id) REFERENCES runs (tenant_id, id)
);

CREATE TABLE attempts (
    tenant_id TEXT NOT NULL,
    id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    state TEXT NOT NULL,
    provider_request_id TEXT,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES run_requests (tenant_id, id)
);

CREATE TABLE ledger_entries (
    tenant_id TEXT NOT NULL,
    id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    cost_nano_usd BIGINT NOT NULL CHECK (cost_nano_usd >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, request_id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES run_requests (tenant_id, id)
);

CREATE TABLE cancellation_events (
    tenant_id TEXT NOT NULL,
    id BIGSERIAL,
    run_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, run_id) REFERENCES runs (tenant_id, id)
);

CREATE TABLE control_operations (
    tenant_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, endpoint, idempotency_key),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES runs (tenant_id, id)
);

ALTER TABLE runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE run_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE cancellation_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE control_operations ENABLE ROW LEVEL SECURITY;

CREATE POLICY runs_tenant_isolation ON runs USING (tenant_id = current_setting('limen.tenant_id', TRUE));
CREATE POLICY run_requests_tenant_isolation ON run_requests USING (tenant_id = current_setting('limen.tenant_id', TRUE));
CREATE POLICY attempts_tenant_isolation ON attempts USING (tenant_id = current_setting('limen.tenant_id', TRUE));
CREATE POLICY ledger_entries_tenant_isolation ON ledger_entries USING (tenant_id = current_setting('limen.tenant_id', TRUE));
CREATE POLICY cancellation_events_tenant_isolation ON cancellation_events USING (tenant_id = current_setting('limen.tenant_id', TRUE));
CREATE POLICY control_operations_tenant_isolation ON control_operations USING (tenant_id = current_setting('limen.tenant_id', TRUE));
