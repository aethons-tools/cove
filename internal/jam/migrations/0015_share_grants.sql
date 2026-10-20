-- Studio collaboration share grants (docs/usage/jam/collaboration.md): ad-hoc
-- access to one session for one user, time-boxed. Keyed by session id; removed
-- when the session ends.
CREATE TABLE share_grants (
    session_id text        NOT NULL,
    user_id    text        NOT NULL REFERENCES users(id),
    doc        jsonb       NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, user_id)
);
