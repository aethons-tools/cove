-- Projects become first-class: every project a role or actor grant names gets a
-- record (an empty one for projects that existed only as a name), then
-- roles.project must reference a real project. Grants live inside the actors
-- doc, so the store enforces their project reference instead of a constraint.
INSERT INTO projects (name, doc)
SELECT name, jsonb_build_object('name', name, 'roster', '{}'::jsonb)
FROM (
    SELECT project AS name FROM roles
    UNION
    SELECT coalesce(nullif(g->>'project', ''), 'default')
    FROM actors,
         jsonb_array_elements(CASE WHEN jsonb_typeof(doc->'grants') = 'array'
                                   THEN doc->'grants' ELSE '[]'::jsonb END) AS g
) refs
ON CONFLICT (name) DO NOTHING;

ALTER TABLE roles
    ADD CONSTRAINT roles_project_fkey FOREIGN KEY (project) REFERENCES projects (name);
