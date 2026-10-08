-- Identity registry (intercom slice 1a-1). Surrogate ids (internal/ident)
-- for users, connections and accounts. participants is the supertable every
-- id-bearing entity inserts into, so later tables can foreign-key any kind.
-- Removal is a tombstone (status = 'removed'); partial unique indexes keep
-- live names unique while letting a removed name be reused. Each table's doc
-- is the source of truth for the cache; the other columns exist to index and
-- constrain.
CREATE TABLE participants (
    id   text PRIMARY KEY,
    kind text NOT NULL
);

CREATE TABLE users (
    id         text PRIMARY KEY REFERENCES participants(id),
    name       text NOT NULL,
    status     text NOT NULL,
    doc        jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_live_name ON users (name) WHERE status = 'live';

-- Login and OIDC uniqueness across live users. A removed user's rows are
-- deleted, freeing them.
CREATE TABLE user_logins (
    login   text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id)
);
CREATE TABLE user_oidc (
    issuer  text NOT NULL,
    subject text NOT NULL,
    user_id text NOT NULL REFERENCES users(id),
    PRIMARY KEY (issuer, subject)
);

CREATE TABLE connections (
    id         text PRIMARY KEY REFERENCES participants(id),
    kind       text NOT NULL,
    name       text NOT NULL,
    status     text NOT NULL,
    doc        jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX connections_live_name ON connections (name) WHERE status = 'live';

CREATE TABLE accounts (
    id            text PRIMARY KEY REFERENCES participants(id),
    connection_id text NOT NULL REFERENCES connections(id),
    service_uid   text,
    handle        text,
    user_id       text REFERENCES users(id),
    status        text NOT NULL,
    doc           jsonb NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX accounts_uid ON accounts (connection_id, service_uid) WHERE service_uid IS NOT NULL;
CREATE UNIQUE INDEX accounts_live_handle ON accounts (connection_id, handle) WHERE handle IS NOT NULL AND status = 'live';
