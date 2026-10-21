# intercom whiteboards — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Shared Excalidraw boards in a conversation that people edit in `/me` and agents edit through tools, merged per element and synced live.

**Architecture:** Boards and their elements live in the intercom store (Mem + `intercompg`), merged per element by Excalidraw's version rule in one transaction that bumps the board's `seq`. `jam.Boards` holds access (the channel's), the agent vocabulary ↔ Excalidraw translation, and a change hub that `/me`'s SSE forwards. `internal/jam/boardrender` draws elements to PNG in pure Go for thumbnails and agents. The editor is an esbuild bundle of Excalidraw built by `just whiteboard`.

**Tech Stack:** Go; pgx; `golang.org/x/image/vector` + `golang.org/x/image/font/gofont`; Excalidraw (npm, MIT) + esbuild (build step only).

**Spec:** [`docs/superpowers/specs/2026-10-21-intercom-whiteboard-design.md`](../specs/2026-10-21-intercom-whiteboard-design.md).

## Global Constraints

- Merge rule: incoming replaces stored iff `version` higher, or equal and `version_nonce` lower. Each accepted change bumps the board's `seq` and stamps the element, in one transaction.
- Access = the channel's: read = person `CanSee` / session member; write = may post; archived ⇒ read-only. 404 alike for missing/forbidden.
- Board edits never post to chat and never wake a session.
- Every cove route added is routed by the cove mux (`cmd/at-jam/mux.go`) and covered by `mux_test.go`.
- `board_apply` is atomic. `board_snapshot` never overwrites a file.
- Hermetic tests; Postgres conformance behind `-tags integration`. Node/npm only in `just whiteboard` (PR C).

## Review Focus

1. Two clients edit the same element at once → both converge on the same winner (version, then nonce), and the loser's client gets the winner back.
2. A delta fetch (`since`) issued during a concurrent write never misses an element (seq bumped in the same transaction).
3. Someone who can see but not post (an archived channel) tries to write → refused, nothing stored.
4. `board_apply` with one bad op (arrow to a missing id) → nothing applied.
5. A huge or malformed element doc (non-JSON, 1 MB text) → refused (per-element cap 64 KB; batch ≤ 500 elements).

## PR split

- **PR A (Tasks 1–3):** store + merge, `jam.Boards` access + hub, sync routes (`/me/boards…`, `/squawks/boards…`, mux).
- **PR B (Tasks 4–5):** `boardrender`, agent vocabulary + MCP tools, thumbnails, docs.
- **PR C (Task 6):** `web/whiteboard` bundle, `just whiteboard`, CI, `/me` editor and cards — needs Node + `registry.npmjs.org` in the kit.

---

### Task 1: Board store and per-element merge (intercom)

**Files:** create `internal/intercom/board.go` (+ test), `internal/intercom/intercompg/migrations/0007_boards.sql`; modify `ident` (`Board Kind = "brd"`), `channel_log.go` (Store), `mem.go`, `intercompg/store.go`, `intercomtest/channels.go`.

**Produces:**
```go
type Board struct{ ID, Channel, CreatedBy ident.ID; Title string; Created time.Time; Seq int64 }
type Element struct{ ID string; Version, Nonce int64; Deleted bool; Seq int64; Doc json.RawMessage }
// ElementOf parses id/version/versionNonce/isDeleted out of an Excalidraw element doc (ErrInvalid: not JSON, no id, > 64 KB).
func ElementOf(doc json.RawMessage) (Element, error)
// Wins reports whether incoming replaces stored (the merge rule).
func Wins(incoming, stored Element) bool
// Store additions:
CreateBoard(b Board) error
GetBoard(id ident.ID) (Board, bool)
ListBoards(channel ident.ID) []Board
MergeElements(board ident.ID, els []Element) (seq int64, lost []Element, err error) // lost: stored winners for incoming losers
BoardElements(board ident.ID, since int64) ([]Element, int64) // elements with Seq > since, and the board's seq
```
Migration:
```sql
CREATE TABLE boards (id text PRIMARY KEY, channel_id text NOT NULL, title text NOT NULL, created_by text NOT NULL, created_at timestamptz NOT NULL, seq bigint NOT NULL DEFAULT 0);
CREATE INDEX boards_channel ON boards (channel_id);
CREATE TABLE board_elements (board_id text NOT NULL REFERENCES boards(id), element_id text NOT NULL, version bigint NOT NULL, version_nonce bigint NOT NULL, deleted boolean NOT NULL, seq bigint NOT NULL, doc jsonb NOT NULL, PRIMARY KEY (board_id, element_id));
CREATE INDEX board_elements_seq ON board_elements (board_id, seq);
```
pg `MergeElements`: one transaction; `SELECT seq FROM boards WHERE id=$1 FOR UPDATE`; per element, load stored (`FOR UPDATE`), apply `Wins`, upsert with `seq+1`…; update `boards.seq`. ≤ 500 elements per call.
- [ ] Failing conformance (Mem + pg): create/get/list; merge new; higher version wins; equal version lower nonce wins; loser returned in `lost`; tombstone; `since` deltas; 8 concurrent merges of the same element converge (stored = max by the rule) and seq strictly increases; batch > 500 and doc > 64 KB → ErrInvalid. Unit: `ElementOf`, `Wins`.
- [ ] Implement; tests + pg integration pass; commit.

### Task 2: `jam.Boards` — access, create, hub

**Files:** create `internal/jam/boards.go` (+ test).

```go
type Boards struct{ /* lg intercom.Store; ic *Intercom; hub */ }
func NewBoards(lg intercom.Store, ic *Intercom) *Boards
func (b *Boards) Create(p Poster, to string, title string, now time.Time) (intercom.Board, error) // plans like a send; posts the card squawk (part application/vnd.jam.board+json {board_id}, body "started a whiteboard: <title>")
func (b *Boards) CanRead(viewer ident.ID, id ident.ID) (intercom.Board, bool)
func (b *Boards) CanWrite(viewer ident.ID, id ident.ID) (intercom.Board, bool) // read + live channel + may post
func (b *Boards) Merge(viewer, id ident.ID, docs []json.RawMessage) (seq int64, lost []json.RawMessage, err error) // ErrSendDenied when not writable
func (b *Boards) Elements(viewer, id ident.ID, since int64) ([]json.RawMessage, int64, error)
func (b *Boards) ForChannelsOf(viewer ident.ID) []intercom.Board
func (b *Boards) Subscribe() (<-chan ident.ID, func()) // board ids changed
```
`validateParts` accepts the board part type (`intercom.BoardPartType`, data `{board_id}`). Hub: buffered fan-out, drops when a subscriber is slow (it refetches by `since`).
- [ ] Failing tests: person who can see reads; non-member session can't; writer on an archived channel refused (Review Focus 3); merge publishes the board id; Create posts the card and the card doesn't wake a session (it's a notice? no — it is a normal post by the creator; assert body/part) → implement → commit.

### Task 3: Sync routes and mux

**Files:** `internal/jam/meui/boards.go` (`POST /me/boards`, `GET|POST /me/boards/{id}/elements`), `internal/jam/meui/events.go` (`board` SSE event `data: <id>`), `internal/jam/squawks_boards.go` (`/squawks/boards`, `/squawks/boards/{id}/elements` — used by Task 5's tools), `cmd/at-jam/mux.go` + `mux_test.go`, `cmd/at-jam/main.go` wiring. Request caps: body ≤ 8 MB.
- [ ] Failing handler tests (create, fetch since, merge returns lost, 404 alike, SSE emits `board`) and mux tests → implement → full verification → **PR A**; final review; merge.

### Task 4: `boardrender`

**Files:** create `internal/jam/boardrender/{render.go,render_test.go,testdata/*.png}`.
```go
func Render(els []json.RawMessage, images func(fileID string) (image.Image, bool), maxSide int) ([]byte, error) // PNG
```
Draw (no roughness): rectangle (roundness), ellipse, diamond, line/arrow polylines from `points` (+ arrowheads), text (gofont, `fontSize`, wrap within container width, `textAlign`), freedraw polyline, frame (outline + name), image (from `images`), stroke colour/width, background fill (`transparent` = none). Bounds = union of non-deleted elements + 20 px; scale to fit `maxSide` (default 1600; thumbnails 480). Unknown type → labelled box. Empty board → small blank PNG.
- [ ] Golden tests per type (regenerate with `-update`), empty board, unknown type → implement → commit.

### Task 5: Agent vocabulary, tools, thumbnails, docs (PR B)

**Files:** `internal/jam/boardops.go` (+ test): `Compact(els) []CompactElement`, `Apply(existing, ops) ([]json.RawMessage, addedIDs, error)`; `internal/jam/squawks_boards.go` (+ `/squawks/boards/{id}/apply`, `/squawks/boards/{id}/read`, `/squawks/boards/{id}/snapshot.png`); `internal/jam/meui/boards.go` (`GET /me/boards/{id}/thumb.png`, cached per seq); `cmd/cove-master/mcp_boards.go` (+ test): `board_create`, `boards`, `board_read`, `board_snapshot` (writes `~/boards/<id>-<seq>.png`, `O_EXCL`), `board_apply`; mux; docs `whiteboards.md` (+ INDEX), `intercom.md`.

Translation: `add rectangle {text}` → a rectangle element + a bound text element (`containerId`, rectangle's `boundElements`); `add arrow {from,to}` → an arrow with `startBinding`/`endBinding` and points from the shapes' facing edges, both shapes' `boundElements` updated; `update` bumps `version`, new random `versionNonce`, `updated` ms; `delete` sets `isDeleted` (and of its bound text). New elements: random `seed`, `version: 1`, defaults (`strokeColor #1e1e1e`, `roughness 1`, `strokeWidth 2`, `fontFamily 1`, `fontSize 20`).
- [ ] Failing tests (Review Focus 4; compact read folds labels and arrow ends; apply round-trips through Render; MCP forwarding + snapshot never overwrites) → implement → verify → **PR B**; final review; merge.

### Task 6: The bundle, the build, the `/me` editor (PR C — after the kit gains Node + npm)

**Files:** `web/whiteboard/{package.json,package-lock.json,entry.jsx,build.mjs}`; `justfile` (`whiteboard`), `.gitignore`, `internal/jam/uiassets/whiteboard/.keep` + embed; CI workflow steps; `internal/jam/meui/templates` (New whiteboard, board card with thumbnail, editor pane, read-only); `docs/DEVELOPMENT.md`, `intercom-ui.md`, `whiteboards.md`.
Entry: mounts `<Excalidraw>` with `initialData` from `GET …/elements`, `onChange` → diff by element version → debounced POST; on SSE `board` for its id → fetch `since` → `reconcileElements` → `updateScene`; `viewModeEnabled` when read-only; image files via `/me/uploads` + `/me/blobs`.
- [ ] Failing Go tests (card renders thumbnail + Open; New whiteboard hidden without the bundle; editor page wiring) → bundle + templates → `just whiteboard && go test ./...` → CI → **PR C**; final review; merge.
