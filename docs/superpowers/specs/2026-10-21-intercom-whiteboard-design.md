# intercom: whiteboards — design

**Status:** approved in brainstorming (2026-10-21); not built.
**Builds on:** channels and the `/me` SSE push ([`2026-10-06-intercom-identity-and-channels-design.md`](2026-10-06-intercom-identity-and-channels-design.md)), message parts ([`…-asks-design.md`](2026-10-21-intercom-asks-design.md)), attachments ([`…-attachments-design.md`](2026-10-21-intercom-attachments-design.md)).
**Decisions (2026-10-21):** uses in priority order: diagrams, freehand, planning, UI feedback; canvas: **Excalidraw** (MIT), bundled by a **build step** (no committed bundle; Node and the npm registry are build-time only); boards **live in a conversation**; concurrent edits merge **per element** (Excalidraw's version model); agents see a board as **structure plus a server-rendered PNG**.

## 1. What and why

People and agents sketch together: architecture diagrams, rough freehand ideas, sticky-note planning, annotated screenshots. A board is a shared canvas in a conversation that people edit in `/me` and agents edit through tools, each seeing the other's changes live.

## 2. Model and storage

- **Board** — `{id: brd_…, channel, title, created_by, created_at, seq}`, in one channel. Kept forever, like messages.
- **Element** — one row per shape, arrow, text or freehand stroke:
  ```
  board_elements(board_id, element_id, version, version_nonce, deleted, seq, doc jsonb)
  PRIMARY KEY (board_id, element_id); INDEX (board_id, seq)
  ```
  `doc` is the Excalidraw element JSON (opaque apart from what the renderer and `board_read` use). A deletion is a tombstone (`deleted`).
- **Merge** — an incoming element replaces the stored one only if its `version` is higher, or equal with a lower `version_nonce` (Excalidraw's reconcile rule): a per-row compare-and-set, so edits to different elements never contend. Each accepted change bumps the board's `seq` in the same transaction and stamps the element with it.
- Tables live in the intercom store (Mem + `intercompg`), next to the channel log; a new `brd` id kind.
- **Images on a board** are attachments (blobs) bound to the board's channel; Excalidraw's `files` map points at blob refs.

## 3. Access

Exactly the channel's: a person who can see the channel reads it, a session reads it if a member; writing needs being able to post there. An archived channel's boards are read-only. Refusals are 404 alike for missing and forbidden.

## 4. Sync

- `POST /me/boards` `{channel, title}` → creates a board and posts its card (a squawk with an `application/vnd.jam.board+json` part `{board_id}`; body "started a whiteboard: Title").
- `GET /me/boards/{id}/elements?since=seq` → elements with `seq` > since, and the board's `seq`.
- `POST /me/boards/{id}/elements` `{elements: [...]}` → merges, returns the new `seq` and the elements that lost (so the client re-applies the winners).
- Each change pushes a `board` event `{id, seq}` on the existing `/me` SSE; open editors fetch the delta and reconcile.
- Board edits post nothing to the chat and **never wake a session**; people say so in the conversation.

## 5. Agents (MCP)

Agents use a simplified vocabulary; Jam translates to and from Excalidraw elements (bound text, arrow bindings, seeds, versions):

- `board_create(title, to?)` — a board in the home channel (or `to`, under the send rules); posts its card; `{board_id}`.
- `boards()` — boards in channels the session is in: `{board_id, channel, title, seq}`.
- `board_read(board_id, since?)` — `{seq, elements: [{id, type, x, y, w, h, text?, from?, to?, color?, frame?, by?}]}`; `type` ∈ rectangle, ellipse, diamond, text, arrow, line, frame, freedraw, image; `text` folds in a shape's bound label; `from`/`to` are an arrow's bound element ids; freedraw carries its bounds only.
- `board_snapshot(board_id, path?)` — renders the board to PNG into the studio (default `~/boards/<id>-<seq>.png`, never overwriting) and returns the path.
- `board_apply(board_id, ops)` — `add {type, id?, x, y, w?, h?, text?, from?, to?, color?, frame?}`, `update {id, …}`, `delete {id}`; atomic (one bad op fails the batch with the reason); merged and pushed like a person's edit; returns the new `seq` and added ids.
- Routes: `/squawks/boards…` mirroring §4 for sessions (and routed by the cove mux).

## 6. `/me`

- A conversation header gets **New whiteboard**. A board card shows its title, a live thumbnail (`GET /me/boards/{id}/thumb.png`) and **Open**, which swaps the pane for the editor (Back to chat).
- The editor loads the elements, sends changed elements debounced (~300 ms), applies `board` pushes, shows who else is viewing (presence only; no live cursors in v1), and is read-only where the viewer can't post.
- Images pasted onto the canvas upload through `/me/uploads`.

## 7. The bundle (build step)

`web/whiteboard/`: `package.json`, a committed `package-lock.json`, and an entry that mounts Excalidraw and wires the sync (`meBoard.open(el, boardId, readOnly)`). `just whiteboard` runs `npm ci` and an esbuild bundle into the git-ignored `internal/jam/uiassets/whiteboard/`, embedded by the binary and served under `/me/static/whiteboard/`. `just build`, CI, the release pipeline and image builds run it first. A committed placeholder keeps `go:embed` valid; without the bundle `/me` hides New whiteboard and cards say "whiteboard not built". Excalidraw's and React's MIT licence files ship with it.

## 8. The server-side renderer

`internal/jam/boardrender`: elements → PNG in pure Go (`golang.org/x/image/vector`, Go fonts from `golang.org/x/image/font/gofont`; no cgo): rectangles, ellipses, diamonds, lines, arrows with heads, wrapped text, freehand polylines, frames, images (from blobs); stroke colour/width and fill. No hand-drawn roughness; an unknown type renders as a labelled box. Cached per `(board, seq)`. It serves card thumbnails and `board_snapshot`.

## 9. Data, upgrade, compatibility

Additive: an intercom migration (`boards`, `board_elements`), the `brd` id kind. An older Jam ignores the board part and shows the card's body.

## 10. Testing

- Merge conformance (Mem + Postgres): higher version wins, nonce tie-break, tombstones, `since` deltas, concurrent writes.
- Ops translation: add/update/delete, arrow binding, bound labels, atomic failure.
- Renderer golden PNGs per element type.
- Access (read/write/archived; 404 alike); `/me` wiring; the bundle built in CI with a load smoke test.

## 11. Docs

New leaf `docs/usage/jam/whiteboards.md` (+ INDEX row); `intercom.md` (tools), `intercom-ui.md` (the editor), `DEVELOPMENT.md` (`just whiteboard`, Node requirement).

## 12. Plans (tentative)

- **A:** store, merge, access, the `/me` and `/squawks` sync routes.
- **B:** the renderer and the agent tools.
- **C:** the bundle, the build step and the `/me` editor.

## 13. Later

Live cursors; Mermaid import; auto-layout (`near: <id>`); linking a board into other channels; tombstone compaction; board templates.
