ALTER TABLE audit_events
    ADD COLUMN actor_id TEXT NOT NULL DEFAULT 'unknown';
