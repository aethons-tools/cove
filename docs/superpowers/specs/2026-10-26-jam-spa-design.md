# Jam UI as a single-page app — design

**Status:** approved (#496); slices 1–4 merged; slice 5 in progress ([plan](../plans/2026-10-26-jam-spa-5-session-view-1.md)).
**Context:** `/app/user` (package `userui`) is server-rendered Go `html/template` plus htmx and inline JS. Its `layout.html` alone is about 1,300 lines, and the session view, inbox and live panes are hard to grow that way. This spec moves `/app/user` to a React single-page app (SPA) built on off-the-shelf components and themes. `/app/admin` (`adminui`) follows later as its own sub-project, on the same foundation.

## Decisions (2026-10-26)

- **Goal:** all three, roughly equally: a consistent look from an off-the-shelf theme, client-side state and routing for the interactive screens, and developer velocity (typed components, hot reload, a real test story).
- **Scope:** the user UI first. The admin UI is a later sub-project and keeps its current look until then.
- **Stack:** React 19 + TypeScript + Vite + **Mantine v9** (v9 has been current since 2026-03; corrected in the slice 1 plan), with React Router and TanStack Query. Mantine was chosen over shadcn/ui + Tailwind (less built in, and we would own copied code) and MUI (runtime CSS-in-JS needs CSP nonces, heavy, looks like Material).
- **Migration:** a **parallel SPA, then one cutover**. The SPA grows at `/app/next/` while `/app/user/` stays the default. Screens are ported slice by slice to parity, then one PR flips `/app/user/` and deletes the old UI. Rejected alternatives: an in-place strangler (React and htmx sharing the DOM and the SSE stream) and islands (no client-side routing or shared state).

## 1. Architecture, build and serving

- **Source:** `web/app/` (Vite, React 19, TypeScript, Mantine v9, React Router, TanStack Query). There's no other global state library. The whiteboard (Excalidraw) becomes a lazily loaded route chunk, and `web/whiteboard/` is deleted at cutover.
- **Build:** `just app` runs Vite into `internal/jam/uiassets/app/`. The output is git-ignored and embedded with a `.keep` placeholder. It builds into a scratch directory and swaps in only on success, as `scripts/whiteboard.sh` does. CI and goreleaser already run setup-node. A Go build without Node still succeeds, and `/app/next/` then answers with a plain "the app bundle wasn't built (run `just app`)" page.
- **Serving:** a Go handler for `/app/next/`:
  - It serves hashed assets with `Cache-Control: immutable`.
  - Every other path gets `index.html`, so client routes deep-link.
  - It sits behind the same gate as `/app/user`: `browserauth.Gate`, `RequireUser`, `RequireSameOrigin`.
  - It injects a bootstrap `<script type="application/json">`: brand, current user, whether they're an admin, attachment max, Web Push key, and whether boards are on.
- **Dev loop:** `just app-dev` runs the Vite dev server with hot reload, proxying `/app/user/api/`, `/app/user/events` and the asset routes to a running `at-jam serve`.
- **Theme:** one Mantine theme module holds brand colours and bundled fonts. Colour scheme is `light | dark | auto` (OS by default, with a toggle), at today's density.
- **Not in scope:** SSR, a Node runtime in production, and the admin UI. Jam stays one Go binary.

## 2. JSON API and live data

- **Reuse the view models.** Handlers already build typed view models (`RailGroup`, `Conversation`, `SessionPane`, `sessionview.EventView`, …) for templates. The API serializes those same structs, so authorization and logic don't move. Each fragment route gets a JSON sibling under `/app/user/api/`:

| Today (HTML) | API (JSON) |
|---|---|
| `GET /app/user/` and `?c=` | `GET api/rail`, `GET api/conversations/{c}` (messages paged by seq) |
| `GET rail`, `presence`, `asks` | `GET api/rail`, `api/presence`, `api/asks` |
| `GET session/{id}/head`, `session/{id}/pane?tab=` | `GET api/sessions/{id}`, `api/sessions/{id}/pane/{tab}` |
| `GET session/{id}/thinking/page`, `…/raw` | `GET api/sessions/{id}/thinking?before=`, `…/thinking/raw` |
| `POST read`, `join`, `leave`, `call-in`, `asks/{id}/answer`, `session/{id}/{action}`, `session/{id}/frame` | the same actions, taking and returning JSON |
| sending a message (form POST) | `POST api/conversations/{c}/messages` |

  Uploads, blobs, board elements, board thumbnails and the push routes are unchanged; they already speak JSON or bytes.
- **Live updates.** `GET /app/user/events` stays as it is: signals only (`changed`, `presence`, `board`), never content. One shared `EventSource` maps each signal to TanStack Query invalidations: `changed` covers the rail and the open conversation, `presence` covers presence, `board` covers that board. Every refetch goes through normal authorization. The Thinking tail stream gains a JSON-rows form (`…/thinking/events?format=json`) that keeps its cursor, `newstream` and the per-viewer stream cap.
- **Types.** Go is the source of truth. `go generate` runs tygo to emit `web/app/src/api/types.ts` from the view-model structs, and CI fails when the file is stale. View-model fields get `json:"…"` tags. Template-only fields are tagged `json:"-"`, except the sanitized message HTML (§3).
- **Errors.** API errors are `{"error": "...", "code": "..."}` with the real status. The UI shows them as Mantine notifications. This replaces the mixed refusal strings (for example "Too many…" shown for any non-404/429 refusal).
- **Lifetime.** The HTML routes stay until cutover and are deleted there.

## 3. Screens and port order

**Layout:** a Mantine `AppShell`.
- **Header:** the title bar, with the same structure and links as the admin's, plus the theme toggle.
- **Navbar:** the rail, with groups, unread counts and the app badge.
- **Main:** the conversation, with a resizable session pane.

Each slice is one PR and is usable at `/app/next/` when it merges:

1. **Foundation:** the `web/app` scaffold, theme, Go serving and bootstrap, tygo, CI, `just app` / `app-dev`, the error envelope, the shell and a read-only rail.
2. **Conversations:** the message list (paging, day dividers), copy as markdown, mark-read, live `changed`, join/leave/call-in, and the presence strip. Message markdown **stays rendered and sanitized server-side by `squawkrender`**, the one sanitizer the relays already use. The API sends `bodyHtml` plus `raw`.
3. **Composer:** the new-message recipient picker, Cmd/Ctrl+Enter, the plain-text toggle, attachments (📎, paste, drag-and-drop, chips, thumbnails), and notification sounds.
4. **Asks:** inline ask cards, the open-asks ("Needs you") panel, answer with comment, and Discuss replies.
5. **Session view, part 1:** the header (status, context %, cost, keep/release/restart/upgrade), the resizable pane with per-session layout in localStorage, and the Previews, Artifacts and Status tabs.
6. **Session view, part 2:** the Thinking tab (live tail, paging, raw, the 2000-row cap; a virtualized list replaces the parked `isConnected`/unobserve minors) and the Studio tab (framed IDE, ticket mint and fallback, Previews "View here").
7. **Boards:** the whiteboard as a lazily loaded route (`/app/next/boards/:id`), board cards and thumbnails, and live `board` updates.
8. **App-level:** Web Push subscribe/unsubscribe and focus, the PWA manifest, and the service worker.
9. **Cutover:** see §5.

- **Parity rule:** a slice is done when everything the old screen does works in the new one. Feature changes wait until after cutover. The exception is a parked bug that porting fixes for free.
- **Process tails** (session view part 4) are built directly in the SPA after slice 6, not in the old UI.

## 4. Security

- **Same gate.** `/app/next/` and `/app/user/api/` use the existing cookie login, `RequireUser` and `RequireSameOrigin`, so every non-GET needs a same or trusted Origin/Referer. No tokens are kept in browser storage, and there is no new auth path.
- **Authorization stays server-side.** Every API handler resolves the participant with `jam.ParticipantFrom` and reuses today's checks: MayReach, board access, the home-channel rule for session event content, and the ssh right for the Studio tab. The SPA hiding a control is never the check.
- **The live stream** stays signal-only (§2).
- **The Studio frame boundary** is unchanged: tickets, `/__jam/frame`, the partitioned frame cookie, and the `referrerpolicy="origin"` iframe.
- **A CSP on the app page** (today's `/app/user` page has none):
  ```
  default-src 'self'; script-src 'self'; connect-src 'self';
  img-src 'self' blob: data:; font-src 'self'; style-src 'self' 'unsafe-inline';
  frame-src <preview origins>; frame-ancestors 'none'; base-uri 'none'; form-action 'self'
  ```
  - `style-src 'unsafe-inline'` is there for Mantine's inline style attributes.
  - The bootstrap is a JSON data block, HTML-escaped, not script.
- **JSON responses** carry `Content-Type: application/json`, `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`.
- **One HTML sink.** Message HTML comes only from `squawkrender`. Exactly one component uses `dangerouslySetInnerHTML`, and an ESLint rule bans it everywhere else.
- **Dependencies:** `package-lock.json` is committed and CI uses `npm ci`. Renovate (`renovate.json`, `config:recommended`) already tracks npm here: its Dependency Dashboard lists `web/whiteboard/package.json`, and its built-in monorepo groups already bundle `react` + `react-dom` (and the Mantine packages) into single PRs. So `web/app` needs no config change. If one is ever needed, it is appended to the existing `renovate.json`, never a replacement. Fonts and assets are bundled, with no CDN at runtime.

## 5. Testing, cutover and docs

**Testing:**
- **Go API tests** (plain `go test`, no Node). Each `/app/user/api/` route gets httptest coverage of its JSON shape and its authorization. Existing userui tests that assert who may see what get JSON twins before the HTML routes are deleted.
- **Component tests:** Vitest, React Testing Library, and MSW mocks typed from `types.ts`. `npm test` also runs `tsc --noEmit` and ESLint.
- **Browser end-to-end:** the existing chromedp harness (`-tags browser`, chrome-headless-shell), retargeted at `/app/next/` slice by slice. Playwright is not used: its browser downloads come from hosts outside the sandbox allow-list.

**CI gates:**
- `types.ts` is current.
- The shell route's initial JS stays within about 350 KB gzipped; the whiteboard, Thinking and Studio are lazy chunks.
- `/app/next/` still answers with the bundle missing.

**Cutover (slice 9), one PR:**
- `/app/user/` page routes serve the SPA, and `/app/next/*` redirects to the same path under `/app/user/`.
- It deletes `userui/templates`, the fragment handlers and their tests, `web/whiteboard`, and the userui-only JS in `uiassets/static` (admin's assets stay).
- The manifest and service worker stay at `/app/`. Push subscriptions survive, since the key and endpoint are unchanged.
- The user checks parity on their local Jam at `/app/next/` before merge. Rollback is reverting the PR.

**Docs:**
- `docs/usage/jam/intercom-ui.md` and `session-view.md` are updated in the slices that change them.
- A new leaf, `docs/web-app.md`, owns the SPA dev loop: layout, `just app` / `app-dev`, adding an API route plus types, and testing. It's linked from `docs/INDEX.md`. `DEVELOPMENT.md`'s "whiteboard editor bundle" section moves into it at cutover.

**After cutover:** process tails in the SPA, then the admin UI sub-project.
