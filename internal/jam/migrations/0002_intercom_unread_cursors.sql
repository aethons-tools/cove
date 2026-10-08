-- The participant intercom UI's per-(participant, channel) unread cursor: a
-- monotonic last-seen append Seq keyed by a free-form participant and channel
-- id. Additive; no change to the existing aggregate tables. CommitUnread
-- upserts forward-only (GREATEST); the UI read model reads it back.

CREATE TABLE IF NOT EXISTS intercom_unread_cursors (
    participant text NOT NULL,
    channel     text NOT NULL,
    seq         bigint NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (participant, channel)
);
