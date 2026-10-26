-- Studio Manager (plan 1): the manager a standing session's state lives on.
-- Existing sessions were raised by the in-process launcher, now the manager "local".
ALTER TABLE standing_sessions ADD COLUMN manager text NOT NULL DEFAULT '';
UPDATE standing_sessions SET manager = 'local';
