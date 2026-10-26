-- Studio Manager registry (docs/usage/jam/studio-managers.md): one row per
-- registered manager name; only the token's hash is stored.
CREATE TABLE managers (
    name       text        PRIMARY KEY,
    token_hash text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL
);
