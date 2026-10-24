-- Setup snapshots (docs/usage/jam/inspect.md): the masked form of what Jam
-- last delivered to a studio, one row per (instance, part). Runtime state:
-- never exported; dropped with the instance.
CREATE TABLE instance_setup (
    actor_id    text        NOT NULL REFERENCES instances(actor_id) ON DELETE CASCADE,
    part        text        NOT NULL CHECK (part IN ('connector', 'context', 'egress', 'kit')),
    at          timestamptz NOT NULL,
    fingerprint text        NOT NULL,
    body        text        NOT NULL,
    PRIMARY KEY (actor_id, part)
);
