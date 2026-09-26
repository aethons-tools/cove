-- Session kinds (session-kinds slice 1): every reservation event carries the kind
-- of session it holds, plus a standing session's name and a personal session's
-- owner. Existing rows are all dispatcher reservations, so the default tags them
-- ephemeral and per-kind counts are unchanged.
ALTER TABLE alloc_events
    ADD COLUMN session_kind  TEXT NOT NULL DEFAULT 'ephemeral',
    ADD COLUMN session_name  TEXT NOT NULL DEFAULT '',
    ADD COLUMN session_owner TEXT NOT NULL DEFAULT '';
