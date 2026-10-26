---
summary: How the Jam app (web/app — React + TypeScript + Vite + Mantine) is built, embedded, served at /app/next/, typed from Go, and tested; the dev loop.
read_when: You are changing the Jam app in web/app, adding a /app/user/api route it reads, or building/testing it locally or in CI.
owns: the web/app source layout, `just app` / `app-dev` / `app-check`, scripts/app.sh and the size budget, the webapp serving package (bootstrap, CSP, not-built page), the tygo type generation, and the app's test layers
prereqs: usage/jam/intercom-ui.md for what the user area does; superpowers/specs/2026-10-26-jam-spa-design.md for why
tier: leaf
updated: 2026-10-26
---

# The Jam app (`web/app`)

The Jam app is the React single-page app replacing the server-rendered `/app/user` inbox
([design](superpowers/specs/2026-10-26-jam-spa-design.md)).
Until the cutover it is served at **`/app/next/`** beside the current inbox, behind the same sign-in.

## Layout

- `web/app/`: Vite + React 19 + TypeScript 6 + Mantine 9, React Router, TanStack Query.
  - `src/api/client.ts` is the `/app/user/api/` client. Every failure is an `ApiError`, and a non-JSON refusal means `signed_out`.
  - `src/api/types.ts` is **generated**; don't edit it (see Types).
  - `src/theme.ts` is the Mantine theme (jam.css's accent and fonts). The colour scheme is light, dark or auto.
  - `src/App.tsx` holds the client routes under `/app/next/`. `src/start.tsx` boots the app (bootstrap, then the router) and shows why when the bootstrap can't load.
  - `src/conversation/` is the conversation pane: `useConversation` pages newest-first with `useInfiniteQuery` (a refetch refreshes every loaded page), and `MessageBody` is the one inner-HTML site.
  - `src/composer/` is the reply box: pure `fence`, `drafts` (the old Inbox's sessionStorage keys, `me-reply:`/`me-drafts:`), `keys` and `upload` (XHR to `/app/user/uploads`) modules under `Composer`. `src/compose/` is New message. `POST /app/user/api/messages` shares `jam.ParticipantSendHandler.Send` with `/app/user/send`, and answers where the message went.
  - `src/sounds/` ports `sounds.js`, keeping its localStorage (`jam-sounds`) and IndexedDB (`jam`/`prefs`) contract with the service worker.
  - `src/live/useLiveEvents.ts` is the one `EventSource`: each signal invalidates query keys (`rail`, `conversation`, `presence`), with the 30 s `POLL_MS` as the fallback.
  - An expired sign-in links back to the current page, so the gate decides how to sign in again; queries never retry an expired sign-in or a 4xx (`shouldRetry`).
- `internal/jam/webapp`: serves a built bundle.
  - Files under `assets/` are served immutable, and a missing one is a 404.
  - Every other path gets `index.html`, with the request's bootstrap replacing `<!--jam:bootstrap-->` and a strict CSP.
  - Without a bundle it answers 503 "not built".
- `internal/jam/userui/api.go` holds the JSON routes. `userui.Next` mounts the app with the member's bootstrap.

## Build

- `just app` (`scripts/app.sh`) runs `npm ci`, `vite build` and the size budget, then swaps the result into `internal/jam/uiassets/app/`. That directory is git-ignored and embedded by at-jam. `just build` runs it when npm is present (`APP_OPTIONAL=1`).
- The size budget: the shell route's initial JS (entry plus modulepreloads) must stay ≤ 350 KB gzipped. Lazy-load anything big with `React.lazy`.
- CI (`gate.yml`) runs the types check, `npm run check` and `scripts/app.sh` before `go test`. Releases build the bundle as a goreleaser hook.

## Dev loop

Run `just dev-watch` (or `dev-serve`), then `just app-dev`, and open `http://localhost:5173/app/next/`.
- Vite hot-reloads the app and proxies `/app/user`, `/app/auth` and `/app/static` to `JAM_URL` (default `http://localhost:8081`, `dev/jam.dev.yml`'s admin-listen).
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
2. Register `GET|POST /app/user/api/…` in `apiRoutes`. Writes are `POST` with JSON (`decodeJSON` answers 415 otherwise), and a refusal never says whether the resource exists: a read of one the viewer may not see answers exactly like a missing one (404 `not_found`); a refused write answers one uniform error whatever the reason (join/leave/call-in: 403 `forbidden`). Resolve the participant with `jam.ParticipantFrom`, reuse the HTML route's checks, and reply with `writeJSON` / `writeAPIError(status, code, msg)`.
3. Test its shape **and who may not see it** in `api_test.go`.
4. Read it in the app with `useQuery` and `apiGet`.

## Tests

- **Go:** `api_test.go` and `webapp_test.go` (plain `go test`).
- **App:** `npm run check` in `web/app` (typecheck, ESLint, Vitest with Testing Library and MSW). Mock the API with MSW handlers typed from `types.ts`.
- **Browser:** `just test-browser` includes `TestBrowserNextAppShowsRail`. It needs the bundle (`just app`) and a headless Chrome, and skips without either.

## Security

- The app page's CSP allows only same-origin scripts. `style-src` permits inline styles for Mantine.
- `dangerouslySetInnerHTML` is banned by ESLint except for the squawkrender message body.
- The bootstrap is a JSON data block (`json.Marshal` escapes `<`), never script.
