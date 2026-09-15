ALTER TABLE squawks ADD COLUMN seq BIGSERIAL;
CREATE INDEX IF NOT EXISTS idx_squawks_seq ON squawks (seq);
