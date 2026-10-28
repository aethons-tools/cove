# Admin SPA slice 9 (cutover) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `/app/admin/` serves the Jam app, and the old admin UI is gone:
- the app's admin area moves from `/app/admin/next/` to `/app/admin/`;
- every old page path, and every `next/` path, redirects to its new home with the query kept;
- the HTML templates, their handlers and tests, htmx and the admin-only static files are deleted;
- the user area's admin tabs match the admin area's: Dashboard · Config · Agents · Health.

**Architecture:**
- `adminui.Handler` keeps three things: the JSON API (`registerApp`), `/suggest`, and a new `redirectOld`. `redirectOld` answers a GET for an old root (`config`, `users`, `projects`, `kits`, `destinations`, `model-specs`, `specs`, `roles`, `intercom`, `agents/{id}/setup`, `next/…`) with a `302` to `appMount + appPath(...)`. Everything else under the mount is the app's `index.html`.
- `appPath` (slice 8) is the old→new table, extended for the setup and `specs`/`roles` paths. The app's `oldHref` (`legacy.ts`) and `NotMovedYet` are deleted, and an unknown app path renders a "No such page" view.
- Dead code is found with `deadcode` (golang.org/x/tools/cmd/deadcode) after the HTML routes are no longer registered, and removed.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§1 "Migration: parallel, then one cutover"; §2 redirects; §6 slice 9). The user checked parity on their local Jam before this slice ("Looks good. Merge and continue").

## Global Constraints

- **Redirects** are `302` (Found), keep the query (except `project` on agent paths, as `appPath` drops it), and are GET-only. A write to an old HTML route gets the API's `404`, never a page.
- **Security is unchanged:**
  - the admin gate wraps everything under `/app/admin/`;
  - `/app/admin/api/` stays under `sameOriginAPI`;
  - session content stays operator-only;
  - the JSON writes keep their checks (managed marks, etags, live-session refusals).
- **Kept from the old UI:**
  - `/app/admin/suggest` (the type-ahead's JSON);
  - `/app/static/` jam.css (the forbidden page), brand and icons.
- **Deleted from the old UI:** htmx, `push.js`, `sounds.js`, `appui/titlebar.html` and its script fields.
- **Rollback** is reverting the PR.

## Review Focus

1. **A bookmark of every old page** lands on its new page with the query kept: a role's setup, an agent's setup, `kits/{k}/draft`, `intercom?participant=…`, `projects/{p}/escalation`, `config`, `specs`, `roles/{p}/{r}`. Task 1 tests a table.
2. **Coverage of the shared write paths doesn't drop.** The JSON tests must still exercise every `jam` call the deleted HTML tests did. Task 2 compares per-function coverage before and after.
3. **The user area's title bar** shows the new admin tabs, with the right badges (config, agents and health counts). Task 1 tests it.
4. **A non-admin** opening `/app/admin/` or an old path is refused by the gate before any redirect, so redirects leak nothing. Task 1 tests it.
5. **`next/` bookmarks** keep working: `/app/admin/next/config/users?x=1` → `/app/admin/config/users?x=1`. Task 1 tests it.

---

### Task 1: Serve the app at /app/admin/, with redirects

**Files:**
- Modify:
  - `internal/jam/adminui/adminui.go` (`Handler` registers only `registerApp`, `registerSuggest` and `redirectOld`; `appMount` becomes `/app/admin/`);
  - `apppath.go` and its test;
  - `api.go`;
  - `internal/jam/appui/titlebar.go` (`adminTabs`);
  - `attention.go` (`BadgeSource` keyed `config`, `agents`, `health`);
  - `web/app/src/admin/paths.ts` (`ADMIN = "/app/admin/"`), `AdminApp.tsx` (`*` → `NoSuchPage`), `web/app/vite.config.ts` (proxy only `/app/admin/api` and `/app/admin/suggest`);
  - every `app_browser_test.go` URL.
- Create: `internal/jam/adminui/redirect.go`, `redirect_test.go`, `web/app/src/admin/NoSuchPage.tsx`
- Delete: `web/app/src/admin/legacy.ts`, `NotMovedYet.tsx`, and their tests; every adminui test file that drives an HTML route (each fails once the route is gone).

**Interfaces:**
- Produces: `func redirectOld(w http.ResponseWriter, r *http.Request) bool`, which reports whether it redirected. `appPath` adds:
  - `agents/{id}/setup` → `agents/{id}`;
  - `projects/{p}/roles/{r}/setup` → `config/projects/{p}/agents/{r}`;
  - `specs` → `config/kits`;
  - `roles` → `""`;
  - `roles/{p}/{r}` → `config/projects/{p}/agents/{r}`.

- [ ] **Step 1: Write the failing tests:**
  - `TestRedirectOld`, a table, as an admin:
    - `/app/admin/users/u?x=1` → 302 `/app/admin/config/users/u?x=1`;
    - `/app/admin/projects/p/roles/r/setup` → `/app/admin/config/projects/p/agents/r`;
    - `/app/admin/agents/a/setup` → `/app/admin/agents/a`;
    - `/app/admin/kits/k/draft` → `/app/admin/config/kits/k`;
    - `/app/admin/intercom?participant=x` → `/app/admin/health/intercom?participant=x`;
    - `/app/admin/config` → `/app/admin/config/tracking`;
    - `/app/admin/specs` → `/app/admin/config/kits`;
    - `/app/admin/next/config/users?x=1` → `/app/admin/config/users?x=1`;
    - `/app/admin/agents/a?project=p` → `/app/admin/agents/a`.
  - These new paths serve the app's index.html (200): `/app/admin/`, `/app/admin/config/tracking`, `/app/admin/agents/a`, `/app/admin/health/intercom`.
  - A POST to `/app/admin/users` is 404 or 405, not a page.
  - `appui`: an admin member's user-area bootstrap tabs are Inbox, Dashboard, Config, Agents and Health, with hrefs `/app/admin/`, `/app/admin/config/tracking`, `/app/admin/agents`, `/app/admin/health`. A non-admin gets a 403 at `/app/admin/users`, with no redirect.
- [ ] **Step 2:** Run them → FAIL.
- [ ] **Step 3:** Implement. Delete the HTML-route test files that now fail. Record each deleted file in the ledger with the JSON test that covers its behaviour.
- [ ] **Step 4:** `just app && just test && go test -tags browser ./internal/jam/adminui/ ./internal/jam/userui/ -run Browser && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `feat(admin)!: serve the app at /app/admin/; old paths redirect`.

### Task 2: Delete the old admin UI

**Files:**
- Delete:
  - `internal/jam/adminui/templates/`;
  - the HTML handlers and anything `deadcode -test ./cmd/... ./internal/...` reports in `adminui`, `appui` and `uiassets`;
  - `uiassets/static/{htmx.min.js,push.js,sounds.js}`;
  - `appui/titlebar.html` and the `Titlebar` script fields;
  - the `/ui/static/` legacy-redirect test rows for removed files.

- [ ] **Step 1:** Before deleting, record per-function coverage of the remaining shared functions (`go test -coverprofile` on `./internal/jam/...` at Task 1's head).
- [ ] **Step 2:** Delete. Repeat `deadcode` until it reports nothing in these packages.
- [ ] **Step 3:** Re-run coverage. Any kept function whose coverage dropped gets a JSON-route test (TDD) or a ledgered ruling.
- [ ] **Step 4:** `just test && just lint && go test -tags browser ./internal/jam/... -run Browser` → PASS.
- [ ] **Step 5: Commit** `chore(admin): delete the old admin UI`.

### Task 3: Docs

**Files:**
- Modify:
  - `docs/web-app.md` (the admin area at `/app/admin/`, no `next/`, the redirects);
  - the admin usage docs under `docs/usage/jam/` (`ui.md`, `ui-console.md`, `ui-pages.md`, `ui-projects.md`, `ui-config.md`, `ui-editing.md`, `ui-attention.md`, `kits-drafts-ui.md`, `intercom-ui.md`, and any other doc naming an old admin path or htmx), so they describe the app's pages and paths;
  - `docs/OVERVIEW.md` if its package map names the templates;
  - the spec's status line: "built (cutover <date>)".
- [ ] **Step 1:** Update the docs. Run the docs audit: no new errors beyond the plan file's own two.
- [ ] **Step 2: Commit** `docs: the admin console is the Jam app`.
