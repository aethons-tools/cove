-- A unit of work's record (docs/modules/ROOT/pages/usage/jam/work-units.adoc):
-- its issue, its state as last reported, the sessions that have been its
-- worker, and the pull requests reported for it. Keyed by its work log.
CREATE TABLE work_units (
    channel_id text PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    title      text NOT NULL DEFAULT '',
    url        text NOT NULL DEFAULT '',
    state      text NOT NULL DEFAULT '' CHECK (state IN ('', 'in-progress', 'in-review', 'needs-input', 'blocked', 'done')),
    state_at   timestamptz
);

-- Each session's turn as the unit's worker, in the order they started (ord).
-- started_at NULL = not known (backfilled); stopped with stopped_at NULL =
-- ended at a time not known.
CREATE TABLE work_unit_workers (
    channel_id text NOT NULL REFERENCES work_units(channel_id) ON DELETE CASCADE,
    ord        integer NOT NULL,
    session_id text NOT NULL,
    label      text NOT NULL,
    started_at timestamptz,
    stopped    boolean NOT NULL DEFAULT false,
    stopped_at timestamptz,
    PRIMARY KEY (channel_id, ord)
);

-- The pull requests reported for the unit, in the order first reported (ord).
CREATE TABLE work_unit_prs (
    channel_id  text NOT NULL REFERENCES work_units(channel_id) ON DELETE CASCADE,
    ord         integer NOT NULL,
    url         text NOT NULL,
    number      integer NOT NULL DEFAULT 0,
    title       text NOT NULL DEFAULT '',
    state       text NOT NULL CHECK (state IN ('open', 'merged', 'closed')),
    reported_at timestamptz NOT NULL,
    PRIMARY KEY (channel_id, ord),
    UNIQUE (channel_id, url)
);

-- Backfill the workers from each work log's session members: a past member
-- (left_seq set) stopped, the log's current worker didn't. A member's times
-- are known only while its instance is: it started when it was raised.
INSERT INTO work_units (channel_id)
SELECT DISTINCT m.channel_id
FROM channel_members m
JOIN channels c ON c.id = m.channel_id
JOIN participants p ON p.id = m.participant_id
WHERE c.kind = 'work' AND p.kind = 'ses'
  AND (m.left_seq IS NOT NULL OR c.doc->>'worker' = m.participant_id);

INSERT INTO work_unit_workers (channel_id, ord, session_id, label, started_at, stopped)
SELECT m.channel_id,
       row_number() OVER (PARTITION BY m.channel_id ORDER BY m.joined_seq, m.participant_id) - 1,
       m.participant_id,
       COALESCE(NULLIF(i.doc->>'name', ''), m.participant_id),
       (i.doc->>'raised_at')::timestamptz,
       m.left_seq IS NOT NULL
FROM channel_members m
JOIN channels c ON c.id = m.channel_id
JOIN participants p ON p.id = m.participant_id
LEFT JOIN instances i ON i.actor_id = m.participant_id
WHERE c.kind = 'work' AND p.kind = 'ses'
  AND (m.left_seq IS NOT NULL OR c.doc->>'worker' = m.participant_id);
