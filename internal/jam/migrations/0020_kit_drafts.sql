-- Kit drafts (docs/usage/jam/kits-drafts.md): at most one per kit, a mutable
-- tree whose objects live in kit_trees / kit_blobs. Working state: not in a
-- config snapshot. Removing the kit removes its draft; a draft's tree must be
-- stored (GC keeps it while the draft is open).
CREATE TABLE kit_drafts (
    name       text        PRIMARY KEY REFERENCES kits (name) ON DELETE CASCADE,
    base       integer     NOT NULL,
    tree       text        NOT NULL REFERENCES kit_trees (sha256),
    opened_at  timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    actor      text        NOT NULL
);
