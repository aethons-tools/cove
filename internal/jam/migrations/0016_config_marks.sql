-- Config-in-git managed marks (docs/usage/jam/config-git.md): one row per
-- object the config repo manages, keyed by kind and name; doc is ConfigMark.
CREATE TABLE config_marks (
    kind       text        NOT NULL,
    name       text        NOT NULL,
    doc        jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, name)
);
