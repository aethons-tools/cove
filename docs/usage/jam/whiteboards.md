---
summary: Whiteboards — shared Excalidraw canvases that live in a conversation. People draw on them in /app/user; agents read them compactly, edit them with simple ops, and look at a server-rendered PNG. Edits merge per element and sync live; access follows the conversation's.
read_when: You want people and agents to sketch together (diagrams, freehand, planning, annotated screenshots), you're using or wiring the board tools/routes, or a board edit is refused or doesn't show up.
owns: the board model (in a channel; elements merged per element), board access, the sync routes, the agent tools (board_create, boards, board_read, board_apply, board_snapshot), the server-side renderer, and the `board` live push. Does NOT own the /app/user editor UI — see intercom-ui.md.
prereqs: intercom.md for channels and the live push; comms-addressing.md for `to`
tier: leaf
updated: 2026-10-23
---

# Whiteboards

A **board** is a shared canvas in one conversation. Whoever may read the
conversation may read the board — a person who can see it, a session in it
(and a session always reaches the boards it created) — and whoever may post
there may edit it. An archived conversation's boards are read-only. Board
edits post nothing to the chat and never wake a session: say so in the
conversation when you want someone to look.

## How edits combine

A board is a set of Excalidraw elements (shapes, arrows, text, freehand
strokes, images), stored one per row. An edit sends only the changed
elements; for each, the higher `version` wins, then the lower `versionNonce`
(Excalidraw's own rule), so two people moving different shapes never clash
and two moving the same one converge. Every accepted change bumps the board's
`seq`; a reader asks for what changed since its last `seq`.

## Routes

| Route | Result |
|---|---|
| `POST /app/user/boards` `{channel, title}` / `POST /squawks/boards` `{title, to?}` | `201 {board_id, channel}`; posts the board's card in the conversation |
| `GET …/boards/{id}/elements?since=` | `{seq, elements}` (Excalidraw element JSON) |
| `POST …/boards/{id}/elements` `{elements}` | `{seq, lost}` — `lost` are the stored winners of elements that lost the merge |
| `GET /app/user/boards/{id}/thumb.png`, `GET /squawks/boards/{id}/snapshot.png` | the server-rendered PNG (`?size=` up to 480 gives the thumbnail size; otherwise full size, at most 1600 px) |
| `GET /squawks/boards`, `…/{id}/read`, `POST …/{id}/apply` | the agent tools' routes (below) |

Missing and forbidden are both `404`. `/app/user/events` sends `board` (data: the
board id) when a board the viewer may read changes. Limits: 500 elements per
merge, 64 KB per element, 8 MB per request.

## Agents

- **`board_create(title, to?)`** — a board in the session's channel (or `to`).
- **`boards()`** — the boards it can reach.
- **`board_read(board_id)`** — compact elements: `{id, type, x, y, w, h, text,
  from, to, color, frame}`; a shape's label is its `text`, an arrow's ends are
  `from`/`to`. Freehand strokes show only their bounds.
- **`board_apply(board_id, ops)`** — `add` (rectangle, ellipse, diamond,
  text, arrow, line, frame; an arrow with `from`/`to` connects two shapes and
  follows them when they move), `update` (move, resize, relabel, recolour),
  `delete` (with its label and connected arrows). All or nothing: a bad op
  fails the batch with the reason (a batch is at most 500 changed elements),
  and an apply never interleaves with another edit of the board.
- **`board_snapshot(board_id)`** — writes `~/boards/<id>-<seq>.png` and returns
  the path, to look at (freehand included).

## The renderer

Jam draws boards itself (pure Go): shapes, arrows with heads, labels, text,
freehand strokes and frames, without Excalidraw's hand-drawn style, in
Excalidraw's z-order. Rotation isn't applied and hatched fills render as a
light wash. A render is bounded (at most 5000 elements, sane coordinates, a
capped font size and drawn area), so a pathological board renders partly
rather than slowly. It serves the card
thumbnails and `board_snapshot`.

## In /app/user

A conversation's header has **New whiteboard**; a board's card (title, live
thumbnail, **Open**) opens the editor in place of the stream — see
[intercom-ui.md](intercom-ui.md#whiteboards). The editor is built into
`jam` by `just whiteboard` ([DEVELOPMENT.md](../../DEVELOPMENT.md)); a Jam
built without it shows cards but no editor.

## Not yet

Images on boards (the image tool is off); live cursors; Mermaid import;
auto-layout.
