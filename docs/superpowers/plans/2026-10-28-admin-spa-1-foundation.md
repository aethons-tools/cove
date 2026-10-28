# Admin SPA slice 1 (foundation and the Config tree) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The admin area of the Jam app exists at `/app/admin/next/`. It has its own bootstrap, title bar and JSON API skeleton, plus the read-only Config tree with attention badges, filtering and remembered expansion. Every page behind it is a placeholder that links to the old admin page.

**Architecture:** One Vite bundle serves both areas. Its hashed assets move to an ungated `/app/assets/`. `main.tsx` lazily loads `src/admin/start.tsx` on `/app/admin/` paths. `adminui` gains `api.go` / `apitypes.go` / `tree.go`, mounted under its existing gate. The API sits behind `RequireSameOrigin`, and the tree is built from today's `attention()` items.

**Tech Stack:** Go 1.26 (`net/http`, `webapp`, tygo), React 19 + TypeScript 6.0 + Mantine 9 + React Router 8 + TanStack Query 5, Vitest + Testing Library + MSW, chromedp.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§1, §3, §4, §6 slice 1); conventions from `docs/web-app.md`.

## Global Constraints

- The parallel admin mounts at **`/app/admin/next/`**: Go `adminui.appMount = "/app/admin/next/"`, TS `ADMIN = "/app/admin/next/"`. Cutover changes both.
- The admin API is under **`/app/admin/api/`**. Errors use `{"error","code"}`. Responses are `no-store` and `nosniff`. An unknown API path is a JSON 404 `not_found`.
- Assets are at **`/app/assets/`** (Vite `base: "/app/"`). They're ungated, files only, with no `index.html` fallback.
- Size budget: initial JS ≤ **350 KB gzipped** for the user entry. Separately, the user entry plus the admin start chunk and its static imports must also be ≤ 350 KB.
- The tree is polled every **3 s** while the page is visible.
- Title bar order for an admin: `Inbox` (members only) · `Dashboard` · `Config` · `Agents` · `Health`.
- Badges:
  - Config is the sum of its tree nodes' config items.
  - Agents counts runtime items (kind broken or stale, tab `agents`).
  - Health counts the tab `health` items.
  - Dashboard counts all items.
- The old admin UI and its title bar don't change in this slice.
- **Deviation from spec §6, my call:** `DataTable`, `Section`, `EditForm` and `ConfirmAction` move to slice 2, where they're first used. Slice 1 has no page that needs them (spec §5: "built where first needed").

## Review Focus

1. **Names that need escaping.** A project, kit or user name with `/`, a space or `%` must produce a tree path that routes and round-trips. Tested in Task 3 (`TestTreePathsEscape`) and Task 5.
2. **A non-member admin (loopback, no login)** must load the admin app. Assets come from the ungated path, and nothing calls `/app/user/*`. Tested in Task 2 (`TestAdminBootstrapLocal`) and Task 6 (browser, loopback).
3. **A stale chunk or a missing asset** must answer 404, never `index.html`, both under `/app/assets/` and under an area prefix. Tested in Task 1.
4. **A deep link to a node whose project no longer exists** must show the placeholder or not-found page, not crash, and the tree must still render. Tested in Task 5 (`unknown path`).
5. **A cross-origin POST to `/app/admin/api/`** must be refused before any handler runs. Tested in Task 2 (`TestAdminAPICrossOrigin`, which uses a test-only POST route).

---

### Task 1: Shared asset path `/app/assets/`

**Files:**
- Modify: `internal/jam/webapp/webapp.go` (split out `Assets`; an area `Handler` 404s `assets/`), `internal/jam/webapp/webapp_test.go`
- Modify: `internal/jam/appui/mux.go` (mount `GET /app/assets/`), `internal/jam/appui/mux_test.go`
- Modify: `web/app/vite.config.ts` (`base: "/app/"`; dev proxy), `web/app/src/boards/assetPath.ts`, `web/app/build/excalidrawFonts.ts` + its test, `web/app/scripts/size-budget.mjs` (regex `/app/assets/`)
- Modify: `internal/jam/userui/app_browser_test.go` (`withPWA` also passes `/app/assets/` to appui; font assertion path)

**Interfaces:**
- Produces: `webapp.Assets(fsys fs.FS) http.Handler`. It serves `GET|HEAD /app/assets/<file>` from `fsys`'s `assets/` as `public, max-age=31536000, immutable` with `nosniff`. Missing files, directories and dotfiles get 404. Without a bundle, every path gets 404.
- Produces: `webapp.Handler` answers 404 for any `<prefix>assets/…` path (assets no longer live under an area).

- [ ] **Step 1: Write the failing tests.** In `webapp_test.go`:
  - `TestAssets`: `/app/assets/index-abc.js` → 200, `Cache-Control` contains `immutable`; `/app/assets/old-999.js`, `/app/assets/`, `/app/assets/.keep` → 404.
  - `TestHandlerNoAreaAssets`: `/app/user/assets/index-abc.js` → 404.
  - In `mux_test.go`, `TestAssetsUngated`: `GET /app/assets/x.js` with no session isn't redirected to login (it isn't a 302).
  - Update `indexHTML` to `src="/app/assets/index-abc.js"`.
- [ ] **Step 2:** `go test ./internal/jam/webapp/ ./internal/jam/appui/` → FAIL (`Assets` undefined).
- [ ] **Step 3:** Implement `Assets` by moving the asset branch out of `Handler`. Mount it in `appui.Handler` as `mux.Handle("GET /app/assets/", webapp.Assets(uiassets.AppFS()))`, next to `/app/static/`.
- [ ] **Step 4: Update the web side.**
  - Vite `base: "/app/"`.
  - Dev proxy: also `"^/app/admin/(?!next/)"` and `"^/app/admin/api/"` → Jam. Leave `/app/assets` unproxied (Vite serves it).
  - `assetPath.ts` and `excalidrawFonts.ts`: `` `${location.origin}/app/assets/excalidraw/` ``.
  - `size-budget.mjs`: regex `"\/app\/(assets\/[^"]+\.js)"`.
- [ ] **Step 5:**
  - `go test ./internal/jam/webapp/ ./internal/jam/appui/ ./internal/jam/userui/` → PASS.
  - `cd web/app && npm run check` → PASS.
  - `scripts/app.sh` → prints `size-budget: initial JS …` under budget.
- [ ] **Step 6:** `just test-browser` (needs the bundle) → the `TestBrowserApp*` tests PASS, the boards font test included.
- [ ] **Step 7: Commit** `feat(app): serve the bundle's assets at /app/assets/ for both areas`.

### Task 2: Admin API skeleton, bootstrap and the app mount

**Files:**
- Create: `internal/jam/adminui/apitypes.go`, `internal/jam/adminui/api.go`, `internal/jam/adminui/api_test.go`
- Modify: `internal/jam/adminui/adminui.go` (`Handler` calls `h.apiRoutes`, mounts the app), `internal/jam/adminui/attention.go` (`appTabBadges`)
- Modify: `tygo.yaml` (a second package), and generate `web/app/src/admin/api/types.ts`; `scripts/app-types-check.sh` if it names the one output file.

**Interfaces:**
- Produces (Go, `apitypes.go`, the only file tygo reads in adminui):
  ```go
  type APIError struct{ Error, Code string }               // json "error","code"
  type NavTab struct{ Key, Label, Href string; Badge int; BadgeRed bool; BadgeTitle string } // same JSON as userui.NavTab
  type Bootstrap struct {
      Brand   string   `json:"brand"`
      Name    string   `json:"name"`    // Principal.DisplayName()
      Initial string   `json:"initial"`
      Roles   string   `json:"roles"`   // "admin" or "member · admin"
      Member  bool     `json:"member"`  // has a user: the Inbox tab shows
      SignOut bool     `json:"signOut"`
      Tabs    []NavTab `json:"tabs"`
  }
  type TreeNode struct {
      Path       string     `json:"path"`  // under the app mount, e.g. "config/projects/acme"
      Label      string     `json:"label"`
      Group      bool       `json:"group"` // expandable
      Badge      int        `json:"badge"`
      BadgeRed   bool       `json:"badgeRed"`
      BadgeTitle string     `json:"badgeTitle"`
      Children   []TreeNode `json:"children"` // never nil
  }
  type TreeResponse struct{ Nodes []TreeNode `json:"nodes"` }
  ```
- Produces (Go, `api.go`):
  - `const appMount = "/app/admin/next/"`.
  - `writeJSON(w, status, v)` and `writeAPIError(w, status, code, msg)`, as userui's.
  - `func adminBootstrap(r *http.Request, items []attnItem) Bootstrap`.
  - Routes:
    - `GET /app/admin/api/bootstrap`;
    - `GET /app/admin/api/tree`, registered in Task 3;
    - the catch-all JSON 404.
    - The whole `/app/admin/api/` subtree is wrapped in `browserauth.RequireSameOrigin(o.trustedOrigins, …)`.
  - `/app/admin/next/` serves `webapp.Handler(webapp.Config{Prefix: appMount, FS: uiassets.AppFS(), Bootstrap: …, Head: appui.HeadMeta, Name: "the Jam app"})`. `FrameSrc` is nil, so `frame-src 'none'`.
- Produces (`attention.go`): `func appTabBadges(items []attnItem) map[string]attnBadge` with these keys:
  - `dashboard`: all items;
  - `config`: items whose `treePath` is ok (Task 3);
  - `agents`: kind `broken` or `stale` with tab `agents`;
  - `health`: tab `health`.
- Tabs Hrefs:
  - Inbox `/app/user/`;
  - Dashboard `appMount`;
  - Config `appMount+"config/tracking"`;
  - Agents `appMount+"agents"`;
  - Health `appMount+"health"`.

- [ ] **Step 1: Write the failing tests** in `api_test.go`. Drive `Handler` with `appui.WithPrincipal` on the request.
  - `TestAdminBootstrap`: an admin who is a member gets tabs keyed `inbox,dashboard,config,agents,health` in that order, `member: true`, and `roles: "member · admin"`.
  - `TestAdminBootstrapLocal`: a `Local` admin with no user gets tabs without `inbox`, `member: false`, and `name` from `DisplayName()`.
  - `TestAppTabBadges`, seeded with items:
    - a broken studio item makes `agents` red with count 1, and `config` 0;
    - a role naming a missing kit makes `config` 1 and `agents` 0;
    - `dashboard` counts both.
  - `TestAdminAPINotFound`: `GET /app/admin/api/nope` gives 404 with a JSON `code: "not_found"`.
  - `TestAdminAPICrossOrigin`: `POST /app/admin/api/nope` with `Origin: https://evil.example` gives 403, and the handler never ran (the body isn't the JSON 404).
  - `TestAdminAppServed`: `GET /app/admin/next/config/projects/x` gives 200 with `id="jam-bootstrap"` when the bundle is built, else 503. A `Content-Security-Policy` containing `frame-src 'none'` is required only when the response is 200.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'Admin|AppTab'` → FAIL.
- [ ] **Step 3:** Implement the types and `api.go`. Then:
  - Add the tygo package `internal/jam/adminui`, with `include_files: [apitypes.go]` and `output_path: web/app/src/admin/api/types.ts`.
  - Add `//go:generate sh -c "cd ../../.. && go tool tygo generate"` to `api.go`.
  - Run `go generate ./internal/jam/adminui/`.
  - Make the types check cover both outputs.
- [ ] **Step 4:** `go test ./internal/jam/adminui/ ./internal/jam/appui/` → PASS; `scripts/app-types-check.sh` → clean.
- [ ] **Step 5: Commit** `feat(admin): admin app mount, bootstrap and API skeleton at /app/admin/next/`.

### Task 3: The Config tree endpoint

**Files:**
- Create: `internal/jam/adminui/tree.go`, `internal/jam/adminui/tree_test.go`

**Interfaces:**
- Consumes: `attention(store, img)` via `frameSource.items()`, `badgeOf`, the `TreeNode` / `TreeResponse` types (Task 2).
- Produces:
  - `func treePath(it attnItem) (string, bool)`, the item's node:
    - `kit:<n>` → `config/kits/<n>`;
    - `dest:<n>` → `config/destinations/<n>`;
    - a project config item on tab `agents` with Subject `<p>/<role>` → `config/projects/<p>/agents/<role>`;
    - tab `escalation` → `config/projects/<p>/escalation`;
    - any other config item with a Scope → `config/projects/<p>`;
    - kinds broken, stale and alert → `false`.

    Each segment is `url.PathEscape`d.
  - `func buildTree(store jam.Store, items []attnItem) []TreeNode`, in this order:
    - `Tracking` → `config/tracking`;
    - `Projects` → `config/projects`. Each project (by name) → `config/projects/<p>` has these children:
      - `Members` → `…/members`, with each member as label = user name, path `…/members/<userID>`;
      - `Agents` → `…/agents`, with each role → `…/agents/<role>`;
      - `Intercom` → `…/intercom`;
      - `Escalation` → `…/escalation`.
    - `Users` → `config/users`, with each user (`store.ListUsers()`, by name) → `config/users/<userID>`;
    - `Kits` → `config/kits/<name>`;
    - `Destinations` → `config/destinations/<name>`;
    - `Model Specs` → `config/model-specs/<name>`.

    A node's badge is `badgeOf` over the items whose `treePath` equals its path or is under it (`path + "/"` prefix). `Group` is true for Projects, each project, Members, Agents, Users, Kits, Destinations and Model Specs, even when empty.
  - Route `GET /app/admin/api/tree` → `TreeResponse{Nodes: buildTree(store, src.items())}`.

- [ ] **Step 1: Write the failing tests** in `tree_test.go`:
  - `TestTreeShape`: seed project `acme` with member `alice`, role `coder`, kit `base`, destination `gh` and model spec `m1`. Assert:
    - the top-level labels are exactly `Tracking, Projects, Users, Kits, Destinations, Model Specs`;
    - `acme`'s children are `Members, Agents, Intercom, Escalation`;
    - the member's path is `config/projects/acme/members/<alice id>`;
    - every `Children` is non-nil in the JSON (`"children":[]`).
  - `TestTreeBadges`: role `coder` names missing kit `nope`. Then:
    - `config/projects/acme/agents/coder` has badge 1;
    - `…/agents`, `acme`, `Projects` each have 1;
    - `Users` has 0.

    A seeded broken studio adds nothing anywhere in the tree.
  - `TestTreePathsEscape`: project `a b/c` gets path `config/projects/a%20b%2Fc`.
  - `TestTreeAPI`: `GET /app/admin/api/tree` → 200 JSON with `nodes[0].path == "config/tracking"`.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run Tree` → FAIL.
- [ ] **Step 3:** Implement `tree.go`. Register the route from `apiRoutes`. Update `appTabBadges`' `config` key to use `treePath`.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): the Config tree API with attention badges`.

### Task 4: The admin area in the app (start, shell, title bar, routes, placeholders)

**Files:**
- Modify: `web/app/src/main.tsx` (area split), `web/app/src/api/client.ts` (`apiAt`), `web/app/src/shell/TitleBar.tsx` (generic `TitleBar` + `UserTitleBar`), `web/app/src/shell/Shell.tsx` (uses `UserTitleBar`), `web/app/scripts/size-budget.mjs` (the admin budget), `web/app/vite.config.ts` (`build.manifest: true`)
- Create:
  - `web/app/src/admin/start.tsx`;
  - `web/app/src/admin/paths.ts`;
  - `web/app/src/admin/AdminApp.tsx` (routes);
  - `web/app/src/admin/AdminShell.tsx`;
  - `web/app/src/admin/legacy.ts` and `legacy.test.ts`;
  - `web/app/src/admin/NotMovedYet.tsx`;
  - `web/app/src/admin/test/render.tsx`;
  - `web/app/src/admin/AdminApp.test.tsx`.

**Interfaces:**
- Consumes: `Bootstrap`, `TreeResponse` from `src/admin/api/types.ts` (Task 2/3).
- Produces:
  - `client.ts`: `apiAt(base: string): { get<T>(path): Promise<T>; post<T>(path, body): Promise<T> }`. The existing `apiGet`/`apiPost` become `apiAt(API)`'s methods, with unchanged behaviour.
  - `admin/paths.ts`: `ADMIN = "/app/admin/next/"`, `adminApi = apiAt("/app/admin/api/")`, `nodeHref(path: string) => ADMIN + path`.
  - `TitleBar` props:

    ```ts
    { brand: string; tabs: NavTab[]; current?: string; name: string; initial: string; roles: string; menu: ReactNode }
    ```

    Here `NavTab` is structurally the same in both areas. The current tab gets `aria-current="page"`. A tab whose `href` starts with the area's mount is a router `Link`; any other tab is a plain anchor. `UserTitleBar({ boot })` holds today's Notifications, Sounds and Sign out menu. The admin menu has only Sign out, when `signOut`, as a plain link to `/app/auth/logout`.
  - `adminRoutes(boot: Bootstrap): RouteObject[]` under `ADMIN`:
    - index → Dashboard placeholder;
    - `config` → `<Navigate to="config/tracking" replace>`;
    - `config/*` → `ConfigLayout` (Task 5) with a `*` child that renders `NotMovedYet`;
    - `agents/*` and `health/*` → `NotMovedYet`;
    - `*` → not found.
  - `AdminShell`: an `AppShell` with the title bar. The navbar (the Config tree, Task 5) is present only under `config/`. On narrow screens it's a drawer opened from a burger in the header (`navbar.collapsed.mobile`). It never opens `/app/user/events`.
  - `legacy.ts`: `oldHref(path: string): string`, the old admin page for a new path:

    | New path | Old page |
    |---|---|
    | `config/tracking` | `/app/admin/config` |
    | `config/projects[/p[/members\|agents\|intercom\|escalation]]` | `/app/admin/projects[/p[/…]]` |
    | `…/members/<uid>` | `/app/admin/users/<uid>` |
    | `…/agents/<role>` | `/app/admin/projects/<p>/roles/<role>` |
    | `config/users[/u]`, `config/kits[/k]`, `config/destinations[/d]`, `config/model-specs[/m]` | the same under `/app/admin/` |
    | `""` (Dashboard) | `/app/admin/` |
    | `agents…`, `health…` | the same under `/app/admin/` |

    Segments are passed through as already escaped. Slice 9's redirects invert this table.
  - `NotMovedYet`: the heading "This page hasn't moved yet" and an anchor "Open it in the current admin" linking `oldHref(<current path under ADMIN>)`.
  - `main.tsx`: when `location.pathname.startsWith("/app/admin/")`, it runs `import("./admin/start").then((m) => m.start(el))`. Otherwise it runs today's `start(el)`. Admin `start` registers the service worker, loads the bootstrap from `#jam-bootstrap` or `adminApi.get("bootstrap")`, and doesn't call `initSounds`.
  - `size-budget.mjs`:
    - read `.vite/manifest.json`;
    - total = the user entry, plus `src/admin/start.tsx`'s chunk and its static `imports` that the entry doesn't already load, all gzipped;
    - require that total ≤ 350 KB;
    - remove `.vite/` from the output afterwards.

- [ ] **Step 1: Write the failing tests.**
  - `legacy.test.ts`: a table of new path → old href covering every row above. It includes `config/projects/a%20b/agents/coder` → `/app/admin/projects/a%20b/roles/coder`.
  - `AdminApp.test.tsx`, through `renderAdmin(boot, path)`:
    - (a) at `ADMIN + "config"` the location becomes `config/tracking`;
    - (b) a local admin's title bar has no Inbox tab, and Config is `aria-current="page"` under `config/…`;
    - (c) at `ADMIN + "agents"` the page shows "This page hasn't moved yet" with a link to `/app/admin/agents`;
    - (d) the Sign out item is absent when `signOut` is false.

    Mock the tree with MSW as `{ nodes: [] }`.
  - The existing `TitleBar.test.tsx` keeps passing against `UserTitleBar`.
- [ ] **Step 2:** `npm test -- admin` → FAIL.
- [ ] **Step 3:** Implement the files above.
- [ ] **Step 4:** `npm run check` → PASS. `scripts/app.sh` → prints both budget lines under 350 KB.
- [ ] **Step 5: Commit** `feat(app): the admin area — start, shell, title bar, placeholder pages`.

### Task 5: The Config tree component

**Files:**
- Create: `web/app/src/admin/config/ConfigLayout.tsx`, `web/app/src/admin/config/ConfigTree.tsx`, `web/app/src/admin/config/treeState.ts`, `web/app/src/admin/config/treeState.test.ts`, `web/app/src/admin/config/ConfigTree.test.tsx`

**Interfaces:**
- Consumes: `adminApi`, `nodeHref`, `TreeNode`.
- Produces:
  - `useTree()`: `useQuery({ queryKey: ["admin", "tree"], queryFn: () => adminApi.get<TreeResponse>("tree"), refetchInterval: 3000 })`. The default `refetchIntervalInBackground: false` pauses it while the tab is hidden.
  - `treeState.ts` holds pure functions:
    - `ancestors(path: string): string[]`: every proper prefix at a `/` boundary that is a node path;
    - `filterTree(nodes: TreeNode[], q: string): TreeNode[]`: keeps a node whose label contains `q` (case-insensitive), or any descendant of one, along with its ancestors;
    - `loadOpen(): Set<string>` and `saveOpen(s: Set<string>)`: localStorage key `jam-admin-tree`, a JSON array of paths, tolerant of bad JSON.
  - `ConfigTree`:
    - A filter `TextInput` (placeholder "Filter") sits at the top.
    - Each row is a router link to `nodeHref(path)`, `active` when it's the current path. Rows are indented by depth and show the badge (red when `badgeRed`, else yellow; `aria-label` = `badgeTitle`).
    - A group row has a separate chevron `ActionIcon` (`aria-label` "Expand <label>" / "Collapse <label>"). The chevron toggles open, and clicking the label navigates.
    - Open = the remembered set ∪ the current path's ancestors ∪ (everything, while filtering).
    - Loading shows a `Loader`. An error shows `Problem`.
  - `ConfigLayout`: `AdminShell`'s navbar content is `ConfigTree`, and the main area is `<Outlet/>`. Closing the mobile drawer happens on navigation.

- [ ] **Step 1: Write the failing tests.**
  - `treeState.test.ts`:
    - `ancestors("config/projects/acme/agents/coder")` equals `["config/projects","config/projects/acme","config/projects/acme/agents"]`;
    - `filterTree` with `"cod"` keeps `Projects › acme › Agents › coder` and drops `Users`;
    - `loadOpen` with `localStorage["jam-admin-tree"]="{bad"` returns an empty set.
  - `ConfigTree.test.tsx` (MSW tree fixture: `acme` with role `coder` badge 1 red, user `alice`):
    - (a) at `ADMIN+"config/projects/acme/agents/coder"` the `coder` link is visible and active, its branch is open, and the badge reads 1;
    - (b) clicking the "Expand Users" chevron shows `alice` without navigating, and reloading the tree keeps it open (from localStorage);
    - (c) typing `ali` in Filter hides `acme`;
    - (d) clicking `Kits` navigates to `ADMIN+"config/kits"` and the page shows the NotMovedYet link `/app/admin/kits`;
    - (e) an unknown path `config/projects/gone` still renders the tree and the NotMovedYet page.
- [ ] **Step 2:** `npm test -- config` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): the admin Config tree`.

### Task 6: Browser test and docs

**Files:**
- Create: `internal/jam/adminui/app_browser_test.go` (`//go:build browser`)
- Modify: `justfile` if `test-browser` names packages
- Modify: `docs/web-app.md`, `docs/usage/jam/INDEX.md` or the admin UI usage doc that describes the tabs (one line pointing at the preview at `/app/admin/next/`), and the spec's status line ("slice 1 merged …" once merged)

**Interfaces:**
- Consumes: everything above; the userui browser harness pattern (`withPWA`-style wrapper serving `/app/sw.js`, `/app/static/`, `/app/assets/` via `appui.Handler`).

- [ ] **Step 1: Write the browser test** `TestBrowserAdminTree`. It serves the adminui `Handler` behind a loopback admin principal.
  - Navigate to `/app/admin/next/config/projects/acme/agents/coder`.
  - Wait for the `coder` link with `aria-current="page"`.
  - Click `Users`, assert the URL ends with `config/users`, then go back and assert the URL ends with `…/agents/coder`.
  - Assert there are no console errors and no CSP violations, using the Log-domain pattern from `userui/app_browser_test.go`.
- [ ] **Step 2:** `just app && just test-browser` → PASS; it skips without Chrome.
- [ ] **Step 3: Docs.**
  - `docs/web-app.md` gains an "Admin area" section covering:
    - the mount `/app/admin/next/` (until cutover);
    - `src/admin/`;
    - the admin API at `/app/admin/api/` (types in `adminui/apitypes.go`, generated into `src/admin/api/types.ts`);
    - the tree;
    - the shared `/app/assets/`;
    - the second size budget.

    Update its frontmatter `summary` / `owns` and its Layout lines that say assets live under `/app/user/`.
  - Add one line to the admin usage doc that owns the tabs.
  - Run the **docs-audit** skill.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for the Config tree; docs`.
