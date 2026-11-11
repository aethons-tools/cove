-- Studio Manager registry (docs/modules/ROOT/pages/usage/jam/studio-managers.adoc): one row per
-- registered manager name; only the token's hash is stored.
CREATE TABLE managers (
    name       text        PRIMARY KEY,
    token_hash text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL
);
