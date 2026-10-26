# Jam SPA, slice 7: boards — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** whiteboards work in the Jam app, matching the old Inbox:
- **In a conversation:**
  - a board's card shows its title, who started it and when, and the server-rendered thumbnail, linking to the board;
  - **New whiteboard** (asking for a title) creates one and opens it.
- **The board itself:**
  - it opens at **`/app/next/boards/:id`**, a lazily loaded route that holds the Excalidraw editor;
  - "← Back to chat" returns to the conversation, and "view only" shows for a reader who may not draw;
  - it syncs element by element, as `web/whiteboard/entry.jsx` does: debounced saves, merge losers reconciled back, the 60 KB element guard, retry with backoff;
  - other people's and agents' edits arrive live via the events stream's `board` signal, with a 30 s poll behind it;
  - leaving with unsaved edits warns.
- **Artifacts** board links open the board in the app.
- **Fonts:** Excalidraw's fonts load **from the app's own bundle under the strict CSP**, with no CDN.

**Spike (done while planning, 2026-10-26).** The existing whiteboard bundle was loaded in a page carrying the app's exact CSP.
- Drawing and saving work: there are no script, worker or `eval` violations.
- **Fonts are the one problem.** Excalidraw asks `https://esm.sh/@excalidraw/excalidraw@0.18.1/dist/prod/fonts/…` for every font family, and the CSP blocks it (`font-src 'self'`). The bundle's own `fonts/` are **never requested**, even with `window.EXCALIDRAW_ASSET_PATH` set to an absolute URL before the import, and the same happens with no CSP. So the old editor is likely loading its fonts from a CDN today (against spec §4, "no CDN at runtime"), or silently using system fonts where egress is blocked.
- Task 4 owns the fix, and its browser test pins it: no request to any origin but the app's, and no CSP violation.

**Architecture:**
- **Go.**
  - `MessageRow.board` (`{id, title}`) and `Conversation.newBoard` join the JSON.
  - `Bootstrap.boards` means "whiteboards on". The app bundles its own editor, so `uiassets.WhiteboardBuilt` no longer gates it.
  - `GET api/boards/{id}` returns `{id, title, channel, readOnly}`. `POST api/boards` takes `{channel, title}` and returns `{board, channel}` with JSON errors.
  - The elements route, the thumbnails and the `board` signal are unchanged (spec §2).
- **App.**
  - **Pure and synchronous:** `src/boards/sync.ts` (which elements to send, the size guard, batches).
  - **Lazy chunk:** `src/boards/BoardEditor.tsx`, Excalidraw plus the sync loop, ported from `entry.jsx`.
  - **Pages and live updates:** `src/boards/BoardPage.tsx` (the route's bar plus a `Suspense` around the editor), `BoardCard` (in `Message`), and `NewBoard` (the header button and title modal). `useLiveEvents` re-dispatches `board` signals as the window event `jam-board`, as `layout.html` does.
- **Build.** `scripts/app.sh` copies Excalidraw's fonts into the bundle at `assets/excalidraw/fonts/`, leaving out the 13 MB CJK Xiaolai set, as `web/whiteboard/build.mjs` does. A module evaluated before Excalidraw sets `window.EXCALIDRAW_ASSET_PATH`.

**Tech Stack:** the slice 1–6 stack plus **`@excalidraw/excalidraw` 0.18.1**, the version `web/whiteboard` pins. It's lazy, so the initial JS doesn't change. `web/whiteboard/` stays until cutover (slice 9).

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md):
- §3 slice 7: "the whiteboard as a lazily loaded route (`/app/next/boards/:id`), board cards and thumbnails, and live `board` updates";
- §1: "The whiteboard (Excalidraw) becomes a lazily loaded route chunk";
- §2: board elements, thumbnails and the `board` signal are unchanged;
- §4: the CSP, and "Fonts and assets are bundled, with no CDN at runtime".

Helpers already on main:
- Go: `fixture`, `env.page`, `apiGet`, `apiPost` (the Go test helper), `checkJSONHeaders`, `getConv`, `jam.NewBoards`, `Boards.Create/Apply/Elements`;
- browser tests: `pageProblems`, `testChanges`, `browserCtx`, `into`, `call`, `TestBrowserWhiteboardEditor`'s drawing steps;
- TS: `renderApp(b, path, client)`, `boot`, `server`, `apiGet`, `apiPost`.

## Global Constraints

- **Parity:**
  - The old page's board routes and editor keep working: `?board=`, `POST /app/user/boards`, `…/elements`, `…/thumb.png` and `web/whiteboard`.
  - `boards_test.go` and `board_browser_test.go` pass unchanged.
- **The sync protocol is unchanged.** The editor talks to `/app/user/boards/{id}/elements`:
  - `GET ?since=` returns `{seq, elements}`;
  - `POST {elements}` returns `{seq, lost}`;
  - batches of at most 500, at most 60 000 bytes per element;
  - one request in flight at a time;
  - debounced 300 ms, but saving at least every 1000 ms during a drag;
  - backoff 1 s → 30 s;
  - a 30 s poll.
- **Excalidraw's embeddable allowlist stays the default**, with no `validateEmbeddable` override. The reason is in `entry.jsx`'s comment: the editor runs on the Jam UI origin, which the frame exchange trusts.
- **The image tool stays off.** `canvasActions.loadScene` and `saveToActiveFile` are off too.
- **Authorization:** `api/boards/{id}` answers a board the viewer may not read exactly like a missing one (404 `not_found`, "no such board"). Writing is the server's check; read-only only disables the UI.
- **CSP:** unchanged (`font-src 'self'`). The board route loads nothing from any other origin, fonts included.
- **JSON:** `{"error","code"}` errors; types in `apitypes.go`, regenerated.
- **Size budget:** initial JS ≤ 350 KB gzipped. Excalidraw lives only in the lazy editor chunk.
- **Docs:** they ship in the same change.
- **gofmt:** use positional struct literals in table tests.

## Review Focus

1. **Fonts:**
   - the board route requests fonts only from the app's own origin, under `assets/excalidraw/fonts/`;
   - there are no CSP violations;
   - text drawn on the board renders in Excalifont, not a fallback.
   (Task 4 browser test.)
2. **A read-only viewer:** they get view mode and nothing is ever POSTed, while edits by others still arrive. (Task 3 test; Task 4 browser test.)
3. **Unsaved edits on leaving:**
   - "← Back to chat" or closing the tab with edits in flight or debounced warns (beforeunload);
   - in-app navigation flushes the pending save first instead of dropping it.
   (Task 3 test.)
4. **A board in another conversation, or gone:** `/app/next/boards/:id` for a missing or unreadable board says so, without leaking whether it exists, and never mounts Excalidraw. (Task 1 Go test; Task 3 test.)
5. **A live push for a board that isn't open** is ignored. A push for the open board pulls only `?since=` the last seq. (Task 2 test; Task 4 browser test.)

---

## File structure

| File | Responsibility |
|---|---|
| `internal/jam/userui/apitypes.go` (modify) | `BoardCard` gains JSON tags (`Built` becomes `json:"-"`); `MessageRow.Board` becomes `board,omitempty`; `Conversation.NewBoard` becomes `newBoard`; `BoardInfo`, `NewBoardRequest`, `NewBoardResponse`. |
| `internal/jam/userui/api_boards.go`, `api_boards_test.go` (new) | `GET api/boards/{id}`, `POST api/boards`. |
| `internal/jam/userui/api.go`, `api_conversation.go`, `boards.go` (modify) | `boardRoutes`; `Bootstrap.Boards`; `newBoard` in the API conversation. |
| `internal/jam/userui/api_conversation_test.go`, `api_test.go` (modify) | The shape test expects `board` and `newBoard`; the bootstrap reflects Boards. |
| `web/app/src/boards/sync.ts`, `sync.test.ts` (new) | Pure sync helpers. |
| `web/app/src/boards/BoardCard.tsx`, `NewBoard.tsx` (new) | The card; New whiteboard. |
| `web/app/src/boards/BoardPage.tsx`, `BoardPage.test.tsx` (new) | The route. |
| `web/app/src/boards/BoardEditor.tsx`, `assetPath.ts` (new) | The lazy editor. |
| `web/app/src/App.tsx`, `conversation/Message.tsx`, `conversation/ConversationHeader.tsx`, `session/PaneTabs.tsx`, `live/useLiveEvents.ts` + tests (modify) | Wiring. |
| `web/app/package.json`, `package-lock.json` (modify) | `@excalidraw/excalidraw`. |
| `scripts/app.sh` (modify) | Copies the fonts. |
| `internal/jam/userui/next_browser_test.go` (modify) | End-to-end. |
| `docs/web-app.md`, `docs/usage/jam/whiteboards.md`, `docs/usage/jam/intercom-ui.md`, the spec's status line (modify) | Docs. |

---

### Task 1: Boards in the API

**Files:**
- Create: `internal/jam/userui/api_boards.go`, `internal/jam/userui/api_boards_test.go`
- Modify: `internal/jam/userui/apitypes.go`, `boards.go`, `api.go`, `api_conversation.go`, `api_conversation_test.go`, `api_test.go`
- Modify: `web/app/src/api/types.ts` (generated), and the TS test factories (`board`/`newBoard` are optional or added as needed)

**Interfaces:**
- Produces (Go types):
  - `BoardCard { ID "id"; Title "title"; Built "-" }`;
  - `MessageRow.Board *BoardCard "board,omitempty"`;
  - `Conversation.NewBoard bool "newBoard"`;
  - `BoardInfo { ID "id"; Title "title"; Channel "channel"; ReadOnly bool "readOnly" }`;
  - `NewBoardRequest { Channel "channel"; Title "title" }`;
  - `NewBoardResponse { Board "board"; Channel "channel" }`.
- Produces (routes):
  - `GET /app/user/api/boards/{id}` answers `BoardInfo`, or 404 `not_found` "no such board" (missing, unreadable, or whiteboards off).
  - `POST /app/user/api/boards` takes `NewBoardRequest` and answers 201 `NewBoardResponse`. Errors:
    - 400 `bad_request` with the store's message for an invalid title;
    - 404 `not_found` "no such conversation" for a channel you may not post to;
    - 500 `internal`.
- `Bootstrap.Boards` is `d.Boards != nil`. The app bundles the editor.
- The API conversation's `newBoard` is `d.Boards != nil && conv.SendTo != "" && d.Boards.MayCreateIn(p.UserID, channel)`.

- [ ] **Step 1: Write the failing tests.**

Make these changes to existing tests:
- **`TestConversationJSONShape`:** add `"newBoard"` to the present keys, and remove `"NewBoard"`/`"newBoard"` from the template-only list. In the message, set `Board: &BoardCard{ID: "brd_1", Title: "Plan", Built: true}` on a second row and assert `m2["board"]` is `{"id":"brd_1","title":"Plan"}` with no `"built"` key. Remove `"Board"`/`"board"` from the message's forbidden keys; the first row has no board, so `omitempty` still keeps `board` off it.
- **`TestAPIBootstrap`** (`api_test.go`): with `e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)`, `boards` is `true` even when `uiassets.WhiteboardBuilt()` is false; with `Boards` nil it is `false`.

Create `api_boards_test.go`:

```go
package userui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

func TestAPIBoard(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	b, err := e.Deps.Boards.Create(jam.Poster{ID: e.alice.ID}, engID, "Arch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(e.Deps, nil)
	rec := apiGet(t, h, &p, "/app/user/api/boards/"+string(b.ID))
	if rec.Code != 200 {
		t.Fatalf("board = %d %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var info BoardInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info != (BoardInfo{ID: string(b.ID), Title: "Arch", Channel: engID, ReadOnly: false}) {
		t.Errorf("board = %+v", info)
	}
	// Unseen and missing answer alike; so does whiteboards off.
	bob, err := e.st.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	outsider := jam.Participant{UserID: bob.ID, Name: "bob", Projects: []string{"proj"}}
	hidden := apiGet(t, h, &outsider, "/app/user/api/boards/"+string(b.ID))
	gone := apiGet(t, h, &p, "/app/user/api/boards/brd_01j9q3zzzzzzzzzzzzzzzzzz")
	if hidden.Code != 404 || hidden.Body.String() != gone.Body.String() || !strings.Contains(gone.Body.String(), "no such board") {
		t.Errorf("hidden %d %s, missing %d %s", hidden.Code, hidden.Body, gone.Code, gone.Body)
	}
	e.Deps.Boards = nil
	if rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/boards/"+string(b.ID)); rec.Code != 404 {
		t.Errorf("boards off = %d", rec.Code)
	}
}

func TestAPINewBoard(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	h := Handler(e.Deps, nil)
	rec := apiPost(t, h, p, "/app/user/api/boards", `{"channel":"`+engID+`","title":"Plan"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var got NewBoardResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !strings.HasPrefix(got.Board, "brd_") || got.Channel != engID {
		t.Errorf("created %+v", got)
	}
	if b, ok := e.Intercom.GetBoard(ident.ID(got.Board)); !ok || b.Title != "Plan" {
		t.Errorf("stored %+v %v", b, ok)
	}
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"channel":"` + engID + `","title":""}`, 400, `"code":"bad_request"`},
		{`{"channel":"chn_01j9q3zzzzzzzzzzzzzzzzzz","title":"x"}`, 404, "no such conversation"},
		{`{"channel":`, 400, `"code":"bad_request"`},
	} {
		if rec := apiPost(t, h, p, "/app/user/api/boards", tc.body); rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s = %d %s, want %d %s", tc.body, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
}
```

Some details for the implementer to check:
- **`e.Intercom.GetBoard` or `e.lg.GetBoard`:** use whichever the code base exposes. `boardCard` uses `d.Log.GetBoard`.
- **The empty title:** check that `Boards.Create` refuses it with `intercom.ErrInvalid`. If it doesn't, use the store's actual invalid case, for example a 300-character title, and ledger it.
- **A read-only case:** add one if the fixture has a channel alice can read but not write; `CanWrite` is the rule. Otherwise ledger that it's covered by Task 3's UI test.

- [ ] **Step 2: Run them to see them fail.**

Run: `GOPROXY=https://proxy.golang.org go test -run 'TestAPIBoard|TestAPINewBoard|TestConversationJSONShape|TestAPIBootstrap' ./internal/jam/userui/`

Expected: a compile error, `undefined: BoardInfo`.

- [ ] **Step 3: Implement.**

Move `BoardCard` from `boards.go` into `apitypes.go`:

```go
// BoardCard is a board's card in its conversation (MessageRow.Board).
type BoardCard struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Built bool   `json:"-"` // the old page's editor bundle is built
}

// BoardInfo is GET api/boards/{id}: the board the app's board route opens,
// its conversation, and whether the viewer may only look.
type BoardInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Channel  string `json:"channel"`
	ReadOnly bool   `json:"readOnly"`
}

// NewBoardRequest is POST api/boards: a whiteboard in a conversation the
// viewer may post to.
type NewBoardRequest struct {
	Channel string `json:"channel"`
	Title   string `json:"title"`
}

// NewBoardResponse is the new board and its conversation.
type NewBoardResponse struct {
	Board   string `json:"board"`
	Channel string `json:"channel"`
}
```

Change these fields' tags:
- `MessageRow.Board`: `json:"board,omitempty"`, with the comment "the board's card (Card == "board")";
- `Conversation.NewBoard`: `json:"newBoard"`, with the comment "offer New whiteboard".

`Bootstrap.Boards`' comment becomes "whiteboards on".

`api.go`:
- in `bootstrap`, `Boards: h.d.Boards != nil` (the app bundles its editor);
- in `apiRoutes`, add `h.boardRoutes(mux)` before the catch-all.

`api_conversation.go`'s `apiConversation`, after `Session`:

```go
	conv.NewBoard = h.d.Boards != nil && conv.SendTo != "" && h.d.Boards.MayCreateIn(p.UserID, ident.ID(conv.ChannelID))
```

`api_boards.go`:

```go
package userui

import (
	"errors"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

func (h *handler) boardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/boards/{id}", h.apiBoard)
	mux.HandleFunc("POST /app/user/api/boards", h.apiNewBoard)
}

// apiBoard is a board the viewer may read (the app's board route): 404 alike
// for missing, unreadable and whiteboards off.
func (h *handler) apiBoard(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	var b intercom.Board
	if h.d.Boards != nil {
		b, _, ok = h.d.Boards.CanRead(p.UserID, ident.ID(r.PathValue("id")))
	}
	if h.d.Boards == nil || !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such board")
		return
	}
	_, writable := h.d.Boards.CanWrite(p.UserID, b.ID)
	writeJSON(w, http.StatusOK, BoardInfo{ID: string(b.ID), Title: b.Title, Channel: string(b.Channel), ReadOnly: !writable})
}

// apiNewBoard starts a whiteboard in a conversation the viewer may post to
// (jam.Boards.Create's checks).
func (h *handler) apiNewBoard(w http.ResponseWriter, r *http.Request) {
	var req NewBoardRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	if h.d.Boards == nil {
		notFound(w)
		return
	}
	b, err := h.d.Boards.Create(jam.Poster{ID: p.UserID}, req.Channel, req.Title, time.Now())
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, NewBoardResponse{Board: string(b.ID), Channel: string(b.Channel)})
	case errors.Is(err, intercom.ErrInvalid):
		writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, jam.ErrSendDenied), errors.Is(err, jam.ErrSendUnresolved), errors.Is(err, jam.ErrRemoved):
		notFound(w)
	default:
		h.lg.Error("api new board failed", "channel", req.Channel, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "couldn't start the whiteboard")
	}
}
```

Notes:
- **Field and method names:** check `CanRead`'s and `Create`'s exact return types (`intercom.Board` with fields `ID`, `Title`, `Channel`) against `internal/jam/boards.go`, and adjust the conversions.
- **The bad-JSON case:** `decodeJSON`'s 400 covers it.

Regenerate the types. Then add `newBoard: false` to the TS `Conversation` factories that need it: `ConversationView.test.tsx`, `AskCard.test.tsx`, `NewMessage.test.tsx` and `SessionView.test.tsx`.

- [ ] **Step 4: Run them to see them pass.**

Run:
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/userui/`
- `cd web/app && npm run check`
- `./scripts/app-types-check.sh` (after `git add`)

Expected: PASS, with `boards_test.go` unchanged.

- [ ] **Step 5: Commit.** Message: `jam app API: board cards, New whiteboard, and board info`.

---

### Task 2: Cards, New whiteboard, and live `board` signals

**Files:**
- Create: `web/app/src/boards/BoardCard.tsx`, `web/app/src/boards/NewBoard.tsx`, `web/app/src/boards/boards.test.tsx`
- Modify: `web/app/src/conversation/Message.tsx`, `web/app/src/conversation/ConversationHeader.tsx`, `web/app/src/session/PaneTabs.tsx`, `web/app/src/live/useLiveEvents.ts`, `web/app/src/live/useLiveEvents.test.tsx`, `web/app/src/session/SessionView.test.tsx`

**Interfaces:**
- Consumes: `MessageRow.board`, `Conversation.newBoard`, `NewBoardResponse` (Task 1); `useBoot().boards`.
- Produces:
  - `<BoardCard m={MessageRow} />` renders:
    - 🖍 + the title;
    - "from · time" (`timeOf(m.time)`);
    - a thumbnail `<img src="/app/user/boards/{id}/thumb.png" alt={title} loading="lazy">` linking to `/app/next/boards/{id}`;
    - an "Open" link (React Router `Link`).
  - `<NewBoard channel />` is a "New whiteboard" button. It opens a Modal titled "New whiteboard" with a "Title" TextInput and a "Start" button, POSTs `boards` `{channel, title}`, and navigates to `/app/next/boards/{board}`. A refusal shows as a red notification.
  - `useLiveEvents`: `es.addEventListener("board", (e) => window.dispatchEvent(new CustomEvent("jam-board", { detail: e.data })))`.
  - Artifacts' board links become `Link`s to `/app/next/boards/{id}`.

- [ ] **Step 1: Write the failing tests.**

`useLiveEvents.test.tsx`: add

```ts
  it("re-dispatches board signals as jam-board window events", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    mount();
    const got: string[] = [];
    const on = (e: Event) => got.push((e as CustomEvent<string>).detail);
    window.addEventListener("jam-board", on);
    FakeEventSource.last!.listeners.board?.forEach((fn) => (fn as (e: MessageEvent) => void)(new MessageEvent("board", { data: "brd_1" })));
    window.removeEventListener("jam-board", on);
    expect(got).toEqual(["brd_1"]);
  });
```

The fake's `listeners` type takes `() => void`. Widen it to `(e?: MessageEvent) => void` if needed.

`boards.test.tsx`:

```tsx
import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, MessageRow } from "../api/types";

const card: MessageRow = { from: "alice", body: "", raw: "", plain: false, at: "09:00", time: new Date(2026, 9, 26, 9, 0).toISOString(), mine: false, seq: 4,
  system: false, sysText: "", card: "board", board: { id: "brd_1", title: "Plan" } };
const conv = (over: Partial<Conversation> = {}): Conversation => ({
  channelId: "chn_a", label: "eng", kind: "room", project: "test", phase: "", needsYou: false, sendTo: "chn_a", messages: [card], lastSeq: 4, earlier: false,
  sessionIds: [], sessions: [], canJoin: false, canLeave: true, callIn: [], openAsks: [], session: "", newBoard: true, ...over,
});
function serve(c: Conversation) {
  const created: unknown[] = [];
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/conversations/:c", () => HttpResponse.json(c)),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.post("/app/user/api/conversations/:c/read", () => new HttpResponse(null, { status: 204 })),
    http.get("/app/user/api/boards/:id", ({ params }) => HttpResponse.json({ id: params.id, title: "Plan", channel: "chn_a", readOnly: false })),
    http.post("/app/user/api/boards", async ({ request }) => {
      created.push(await request.json());
      return HttpResponse.json({ board: "brd_9", channel: "chn_a" }, { status: 201 });
    }),
  );
  return created;
}
const withBoards = { ...boot, boards: true };

describe("boards in a conversation", () => {
  it("shows a board's card: title, who and when, the thumbnail, linking to the board", async () => {
    serve(conv());
    renderApp(withBoards, "/app/next/c/chn_a");
    const c = (await screen.findByText(/Plan/)).closest("[data-board]") as HTMLElement;
    expect(within(c).getByText(/alice/)).toBeInTheDocument();
    expect(within(c).getByRole("img", { name: "Plan" })).toHaveAttribute("src", "/app/user/boards/brd_1/thumb.png");
    expect(within(c).getByRole("link", { name: "Open" })).toHaveAttribute("href", "/app/next/boards/brd_1");
  });

  it("starts a whiteboard with a title and opens it", async () => {
    const created = serve(conv());
    const user = userEvent.setup();
    renderApp(withBoards, "/app/next/c/chn_a");
    await user.click(await screen.findByRole("button", { name: "New whiteboard" }));
    await user.type(await screen.findByLabelText("Title"), "Sketch");
    await user.click(screen.getByRole("button", { name: "Start" }));
    await waitFor(() => expect(created).toEqual([{ channel: "chn_a", title: "Sketch" }]));
    expect(await screen.findByRole("link", { name: "← Back to chat" })).toHaveAttribute("href", "/app/next/c/chn_a");
  });

  it("offers New whiteboard only where the viewer may start one", async () => {
    serve(conv({ newBoard: false }));
    renderApp(withBoards, "/app/next/c/chn_a");
    await screen.findByText(/Plan/);
    expect(screen.queryByRole("button", { name: "New whiteboard" })).toBeNull();
  });
});
```

The second test lands on the board route, which Task 3 builds. Until then it fails at the "Back to chat" line. Mark that assertion `// Task 3` and expect it to pass after Task 3.

In `SessionView.test.tsx`'s "lists previews, artifacts and status", the board link becomes `/app/next/boards/brd_1`.

- [ ] **Step 2: Run them to see them fail.**

Run: `cd web/app && npx vitest run src/boards src/live src/session`

Expected: FAIL. The card is still the placeholder, there's no New whiteboard button, and no `jam-board` event fires.

- [ ] **Step 3: Implement.**

`BoardCard.tsx`:

```tsx
import { Anchor, Image, Paper, Text } from "@mantine/core";
import { Link } from "react-router";
import type { MessageRow } from "../api/types";
import { timeOf } from "../conversation/days";

// BoardCard is a board's card where it was started: its thumbnail (the
// server's render) and a link into the board.
export function BoardCard({ m }: { m: MessageRow }) {
  const b = m.board!;
  const href = `/app/next/boards/${encodeURIComponent(b.id)}`;
  return (
    <Paper data-seq={m.seq} data-board={b.id} withBorder p="xs" my={4} maw={360}>
      <Text size="sm" fw={600}>🖍 {b.title} <Text span size="xs" c="dimmed" fw={400}>{m.mine ? "you" : m.from} · {timeOf(m.time)}</Text></Text>
      <Anchor component={Link} to={href} display="block" mt={4}>
        <Image loading="lazy" src={`/app/user/boards/${encodeURIComponent(b.id)}/thumb.png`} alt={b.title} radius="sm" bg="white" fit="contain" mah={200} />
      </Anchor>
      <Anchor component={Link} to={href} size="sm">Open</Anchor>
    </Paper>
  );
}
```

`Message.tsx`: before the `m.card` placeholder branch, add `if (m.card === "board" && m.board) return <BoardCard m={m} />;`. The placeholder stays for any other card.

`NewBoard.tsx`:

```tsx
import { Button, Group, Modal, Stack, TextInput } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useState } from "react";
import { useNavigate } from "react-router";
import { apiPost, type ApiError } from "../api/client";
import type { NewBoardResponse } from "../api/types";

// NewBoard is New whiteboard: a title, then the new board, opened.
export function NewBoard({ channel }: { channel: string }) {
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const go = useNavigate();
  const start = async () => {
    setBusy(true);
    try {
      const b = await apiPost<NewBoardResponse>("boards", { channel, title: title.trim() });
      setOpen(false);
      void go(`/app/next/boards/${encodeURIComponent(b.board)}`);
    } catch (e) {
      notifications.show({ color: "red", message: (e as ApiError).message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Button size="xs" variant="default" onClick={() => { setTitle(""); setOpen(true); }}>New whiteboard</Button>
      <Modal opened={open} onClose={() => setOpen(false)} title="New whiteboard" size="sm">
        <form onSubmit={(e) => { e.preventDefault(); if (title.trim()) void start(); }}>
          <Stack>
            <TextInput label="Title" value={title} onChange={(e) => setTitle(e.currentTarget.value)} data-autofocus />
            <Group justify="flex-end"><Button type="submit" disabled={!title.trim()} loading={busy}>Start</Button></Group>
          </Stack>
        </form>
      </Modal>
    </>
  );
}
```

`ConversationHeader.tsx`: in the control group, add `{c.newBoard && boot.boards && <NewBoard channel={c.channelId} />}`, using `useBoot()`.

`useLiveEvents.ts`: add the `board` listener, with the comment "the open board's editor listens for these (the old page's contract)".

`PaneTabs.tsx`'s Artifacts board anchor becomes `component={Link} to={`/app/next/boards/${enc(b.id)}`}`. Drop the "current Inbox" comment.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run && npm run typecheck && npm run lint`

Expected: PASS, except the one assertion marked `// Task 3`. Comment it out with `// Task 3` if it blocks the suite, then restore it in Task 3. Ledger this.

- [ ] **Step 5: Commit.** Message: `jam app: board cards, New whiteboard, live board signals`.

---

### Task 3: The board route and the sync logic

**Files:**
- Create: `web/app/src/boards/sync.ts`, `web/app/src/boards/sync.test.ts`, `web/app/src/boards/BoardPage.tsx`, `web/app/src/boards/BoardPage.test.tsx`, `web/app/src/boards/BoardEditor.tsx`, `web/app/src/boards/assetPath.ts`
- Modify: `web/app/src/App.tsx`, `web/app/package.json`, `package-lock.json` (`npm install --save-exact @excalidraw/excalidraw@0.18.1`)

**Interfaces:**
- Produces (`sync.ts`):
  - the constants `SEND_DELAY = 300`, `MAX_WAIT = 1000`, `MAX_ELEMENT = 60000`, `BATCH = 500`, `POLL = 30000`, `RETRY_MAX = 30000`;
  - `type El = { id: string; version: number }`;
  - `pending(els: readonly El[], sent: Map<string, number>): El[]`, the elements whose version Jam doesn't have;
  - `snapshot(els: readonly El[], sent: Map<string, number>): { batches: { id: string; version: number; json: string }[][]; tooLarge: boolean }`. It JSON-encodes each pending element, marks one over `MAX_ELEMENT` as sent (it is never sent, and sets `tooLarge`), and splits the rest into batches of `BATCH`;
  - `body(batch): string`, giving `'{"elements":[' + jsons + "]}"`;
  - `nextRetry(ms: number): number`, which doubles up to `RETRY_MAX`.
- Produces (route and components):
  - `/app/next/boards/:id` is `<BoardPage />`. It fetches `api/boards/{id}` and renders a bar:
    - "← Back to chat" (`Link` to `/app/next/c/{channel}`);
    - the title;
    - a "view only" badge when `readOnly`;
  - and then `<Suspense><BoardEditor id readOnly /></Suspense>`, with `const BoardEditor = lazy(() => import("./BoardEditor"))`.
  - A 404 shows "This whiteboard isn't available to you." and never loads the editor.
  - `BoardEditor` (default export, `{ id: string; readOnly: boolean }`) is the port of `entry.jsx`'s `Board`, using `sync.ts`. It sets `data-board-editor` and exposes `jamElementCount()` on its root element (for tests).
  - `assetPath.ts` sets `window.EXCALIDRAW_ASSET_PATH`. It is imported first by `BoardEditor`.

- [ ] **Step 1: Write the failing tests.**

`sync.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { body, BATCH, MAX_ELEMENT, nextRetry, pending, snapshot } from "./sync";

describe("board sync", () => {
  it("sends only versions Jam doesn't have", () => {
    const sent = new Map([["a", 2], ["b", 1]]);
    expect(pending([{ id: "a", version: 2 }, { id: "b", version: 3 }, { id: "c", version: 1 }], sent).map((e) => e.id)).toEqual(["b", "c"]);
  });

  it("batches, and never lets an oversized element block the rest", () => {
    const sent = new Map<string, number>();
    const els = Array.from({ length: BATCH + 2 }, (_, i) => ({ id: `e${i}`, version: 1 }));
    const big = { id: "big", version: 4, pad: "x".repeat(MAX_ELEMENT) };
    const s = snapshot([...els, big], sent);
    expect(s.batches.map((b) => b.length)).toEqual([BATCH, 2]);
    expect(s.tooLarge).toBe(true);
    expect(sent.get("big")).toBe(4); // settled: it is never re-tried
    expect(body(s.batches[1]!)).toBe(`{"elements":[${JSON.stringify(els[BATCH])},${JSON.stringify(els[BATCH + 1])}]}`);
  });

  it("backs off by doubling to 30 s", () => {
    expect([nextRetry(1000), nextRetry(16000), nextRetry(30000)]).toEqual([2000, 30000, 30000]);
  });
});
```

`BoardPage.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";

// Excalidraw needs a real canvas: the editor is stubbed here (the browser
// test drives the real one).
vi.mock("./BoardEditor", () => ({ default: ({ id, readOnly }: { id: string; readOnly: boolean }) => <div data-testid="editor">{id}:{String(readOnly)}</div> }));

const serve = (status: number, readOnly = false) =>
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/boards/:id", ({ params }) => status === 200
      ? HttpResponse.json({ id: params.id, title: "Plan", channel: "chn_a", readOnly })
      : HttpResponse.json({ error: "no such board", code: "not_found" }, { status })),
  );

describe("the board route", () => {
  it("opens the editor under a bar back to its conversation", async () => {
    serve(200);
    renderApp({ ...boot, boards: true }, "/app/next/boards/brd_1");
    expect(await screen.findByTestId("editor")).toHaveTextContent("brd_1:false");
    expect(screen.getByRole("link", { name: "← Back to chat" })).toHaveAttribute("href", "/app/next/c/chn_a");
    expect(screen.getByText("Plan")).toBeInTheDocument();
    expect(screen.queryByText("view only")).toBeNull();
  });

  it("says view only to a reader", async () => {
    serve(200, true);
    renderApp({ ...boot, boards: true }, "/app/next/boards/brd_1");
    expect(await screen.findByTestId("editor")).toHaveTextContent("brd_1:true");
    expect(screen.getByText("view only")).toBeInTheDocument();
  });

  it("says so for a board that isn't there or isn't theirs, without loading the editor", async () => {
    serve(404);
    renderApp({ ...boot, boards: true }, "/app/next/boards/brd_x");
    expect(await screen.findByText("This whiteboard isn't available to you.")).toBeInTheDocument();
    expect(screen.queryByTestId("editor")).toBeNull();
  });
});
```

Restore Task 2's "starts a whiteboard…" assertion that was marked `// Task 3`. It needs `boards.test.tsx` to mock `./BoardEditor` the same way, so add the same `vi.mock` there.

- [ ] **Step 2: Run them to see them fail.**

Run: `npx vitest run src/boards`

Expected: FAIL, "Failed to resolve import ./sync" and the missing route.

- [ ] **Step 3: Implement.**

Install: `npm install --save-exact --no-audit --no-fund @excalidraw/excalidraw@0.18.1`.

`sync.ts`:

```ts
// The board sync's rules (web/whiteboard/entry.jsx, ported): Jam merges per
// element; the editor sends only versions Jam lacks, in batches, never an
// element over Jam's per-element cap (64 KB; 60 000 bytes of JSON here).
export const SEND_DELAY = 300; // ms after the last change
export const MAX_WAIT = 1000; // ms: save at least this often during a long drag
export const MAX_ELEMENT = 60000;
export const BATCH = 500;
export const POLL = 30000; // ms: catch up if a live push was missed
export const RETRY_MAX = 30000;

export interface El { id: string; version: number }
export interface Snap { id: string; version: number; json: string }

export const pending = <T extends El>(els: readonly T[], sent: Map<string, number>): T[] => els.filter((e) => sent.get(e.id) !== e.version);

// snapshot freezes what to send now (Excalidraw mutates elements in place,
// so a later edit is never mistaken for one already saved). An oversized
// element counts as sent, so it never blocks the rest.
export function snapshot(els: readonly El[], sent: Map<string, number>): { batches: Snap[][]; tooLarge: boolean } {
  const out: Snap[] = [];
  let tooLarge = false;
  for (const e of pending(els, sent)) {
    const json = JSON.stringify(e);
    if (json.length > MAX_ELEMENT) {
      sent.set(e.id, e.version);
      tooLarge = true;
      continue;
    }
    out.push({ id: e.id, version: e.version, json });
  }
  const batches: Snap[][] = [];
  for (let i = 0; i < out.length; i += BATCH) batches.push(out.slice(i, i + BATCH));
  return { batches, tooLarge };
}

export const body = (batch: Snap[]) => '{"elements":[' + batch.map((b) => b.json).join(",") + "]}";
export const nextRetry = (ms: number) => Math.min(ms * 2, RETRY_MAX);
```

`assetPath.ts`:

```ts
// Excalidraw loads its fonts from window.EXCALIDRAW_ASSET_PATH + "fonts/…";
// scripts/app.sh copies them into the bundle there. Imported before
// Excalidraw itself, so it is set when Excalidraw's modules evaluate.
declare global { interface Window { EXCALIDRAW_ASSET_PATH?: string | string[] } }
window.EXCALIDRAW_ASSET_PATH = `${location.origin}/app/next/assets/excalidraw/`;
export {};
```

`BoardEditor.tsx` ports `entry.jsx`'s `Board`, which stays as the reference. The changes:
- types, from `@excalidraw/excalidraw/types` (`ExcalidrawImperativeAPI`) and `@excalidraw/excalidraw/element/types` (`OrderedExcalidrawElement`);
- the base `/app/user`;
- `sync.ts` for `pending`, `snapshot` and `body`;
- the `jam-board` window event, filtered to `detail === id`;
- the root `div` sizes the canvas (`position: absolute; inset: 0` inside a relative, flex-filling parent).

Its imports are `import "./assetPath";` **first**, then `import { Excalidraw, reconcileElements, restoreElements, CaptureUpdateAction } from "@excalidraw/excalidraw"; import "@excalidraw/excalidraw/index.css";`.

On top of the port, it adds these behaviors:
- **Flush on leave.** On unmount (in-app navigation, such as Back to chat), a debounced save is run at once (`send()` without waiting) rather than dropped. `beforeunload` keeps the warning.
- **Root element.** The root is `<div data-board-editor ref={root}>`, and `root.current.jamElementCount = () => api.getSceneElements().length`, as the old one did, for the browser test.
- **Save errors.** They show in a `Text c="red"` banner over the canvas. The texts are "save failed (…)" and "a drawing is too large to save; split it into smaller strokes".

`BoardPage.tsx`:

```tsx
import { Anchor, Badge, Group, Loader, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense } from "react";
import { Link, useParams } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { BoardInfo } from "../api/types";

const BoardEditor = lazy(() => import("./BoardEditor"));

// BoardPage is /app/next/boards/:id: the board (its own chunk, Excalidraw)
// under a bar back to its conversation.
export function BoardPage() {
  const { id = "" } = useParams();
  const q = useQuery<BoardInfo, ApiError>({ queryKey: ["board", id], queryFn: () => apiGet<BoardInfo>(`boards/${encodeURIComponent(id)}`) });
  if (q.isPending) return <Loader m="md" size="sm" />;
  if (!q.data) return <Text m="md">{q.error?.status === 404 ? "This whiteboard isn't available to you." : q.error?.message}</Text>;
  const b = q.data;
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "calc(100dvh - var(--app-shell-header-height) - 2 * var(--app-shell-padding))" }}>
      <Group gap="sm" px="sm" py={6} style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
        <Anchor component={Link} to={`/app/next/c/${encodeURIComponent(b.channel)}`} size="sm">← Back to chat</Anchor>
        <Text fw={700} size="sm">{b.title}</Text>
        {b.readOnly && <Badge size="xs" variant="light">view only</Badge>}
      </Group>
      <div style={{ flex: 1, minHeight: 0, position: "relative" }}>
        <Suspense fallback={<Loader m="md" size="sm" />}><BoardEditor key={b.id} id={b.id} readOnly={b.readOnly} /></Suspense>
      </div>
    </div>
  );
}
```

`App.tsx`: add `{ path: "boards/:id", element: <BoardPage /> }`.

Notes:
- **The title:** "Plan" appears in the bar only, because the rail is empty in the test.
- **Types:** check `@excalidraw/excalidraw` 0.18.1's type export paths against `node_modules/@excalidraw/excalidraw/dist/types/`. If a subpath isn't exported, import the types from the package root, and ledger it.
- **The lazy split:** `BoardPage` must not import `@excalidraw/excalidraw` directly, so it stays out of the initial chunk.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run && npm run typecheck && npm run lint && cd ../.. && just app`

Expected:
- the tests PASS;
- the size budget still passes, because Excalidraw is in a `BoardEditor-*.js` chunk.

- [ ] **Step 5: Commit.** Message: `jam app: the board route (lazy Excalidraw) and its sync`.

---

### Task 4: Fonts from the bundle, and end-to-end under the CSP

**Files:**
- Modify: `scripts/app.sh`, `web/app/src/boards/assetPath.ts` (if the fix needs it), `internal/jam/userui/next_browser_test.go`

**Interfaces:**
- `scripts/app.sh`, after the Vite build into its scratch directory, copies `web/app/node_modules/@excalidraw/excalidraw/dist/prod/fonts/*` (except `Xiaolai`) into `<scratch>/assets/excalidraw/fonts/`.
- `webapp` serves them as immutable assets. They live under `assets/`, so a missing one is a 404, not `index.html`.

- [ ] **Step 1: Write the failing browser test** (append to `next_browser_test.go`):

```go
// The app's board route mounts Excalidraw under the app's CSP, loading every
// font from the app's own bundle (no CDN), saves a drawn rectangle, and shows
// an agent's edit pushed live — and a reader can't draw.
func TestBrowserNextBoard(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	b, err := e.Deps.Boards.Create(jam.Poster{ID: e.alice.ID}, engID, "Arch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, nil)
	var mu sync.Mutex
	var foreign []string // requests the page made to anything but this server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = appui.WithPrincipal(jam.WithParticipant(r, p), appui.Principal{User: &p})
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	var problems pageProblems
	var fontsLoaded bool
	err = chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.EmulateViewport(1400, 1000),
		chromedp.Navigate(srv.URL+"/app/next/boards/"+string(b.ID)),
		chromedp.WaitVisible(chromedp.CSS(`.excalidraw canvas`)),
		chromedp.Click(chromedp.CSS(`[data-testid="toolbar-rectangle"]`)),
		chromedp.MouseEvent(chromedp.MouseMoved, 700, 450),
		chromedp.MouseEvent(chromedp.MousePressed, 700, 450, chromedp.ButtonLeft),
		chromedp.MouseEvent(chromedp.MouseMoved, 760, 500, chromedp.ButtonLeft),
		chromedp.MouseEvent(chromedp.MouseMoved, 820, 540, chromedp.ButtonLeft),
		chromedp.MouseEvent(chromedp.MouseReleased, 820, 540, chromedp.ButtonLeft),
		chromedp.Sleep(500*time.Millisecond),
		into(&fontsLoaded, chromedp.Evaluate[bool](`(async function(){ await document.fonts.ready;
			var r = performance.getEntriesByType('resource').map(function(e){ return e.name; });
			window.__foreign = r.filter(function(n){ return n.indexOf(location.origin) !== 0; });
			return r.some(function(n){ return n.indexOf('/app/next/assets/excalidraw/fonts/') >= 0; }); })()`, chromedp.EvalAsPromise)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var outside []string
	if err := chromedp.Do(ctx, into(&outside, chromedp.Evaluate[[]string](`window.__foreign`))); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	foreign = append(foreign, outside...)
	mu.Unlock()
	if !fontsLoaded || len(foreign) > 0 {
		t.Errorf("fonts from the bundle: %v; requests elsewhere: %v", fontsLoaded, foreign)
	}
	var saved []json.RawMessage
	for range 50 {
		saved, _, _ = e.Deps.Boards.Elements(e.alice.ID, b.ID, 0)
		if len(saved) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(saved) != 1 || !strings.Contains(string(saved[0]), `"type":"rectangle"`) {
		t.Fatalf("saved = %s", saved)
	}
	if _, _, err := e.Deps.Boards.Apply(e.alice.ID, b.ID, []jam.BoardOp{{Op: "add", Type: "ellipse", ID: "agent-shape", Text: "from an agent"}}); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Do(ctx, chromedp.Poll[chromedp.Void](`document.querySelector('[data-board-editor]').jamElementCount() >= 3`, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("the agent's edit didn't arrive: %v", err)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}
```

Notes:
- **The `chromedp` API:** check `chromedp.EvalAsPromise` (or the v0.21 generic option name) against the module, and use `chromedp.Evaluate[[]string]` as the other tests use `Evaluate[T]`.
- **Excalifont:** if no text is drawn, the fonts may never be requested. Then `fontsLoaded` stays false, because Excalidraw loads fonts lazily, per use. In that case, add a text element first: use the agent's `Apply` with `Text` *before* checking, and wait for it to render. The assertion that matters is `len(foreign) == 0` together with **no CSP problems**, which is the spike's failure mode.
- **The agent's edit** reaches the page through the events stream's `board` signal. `Handler` serves `/app/user/events`, and `Boards.Apply` publishes to `Boards.Subscribe`.
- **Imports:** `encoding/json` and `sync` if they're new to the file.

Run: `just app && GOPROXY=https://proxy.golang.org go test -tags browser -run TestBrowserNextBoard ./internal/jam/userui/`

Expected: FAIL with `fonts from the bundle: false; requests elsewhere: [https://esm.sh/…]` and CSP problems, which is the spike's finding.

- [ ] **Step 2: Fix the fonts.**

1. In `scripts/app.sh`, after the Vite build into the scratch directory (the script's existing `$tmp`, or whatever it names it), add:

   ```sh
   # Excalidraw's fonts, served from the bundle (no CDN; the CSP allows
   # font-src 'self' only), without the 13 MB CJK Xiaolai set (as web/whiteboard).
   fonts=web/app/node_modules/@excalidraw/excalidraw/dist/prod/fonts
   mkdir -p "$tmp/assets/excalidraw/fonts"
   for f in "$fonts"/*; do
     [ "$(basename "$f")" = Xiaolai ] || cp -R "$f" "$tmp/assets/excalidraw/fonts/"
   done
   ```

2. Rebuild and re-run the test. If fonts still come from `esm.sh`, which is what the spike saw even with the path set, use **systematic-debugging** and find out why Excalidraw 0.18.1 ignores `EXCALIDRAW_ASSET_PATH`:
   - Read the font URL code (`ExcalidrawFontFace.createUrls` and its callers) in `node_modules/@excalidraw/excalidraw/dist/prod/`.
   - Check whether the font sources are built **before** `assetPath.ts` runs (module evaluation order inside Vite's chunk), whether the `uri`s are absolute in the dev/prod build Vite picks, and whether `document.fonts` is filled from `@font-face` rules in `index.css`.
   - The fix must keep `font-src 'self'`. Acceptable fixes:
     - set the path earlier (an entry module, or `index.html`'s external script);
     - point Vite's resolve at the `prod` build;
     - or, as a last resort, register the `@font-face` rules ourselves from the bundled files.
   - Ledger the cause and the fix.

3. Old page: leave its editor alone (it is deleted at cutover), but record the finding in the PR body.

Run the browser test until it passes. Then run the whole suite.

Run:
- `just app && GOPROXY=https://proxy.golang.org go test -tags browser ./internal/jam/userui/`
- `just app-check`

Expected: PASS, with no page problems and `TestBrowserWhiteboardEditor` unchanged.

- [ ] **Step 3: Commit.** Message: `jam app: Excalidraw's fonts from the bundle; the board route end-to-end`.

---

### Task 5: Docs

**Files:**
- Modify: `docs/web-app.md`, `docs/usage/jam/whiteboards.md`, `docs/usage/jam/intercom-ui.md`, `docs/superpowers/specs/2026-10-26-jam-spa-design.md`

- [ ] **Step 1: Write the docs.**

In `docs/web-app.md`'s Layout, add:

```markdown
  - `src/boards/` holds whiteboards:
    - `BoardCard`, the card in a message;
    - `NewBoard`, which uses `POST /app/user/api/boards`;
    - `BoardPage`, the `/app/next/boards/:id` route, with info from `GET /app/user/api/boards/{id}`;
    - `BoardEditor`, the lazy Excalidraw chunk, ported from `web/whiteboard/entry.jsx`, with the rules in `sync.ts`. It syncs through the unchanged `/app/user/boards/{id}/elements` and refetches on the window event `jam-board`, which `useLiveEvents` re-dispatches from the stream's `board` signal.
    - `scripts/app.sh` copies Excalidraw's fonts into `assets/excalidraw/fonts/` (no CJK set), and `assetPath.ts` points Excalidraw at them, so no font comes from a CDN.
```

In the Build bullet, mention the fonts copy in one clause.

In `docs/usage/jam/whiteboards.md`, find the section on opening a board and add one sentence: "In the Jam app (`/app/next/`), a board opens at `/app/next/boards/<id>`; the old Inbox's `?board=` link keeps working until the cutover." Run the docs-audit on it.

In `docs/usage/jam/intercom-ui.md`'s preview section, change "whiteboards still happen here." to "whiteboards too." and keep the line minimal.

In the spec's status line, change it to `slices 1–6 merged; slice 7 (boards) in progress ([plan](../plans/2026-10-26-jam-spa-7-boards.md))`.

- [ ] **Step 2: Full verification.**

Run:
- `just app`;
- `just app-check`;
- `./scripts/app-types-check.sh`;
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/...`;
- `GOPROXY=https://proxy.golang.org go test -tags browser ./internal/jam/userui/`;
- `STRICT=1 ./scripts/lint.sh`;
- `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs`, checking that no new errors come from the touched docs.

Expected: all PASS.

- [ ] **Step 3: Commit.** Message: `jam app: boards docs`.

---

## Self-review notes

- **Spec coverage:**
  - the lazy route: Task 3;
  - board cards and thumbnails: Task 2, with the thumbnail route unchanged;
  - live `board` updates: Task 2 (dispatch) and Task 4 (end-to-end);
  - no CDN: Task 4;
  - the CSP: Task 4's test.
- **Parity:**
  - New whiteboard: Task 2;
  - "Back to chat" and "view only": Task 3;
  - the sync rules: Task 3's `sync.ts` plus the port;
  - the unsaved warning: Task 3;
  - the agent edit arriving live: Task 4.
  - The old "whiteboard not built on this Jam" state doesn't apply to the app, which always bundles its editor.
- **Risk is front-loaded:**
  - the spike proved Excalidraw runs under the CSP except for fonts;
  - Task 4 makes the fonts provably local, with a debugging budget and ruled fallbacks.
- **Type consistency:**
  - `BoardInfo`, `NewBoardRequest`/`NewBoardResponse` and `MessageRow.board` are the same in Go, TS and the tests;
  - `BoardEditor`'s props `{id, readOnly}` match in Task 3 and the mocks.
