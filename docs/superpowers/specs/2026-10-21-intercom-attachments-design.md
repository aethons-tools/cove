# intercom: attachments — design

**Status:** approved in brainstorming (2026-10-21); not built.
**Builds on:** the message **parts** envelope from structured asks ([`2026-10-21-intercom-asks-design.md`](2026-10-21-intercom-asks-design.md)): a reference part `{type, ref, name, size}` was reserved for this and is refused until it lands.
**Scope decisions (2026-10-21):** files up to ~25 MB now behind an interface that grows to large artifacts; **S3-compatible** object storage (MinIO first); bytes **proxied through Jam** now, presigned direct URLs later; attachments are **kept forever** like messages; relays paused (`/me` and agents only).

## 1. What and why

People and agents exchange screenshots, logs, PDFs, patches and CSVs in a conversation. Images render inline in `/me`; anything else is a download. An agent attaches files from its studio and fetches attachments into it. Today only a link both sides can reach works.

## 2. Flow: upload, then reference

1. **Upload.** `POST /me/uploads` (a participant) or `POST /squawks/uploads` (a live session): the raw body, `Content-Type`, and the filename in `X-Filename` (or `Content-Disposition`). Jam streams it into the bucket as it arrives (never buffered whole), hashing it (sha256), refusing past `max-size` (**413**). It records a **blob** `{id: blb_…, uploader, state: pending, size, type, name, sha256, created_at}` and returns `201 {ref, size, type, name}`.
2. **Send.** A squawk carries reference parts `parts: [{type, ref: "blb_…", name, size}]` (at most 10). The body is optional with attachments; Jam fills it with the file names so an older reader sees something. The send is accepted only if every ref is the **sender's own pending** blob; on append the blob becomes `attached` and records its `channel_id` and `squawk_id`. A blob attaches once — forwarding is a new upload — so who may read it is one channel's rule.
3. **Download.** `GET /me/blobs/{id}` (a participant who **can see** the blob's channel) or `GET /squawks/blobs/{id}` (a session that is a **member** of it). Jam streams from the bucket with `Content-Type`, `Content-Length`, `ETag`, and `Range` support. Raster images (`image/png|jpeg|gif|webp`) are `inline`; everything else — HTML and SVG included — is `attachment`. Always `X-Content-Type-Options: nosniff` and a restrictive `Content-Security-Policy: sandbox`, so an uploaded file never runs as Jam's origin. A pending blob is readable only by its uploader.
4. **Sweep.** Pending blobs older than 24h are deleted (bucket object, then row) by a periodic pass.

Bucket keys are `blobs/<blb_id>`, never a filename.

## 3. `/me`

- The composer takes files three ways: 📎 (picker), **paste** (a clipboard screenshot), **drag-and-drop**. Each pending file is a chip (name, size, progress, ✕); Send waits for uploads and sends their refs.
- In the stream, images render as lazy inline thumbnails (click: full size); other files as a download chip (icon, name, size). A message may be text, files, or both.
- Errors in place: 413 "too large (max N MB)", 503 "attachments are off" (no `blobs:` — the 📎 is hidden then).

## 4. Agents (MCP)

- `send(text, to?, content_type?, attach?: [paths])`: cove-master reads each file from the studio, uploads it to `/squawks/uploads`, then sends with the refs. The file leaves the studio only through Jam's brokered path: **no new egress**.
- `fetch(ref, path?)`: downloads an attachment the session can see into the studio (default `~/attachments/<blb_id>-<name>`, created 0700; existing files are not overwritten) and returns the path.
- `read` entries list attachments as parts `{type, ref, name, size}`.

## 5. Storage

- `internal/blobstore.Store`: `Put(ctx, key, r io.Reader, size int64, contentType string) error`, `Get(ctx, key, rng *Range) (io.ReadCloser, Info, error)`, `Stat(ctx, key) (Info, error)`, `Delete(ctx, key) error`; `Presign(ctx, method, key, ttl) (string, error)` is part of the interface and returns `ErrUnsupported` until direct URLs are switched on.
- Implementations: **S3** via `github.com/minio/minio-go/v7` (SigV4, path-style, multipart; works with MinIO, AWS S3, R2, GCS interop) and **Mem** (tests).
- Blob metadata: a `blobs` table in Jam's store (Postgres + MemStore), keyed by `blb_` id (a new `ident` kind). The squawk's part carries only the ref.

## 6. Config

```yaml
blobs:
  endpoint: http://minio.internal:9000
  bucket: jam-attachments
  region: us-east-1
  path-style: true            # MinIO
  max-size: 25MB              # default
  access-key-cred: blobs-key  # demanded in credentials:
  secret-key-cred: blobs-secret
```

At startup Jam checks the bucket exists and refuses to start if `blobs:` is set but unreachable or misconfigured. No `blobs:` → attachments are off (uploads 503; reference parts refused). Credentials are resolved on the host and never logged; logs carry blob ids and sizes, never names or contents.

## 7. Rules

- Upload: a `/me` participant, or a live session; ≤ `max-size`; any content type (stored as given, sniffing off).
- Send: only your own pending blobs; ≤ 10 per message.
- Download: can see the channel (person) / member of it (session); pending: uploader only. A refusal never says whether a blob exists (404 alike).
- Kept forever; no per-user quota in v1 (the size cap bounds abuse).
- Relays (paused) skip squawks with reference parts.

## 8. Data, upgrade, compatibility

Additive: the `blobs` table, the `blb` id kind, the `blobs:` config block. An older Jam ignores parts and shows the body (file names). Rollback leaves objects in the bucket unreferenced.

## 9. Testing

- `blobstore` conformance against Mem and an in-process S3 emulator (`github.com/johannesboyne/gofakes3`): put/get/range/stat/delete, missing keys.
- Handlers: size cap (413, nothing stored), ownership on send, access on download (403/404 alike), inline vs attachment and the nosniff/CSP headers, pending visibility, the 24h sweep.
- MCP: `attach` upload+send and `fetch` round-trips against a fake Jam.
- `/me`: picker/paste/drop wiring, chips, thumbnails vs download chips.
- A documented manual smoke test against MinIO.

## 10. Docs

New leaf `docs/usage/jam/attachments.md` (+ INDEX row); `serve.md` (the `blobs:` block); `intercom.md` (`attach`, `fetch`, parts); `intercom-ui.md` (composer and rendering).

## 11. Plans (tentative)

- **A:** `blobstore` (S3 + Mem + conformance), `blobs:` config, the `blobs` table and `blb` ids.
- **B:** upload/send/download routes and rules, the sweep, the agent tools.
- **C:** `/me` composer and rendering.

## 12. Later

Presigned direct upload/download (large artifacts); per-project retention; operator purge of an archived channel's files; quotas; relay rendering when relays resume.
