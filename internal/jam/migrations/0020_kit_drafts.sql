-- Kit drafts (docs/usage/jam/kits-drafts.md): at most one per kit, a mutable
-- tree whose objects live in kit_trees / kit_blobs. Working state: not in a
-- config snapshot. Removing the kit removes its draft.
CREATE TABLE kit_drafts (
    name       text        PRIMARY KEY REFERENCES kits (name) ON DELETE CASCADE,
    base       integer     NOT NULL,
    tree       text        NOT NULL,
    opened_at  timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    actor      text        NOT NULL
);
