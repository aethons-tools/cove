-- The channel registry (intercom slice 2a): a channel is a conversation the
-- intercom owns as a row; its kind (chat, ticket, room) is the source that
-- gives it meaning. Roster channels become rooms (roster_schema 5). A
-- project's channels go with it until channels carry history (slice 2b).
CREATE TABLE channels (
    id         text PRIMARY KEY REFERENCES participants(id),
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    key        text NOT NULL,
    status     text NOT NULL,
    doc        jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX channels_live_key ON channels (project_id, kind, key) WHERE status = 'live';

-- Bindings: the surfaces a channel is rendered on. A (connection, ref) feeds
-- ingress to at most one live channel. live mirrors the channel's status so
-- the partial index can see it.
CREATE TABLE channel_bindings (
    channel_id    text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    connection_id text NOT NULL REFERENCES connections(id),
    ref           text NOT NULL,
    mode          text NOT NULL CHECK (mode IN ('both', 'egress')),
    live          boolean NOT NULL,
    PRIMARY KEY (channel_id, connection_id, ref)
);
CREATE UNIQUE INDEX channel_bindings_ingress ON channel_bindings (connection_id, ref) WHERE mode = 'both' AND live;

-- Membership, from the log position a participant joined at; left_seq NULL =
-- a current member.
CREATE TABLE channel_members (
    channel_id     text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    participant_id text NOT NULL REFERENCES participants(id),
    joined_seq     bigint NOT NULL,
    left_seq       bigint,
    PRIMARY KEY (channel_id, participant_id, joined_seq)
);
CREATE INDEX channel_members_current ON channel_members (participant_id) WHERE left_seq IS NULL;
