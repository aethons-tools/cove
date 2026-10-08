-- The channel log (intercom slice 2b): every squawk is in one channel, from
-- one participant, with its audience recorded as deliveries. The legacy log
-- (target-addressed squawks) is frozen under legacy_* names, read-only; the
-- new log's seqs continue from its tail, so cursors stay monotonic. No
-- foreign keys reach the jam registry: the two migrators are independent, and
-- the intercom validates ids before it appends.
ALTER TABLE squawks RENAME TO legacy_squawks;
ALTER TABLE squawk_recipients RENAME TO legacy_squawk_recipients;
ALTER INDEX squawks_pkey RENAME TO legacy_squawks_pkey;
ALTER INDEX squawk_recipients_pkey RENAME TO legacy_squawk_recipients_pkey;

CREATE SEQUENCE squawk_log_seq;
SELECT setval('squawk_log_seq', COALESCE((SELECT max(seq) FROM legacy_squawks), 0) + 1, false);

CREATE TABLE squawks (
    seq                  bigint PRIMARY KEY DEFAULT nextval('squawk_log_seq'),
    id                   text NOT NULL UNIQUE,
    channel_id           text NOT NULL,
    from_id              text NOT NULL,
    body                 text NOT NULL,
    at                   timestamptz NOT NULL,
    reply_to             text NOT NULL DEFAULT '',
    content_type         text NOT NULL,
    origin_connection_id text NOT NULL DEFAULT '',
    origin_ref           text NOT NULL DEFAULT ''
);
ALTER SEQUENCE squawk_log_seq OWNED BY squawks.seq;
CREATE INDEX squawks_channel ON squawks (channel_id, seq);
CREATE INDEX squawks_reply ON squawks (reply_to) WHERE reply_to <> '';

-- Deliveries: who heard each squawk (its audience at append).
CREATE TABLE squawk_deliveries (
    participant_id text NOT NULL,
    seq            bigint NOT NULL REFERENCES squawks (seq) ON DELETE CASCADE,
    PRIMARY KEY (participant_id, seq)
);

-- cutover_seq: the new log's first seq; legacy squawks are below it.
CREATE TABLE intercom_settings (key text PRIMARY KEY, value bigint NOT NULL);
INSERT INTO intercom_settings (key, value)
    VALUES ('cutover_seq', COALESCE((SELECT max(seq) FROM legacy_squawks), 0) + 1);
