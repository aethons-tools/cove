-- Projects are keyed by id and removed as tombstones (intercom 1b-3): a
-- removed project's row stays (status 'removed') so its id keeps resolving,
-- and only live projects' names are unique, so a name can be reused and a
-- project renamed by updating one row. Every reference is by id since 0013.
UPDATE projects SET id = doc->>'id' WHERE id IS NULL AND doc ? 'id';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM projects WHERE id IS NULL) THEN
        RAISE EXCEPTION 'projects without ids: start a Jam from 2026-10-06 or later once (it mints them), then upgrade';
    END IF;
END $$;

ALTER TABLE projects ADD COLUMN status text NOT NULL DEFAULT 'live';
ALTER TABLE projects DROP CONSTRAINT projects_pkey;
ALTER TABLE projects ALTER COLUMN id SET NOT NULL;
ALTER TABLE projects ADD PRIMARY KEY (id);
CREATE UNIQUE INDEX projects_live_name ON projects (name) WHERE status = 'live';
