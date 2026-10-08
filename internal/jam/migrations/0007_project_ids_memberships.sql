-- Project ids (intercom slice 1a-2). Nullable at first: rows written before
-- this change get their id from PostgresStore.load (ensureProjectIDs), which
-- mints it in Go (internal/ident) and persists it. Memberships are (project,
-- user) pairs; removing a user deletes theirs, and removing a project is
-- refused while it has any.
ALTER TABLE projects ADD COLUMN id text UNIQUE REFERENCES participants(id);

CREATE TABLE memberships (
    project_id text NOT NULL REFERENCES projects(id),
    user_id    text NOT NULL REFERENCES users(id),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX memberships_user ON memberships (user_id);
