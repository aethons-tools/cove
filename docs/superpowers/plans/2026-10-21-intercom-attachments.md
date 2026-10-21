# intercom attachments — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** People and agents attach files (≤ 25 MB) to messages; files live in S3-compatible storage behind Jam, which proxies uploads and access-checked downloads.

**Architecture:** `internal/blobstore` is the byte store (S3 via minio-go; Mem for tests). Blob **metadata** lives in the intercom store (`intercom.Store`: Mem + `intercompg`), so a send binds its reference parts to the squawk atomically inside the serialized append — the same pattern as the ask fold. `jam.Attachments` holds the policy (upload caps, who may read) and serves bytes; `/squawks`, `/me`, cove-master and the `/me` UI use it.

**Tech Stack:** Go; `github.com/minio/minio-go/v7`; `github.com/johannesboyne/gofakes3` (tests only); pgx; html/template + htmx.

**Spec:** [`docs/superpowers/specs/2026-10-21-intercom-attachments-design.md`](../specs/2026-10-21-intercom-attachments-design.md). Ruling while planning: blob metadata lives in the intercom store, not Jam's registry store (spec §5 said "Jam's store") — attaching must be atomic with the append; cost if wrong: one table moves.

## Global Constraints

- Per-file cap `blobs.max-size`, default 25 MB; ≤ 10 attachments per message.
- Bucket key `blobs/<blb_id>`; never a filename.
- Downloads: `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox`; inline only for `image/png|jpeg|gif|webp`, else `Content-Disposition: attachment`.
- Pending blobs: readable by the uploader only; swept after 24h.
- Refusals never reveal existence: 404 alike for missing/forbidden downloads.
- Credentials resolved on the host, never logged; logs carry blob ids and sizes, never names or contents.
- No `blobs:` config ⇒ attachments off: uploads 503, reference parts refused.
- Relays skip any squawk with parts (already true).
- Hermetic tests; Postgres conformance behind `-tags integration`.

## Review Focus

1. An upload over the cap → 413 and **nothing** left in the bucket or table (streamed limit, cleanup on error).
2. Someone sends a ref they didn't upload, or one already attached → refused, the other message untouched.
3. A person who can't see the channel (or a session not in it) requests a blob → 404, same as a missing id.
4. An uploaded `text/html` or `image/svg+xml` file is downloaded, never rendered inline.
5. A crash between bucket Put and row insert leaves an orphan object → the sweep removes orphans it can identify (pending rows past 24h); objects with no row are tolerated (documented).

## PR split

- **PR A (Tasks 1–3):** blobstore, blob metadata + atomic attach, config and startup check.
- **PR B (Tasks 4–5):** upload/send/download routes and rules, sweep, agent tools, docs.
- **PR C (Task 6):** `/me` composer and rendering, docs.

---

### Task 1: `internal/blobstore` (Mem, S3, conformance) and the `blb` id kind

**Files:** Create `internal/blobstore/{blobstore.go,mem.go,s3.go,conformance_test.go}`; modify `internal/ident/ident.go` (`Blob Kind = "blb"`, in `kinds`); `go.mod`.

**Produces:**
```go
type Info struct{ Size int64; ContentType, ETag string; Modified time.Time }
type Object interface{ io.ReadSeekCloser }
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error // size -1 = unknown
	Open(ctx context.Context, key string) (Object, Info, error) // ErrNotFound
	Stat(ctx context.Context, key string) (Info, error)
	Delete(ctx context.Context, key string) error // missing key: no error
	Presign(ctx context.Context, method, key string, ttl time.Duration) (string, error) // ErrUnsupported for now
	Check(ctx context.Context) error // the bucket exists and is reachable
}
var ErrNotFound, ErrUnsupported error
func NewMem() *Mem
type S3Config struct{ Endpoint, Bucket, Region, AccessKey, SecretKey string; PathStyle bool }
func NewS3(cfg S3Config) (*S3, error)
```
`Open` returns a seekable object (Mem: `bytes.Reader`; S3: `*minio.Object`), so handlers use `http.ServeContent` for ranges.

- [ ] Failing conformance test run against `NewMem()` and against `NewS3` pointed at an `httptest.Server` wrapping `gofakes3` (path-style, bucket created first): put/open/read/seek, stat, delete then open → ErrNotFound, delete missing → nil, Check ok, Check on a missing bucket → error, Presign → ErrUnsupported. `ident.New(ident.Blob)` parses with kind `blb`.
- [ ] Implement; `go get github.com/minio/minio-go/v7 github.com/johannesboyne/gofakes3` (GOPROXY=https://proxy.golang.org).
- [ ] Tests pass; commit `blobstore: S3-compatible byte store`.

### Task 2: Blob metadata and atomic attach in the intercom store

**Files:** Create `internal/intercom/blob.go`, `internal/intercom/blob_test.go`, `internal/intercom/intercompg/migrations/0006_blobs.sql`; modify `ask.go` (`validateParts`), `channel_log.go` (Store), `mem.go`, `intercompg/store.go`, `intercomtest/channels.go`.

**Produces:**
```go
type BlobState string // "pending" | "attached"
type Blob struct {
	ID ident.ID; Uploader ident.ID; State BlobState
	Size int64; Type, Name, SHA256 string
	Channel ident.ID; Squawk string; Created time.Time
}
const MaxAttachments = 10
var ErrBlobNotYours error // a ref that isn't the sender's own pending blob
// Store additions:
CreateBlob(b Blob) error          // state pending
GetBlob(id ident.ID) (Blob, bool)
PendingBlobsBefore(t time.Time) []Blob
DeleteBlob(id ident.ID) error     // pending only
```
`validateParts` now accepts reference parts: `Ref` a `blb_` id, `Type` set, no `Data`, at most `MaxAttachments`; asks rules unchanged. `Append`, under its lock: every ref must name a blob with `State==pending && Uploader==m.From`, else `ErrBlobNotYours` (wraps `ErrInvalid`) and nothing is stored; on success each becomes `attached` with `Channel`, `Squawk`. Name/size/type in the part are overwritten from the blob row (the sender can't lie).

Migration `0006_blobs.sql`:
```sql
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
```
- [ ] Failing conformance cases (Mem + pg): create/get; send with own pending ref attaches it (Channel/Squawk set, part fields from the row); a second send of the same ref → ErrBlobNotYours, no append; someone else's ref → refused; 11 refs → ErrInvalid; PendingBlobsBefore; DeleteBlob of an attached blob refused. Unit: `validateParts` ref rules.
- [ ] Implement Mem then pg; tests + local pg integration pass; commit `intercom: blob metadata and atomic attach`.

### Task 3: `blobs:` config, credentials, startup check, wiring

**Files:** `cmd/at-jam/config.go` (+ `blobsConfig`, `validateBlobs`), `cmd/at-jam/config_test.go`, `cmd/at-jam/main.go` (resolve creds in memory, `blobstore.NewS3`, `Check` → refuse start), `docs/usage/jam/serve.md` (the block).

```go
type blobsConfig struct {
	Endpoint string `yaml:"endpoint"`; Bucket string `yaml:"bucket"`; Region string `yaml:"region"`
	PathStyle bool `yaml:"path-style"`; MaxSize string `yaml:"max-size"` // e.g. 25MB
	AccessKeyCred string `yaml:"access-key-cred"`; SecretKeyCred string `yaml:"secret-key-cred"`
}
func (c serveConfig) validateBlobs() error
func (b *blobsConfig) maxBytes() int64 // default 25 MiB
```
Rules: endpoint `http(s)://host[:port]` (http only for loopback or a dotted private host — follow `kits` url rules where they apply), bucket required, both creds demanded and plain static (as `validateSSH`), max-size parses (`KB|MB|GB`), ≤ 1 GB.
- [ ] Failing config tests (missing/undemanded creds, bad endpoint, bad size, default size) → implement → pass; commit `serve: blobs config`.
- [ ] Commit PR A; open PR; final review; merge when green.

### Task 4: `jam.Attachments` — upload, send, download, sweep; routes

**Files:** Create `internal/jam/attachments.go`, `internal/jam/attachments_test.go`; modify `internal/jam/squawks.go` (POST `/squawks/uploads`, GET `/squawks/blobs/{id}`, `attachments` on send), `internal/jam/me_send.go` (`attachments`), `internal/jam/meui/handler.go` (POST `/me/uploads`, GET `/me/blobs/{id}`), `cmd/at-jam/main.go` (construct, sweep ticker hourly).

```go
type Attachments struct{ /* store intercom.Store; bytes blobstore.Store; ic *Intercom; max int64 */ }
func NewAttachments(lg intercom.Store, bytes blobstore.Store, ic *Intercom, max int64) *Attachments // nil bytes ⇒ off
func (a *Attachments) Enabled() bool
func (a *Attachments) Upload(ctx context.Context, uploader ident.ID, name, contentType string, r io.Reader) (intercom.Blob, error) // ErrTooLarge, ErrAttachmentsOff
func (a *Attachments) Parts(refs []string) []intercom.Part // reference parts for a send
func (a *Attachments) Serve(w http.ResponseWriter, r *http.Request, viewer ident.ID, isSession bool, id ident.ID)
func (a *Attachments) Sweep(ctx context.Context, now time.Time)
```
Upload streams `io.LimitReader(r, max+1)` through a sha256 `TeeReader` into `bytes.Put(ctx, "blobs/"+id, …, -1, type)`; over max → delete the object, `ErrTooLarge` (413), no row. Then `CreateBlob`. Name: base name only, ≤ 255 B, control chars stripped; type defaults `application/octet-stream`. Serve: pending ⇒ viewer must be uploader; attached ⇒ person `ic.CanSee(viewer, channel)` / session `isMemberOf`; else 404; then headers (Global Constraints) + `http.ServeContent`. Send: `/squawks` and `/me/send` accept `attachments: ["blb_…"]`; body may be empty when attachments are present (filled with the names); `ErrBlobNotYours` → 400.
- [ ] Failing tests for Review Focus 1–4 and: upload 201 shape; send with refs reaches the reader's inbox with parts; session download of a channel it's in; pending visible to uploader only; sweep removes a 25h-old pending blob (object + row) and keeps an attached one; attachments off → uploads 503, send with refs 400.
- [ ] Implement; pass; commit `jam: attachments — upload, send, download, sweep`.

### Task 5: Agent tools and docs (PR B)

**Files:** `cmd/cove-master/mcp_attach.go` (+ test): `send` gains `attach []string` (paths; each uploaded via `POST /squawks/uploads` with `X-Filename`, then refs sent in `attachments`); new `fetch(ref, path?)` (GET `/squawks/blobs/{ref}` to `~/attachments/<ref>-<name>` or `path`, 0700 dir, `O_EXCL` file, returns path); `read` parts already carry refs. Docs: create `docs/usage/jam/attachments.md` + INDEX row; `intercom.md` (attach/fetch); `serve.md` link.
- [ ] Failing MCP tests (fake Jam records the upload body and the send's attachments; fetch writes the bytes, refuses to overwrite) → implement → full verification (lint, unit, pg integration, docs audit) → commit; open **PR B**; final review; merge.

### Task 6: `/me` composer and rendering (PR C)

**Files:** `internal/jam/meui/conversation.go` (`MessageRow.Files []FileChip{ID, Name, Size, Image bool}` from reference parts), `templates/inbox.html` (thumbnails `<img loading="lazy" src="/me/blobs/{id}">` linking full size; download chips), `templates/layout.html` (📎 picker, paste, drop; pending chips with progress via `XMLHttpRequest` upload to `/me/uploads`; `meSend` includes `attachments`; hide 📎 when off), `meui` Deps gains `Attachments`; docs `intercom-ui.md`.
- [ ] Failing tests: an image part renders a thumbnail `src="/me/blobs/blb_…"`; a PDF renders a download chip; the composer has the 📎/paste/drop wiring and `attachments` in meSend; with attachments off there is no 📎 → implement → verify → commit; open **PR C**; final review; merge.
