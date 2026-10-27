---
summary: How the Jam app (web/app — React + TypeScript + Vite + Mantine) is built, embedded, served at /app/user/, typed from Go, and tested; the dev loop.
read_when: You are changing the Jam app in web/app, adding a /app/user/api route it reads, or building/testing it locally or in CI.
owns: the web/app source layout, `just app` / `app-dev` / `app-check`, scripts/app.sh and the size budget, the webapp serving package (bootstrap, CSP, not-built page), the tygo type generation, and the app's test layers
prereqs: usage/jam/intercom-ui.md for what the user area does; superpowers/specs/2026-10-26-jam-spa-design.md for why
tier: leaf
updated: 2026-10-27
---

# The Jam app (`web/app`)

The Jam app is the React single-page app that is the user area, served at **`/app/user/`**
behind the user gate ([what it does](usage/jam/intercom-ui.md),
[design](superpowers/specs/2026-10-26-jam-spa-design.md)). It replaced the server-rendered
Inbox at the cutover. Old links keep working: `src/routes/Home.tsx` redirects the old Inbox's
`?c=` (`&board=`, `&tab=`) URLs to the app's paths, and `appui` redirects `/app/next/…`
(the preview address) to `/app/user/…`.

## Layout

- `web/app/`: Vite + React 19 + TypeScript 6 + Mantine 9, React Router, TanStack Query.
  - `src/api/client.ts` is the `/app/user/api/` client. Every failure is an `ApiError`, and a non-JSON refusal means `signed_out`.
  - `src/api/types.ts` is **generated**; don't edit it (see Types).
  - `src/theme.ts` is the Mantine theme (jam.css's accent and fonts). The colour scheme is light, dark or auto.
  - `src/App.tsx` holds the client routes under `/app/user/` (`src/paths.ts`' `APP`; `vite.config.ts`' `base`). `src/start.tsx` boots the app (bootstrap, then the router) and shows why when the bootstrap can't load.
  - `src/conversation/` is the conversation pane: `useConversation` pages newest-first with `useInfiniteQuery` (a refetch refreshes every loaded page), and `MessageBody` is the one inner-HTML site.
  - `src/composer/` is the reply box: pure `fence`, `drafts` (sessionStorage keys `me-reply:`/`me-drafts:`, the old Inbox's), `keys` and `upload` (XHR to `/app/user/uploads`) modules under `Composer`. `src/compose/` is New message. `POST /app/user/api/messages` shares `jam.ParticipantSendHandler.Send` with the JSON `/app/user/send`, and answers where the message went.
  - `src/asks/` holds the ask cards (`AskCardView`: answer with an option, or Other… plus a comment; Discuss) and the open-asks panel. `POST /app/user/api/asks/{id}/answer` maps refusals through `answerOutcome`.
  - `src/session/` is the session view: `SessionHeader` (actions go to `POST /app/user/api/sessions/{id}/{action}`, through `sessionOutcome`), and `SessionView`'s resizable pane with its tabs (`PaneTabs`). `layout.ts` keeps the layout in localStorage, `jam-pane:<session>` / `jam-pane:last` (the old Inbox's keys).
  - `src/session/thinking/` is the Thinking tab, a lazily loaded chunk. `feed.ts` reduces the live JSON feed (`…/thinking/events`) and pages into items and owns the 2000-row cap; `rows.ts` derives the display rows (pairing, cost dividers, thinking runs, filters); `useThinkingFeed` holds the EventSource and its refusal backoff; `ThinkingTab` renders with `@tanstack/react-virtual`. Its preferences live in localStorage `jam-thinking`.
  - `src/session/frames/` holds the framed views. `useFrame` mints with `POST /app/user/api/sessions/{id}/frame` (through `frameOutcome`), heeds a `frame-error` only from its own current frame, and re-mints at most once per signin/cookies per user action. `StudioTab` (a lazy chunk) is kept mounted across tabs by `SessionView`; Previews' View here uses the same `FrameView`.
  - `src/boards/` is whiteboards: `BoardCard` (in a message), `NewBoard` (`POST /app/user/api/boards`), `BoardPage` (the `/app/user/boards/:id` route, from `GET /app/user/api/boards/{id}`) and `BoardEditor`, the lazy Excalidraw chunk, with its sync rules in `sync.ts`. It syncs through `/app/user/boards/{id}/elements` and refetches on the window event `jam-board`, which `useLiveEvents` re-dispatches from the stream's `board` signal.
  - `src/push/` is the app-level PWA plumbing: `sw.ts` registers the `/app/` service worker (`appui/sw.js`) from the bundle; `push.ts` + `PushToggle` are the Notifications item (ported from `uiassets/static/push.js`, keeping its `jam.push.synced:` marker; Sign out unsubscribes this device first); `useFocusReport` posts `/app/user/push/focus`; `useAppBadge` sets the icon badge from the rail's unread total. The page head (manifest, icons; no inline script) is `appui.HeadMeta`, put at `index.html`'s `<!--jam:head-->` by `webapp.Config.Head`.
  - `src/sounds/` ports `sounds.js`, keeping its localStorage (`jam-sounds`) and IndexedDB (`jam`/`prefs`) contract with the service worker.
  - `src/live/useLiveEvents.ts` is the one `EventSource`: each signal invalidates query keys (`rail`, `conversation`, `presence`, `session`), with the 30 s `POLL_MS` as the fallback.
  - An expired sign-in links back to the current page, so the gate decides how to sign in again; queries never retry an expired sign-in or a 4xx (`shouldRetry`).
- `internal/jam/webapp`: serves a built bundle.
  - Files under `assets/` are served immutable, and a missing one is a 404.
  - Every other path gets `index.html`, with the request's bootstrap replacing `<!--jam:bootstrap-->` and a strict CSP.
  - Without a bundle it answers 503 "not built".
- `internal/jam/userui`: `Handler` serves the user area. `api*.go` hold the JSON routes; beside them sit the byte and stream routes (events, uploads, blobs, board elements and thumbnails, the Thinking feed, push), and every other `/app/user/` path is `webapp` with the member's bootstrap (`app()`, the mux's method-less catch-all).

## Build

- `just app` (`scripts/app.sh`) runs `npm ci`, `vite build` and the size budget, then swaps the result into `internal/jam/uiassets/app/`. That directory is git-ignored and embedded by at-jam. `just build` always runs it, so building at-jam needs Node 22 and npm (the bundle is the whole user area); `APP_OPTIONAL=1 scripts/app.sh` skips it where npm is missing, and that Jam's `/app/user/` answers "not built". `just dev-watch`'s rebuilds don't rebuild the bundle: run `just app` first.
- The size budget: the shell route's initial JS (entry plus modulepreloads) must stay ≤ 350 KB gzipped. Lazy-load anything big with `React.lazy`.
- Excalidraw's fonts: `scripts/app.sh` copies them into `assets/excalidraw/fonts/` (without the CJK Xiaolai set), `src/boards/assetPath.ts` points `EXCALIDRAW_ASSET_PATH` there, and `build/excalidrawFonts.ts` (a Vite plugin) rewrites Excalidraw's CDN fallback (esm.sh) to the same bundle, failing the build if the pattern changes, if nothing was rewritten, or if any chunk still names esm.sh. So no font comes from another origin (the CSP is `font-src 'self'`).
- CI (`gate.yml`) runs the types check, `npm run check` and `scripts/app.sh` before `go test`. Releases build the bundle as a goreleaser hook.
- Dependencies: Renovate (`renovate.json`) updates `web/app` like any npm project. TypeScript is held below 6.1 there, matching typescript-eslint's peer range. Raise that cap when typescript-eslint supports a newer TypeScript.

## Dev loop

Run `just dev-watch` (or `dev-serve`), then `just app-dev`, and open `http://localhost:5173/app/user/`.
- Vite serves the app's pages and hot-reloads them, and proxies to `JAM_URL` the routes the app reads beside them (`/app/user/api`, `events`, `session`, `uploads`, `blobs`, `push`, board elements and thumbnails), `/app/auth` and `/app/static` (default `http://localhost:8081`, `dev/jam.dev.yml`'s admin-listen).
- The proxy rewrites `Origin`, so writes pass the same-origin check.
- With the dev identity on, loopback requests are already signed in.

## Types

Go is the source of truth. JSON types live in `internal/jam/userui/apitypes.go`, the only file tygo reads (`tygo.yaml`).
After changing them, run `go generate ./internal/jam/userui/` and commit `types.ts`. `scripts/app-types-check.sh` (CI) fails when it's stale.
Rules:
- every field has a `json` tag;
- lists are never `nil` in a response;
- a `jam` string kind needs a `type_mappings` entry.

## Adding an API route

1. Add its response type to `apitypes.go`, then regenerate.
2. Register `GET|POST /app/user/api/…` in `apiRoutes`. Writes are `POST` with JSON (`decodeJSON` answers 415 otherwise), and a refusal never says whether the resource exists: a read of one the viewer may not see answers exactly like a missing one (404 `not_found`); a refused write answers one uniform error whatever the reason (join/leave/call-in: 403 `forbidden`). Resolve the participant with `jam.ParticipantFrom`, reuse the existing access checks (`jam.UserChannel`, `sessionFor`), and reply with `writeJSON` / `writeAPIError(status, code, msg)`.
3. Test its shape **and who may not see it** in `api_test.go`.
4. Read it in the app with `useQuery` and `apiGet`.

## Tests

- **Go:** `internal/jam/userui`'s `api*_test.go` (shared fixture in `fixture_test.go`) and `webapp_test.go` (plain `go test`).
- **App:** `npm run check` in `web/app` (typecheck, ESLint, Vitest with Testing Library and MSW). Mock the API with MSW handlers typed from `types.ts`. `renderApp` (`src/test/render.tsx`) mounts the whole app with Mantine's `env="test"`: jsdom has no layout, and without it Mantine hides menus and popovers whose anchor looks detached.
- **Browser:** `just test-browser` runs `app_browser_test.go`'s `TestBrowserApp*` tests (rail, conversation, composer, asks, session pane, Thinking, Studio, boards, push) and the service worker's. It needs the bundle (`just app`) and a headless Chrome, and skips without either.

## Security

- The app page's CSP allows only same-origin scripts. `style-src` permits inline styles for Mantine.
- Its `frame-src` is the preview hosts (`https://*.<previews domain>[:admin port]`, `appFrameSrc`) when pane framing is on (previews and `ui-origins` set), else `'none'`; the frame boundary itself is [previews.md](usage/jam/previews.md)'s.
- `dangerouslySetInnerHTML` is banned by ESLint except for the squawkrender message body.
- The bootstrap is a JSON data block (`json.Marshal` escapes `<`), never script.
