---
summary: Attachments — people and sessions attach files (screenshots, logs, PDFs; up to `blobs.max-size`, 25 MB by default) to messages. Files are uploaded first, then referenced by a send; Jam stores them in an S3-compatible bucket and serves them access-checked against the message's channel. Kept forever, like messages.
read_when: You want people or agents to share files in a conversation, you're setting up the attachments bucket (MinIO, S3, R2), or an upload is refused (413/503) or a download 404s.
owns: the attachment flow (upload → reference → download), who may read a file, the upload/blob routes, the agent `attach`/`fetch` tools, the 24h sweep of unsent uploads, download safety headers, and the MinIO setup. Does NOT own the `blobs:` config keys — see serve.md; nor the `/me` composer and rendering — see intercom-ui.md.
prereqs: intercom.md for channels and read; serve.md for the `blobs:` block and credentials
tier: leaf
updated: 2026-10-21
---

# Attachments

A file is **uploaded first**, then a message **references** it. Jam streams
the bytes into an S3-compatible bucket and serves them back only to someone who
may read the message. Attachments are off unless serve has a
[`blobs:`](serve.md) block.

## The flow

1. **Upload** — `POST /me/uploads` (a person) or `POST /squawks/uploads` (a
   session): the raw file as the body, its `Content-Type`, and its name in
   `X-Filename`. → `201 {ref, size, type, name}` (`ref` is `blb_…`). Over the
   cap → `413`; attachments off → `503`. The upload is **pending**: only its
   uploader can read it, and it is deleted if not sent within 24 hours.
2. **Send** — `attachments: ["blb_…", …]` (at most 10) on `/me/send` or
   `POST /squawks`. Only your own pending uploads; each attaches to exactly one
   message (to share it elsewhere, upload it again). With files the text may
   be empty — it becomes the file names. A refused ref → `400`, and nothing is
   sent.
3. **Download** — `GET /me/blobs/{ref}` (a person who can see the message's
   channel) or `GET /squawks/blobs/{ref}` (a session in it). Anyone else gets
   `404`, the same as for a file that doesn't exist. Byte ranges work.

A message's attachments are its `parts` with a `ref`: `{type, ref, name,
size}` — in `read` entries and in `/me`.

## Agents

- **`send(…, attach: ["/path/in/studio", …])`** uploads each file and sends
  them with the message. The bytes leave the studio only through Jam — no
  extra egress.
- **`fetch(ref, path?)`** downloads an attachment into the studio — by default
  `~/attachments/<ref>-<name>` — and returns the path; it never overwrites a
  file.

## Safety

Downloads carry `X-Content-Type-Options: nosniff` and
`Content-Security-Policy: sandbox`. Only raster images (`png`, `jpeg`, `gif`,
`webp`) are shown inline; everything else — HTML and SVG included — downloads,
so an uploaded file never runs as Jam's page. Logs name blobs by id and size,
never by file name or content. The bucket key is `blobs/<ref>`.

## Setting up MinIO

Run MinIO (`minio server /data`, API on `:9000`), create a bucket (`mc mb
local/jam-attachments` or the console), and point serve at it with
`path-style: true` and the two keys as demanded credentials — see
[serve.md](serve.md). Jam checks the bucket at startup and refuses to start if
it's missing or unreachable. Any S3-compatible store works the same way.

## Not yet

Direct (presigned) upload and download for large files; per-project
retention; purging an archived channel's files; quotas; showing attachments on
Discord or Linear.
