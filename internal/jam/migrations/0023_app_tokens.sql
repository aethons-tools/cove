-- App tokens (docs/usage/jam/app-tokens.md): opaque bearer credentials a user
-- granted to a client (VS Code) through the consent page. Only the token's
-- hash is stored. Capped per user in the store (least recently used evicted);
-- deleted on revoke, expiry and user removal.
CREATE TABLE app_tokens (
    id           text        PRIMARY KEY,
    user_id      text        NOT NULL REFERENCES users(id),
    token_hash   text        NOT NULL UNIQUE,
    label        text        NOT NULL,
    operator_id  text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL
);
CREATE INDEX app_tokens_user_id_idx ON app_tokens (user_id);
