-- Phase 1 control-plane schema: one document table per aggregate. The doc is the
-- Go struct marshaled with its json tags; key columns give DB-enforced
-- uniqueness. version/updated_at are unused by Phase-1 logic (the B hook).

CREATE TABLE IF NOT EXISTS actors (
    token_hash text PRIMARY KEY,
    id         text NOT NULL UNIQUE,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS roles (
    project    text NOT NULL,
    name       text NOT NULL,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project, name)
);

CREATE TABLE IF NOT EXISTS kits (
    name       text PRIMARY KEY,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS instances (
    actor_id   text PRIMARY KEY,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS destinations (
    name       text PRIMARY KEY,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS projects (
    name       text PRIMARY KEY,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
