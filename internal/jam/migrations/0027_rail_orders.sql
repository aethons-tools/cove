-- The rail's saved order (docs/modules/ROOT/pages/usage/jam/inbox-rail.adoc#order):
-- each person's order of the Me (conversation ids) and Agents (session ids)
-- sections. Saving replaces it. Kept when the person is removed, like their
-- read cursors.
CREATE TABLE rail_orders (
    user_id    text        NOT NULL,
    section    text        NOT NULL CHECK (section IN ('me', 'agents')),
    ids        text[]      NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, section)
);
