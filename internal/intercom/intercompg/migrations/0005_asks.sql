-- Message parts and the asks read-model (intercom structured asks): a squawk
-- may carry typed parts (an ask event today); each ask's current state is
-- folded in the same transaction as its event's append, and can be rebuilt
-- from the log.
ALTER TABLE squawks ADD COLUMN parts jsonb;

CREATE TABLE asks (
    id         text PRIMARY KEY,
    channel_id text NOT NULL,
    asker_id   text NOT NULL,
    state      text NOT NULL,
    last_seq   bigint NOT NULL,
    doc        jsonb NOT NULL
);
CREATE INDEX asks_channel_open ON asks (channel_id) WHERE state = 'open';
CREATE INDEX asks_asker ON asks (asker_id, state);
