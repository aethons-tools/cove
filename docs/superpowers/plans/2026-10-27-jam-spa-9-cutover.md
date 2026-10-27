# Jam SPA, slice 9: cutover — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the Jam app becomes the user UI. In one PR:
- `/app/user/` page routes serve the SPA, and `/app/next/*` redirects to the same path under `/app/user/`.
- These are deleted:
  - `userui/templates`;
  - the HTML fragment and form handlers, and their tests;
  - `web/whiteboard` and its build;
  - the old Inbox's inline JS (it lives in `layout.html`, so it goes with the templates).
- The manifest and service worker stay at `/app/`, so push subscriptions survive.
- Old links keep working:
  - `/app/user/?c=<id>` (bookmarks, notification clicks, `/me/?c=` redirects) opens `/app/user/c/<id>`;
  - `&board=<id>` opens the board;
  - `&tab=<t>` opens that pane tab.

**Architecture:**
- **Go.** `userui.Handler` serves everything under `/app/user/`:
  - the JSON API and the byte/stream routes it keeps (events, uploads, blobs, board elements and thumbnails, push, the Thinking SSE feed, which becomes JSON only);
  - a catch-all `GET /app/user/` that serves the bundle through `webapp.Handler` with prefix `/app/user/`;
  - `userui.Next` and `appui.Deps.Next` go;
  - `appui` redirects `/app/next/` with its existing `LegacyRedirect`;
  - the app's `frame-src` comes through `userui.WithFrameSrc`.
- **App.**
  - Vite's `base` and every route and link move to `/app/user/`, through one constant, `APP`;
  - `Home` turns the old query links into app routes;
  - "Classic Inbox" and the "current Inbox" placeholders go.
- **Service worker.** A click opens `/app/user/c/<id>`. Old `/app/next/` tabs still count as app windows until they reload.
- **Tests.** Before any deletion, each server test that asserts *who may see or do what* through an HTML route gets a JSON twin (spec §5). The browser tests move from `/app/next/` to `/app/user/`. The old UI's browser tests go, and the shared helpers and service-worker tests stay.

**Tech Stack:** unchanged. No new dependencies; `web/whiteboard`'s go.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md) §5, Cutover:
- "`/app/user/` page routes serve the SPA, and `/app/next/*` redirects to the same path under `/app/user/`";
- "deletes `userui/templates`, the fragment handlers and their tests, `web/whiteboard`, and the userui-only JS in `uiassets/static` (admin's assets stay)";
- "The manifest and service worker stay at `/app/`. Push subscriptions survive";
- "The user checks parity on their local Jam at `/app/next/` before merge. Rollback is reverting the PR";
- §2: "The HTML routes stay until cutover and are deleted there";
- §5: "Existing userui tests that assert who may see what get JSON twins before the HTML routes are deleted";
- `DEVELOPMENT.md`'s "whiteboard editor bundle" section moves into `web-app.md`. With the bundle gone, the section is deleted rather than moved.

**`uiassets/static`:** `htmx.min.js`, `jam.css`, `sounds.js`, `push.js` and the icons are all still used by `adminui` and the shared title bar (`appui/titlebar.html`). So **nothing in `uiassets/static` is userui-only**; the old Inbox's own JS is inline in `layout.html`. Task 4 re-checks this with grep before closing it.

## Global Constraints

- **Rollback is reverting the PR.** No data or schema change. Push subscriptions, the VAPID key and the `/app/` worker scope are unchanged.
- **Authorization is unchanged:** the same gate (`RequireUser`, `RequireSameOrigin`) on `/app/user/`, and every API route keeps its checks. No test that asserts a refusal is deleted without a JSON twin in Task 1.
- **The admin UI is untouched:** `/app/admin/`, `adminui`, the shared title bar and `uiassets/static`. `/app/` landing still sends members to `/app/user/`.
- **Old links keep working:**
  - `/app/next/…` redirects with a 308 (query kept, `max-age=3600`, as the other legacy redirects);
  - `/app/user/?c=…[&board=…][&tab=…]` lands in the app;
  - `/me/…` still goes to `/app/user/…`.
- **CSP:** unchanged, now on `/app/user/` pages.
- **Size budget:** initial JS ≤ 350 KB gzipped.
- **Docs:** they ship in the same change. `intercom-ui.md` describes the app (no "preview" section), and it stays within or below its line budget.
- **gofmt:** use positional struct literals; CI runs Go 1.26.

## Review Focus

1. **Every refusal the deleted HTML tests asserted** still has a test on the JSON or stream route that replaced it:
   - who may see a conversation, a session, a board or a Thinking feed;
   - History is read-only;
   - a renamed viewer doesn't see another person's History;
   - the per-viewer feed cap;
   - the access re-check on revoke.
   (Task 1.)
2. **Old URLs:**
   - a notification's `/app/user/?c=<id>` (from an older service worker still installed) opens that conversation;
   - `&board=` opens the board, and `&tab=` the tab;
   - `/app/next/c/<id>?tab=x` redirects with its query;
   - a POST to `/app/next/…` is a 404, not a redirect.
   (Tasks 2 and 3 tests.)
3. **Route shadowing:** with a catch-all `GET /app/user/` for the SPA, every kept route still answers itself:
   - `/app/user/api/…` (including the API's JSON 404), `/app/user/events`, `/app/user/blobs/{id}`;
   - `/app/user/boards/{id}/thumb.png` and `/elements`, `/app/user/push/key`, `/app/user/session/{id}/thinking/events`;
   - `/app/user/send`, which `cmd/at-jam` mounts beside the handler.
   An unknown non-GET is a 404 or 405, never `index.html`. (Task 2 test.)
4. **Not-built bundle:** with no bundle, `/app/user/` answers the plain "not built" page (503), and the API still works. (Task 2 test.)
5. **No dead references:** nothing still references the deleted pieces:
   - `templates/`, `pages`, `inboxPage`;
   - `uiassets.Whiteboard*`, `scripts/whiteboard.sh` or `web/whiteboard`, in CI, goreleaser, `just` and `build.sh`;
   - the `/app/next` literals.
   (Task 4: build, grep, and the CI workflows' paths.)

---

### Task 1: JSON twins for the HTML routes' authorization tests

**Files:**
- Modify: `internal/jam/userui/api_test.go`, `api_conversation_test.go`, `api_sessions_test.go`, `thinking_test.go`, `api_asks_test.go`

**Approach.** Each twin exercises the same fixture and refusal as the HTML test it replaces, through the route that stays. Twins characterize existing behavior, so they pass on the first run. **To prove each one can fail**, break the guarded check temporarily (for example, make `sessionFor` return ok for anyone, or drop the History `SendTo` guard), see the twin fail, restore, and ledger it.

These are the twins, each named after its HTML original:

| HTML test (deleted in Task 3) | Twin (new, through) | Asserts |
|---|---|---|
| `TestRailUnreadAndHistory` | `TestAPIRailUnreadAndHistory` (`api/rail`, `api/conversations/{c}`) | bob's chat has `unread: 1`, then 0 after `CommitChannelRead`; the History row exists (`bucket` history, label "History (before the upgrade)" group); the History conversation has `sendTo: ""` (read-only) and its message; after `RemoveMember`, neither eng nor bob is on the rail |
| `TestHistoryIsTheViewersNameOnly` | `TestAPIHistoryIsTheViewersNameOnly` (`api/rail`) | a renamed viewer (`alice-proj`) has no History group |
| `TestRailCarriesUnreadTotal` | `TestAPIRailUnreadTotal` (`api/rail`) | `unreadTotal` equals `jam.UnreadTotal(...)`, ≥ 2 |
| `TestInboxFailsClosedWithoutParticipant` | already `TestAPIWithoutParticipantIsJSON403` | no work |
| `TestSessionCommandsMatrix`, `TestSessionCommandsAnyViewer`, `TestSessionCommandFailure` | `TestAPISessionCommandsMatrix`, `TestAPISessionCommandsAnyViewer` (`POST api/sessions/{id}/{cmd}`) | the same matrix rows (phase × activity × command → 204 or 409), using `sessAPI`; any viewer of the channel may press, and an outsider gets 404 `not_found`; a failing command is 502 `action_failed` (already in `TestAPISessionActions`, so no new row) |
| `TestSessionActionsPermissions` | already `TestAPISessionActions` | check it covers "release a standing session as operator → 403", and add it if missing |
| `TestSessionHiddenFromOutsiders`, `TestSessionPaneHiddenFromOutsiders` | already `TestAPISessionHiddenIsUniform` | no work |
| `TestSessionPaneArtifacts` | `TestAPISessionPaneArtifacts` (`api/sessions/{id}/pane/artifacts`) | the same fixture: PR and issue labels, the `(…)` URL trimmed, the PR deduped, the ask with its state, the board, and nothing from another channel |
| `TestSessionPaneStatusKit` | `TestAPISessionPaneStatusKit` | `Kit` reads `my-app@v3`, then `my-app (draft @abababa)`, never `@v0` |
| `TestThinkingHiddenFromOutsiders` | `TestThinkingJSONHiddenFromOutsiders` (`…/thinking/events?format=json`) | an outsider gets 404 |
| `TestThinkingStreamCap` | `TestThinkingJSONStreamCap` | the fifth JSON feed for one viewer is 429 |
| `TestThinkingEventsEndOnRevoke` | `TestThinkingJSONEndsOnRevoke` | with `?format=json`, the feed ends once the viewer leaves the home channel |
| `TestThinkingEventsBackfillLiveInert` | `TestThinkingJSONBackfillAndLive` | the backfill tail, then a published event, arrive as `ThinkingEvent` JSON, with agent markup as data |
| `TestThinkingNoStore` | `TestThinkingJSONNoStore` | without events, the feed answers 204 |
| `TestFrameRoute`, `TestFrameRouteOff`, `TestFrameRouteRateLimited` | already `TestAPIFrame`, `TestAPIFrameRateLimited` | no work |
| `TestAskAnswerRefusals`, `TestAskForOthersAndRail` | already `TestAPIAnswerAskRefusals`; add `TestAPIAskForOthers` (`api/conversations/{c}`) | an ask for bob shows `canAnswer: false` and `for: "bob"` to alice, and it is on the rail's Needs you for bob, not alice. Mirror the HTML test's assertions |
| `TestBoardEditorRefusesUnseen` | already `TestAPIBoard` | no work |

- [ ] **Step 1: Write each new twin.** Port the HTML test's fixture verbatim, then assert on the decoded JSON (`getConv`, `apiGet` + `json.Unmarshal` into `RailResponse`, `sessAPI` + `decodeAs`). The thinking twins reuse `thinkEnv`, `sseLines` and `sseRead` with `?format=json` and parse `data:` lines as `ThinkingEvent`.

- [ ] **Step 2: Run them to see them pass,** then **prove each can fail.** Break the guard for one run per twin, see it fail, restore. Ledger each line: `Task 1: twin <name> — broke <guard>, saw FAIL, restored`.

Run: `GOPROXY=https://proxy.golang.org go test ./internal/jam/userui/`

Expected: PASS.

- [ ] **Step 3: Commit.** Message: `jam: JSON twins for the HTML routes' authorization tests`.

---

### Task 2: Serve the app at `/app/user/`

**Files:**
- Modify: `internal/jam/userui/handler.go`, `internal/jam/userui/api.go`, `internal/jam/userui/userui.go` (options), `internal/jam/appui/mux.go`, `internal/jam/appui/mux_test.go`, `cmd/at-jam/main.go`
- Modify: `web/app/vite.config.ts`, `web/app/src/App.tsx`, a new `web/app/src/paths.ts`, every `/app/next` literal in `web/app/src` (see Step 3), `web/app/src/routes/Home.tsx`, `web/app/src/shell/TitleBar.tsx`, `web/app/src/conversation/Message.tsx`, `web/app/src/boards/assetPath.ts`, `web/app/build/excalidrawFonts.ts`
- Modify: `internal/jam/appui/sw.js`, `internal/jam/appui/pwa_test.go`, `internal/jam/userui/browser_test.go` (the service-worker tests' expectations), `internal/jam/userui/next_browser_test.go` (paths)
- Test: `internal/jam/userui/api_test.go`, `web/app/src/routes/Home.test.tsx` (new)

**Interfaces:**
- **Go.**
  - `userui.Handler(d, lg, opts...)` adds, last, `mux.Handle("GET /app/user/", h.app)`, where `h.app = webapp.Handler(webapp.Config{Prefix: "/app/user/", FS: uiassets.AppFS(), FrameSrc: h.frameSrc, Name: "the Jam app", Head: appui.HeadMeta, Bootstrap: …})`.
  - The more specific GET patterns (`/app/user/api/`, `/app/user/events`, `/app/user/blobs/{id}`, `/app/user/boards/{id}/thumb.png`, `/app/user/push/key`, `/app/user/session/{id}/thinking/events`) win over it.
  - `GET /app/user/{$}` (the old full page) goes in Task 3. Until then the SPA catch-all and `{$}` are both registered; delete `{$}` in this task so `/app/user/` is the SPA.
  - `func WithFrameSrc(origins []string) Option`.
  - `userui.Next` is deleted.
  - `Bootstrap`'s Inbox tab keeps the title bar's own `href` (`/app/user/`), so the `/app/next/` override goes.
- **appui.**
  - `Deps.Next` is deleted;
  - `/app/next/` becomes `mux.Handle("/app/next/", LegacyRedirect("/app/next/", "/app/user/"))`, which is a 404 for non-GET/HEAD by `LegacyRedirect`'s own rule.
- **cmd/at-jam.** `nextApp` is deleted, and `userOpts` gains `userui.WithFrameSrc(appFrameSrc(&cfg))`.
- **App.**
  - `src/paths.ts` exports `export const APP = "/app/user/";`.
  - Every route and link uses it: `` `${APP}c/${…}` ``, `${APP}boards/…`, `${APP}new`. The route root is `path: APP`.
  - Vite's `base` is `"/app/user/"`.
  - `assetPath.ts` and `build/excalidrawFonts.ts` use `/app/user/assets/excalidraw/`.
- **The `Home` route** (`/app/user/`) reads `?c=`, `?board=` and `?tab=`:
  - `board` set → `navigate(APP + "boards/" + board, { replace: true })`;
  - else `c` set → `navigate(APP + "c/" + c + (tab ? "?tab=" + tab : ""), { replace: true })`;
  - else the "Pick a conversation." text;
  - a `#ask-<id>` hash is carried to the conversation route.
- **The service worker:**
  - `jamTarget(channel)` returns `/app/user/c/<channel>` (or `/app/user/`);
  - `jamPickClient` reuses a window on `/app/user/` or (until it reloads) `/app/next/`, and never an admin window;
  - `jamArea` goes, because every app window is now the app.
- **Removed:** the "Classic Inbox" menu item, and `Message.tsx`'s "current Inbox" placeholder. The placeholder becomes a plain "This <card> can't be shown here." line, because ask and board cards both render now.
- **Changed:** `Message.tsx`'s `reAsk` link becomes an in-app `Link` to `` `${APP}c/${channel}#ask-${id}` ``.

- [ ] **Step 1: Write the failing tests.**

Go, in `api_test.go`, replacing `TestNextServesTheAppWithBootstrap`:

```go
// The user area serves the Jam app at every page path, and every kept route
// still answers itself (none is shadowed by the app's catch-all).
func TestUserAreaServesTheApp(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil, WithPush("k"))
	for _, path := range []string{"/app/user/", "/app/user/c/" + engID, "/app/user/boards/brd_x", "/app/user/new"} {
		rec := apiGet(t, h, &p, path)
		switch {
		case rec.Code == 503: // the bundle isn't built: webapp's not-built page
		case rec.Code == 200 && strings.Contains(rec.Body.String(), `"name":"alice"`) && strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'"):
		default:
			t.Errorf("GET %s = %d %s", path, rec.Code, rec.Header())
		}
	}
	for _, tc := range []struct {
		method, path string
		code         int
		ctype        string
	}{
		{"GET", "/app/user/api/rail", 200, "application/json"},
		{"GET", "/app/user/api/nope", 404, "application/json"},
		{"GET", "/app/user/push/key", 200, "application/json"},
		{"GET", "/app/user/blobs/blb_01j9q3zzzzzzzzzzzzzzzzzz", 404, ""},
		{"POST", "/app/user/c/x", 405, ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req = appui.WithPrincipal(jam.WithParticipant(req, p), appui.Principal{User: &p})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code || (tc.ctype != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), tc.ctype)) || strings.Contains(rec.Body.String(), "<!--jam") || strings.Contains(rec.Body.String(), `id="jam-bootstrap"`) {
			t.Errorf("%s %s = %d %q (want %d %s, not the app's page)", tc.method, tc.path, rec.Code, rec.Header().Get("Content-Type"), tc.code, tc.ctype)
		}
	}
}
```

Some details to check while writing it:
- **The 405 row:** `POST /app/user/c/x` matches no POST pattern but does match `GET /app/user/`, so ServeMux answers 405. Use whatever it actually answers, but never 200 with the page, and ledger that.
- **The blob row:** `blobs` with an unknown id: check the blob route's code for missing (404), and adjust.

In `mux_test.go` (appui):

```go
func TestNextRedirectsToTheUserArea(t *testing.T) {
	m := Handler(Deps{Gate: NewGate(GateConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})})
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/app/next/c/chn_1?tab=status", nil))
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/app/user/c/chn_1?tab=status" {
		t.Errorf("GET /app/next/… = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("POST", "/app/next/c/chn_1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /app/next/… = %d", rec.Code)
	}
}
```

Use `mux_test.go`'s own helpers for building `Handler`; it has a pattern for a Gate.

App, `src/routes/Home.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";

vi.mock("../boards/BoardEditor", () => ({ default: () => <div data-testid="editor" /> }));

describe("old Inbox links", () => {
  const serve = () => server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })),
    http.get("/app/user/api/conversations/:c", ({ params }) => HttpResponse.json({ channelId: params.c, label: "eng", kind: "room", project: "p", phase: "", needsYou: false, sendTo: String(params.c),
      messages: [], lastSeq: 0, earlier: false, sessionIds: [], sessions: [], canJoin: false, canLeave: false, callIn: [], openAsks: [], session: "", newBoard: false })),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.get("/app/user/api/boards/:id", ({ params }) => HttpResponse.json({ id: params.id, title: "Plan", channel: "chn_a", readOnly: false })),
  );
  it("?c= opens the conversation", async () => {
    serve();
    renderApp(boot, "/app/user/?c=chn_a");
    expect(await screen.findByRole("heading", { name: "eng" })).toBeInTheDocument();
  });
  it("&board= opens the board", async () => {
    serve();
    renderApp(boot, "/app/user/?c=chn_a&board=brd_1");
    expect(await screen.findByTestId("editor")).toBeInTheDocument();
  });
  it("with neither, asks for a conversation", async () => {
    serve();
    renderApp(boot, "/app/user/");
    expect(await screen.findByText("Pick a conversation.")).toBeInTheDocument();
  });
});
```

Every app test that renders a route moves from `/app/next/…` to `/app/user/…`: `renderApp(boot, "/app/next/…")` and the expected `href`s. Do it mechanically: `grep -rl '/app/next' src | xargs sed -i 's#/app/next/#/app/user/#g'`. Then review the diff, because `boot.tabs[0].href` in `render.tsx` becomes `/app/user/` too.

Service worker, in `browser_test.go`:
- `TestBrowserServiceWorkerNotice` now wants `jamTarget('chn_1')` to be `"/app/user/c/chn_1"`;
- `TestBrowserServiceWorkerAppWindow` becomes `TestBrowserServiceWorkerClickWindow`. It covers: `[admin, next, user]` → 1; `[admin, user]` → 1; `jamTarget('chn_1')` → `/app/user/c/chn_1`; `jamTarget('')` → `/app/user/`.
- `TestServiceWorker` (appui) swaps `"/app/user/?c="` for `"/app/user/c/"` and drops `"jamArea("`.

Browser tests (`next_browser_test.go`): every `Next(e.Deps, nil, …)` becomes `Handler(e.Deps, nil, …)` (one handler now; the API handler and the app are the same). The `strings.HasPrefix(r.URL.Path, "/app/next/")` dispatch goes, and every `/app/next/` path becomes `/app/user/`. The Studio test passes the frame-src via `WithFrameSrc([]string{pv.URL})`.

- [ ] **Step 2: Run them to see them fail.**

Run:
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/userui/ ./internal/jam/appui/`
- `cd web/app && npx vitest run src/routes`

Expected: FAIL. `/app/user/` is still the old full page, `/app/next/` isn't redirected, and `Home` ignores `?c=`.

- [ ] **Step 3: Implement.**

**Go (`handler.go`/`api.go`):**
- move `Next`'s `webapp.Handler(...)` into `newHandler` as `h.app` (with `Prefix: "/app/user/"`);
- in `Handler`, delete `mux.HandleFunc("GET /app/user/{$}", h.full)` and add `mux.Handle("GET /app/user/", h.app)` after the other routes;
- delete `Next`;
- add `WithFrameSrc`;
- in `bootstrap`, drop the `/app/next/` href override.

**appui (`mux.go`):** delete `Deps.Next` and its mount, and add:

```go
	// The Jam app's pre-cutover home: same path, now under /app/user/.
	mux.Handle("/app/next/", LegacyRedirect("/app/next/", "/app/user/"))
```

**cmd/at-jam:**
- delete `nextApp`, `Next: nextApp`, and the comment about `/app/next/`;
- add `userui.WithFrameSrc(appFrameSrc(&cfg))` to `userOpts`;
- check `cmd/at-jam`'s tests for `Next:`.

**App:**
- create `src/paths.ts`:

```ts
// APP is the Jam app's mount: the user area (webapp serves index.html for
// every page path under it; Vite builds for it).
export const APP = "/app/user/";
```

- `App.tsx`: `path: APP` (import from `./paths`).
- Replace every link literal with `APP` (`Rail.tsx`, `PaneTabs.tsx`, `NewMessage.tsx`, `RecipientPicker.tsx`, `BoardPage.tsx`, `BoardCard.tsx`, `NewBoard.tsx`) and update their comments.
- `vite.config.ts`: `base: "/app/user/"`.
- `assetPath.ts`: `` `${location.origin}/app/user/assets/excalidraw/` ``.
- `build/excalidrawFonts.ts`: the replacement becomes `` "`${location.origin}/app/user/assets/excalidraw/`" ``, and `build/excalidrawFonts.test.ts`'s expectation is updated.
- `Home.tsx`:

```tsx
import { Text } from "@mantine/core";
import { useEffect } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import { APP } from "../paths";

// Home is /app/user/. It turns the old Inbox's links (?c=<channel>
// [&board=<id>][&tab=<tab>], from bookmarks and notifications) into the app's
// routes, else asks for a conversation.
export function Home() {
  const [q] = useSearchParams();
  const { hash } = useLocation();
  const go = useNavigate();
  const c = q.get("c"), board = q.get("board"), tab = q.get("tab");
  useEffect(() => {
    if (board) void go(`${APP}boards/${encodeURIComponent(board)}`, { replace: true });
    else if (c) void go(`${APP}c/${encodeURIComponent(c)}${tab ? `?tab=${encodeURIComponent(tab)}` : ""}${hash}`, { replace: true });
  }, [board, c, tab, hash, go]);
  if (board || c) return null;
  return <Text c="dimmed">Pick a conversation.</Text>;
}
```

- `TitleBar.tsx`: delete the "Classic Inbox" item.
- `Message.tsx`:
  - the unknown-card placeholder becomes `<Text size="sm" c="dimmed">This {CARD_NAME[m.card] ?? m.card} can't be shown here.</Text>`;
  - the `reAsk` anchor becomes `component={Link} to={`${APP}c/${encodeURIComponent(channel)}#ask-${m.reAsk.id}`}`.

**Service worker (`sw.js`):**

```js
// jamTarget is the Jam app URL that opens conversation channel.
function jamTarget(channel) {
  return channel ? '/app/user/c/' + encodeURIComponent(channel) : '/app/user/';
}

// jamPickClient is the index of the window (by URL) a notification click
// should reuse — the first on the Jam app ('/app/user/', or a tab still on
// its pre-cutover '/app/next/') — or -1 to open a new one. An /app/admin/
// (or any other) window is never navigated away.
function jamPickClient(urls) { /* as now: /app/user/ or /app/next/ */ }
```

Then the click handler navigates to `jamTarget(channel)`, and `jamArea` is deleted.

- [ ] **Step 4: Run them to see them pass.**

Run:
- `just app` (the base changed);
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/... ./cmd/at-jam/`;
- `cd web/app && npm run check`;
- `GOPROXY=https://proxy.golang.org go test -tags browser -run 'TestBrowserNext|TestBrowserServiceWorker' ./internal/jam/userui/`.

Expected:
- PASS;
- the old UI's HTML tests now fail where they request `/app/user/` (the full page). Leave them; Task 3 deletes them. Record the failing names in the ledger.

- [ ] **Step 5: Commit.** Message: `jam: the Jam app is the user area (/app/next redirects)`.

---

### Task 3: Delete the old Inbox

**Files to delete:**
- `internal/jam/userui/templates/` (`inbox.html`, `layout.html`);
- `internal/jam/userui/styles_test.go`, `titlebar_parity_test.go`, `handler_test.go` (all but `fixture` and the helpers other tests use: move `fixture`, `env`, `e.post`, `isIn`, `postAs` and `pageFor`, as still needed, into a new `fixture_test.go`), `attach_test.go`, `boards_test.go`, `asks_test.go` (all but `TestMeUploadAndBlob`, `yesNoOpts`, `env.ask` and `askerID`, moved to `fixture_test.go` or `api_asks_test.go`), `sessionpane_test.go` (all but `TestLinksIn`, moved to `api_sessions_test.go`), `frame_test.go` (all but `frameEnv`, `fakeFrames`, `frameCall` and `TestMintLimiterSlides`, moved to `frame_test.go`, which stays trimmed), `session_test.go` (all but `sessionEnv`, `fakeOps`, `cmdCall`, `asPrincipal`, `env.homeOf`, `ticketSession`, `TestKTokens` and `TestContextUse`), `presence_test.go` (keep `presenceFixture`, `TestPresenceRows`, `TestPresenceWithoutTrackerFallsBackToPhase` and `TestEventsStreamsPresenceSignal`; delete any HTML strip test), `thinking_test.go` (delete the HTML-fragment tests; keep the helpers, `TestThinkingEventOf`, `TestThinkingEventsJSON` and Task 1's twins), `userui_test.go` (keep the pure `TestGroupRail*`/`TestRailMission`);
- browser tests: `board_browser_test.go`, `sessionpane_browser_test.go`, `studio_browser_test.go` (move `studioFrames` and `framePage` to `next_browser_test.go` first), `thinking_browser_test.go` (move `thinkRaw`, `assistantBlock` and `toolResultBlock` first). In `browser_test.go`, keep `browserCtx`, `into`, `call`, `onNewDocument`, `evalInto`, `pushStub`, `waitFor` and the `TestBrowserServiceWorker*` tests; delete every old-Inbox test (`serveInbox`, `withStatic` if unused, the composer, copy, presence, narrow, long-brand, push-toggle, sign-out, app-badge, chimes and sounds tests). Rename `next_browser_test.go` to `app_browser_test.go` and `TestBrowserNext*` to `TestBrowserApp*`.
- **Go code:**
  - `handler.go`: the HTML routes and handlers (`full`, `rail`, `stream`, `markRead`, `join`, `leave`, `callIn`, `membership`, `answerAsk`'s route, `presenceStrip`, `askPanel`, `sessionHeadRoute`, `sessionPaneRoute`, `thinkingPage`, `thinkingRaw`, `frameRoute`, `sessionAction`), `pages`/the template parse, `inboxPage` and `render`;
  - `thinking.go`: `thinkingFragment`; the feed is JSON only. Without `?format=json` it still sends JSON, since the app passes it and nothing else uses the HTML form;
  - `boards.go`: `BoardEditor`, `boardEditor`, `whiteboardBuilt` and `BoardCard.Built`;
  - `presence.go`: the strip route;
  - `session.go`: `sessionHeadRoute`/`sessionAction`, keeping `sessionOutcome` and `sessionHead`;
  - `sessionpane.go`: `sessionPaneRoute` and `paneTabs`' HTML use, keeping `buildPane`;
  - `frame.go`: `frameRoute`, keeping `frameOutcome`;
  - `asks.go`: `answerAsk`, keeping `answerOutcome` and the card builders.
- **`apitypes.go`:** drop the template-only fields (`HasMessages`, `Head`, `Board *BoardEditor`, `Channel`, `Streams`/`EventsOff` comments that mention templates), and keep every JSON field. Re-run `go generate`; `types.ts` should not change, or only its comments.

**Interfaces:**
- `userui.Handler`'s mux keeps:
  - `POST /app/user/uploads`, `GET /app/user/blobs/{id}`;
  - `POST /app/user/boards`, `/app/user/boards/{id}/elements`, `GET /app/user/boards/{id}/thumb.png`;
  - `GET /app/user/events`, `GET /app/user/session/{id}/thinking/events`;
  - the push routes, the API routes, and the app catch-all.
- Check `POST /app/user/boards` (the HTML create): the app uses `POST api/boards`. Delete it if no client uses it. The old editor's `entry.jsx` goes in Task 4, and the agent tools go through MCP, not this route. Ledger the decision.

- [ ] **Step 1: Move the shared helpers,** then delete the old-UI tests and code listed above. Do it file by file, compiling as you go (`go vet ./internal/jam/userui/ && go vet -tags browser ./internal/jam/userui/`).

- [ ] **Step 2: Run the suites.**

Run:
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/... ./cmd/at-jam/`
- `GOPROXY=https://proxy.golang.org go test -tags browser ./internal/jam/userui/`
- `cd web/app && npm run check`

Expected: PASS, with no test left that renders the old HTML.

- [ ] **Step 3: Check that nothing still references the old UI.**

Run:
- `grep -rn 'templates/\|inboxPage\|pages\["inbox"\]\|hx-\|htmx' internal/jam/userui/` (expected: nothing);
- `grep -rn 'Whiteboard' internal/jam/userui/` (expected: only `uiassets` calls that Task 4 removes).

- [ ] **Step 4: Commit.** Message: `jam: delete the old Inbox (templates, fragment handlers, their tests)`.

---

### Task 4: Delete the whiteboard bundle and its build

**Files to delete:**
- `web/whiteboard/` (`build.mjs`, `entry.jsx`, `package.json`, `package-lock.json`);
- `scripts/whiteboard.sh`;
- `internal/jam/uiassets/whiteboard/.keep` (with its embed), and `internal/jam/uiassets/whiteboard_test.go`.

**Files to modify:**
- `internal/jam/uiassets/uiassets.go`: drop `whiteboardFiles`, `whiteboard`, `whiteboardVersion`, `WhiteboardBuilt`, `WhiteboardScript`, `WhiteboardStyles`, `WhiteboardBase`, and the `whiteboard/` branch of `Handler`;
- `justfile`: drop the `whiteboard` recipe and its mention in `build`;
- `scripts/build.sh`: drop the `WHITEBOARD_OPTIONAL` step;
- `.goreleaser.yaml`: drop the `scripts/whiteboard.sh` hook;
- `.github/workflows/gate.yml`: drop the whiteboard bundle step and `web/whiteboard/package-lock.json` from the setup-node cache paths;
- `.github/workflows/release.yml`: drop `scripts/whiteboard.sh` and `web/whiteboard/**` from the path filters and the cache paths;
- `.gitignore`: drop the whiteboard lines;
- `renovate.json`: drop any `web/whiteboard` rule (append-only policy: only delete a rule that names the deleted directory);
- `cmd/at-jam`: anything calling `uiassets.Whiteboard*`.

- [ ] **Step 1: Delete and edit as listed.**

- [ ] **Step 2: Check for leftovers.**

Run: `grep -rn 'whiteboard\.sh\|web/whiteboard\|WhiteboardBuilt\|WhiteboardScript\|WhiteboardStyles\|WhiteboardBase\|WHITEBOARD_OPTIONAL' --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=superpowers .`

Expected: nothing, outside history docs.

Also confirm that `uiassets/static` has no userui-only file left: `for f in internal/jam/uiassets/static/*.js internal/jam/uiassets/static/*.css; do grep -rln "$(basename $f)" internal --include=*.go --include=*.html | grep -v _test; done`. Every file is still referenced by `adminui` or `appui`. Ledger the result.

- [ ] **Step 3: Build and test.**

Run:
- `GOPROXY=https://proxy.golang.org go build ./... && go test ./...`
- `just app && just app-check`
- `STRICT=1 ./scripts/lint.sh`
- `bash -n scripts/build.sh`
- check the workflows with `actionlint` if available, else read the diff.

Expected: PASS.

- [ ] **Step 4: Commit.** Message: `jam: delete the old whiteboard bundle and its build`.

---

### Task 5: Docs, and the parity checklist

**Files:**
- Modify: `docs/usage/jam/intercom-ui.md`, `docs/usage/jam/session-view.md`, `docs/usage/jam/whiteboards.md`, `docs/usage/jam/intercom.md`, `docs/usage/jam/INDEX.md`, `docs/web-app.md`, `docs/DEVELOPMENT.md`, `docs/INDEX.md`, `docs/OVERVIEW.md` (if it names the old Inbox or `web/whiteboard`), and the spec's status line
- Create (if `intercom-ui.md` must split to fit its budget): a leaf beside it, linked from `docs/usage/jam/INDEX.md`

- [ ] **Step 1: Rewrite the docs for the app.** Use the **docs-author** skill.
  - **`intercom-ui.md`** describes the user area as the app: the rail, conversations, composer, asks, session view, boards and notifications. It drops the "Preview: the Jam app at `/app/next/`" section and every htmx-era detail: fragment routes, `hx-` triggers, `?c=` as the URL scheme (now `/app/user/c/<id>`; old links redirect). It's at 237 lines against a 200-line budget, so split by subject: keep the rail, conversation and composer here, and move the session view details to `session-view.md` (which owns them) and notifications to a leaf, if needed.
  - **`session-view.md`:** the tabs and pane now in the app, with Studio, Thinking and View here; remove the htmx and `layout.html` references.
  - **`whiteboards.md`:** the editor is the app's `/app/user/boards/<id>` route, and `web/whiteboard` is gone.
  - **`web-app.md`:**
    - paths `/app/next/` → `/app/user/`;
    - "while it is ported" wording goes;
    - `userui.Next` → `Handler`'s catch-all;
    - a line on old links (`?c=` and `/app/next/` redirects).
  - **`DEVELOPMENT.md`:** delete the "whiteboard editor bundle" section and `just whiteboard`.
  - **Spec status:** `**Status:** done — slices 1–9 merged (#506 … this PR); the admin UI is the next sub-project.`

  Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs`

  Expected: no new errors or warnings on the touched docs, and `intercom-ui.md` within its budget.

- [ ] **Step 2: Write the parity checklist for the user's local check.** It goes in the PR body, not a doc. List what to try on their local Jam before merge, one line each:
  - the rail and unread counts;
  - a conversation: paging, day dividers, copy, mark read, live updates;
  - join, leave and call-in;
  - the composer: Cmd/Ctrl+Enter, plain text, attachments (📎, paste, drop), drafts (⌘↓), the reply survives navigation;
  - New message;
  - asks: answer, Other with a comment, Discuss;
  - the session header and actions;
  - the pane: drag, the keyboard, ⇄, remembered layout;
  - Previews and View here, Artifacts, Status;
  - Thinking: live, Load earlier, filters, Raw;
  - Studio: the IDE stays across tabs;
  - boards: create, draw, live agent edits, back to chat;
  - Notifications: on/off, sign out, focus, the badge, a click opening the app;
  - sounds;
  - an old `?c=` bookmark and a `/app/next/` link redirect.

- [ ] **Step 3: Full verification.**

Run:
- `just app`;
- `just app-check`;
- `./scripts/app-types-check.sh`;
- `GOPROXY=https://proxy.golang.org go test ./...`;
- `GOPROXY=https://proxy.golang.org go test -tags browser ./internal/jam/userui/`;
- `STRICT=1 ./scripts/lint.sh`.

Expected: all PASS.

- [ ] **Step 4: Commit.** Message: `jam: the user area docs describe the app`.

---

## Self-review notes

- **Spec coverage:**
  - `/app/user/` serves the SPA: Task 2;
  - `/app/next/*` redirects: Task 2;
  - the templates, fragment handlers and their tests are deleted: Task 3;
  - `web/whiteboard` is deleted: Task 4;
  - userui-only static JS: none exists, confirmed in Task 4;
  - the manifest and worker stay at `/app/`: Task 2 (scope unchanged);
  - JSON twins: Task 1;
  - docs: Task 5;
  - the user's parity check: Task 5's checklist, before merge.
- **Order is safety:** the twins (Task 1) land before any deletion (Task 3), and Task 2 switches serving while the old code still compiles, so each commit builds.
- **Rollback:** reverting the PR restores the old Inbox, its routes and `web/whiteboard`. There's no data migration.
- **The one new behavior** is the old-link redirect in `Home`, which Task 2's tests pin.
