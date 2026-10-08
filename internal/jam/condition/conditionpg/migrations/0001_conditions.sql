-- 0001_conditions.sql — operator-attention condition occurrences (one row per
-- (key, since)); open rows have resolved_at NULL. See docs/usage/jam/monitoring.md.
CREATE TABLE IF NOT EXISTS attention_conditions (
    key         text        NOT NULL,
    since       timestamptz NOT NULL,
    doc         jsonb       NOT NULL,
    resolved_at timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key, since)
);
CREATE INDEX IF NOT EXISTS attention_conditions_resolved ON attention_conditions (resolved_at);
