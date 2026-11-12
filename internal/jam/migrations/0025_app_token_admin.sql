-- App tokens minted with operator standing from now on may use the admin
-- API (docs/modules/ROOT/pages/usage/jam/app-tokens.adoc). Existing tokens stay without it.
ALTER TABLE app_tokens ADD COLUMN admin boolean NOT NULL DEFAULT false;
