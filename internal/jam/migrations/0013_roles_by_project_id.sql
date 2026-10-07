-- Roles reference their project by id (intercom 1b-2a), so a project can be
-- renamed without touching them. Every project has an id by now (minted at
-- load since 0007); a store that skipped those releases must start one of them
-- first. Grants and instances, which name projects inside their docs, are
-- rewritten by the Go registry migration (roster_schema step 6).
ALTER TABLE roles ADD COLUMN project_id text REFERENCES projects (id);

UPDATE roles r
SET project_id = coalesce(p.id, p.doc->>'id')
FROM projects p
WHERE p.name = r.project;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM roles WHERE project_id IS NULL) THEN
        RAISE EXCEPTION 'roles reference projects without ids: start a Jam from 2026-10-06 or later once (it mints them), then upgrade';
    END IF;
END $$;

ALTER TABLE roles DROP CONSTRAINT roles_project_fkey;
ALTER TABLE roles DROP CONSTRAINT roles_pkey;
ALTER TABLE roles DROP COLUMN project;
ALTER TABLE roles ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE roles ADD PRIMARY KEY (project_id, name);
