ALTER TABLE messages ADD COLUMN seq BIGSERIAL;
CREATE INDEX IF NOT EXISTS idx_messages_seq ON messages (seq);
CREATE INDEX IF NOT EXISTS idx_recipients_target_seq ON message_recipients (kind, ref);
