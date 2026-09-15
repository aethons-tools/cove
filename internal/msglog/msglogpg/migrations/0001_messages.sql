CREATE TABLE IF NOT EXISTS messages (
    id        text PRIMARY KEY,
    from_kind text NOT NULL,
    from_ref  text NOT NULL,
    body      text NOT NULL,
    at        timestamptz NOT NULL,
    project   text NOT NULL DEFAULT '',
    reply_to  text NOT NULL DEFAULT '',
    "to"      jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS message_recipients (
    message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    ref        text NOT NULL,
    PRIMARY KEY (message_id, kind, ref)
);
CREATE INDEX IF NOT EXISTS idx_recipients_target ON message_recipients (kind, ref, message_id);
CREATE INDEX IF NOT EXISTS idx_messages_reply_to ON messages (reply_to) WHERE reply_to <> '';
CREATE INDEX IF NOT EXISTS idx_messages_project_id ON messages (project, id);
