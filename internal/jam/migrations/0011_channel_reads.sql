-- The /me unread cursor on the channel log (intercom slice 2b): how far a
-- participant has read each channel. Forward-only. intercom_unread_cursors
-- (the legacy log's, keyed by synthetic names) is left unused.
CREATE TABLE channel_reads (
    participant_id text NOT NULL,
    channel_id     text NOT NULL,
    seq            bigint NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (participant_id, channel_id)
);
