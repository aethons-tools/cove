-- Standing sessions (intercom slice 1b): which session a declared standing
-- session (project, role, name) currently is. A reset replaces the entry with
-- a new session; restarts and upgrades keep it. Seeded once by the registry
-- migration (roster_schema 4) with the pre-registry ids live sessions run under.
CREATE TABLE standing_sessions (
    project_id text NOT NULL REFERENCES projects(id),
    role       text NOT NULL,
    name       text NOT NULL,
    session_id text NOT NULL UNIQUE,
    PRIMARY KEY (project_id, role, name)
);
