CREATE TABLE IF NOT EXISTS squawks (
    id        text PRIMARY KEY,
    from_kind text NOT NULL,
    from_ref  text NOT NULL,
    body      text NOT NULL,
    at        timestamptz NOT NULL,
    project   text NOT NULL DEFAULT '',
    reply_to  text NOT NULL DEFAULT '',
    "to"      jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS squawk_recipients (
    squawk_id text NOT NULL REFERENCES squawks(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    ref        text NOT NULL,
    PRIMARY KEY (squawk_id, kind, ref)
);
CREATE INDEX IF NOT EXISTS idx_recipients_target ON squawk_recipients (kind, ref, squawk_id);
CREATE INDEX IF NOT EXISTS idx_squawks_reply_to ON squawks (reply_to) WHERE reply_to <> '';
CREATE INDEX IF NOT EXISTS idx_squawks_project_id ON squawks (project, id);
