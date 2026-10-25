-- Kit trees (docs/usage/jam/kits-directory.md): every kit version is
-- a content-addressed tree of blobs, shared by every kit and version. Config:
-- exported with the kits (ConfigSnapshot kit_trees / kit_blobs).
CREATE TABLE kit_blobs (
    sha256 text   PRIMARY KEY,
    size   bigint NOT NULL,
    bytes  bytea  NOT NULL
);
CREATE TABLE kit_trees (
    sha256  text  PRIMARY KEY,
    entries jsonb NOT NULL
);
