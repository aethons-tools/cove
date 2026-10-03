-- Jam-wide settings, one jsonb doc per key. Key 'context' holds the Jam
-- session-context layer (sessionctx.Layer).
CREATE TABLE jam_settings (
    key        text PRIMARY KEY,
    doc        jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
