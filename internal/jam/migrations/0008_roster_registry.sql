-- Registry-backed roster (intercom slice 1a-3a). A membership carries the
-- user's per-project delivery addresses. legacy_human_aliases records which
-- user each pre-registry roster human (project name, human name) became; the
-- humans migration writes it once and nothing changes it afterwards. The
-- migration itself is Go (PostgresStore.load), marked done by the jam_settings
-- row 'roster_schema'.
ALTER TABLE memberships ADD COLUMN delivery jsonb NOT NULL DEFAULT '[]';

CREATE TABLE legacy_human_aliases (
    project_name text NOT NULL,
    human_name   text NOT NULL,
    user_id      text NOT NULL REFERENCES users(id),
    PRIMARY KEY (project_name, human_name)
);
