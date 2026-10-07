-- Session events carry their project's and (personal) owner's ids beside the
-- name labels (intercom 1b-2b); events recorded before are backfilled at
-- serve startup (Store.BackfillIDs).
ALTER TABLE session_events ADD COLUMN project_id text NOT NULL DEFAULT '';
ALTER TABLE session_events ADD COLUMN owner_id text NOT NULL DEFAULT '';
