-- Session events carry their project's and (personal) owner's ids beside the
-- name labels (intercom 1b-2b); events recorded before are backfilled at
-- serve startup (Store.BackfillIDs). The partial indexes keep that backfill
-- cheap once done: they hold only the rows still waiting for an id.
ALTER TABLE session_events ADD COLUMN project_id text NOT NULL DEFAULT '';
ALTER TABLE session_events ADD COLUMN owner_id text NOT NULL DEFAULT '';
CREATE INDEX session_events_project_unbackfilled ON session_events (project) WHERE project_id = '' AND project <> '';
CREATE INDEX session_events_owner_unbackfilled ON session_events (owner) WHERE owner_id = '' AND owner <> '';
