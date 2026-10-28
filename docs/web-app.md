---
summary: How the Jam app (web/app — React + TypeScript + Vite + Mantine) is built, embedded, served at /app/user/ and (the admin area, in preview) /app/admin/next/, typed from Go, and tested; the dev loop.
read_when: You are changing the Jam app in web/app, adding a /app/user/api or /app/admin/api route it reads, or building/testing it locally or in CI.
owns: the web/app source layout, the admin area's mount, API and Config tree plumbing, the shared /app/assets/ path, `just app` / `app-dev` / `app-check`, scripts/app.sh and the size budget, the webapp serving package (bootstrap, CSP, not-built page), the tygo type generation, and the app's test layers
prereqs: usage/jam/intercom-ui.md for what the user area does; superpowers/specs/2026-10-26-jam-spa-design.md for why
tier: leaf
updated: 2026-10-28
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
  - `src/composer/` is the reply box: pure `fence`, `drafts` (sessionStorage keys `me-reply:`/`me-drafts:`, the old Inbox's), `keys` and `upload` (XHR to `/app/user/uploads`) modules under `Composer`. `src/compose/` is New message and New session (`GET /app/user/api/session-roles`, `POST /app/user/api/sessions`). `POST /app/user/api/messages` shares `jam.ParticipantSendHandler.Send` with the JSON `/app/user/send`, and answers where the message went.
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
  - `Assets` serves the bundle's hashed files at **`/app/assets/`** for both areas (Vite's `base` is `/app/`), immutable; a missing one is a 404. `appui` mounts it ungated, like `/app/static/`: it is code, not data, and an admin needn't be a member.
  - `Handler` serves an area: every page path gets `index.html`, with the request's bootstrap replacing `<!--jam:bootstrap-->` and a strict CSP; an `assets/` path under an area is a 404.
  - Without a bundle it answers 503 "not built".
- `internal/jam/userui`: `Handler` serves the user area. `api*.go` hold the JSON routes; beside them sit the byte and stream routes (events, uploads, blobs, board elements and thumbnails, the Thinking feed, push), and every other `/app/user/` path is `webapp` with the member's bootstrap (`app()`, the mux's method-less catch-all).

## Admin area

The admin console is moving into the app ([design](superpowers/specs/2026-10-28-admin-spa-design.md)). Until its cutover it is served at **`/app/admin/next/`** (`adminui.appMount`, `src/admin/paths.ts`' `ADMIN`), behind the admin gate, beside the old pages.
- One bundle: `src/main.tsx` lazily imports `src/admin/start.tsx` on `/app/admin/` paths, so the user area's initial load doesn't carry it.
- `src/admin/`: `AdminApp.tsx` (routes), `AdminShell.tsx` (the title bar — `shell/TitleBar.tsx`, shared with the user area's `UserTitleBar` — and, on `config/` pages, the Config tree as the navbar, a drawer on narrow screens), `config/` (the tree: `ConfigTree`, polled every 3 s, with `treeState.ts`' filter, ancestors and remembered open branches in localStorage `jam-admin-tree`), `legacy.ts` (`oldHref`: a new path's old admin page, linked by `NotMovedYet` for pages not ported yet).
- The API is `/app/admin/api/` (`adminui/api*.go`, `tree.go`), with the user API's error envelope and headers. Its origin check (`sameOriginAPI`) refuses a cross-origin write with a JSON 403 `forbidden`; writes are `POST`/`DELETE` with JSON (`decodeJSON`: 415 otherwise), and `apiFail` maps a write error's status to its code (`bad_request`, `not_found`, `conflict`, `stale`, …). Routes: `GET bootstrap` (title-bar tabs and badges); `GET tree` (the Config tree with its attention badges: configuration trouble only; a studio's runtime trouble badges the Agents tab); `users…` (`api_users.go`); `tracking…` (`api_tracking.go`, the Config page's twin: Diff, Apply by digest, Track, Untrack, Release); `projects…` (`api_projects.go`: the list, rename, delete, and a project's section writes — context, chat service, members, rooms, escalation — each answering with the project; `?section=intercom` adds its recent log); a project's `agents` and `roles…` (`api_roles.go`: create, delete, Request, the role's section writes answering `{role, message}`, and its `setup` — sections, the section filter and the image's manager, audit-logged); `destinations…` and `model-specs…` (`api_specs.go`: list, create, edit, delete; write bodies are the old forms' fields as strings, read by the same `destFromFields` / `specFromFields`); `kits…` (`api_kits.go`: list, create, import, a kit at `?v=&diff=&file=`, push, pin, track/untrack/sync, delete — masking of an unparsable kit.yml stays server-side); `kits/{name}/draft…` (`api_kit_draft.go`: open, discard, save — a stale etag is 412 `stale` —, rename, delete, mode, add and replace as multipart uploads, publish, download; each write answers the section and its message). Each calls the same `jam` functions as the old page's handler. Its types are `adminui/apitypes.go`, generated into `src/admin/api/types.ts`.
- Shared admin components live in `src/admin/ui/`: `DataTable`, `Section`, `EditForm` (a write form: busy, confirm, refusal inline), `ConfirmAction` (a confirmed action, run once), `ManagedBadge` / `driftNote`, `DiffView`, `TypeAhead` (over `GET /app/admin/suggest`), `ContextPanel` (an agent-context card), `SquawkTable` (bodies through `MessageBody`), `StatusBadge`, `AttnFlag`, `CovesTable` (the studio table), `SetupView` (a setup's sections), `Facts` (a label/value list), and `useAdminMutation` (refreshes the tree after a write). Pages are under `src/admin/<area>/` (`users/`, `tracking/`, `projects/`, `roles/`, `specs/` — destinations and model-specs, `kits/` — a kit's version, diff and file are search params, in-page selectors, and its Draft is a section (`?dfile=`, `&ddiff=1`) whose editor holds navigation while it has unsaved edits (`useUnsavedGuard`); a member leaf is the user's page under its membership; a role's Setup is a section read only once opened).

## Build

- `just app` (`scripts/app.sh`) runs `npm ci`, `vite build` and the size budget, then swaps the result into `internal/jam/uiassets/app/`. That directory is git-ignored and embedded by jam. `just build` always runs it, so building jam needs Node 22 and npm (the bundle is the whole user area); `APP_OPTIONAL=1 scripts/app.sh` skips it where npm is missing, and that Jam's `/app/user/` answers "not built". `just dev-serve` runs the last `just build`, bundle included.
- The size budget: each area's initial JS must stay ≤ 350 KB gzipped — the user area's is the entry plus its modulepreloads; the admin area's adds `src/admin/start.tsx`'s chunk and its static imports (read from Vite's manifest, which the check then deletes). Lazy-load anything big with `React.lazy`.
- Excalidraw's fonts: `scripts/app.sh` copies them into `assets/excalidraw/fonts/` (without the CJK Xiaolai set), `src/boards/assetPath.ts` points `EXCALIDRAW_ASSET_PATH` there, and `build/excalidrawFonts.ts` (a Vite plugin) rewrites Excalidraw's CDN fallback (esm.sh) to the same bundle, failing the build if the pattern changes, if nothing was rewritten, or if any chunk still names esm.sh. So no font comes from another origin (the CSP is `font-src 'self'`).
- CI (`gate.yml`) runs the types check, `npm run check` and `scripts/app.sh` before `go test`. Releases build the bundle as a goreleaser hook.
- Dependencies: Renovate (`renovate.json`) updates `web/app` like any npm project. TypeScript is held below 6.1 there, matching typescript-eslint's peer range. Raise that cap when typescript-eslint supports a newer TypeScript.

## Dev loop

Run `just dev-serve`, then `just app-dev`, and open `http://localhost:5173/app/user/` (the admin area: `/app/admin/next/`).
- Vite serves the app's pages and hot-reloads them, and proxies to `JAM_URL` the routes the app reads beside them (`/app/user/api`, `events`, `session`, `uploads`, `blobs`, `push`, board elements and thumbnails), the old admin pages and `/app/admin/api` (everything under `/app/admin/` but `next/`), `/app/auth` and `/app/static` (default `http://localhost:8081`, `dev/jam.dev.yml`'s admin-listen).
- The proxy rewrites `Origin`, so writes pass the same-origin check.
- With the dev identity on, loopback requests are already signed in.

## Types

Go is the source of truth. JSON types live in `internal/jam/userui/apitypes.go` and, for the admin area, `internal/jam/adminui/apitypes.go`: the only files tygo reads (`tygo.yaml`).
After changing them, run `go generate ./internal/jam/userui/` (it regenerates both) and commit `src/api/types.ts` and `src/admin/api/types.ts`. `scripts/app-types-check.sh` (CI) fails when it's stale.
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
- **Browser:** `just test-browser` runs adminui's `TestBrowserAdmin*` (helpers in `internal/jam/browsertest`) and userui's `app_browser_test.go` `TestBrowserApp*` tests (rail, conversation, composer, asks, session pane, Thinking, Studio, boards, push) and the service worker's. It needs the bundle (`just app`) and a headless Chrome, and skips without either.

## Security

- The app page's CSP allows only same-origin scripts. `style-src` permits inline styles for Mantine.
- Its `frame-src` is the preview hosts (`https://*.<previews domain>[:admin port]`, `appFrameSrc`) when pane framing is on (previews and `ui-origins` set), else `'none'`; the frame boundary itself is [previews.md](usage/jam/previews.md)'s.
- `dangerouslySetInnerHTML` is banned by ESLint except for the squawkrender message body.
- The bootstrap is a JSON data block (`json.Marshal` escapes `<`), never script.
