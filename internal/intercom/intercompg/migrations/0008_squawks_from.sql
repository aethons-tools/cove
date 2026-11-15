-- A session's activity (the session view): the squawks it authored, by seq.
CREATE INDEX squawks_from ON squawks (from_id, seq);
