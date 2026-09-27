CREATE TABLE alloc_events (
    global_seq      BIGSERIAL PRIMARY KEY,
    category        TEXT NOT NULL,
    stream_id       TEXT NOT NULL,
    stream_revision BIGINT NOT NULL,
    kind            TEXT NOT NULL,
    reservation_id  TEXT NOT NULL DEFAULT '',
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    data            JSONB NOT NULL DEFAULT '{}',
    UNIQUE (stream_id, stream_revision)
);
CREATE INDEX idx_alloc_events_stream ON alloc_events (stream_id, stream_revision);
CREATE INDEX idx_alloc_events_category ON alloc_events (category, global_seq);
