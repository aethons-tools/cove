-- Web Push subscriptions (docs/usage/jam): one row per browser endpoint,
-- owned by one user. Upserted by endpoint; capped per user in the store;
-- deleted on unsubscribe, a 404/410 from the push service, and RemoveUser.
CREATE TABLE push_subscriptions (
    endpoint   text        PRIMARY KEY,
    user_id    text        NOT NULL REFERENCES users(id),
    p256dh     text        NOT NULL,
    auth       text        NOT NULL,
    user_agent text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    last_ok_at timestamptz NOT NULL
);
CREATE INDEX push_subscriptions_user_id_idx ON push_subscriptions (user_id);
