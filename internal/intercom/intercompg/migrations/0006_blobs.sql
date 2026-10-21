-- Attachment metadata (intercom attachments): an upload is a pending blob;
-- a send's reference parts attach it to the squawk in the same transaction
-- as the append. The bytes live in the blob store (internal/blobstore).
CREATE TABLE blobs (
    id          text PRIMARY KEY,
    uploader_id text NOT NULL,
    state       text NOT NULL,
    size        bigint NOT NULL,
    type        text NOT NULL,
    name        text NOT NULL,
    sha256      text NOT NULL,
    channel_id  text NOT NULL DEFAULT '',
    squawk_id   text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL
);
CREATE INDEX blobs_pending ON blobs (created_at) WHERE state = 'pending';
