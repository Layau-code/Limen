CREATE TABLE semantic_shadow_evaluations (
    tenant_id TEXT NOT NULL,
    decision_id TEXT NOT NULL,
    model_version TEXT NOT NULL,
    template_version TEXT NOT NULL,
    mapping_version TEXT NOT NULL,
    state_hash TEXT NOT NULL,
    state_length INTEGER NOT NULL CHECK (state_length >= 0),
    truncated BOOLEAN NOT NULL,
    language TEXT NOT NULL,
    status TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    assessment_json JSONB NOT NULL,
    counterfactual_targets JSONB NOT NULL DEFAULT '[]'::jsonb,
    counterfactual_plan_hash TEXT NOT NULL DEFAULT '',
    input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cost_nano_usd BIGINT NOT NULL DEFAULT 0 CHECK (cost_nano_usd >= 0),
    cost_known BOOLEAN NOT NULL DEFAULT FALSE,
    duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, decision_id, model_version, template_version, mapping_version),
    FOREIGN KEY (tenant_id, decision_id) REFERENCES decision_journal (tenant_id, decision_id)
);

CREATE INDEX semantic_shadow_evaluations_tenant_created_idx ON semantic_shadow_evaluations (tenant_id, created_at DESC, decision_id DESC);
ALTER TABLE semantic_shadow_evaluations ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_shadow_evaluations FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_shadow_evaluations_tenant_isolation ON semantic_shadow_evaluations
    USING (tenant_id = current_setting('limen.tenant_id', TRUE));
