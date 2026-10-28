# Admin SPA slice 8 (Dashboard, Health, search) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The admin app's last screens work at parity with the old pages, after which no page is `NotMovedYet`:
- **Dashboard** (the area root): the summary tiles, Needs attention, the Jam-wide agent context card (save, clear), Managers, and the running-studio table (polled every 3 s);
- **Health** (`health`): open and resolved conditions with their fixes (polled every 10 s), a link to Silences, and the **channel log** at `health/intercom` (Channels or Legacy, filters in the URL);
- **Search**: a title-bar box (`/` focuses it from anywhere) and `search?q=`, with live results; Enter jumps straight to a single exact match (`go=1`).

**Architecture:** `adminui/api_dash.go` holds the JSON twins of the dashboard, the Jam context writes, health, the channel log and search. Search hits and attention items carry **app paths** (relative to the admin mount, e.g. `config/users/usr_1`), made by one Go mapping `appPath(oldURL)` from the old admin URLs those builders already produce. Slice 9's redirects will invert the same table. The app adds `dash/`, `health/` and `search/` page folders and an `AttentionList` shared component. The title bar gains a search slot.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§1 title bar "plus today's search box"; §2 URLs: Dashboard ``, Health `health`, channel log `health/intercom`, Search `search?q=`; §4 polling rates; §5 `AttentionList`; §6 slice 8).

## Global Constraints

- **Polling:** the studio table every 3 s and Health every 10 s, only while the tab is visible (TanStack `refetchInterval`, `refetchIntervalInBackground: false`).
- **Copy kept from the old pages:**
  - the tiles: Running, Waiting, Setting up, Lost / terminating, Idled, Projects, Agents, Users, Specs;
  - "Needs attention", "Nothing needs attention.", and the kind chips broken / out of date / warning / config;
  - Health: "Problems Jam has detected in itself — each with how to fix it. Pushed to operators by Alertmanager when configured.", "Open", "Resolved (7 days)", "None.", "— lasted X, resolved Y ago";
  - Channel log: "The message log is unavailable.", "Ignored an unparseable date (expected YYYY-MM-DD).", "No squawks match." / "No squawks logged yet.";
  - Search: "Type at least 2 characters.", "Search by name, id, unit, handle, route, prompt or message text.", "No matches for <q>.", "<n> match(es) for <q>".
- **The Jam context write** reuses `jam.ParseContextYAML` + `jam.SetJamContextChecked`. It logs `ui jam context set|cleared` with the operator and never the YAML.
- **Highlighting** is React text split on the query (case-insensitive), wrapped in `<mark>`. No HTML comes from the server for search.
- **Squawk bodies** remain `squawkrender`'s sanitized HTML, rendered by the existing `SquawkTable` / `MessageBody` and nowhere else.
- **No tabs inside a page:** the channel log's Channels/Legacy choice is a "Log" select in its filter form, not a tab strip.

## Review Focus

1. **An old URL that `appPath` doesn't know** (e.g. `/app/admin/config`, a role with a `/` in its name escaped, an agent URL with `?project=`) must map to the right page or the dashboard. It must never produce a path outside the mount. Task 1 tests a table of these.
2. **Search Enter with zero or several exact matches** shows the results page and does not jump. With exactly one, it replaces the history entry, so Back returns to where the operator was. Task 5 tests both.
3. **The `/` shortcut** must not fire while typing in an input, a textarea or a contenteditable (the Draft editor, the context YAML). Task 5 tests it.
4. **The channel log with a bad date** keeps the other filters, shows the banner, and treats the date as unbounded. Changing filters keeps `?log=legacy`. Task 2 and Task 4 test these.
5. **Clearing the Jam context** while the panel's YAML has unsaved edits. As with ContextPanel elsewhere, the panel is keyed by the stored YAML, so the cleared state replaces the editor's text. Task 3 tests it.

---

### Task 1: Dashboard API and `appPath`

**Files:**
- Create: `internal/jam/adminui/api_dash.go`, `internal/jam/adminui/api_dash_test.go`, `internal/jam/adminui/apppath.go`, `internal/jam/adminui/apppath_test.go`
- Modify: `internal/jam/adminui/apitypes.go`, `api.go` (`registerApp` gains a `dashAPI` argument), `adminui.go` (pass it), `context_edit.go` (extract `setJamContext(store, log, r, b)` used by both routes), then `go generate ./internal/jam/adminui/`

**Interfaces:**
- Produces:
  - `func appPath(old string) string`. The map, with segments kept escaped and the query kept:
    - `/app/admin/` → `""`;
    - `config` → `config/tracking`;
    - `users/{id}` → `config/users/{id}`;
    - `projects/{p}` → `config/projects/{p}`;
    - `projects/{p}/{members|agents|intercom|escalation}` → the same under `config/`;
    - `projects/{p}/roles/{r}` → `config/projects/{p}/agents/{r}`;
    - `kits/{k}`, `destinations/{d}`, `model-specs/{m}`, and the bare lists → under `config/`;
    - `kits/{k}/draft` → `config/kits/{k}`;
    - `agents…` → the same (dropping `project` from the query);
    - `intercom?…` → `health/intercom?…`;
    - `health` and `search?…` → the same;
    - anything else → `""`.
    
    Use `oldHref`'s table (`web/app/src/admin/legacy.ts`) as the inverse reference.
  - `type AttnRow struct { Kind string "kind"; Why string "why"; Path string "path" }`, built from `attnItem` (`Path: appPath(it.Href)`).
  - `type ManagerRow struct { Name; Connected bool; Version string; Studios, Capacity int; LastSeen string /* "15:04:05", "" = never */; Strays []string /* actor ids */ }`.
  - `type DashStats` mirrors `stats`, with json tags `studios`, `running`, `waiting`, `settingUp`, `idled`, `attention`, `projects`, `agents`, `users`, `specs`.
  - `type Dashboard struct { Stats DashStats; Attention []AttnRow; JamContext ContextPanel; HasManagers bool; Managers []ManagerRow; Coves []CoveRow }`.
  - Routes:
    - `GET api/dashboard`;
    - `POST api/jam/context` with `{yaml}` → `ContextPanel`;
    - `DELETE api/jam/context` → `ContextPanel`.
  - `type dashAPI struct { managers func() []jam.ManagerStatus; amURL string }`. The conditions come from `frameSource.conds`.

- [ ] **Step 1: Write the failing tests.**
  - `TestAppPath`, table-driven over every row above, plus these cases:
    - `/app/admin/projects/a%2Fb/roles/c` → `config/projects/a%2Fb/agents/c`;
    - `/app/admin/agents/x?project=p&status=running` → `agents/x?status=running`;
    - `/elsewhere` → `""`;
    - `/app/admin/../user` → `""`.
  - `TestDashboardAPI`, against a MemStore with one project, one broken kit, and a manager func returning one disconnected manager with a stray:
    - `stats.projects` is 1;
    - `attention[0].path` is `config/kits/<k>`;
    - `managers[0].strays` lists the stray;
    - `coves` is `[]`, not null.
  - `TestJamContextAPI`:
    - POST `{yaml: "core: hi"}` answers a panel whose `core` is "hi";
    - bad YAML is 400 `bad_request`;
    - DELETE answers `empty: true`;
    - the log buffer never contains "hi";
    - a cross-origin POST is 403.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'AppPath|DashboardAPI|JamContextAPI'` → FAIL (undefined).
- [ ] **Step 3:** Implement. The HTML dashboard keeps its own handler. `setJamContext` is shared by both route sets.
- [ ] **Step 4:** `go generate ./internal/jam/adminui/ && go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): dashboard API, Jam context writes, appPath`.

### Task 2: Health, channel log and search API

**Files:**
- Modify: `internal/jam/adminui/api_dash.go`, `api_dash_test.go`, `apitypes.go`, `intercom.go` (extract `parseSquawkQuery(q url.Values) (squawkFilter, squawksData)` from `handleIntercom`, which then uses it), and `search.go` (no behaviour change; its hits keep old URLs for the HTML page).

**Interfaces:**
- Produces:
  - `type HealthRow struct { Key, Severity, Summary, Fix, Detail, Age, Lasted string }`.
  - `type Health struct { Open, Resolved []HealthRow; Silences string }`: `GET api/health`, built from `healthData`'s fields. With no tracker, both lists are empty.
  - `SquawkRow` gains `To []ToCell` (`ToCell{Target string; External bool}`), filled for legacy rows in `squawkRowsOf`.
  - `type ChannelLog struct { Configured, Legacy, Filtered, BadDate bool; Rows []SquawkRow }`: `GET api/intercom` with `log, project, participant, q, since, until`, the same semantics as `handleIntercom`.
  - `type SearchHit struct { Title, Sub, Path string; Exact bool }`.
  - `type SearchGroup struct { Key, Name string; Hits []SearchHit; Count int; More string /* app path */ }`.
  - `type SearchResult struct { Q string; TooShort bool; Total int; Groups []SearchGroup; Jump string /* app path of the single exact hit, else "" */ }`: `GET api/search?q=`.

- [ ] **Step 1: Write the failing tests.**
  - `TestHealthAPI` (a tracker with one open and one resolved condition): severity, summary, fix and `lasted` present; `silences` ends `/#/silences` when an Alertmanager URL is set.
  - `TestChannelLogAPI`:
    - `?q=` filters bodies;
    - `?since=bad` gives `badDate: true` and `filtered: true`, and the other filters still apply;
    - `?log=legacy` returns legacy rows with `to`;
    - a nil reader gives `configured: false`.
  - `TestSearchAPI`:
    - `q=a` → `tooShort`;
    - a user named exactly the query → `jump: "config/users/<id>"`;
    - two exact hits → `jump: ""`;
    - squawks' `more` is `health/intercom?q=…`;
    - every hit path has no `/app/admin/` prefix.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement. The HTML channel log goes through the extracted parser. Run `go test ./internal/jam/adminui/ -run Intercom` to check its tests still pass.
- [ ] **Step 4:** `go generate ./internal/jam/adminui/ && go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): health, channel log and search API`.

### Task 3: Dashboard page

**Files:**
- Create: `web/app/src/admin/dash/DashboardPage.tsx`, `dash/dashboard.test.tsx`, `web/app/src/admin/ui/AttentionList.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (index route), `web/app/src/admin/ui/SquawkTable.tsx` (a channel links `health/intercom?participant=<channelId>`), `agents/AgentPage.tsx` ("in Intercom" → `nodeHref("health/intercom?participant=…")`)

**Interfaces:**
- Consumes: `Dashboard`, `AttnRow`, `ContextPanel` (Task 1).
- Produces: `AttentionList({ items }: { items: AttnRow[] })`: the "Needs attention" Section. Each row has a ⚠ flag (red for broken), its `why` linked to `nodeHref(path)`, and its kind chip.
- **DashboardPage:**
  - polls `["admin","dashboard"]` every 3 s;
  - tiles link `agents?status=running|waiting|setting-up|attention|idled`, `agents`, `config/users` and `config/kits`; Projects is a plain tile;
  - then AttentionList, then `ContextPanel` keyed by `yaml` with save/clear via `useAdminMutation` (which sets the query's `jamContext`);
  - Managers (when `hasManagers`) in a DataTable: connected/disconnected badge, version, studios/capacity, last seen, strays count with the ids as its title;
  - "Running agents" with `CovesTable`.

- [ ] **Step 1: Write the failing tests** (`dashboard.test.tsx`):
  - the tiles' counts and hrefs (e.g. the Waiting tile → `ADMIN + "agents?status=waiting"`);
  - an attention row links `ADMIN + "config/kits/k"`, and "Nothing needs attention." shows when there are none;
  - the Managers table shows only with `hasManagers`;
  - saving the context POSTs `{yaml}`;
  - clearing it (confirm) DELETEs, and the editor shows the cleared state even with unsaved text typed (Review Focus 5);
  - the coves table lists a studio linking `agents/<id>`.
- [ ] **Step 2:** `npx vitest run src/admin/dash` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npx vitest run src/admin && npx tsc -b && npx eslint src` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin dashboard`.

### Task 4: Health and the channel log

**Files:**
- Create: `web/app/src/admin/health/HealthPage.tsx`, `health/ChannelLogPage.tsx`, `health/health.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (`health`, `health/intercom`; drop the `health/*` NotMovedYet)

**Interfaces:**
- Consumes: `Health`, `ChannelLog` (Task 2).
- **HealthPage:**
  - polls every 10 s;
  - its header has the subtitle copy, a "Channel log" link (`health/intercom`) and "Silences ↗" when set;
  - the Open section lists each condition with a severity badge (critical red, warning yellow, info gray), its summary, "for <age>", the fix in a copyable `Code` block, Detail in a `<details>`, and the key;
  - the Resolved (7 days) section.
- **ChannelLogPage:**
  - the filter form's fields come from the URL: Log (Channels/Legacy), Project (TypeAhead projects), Participant (TypeAhead participants), Body contains, Since, Until (date);
  - Filter sets the search params, Reset clears them (keeping `log`), and Refresh refetches;
  - BadDate shows the banner;
  - `SquawkTable` gets a To column for legacy rows (`to`, with an internal/external badge).

- [ ] **Step 1: Write the failing tests** (`health.test.tsx`):
  - an open condition shows its fix and severity;
  - "Nothing needs attention." and "None." show when empty;
  - Silences links out;
  - the channel log mounted at `?log=legacy&since=bad` shows the banner, the Log select reads Legacy, and a legacy row shows its `to` target;
  - submitting Body contains "x" sets `?log=legacy&q=x`;
  - not configured shows "The message log is unavailable.".
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npx vitest run src/admin && npx tsc -b && npx eslint src` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin health and channel log`.

### Task 5: Search

**Files:**
- Create: `web/app/src/admin/search/SearchPage.tsx`, `search/SearchBox.tsx`, `search/highlight.tsx`, `search/search.test.tsx`
- Modify: `web/app/src/shell/TitleBar.tsx` (an optional `search?: ReactNode` slot before the theme toggle), `web/app/src/admin/AdminShell.tsx` (passes `<SearchBox />`), `AdminApp.tsx` (the `search` route; the last `*` stays NotMovedYet for unknown paths)

**Interfaces:**
- Consumes: `SearchResult` (Task 2).
- Produces:
  - `Highlight({ text, q }: { text: string; q: string })`: text with each case-insensitive occurrence of `q` in `<mark>`.
  - **SearchBox:**
    - an input "Search everything…" (aria-label "Search");
    - `/` focuses it unless the event target is an input, textarea, select or contenteditable, or a modifier is held;
    - Enter navigates to `search?q=<v>&go=1`.
  - **SearchPage:**
    - reads `?q=`, and its own big input updates `?q=` (replace, 200 ms debounce);
    - the query is `["admin","search",q]`;
    - when `go=1` and `jump` is set → `navigate(nodeHref(jump), { replace: true })`; else it drops `go` from the URL (replace) and shows the results;
    - groups as Sections (title, count, "showing N" when capped; squawks: "all N in Intercom" / "open in Intercom" linking `more`);
    - each hit is a link with highlighted title and sub.

- [ ] **Step 1: Write the failing tests** (`search.test.tsx`):
  - pressing `/` on the body focuses Search, while `/` typed in a textarea doesn't move focus;
  - Enter with one exact hit lands on its page and `router.state.historyAction` is `REPLACE`;
  - Enter with two exact hits shows the results, and the URL no longer has `go`;
  - "Type at least 2 characters." shows for `q=a`;
  - `Highlight` marks `Ali` in `alice` for `q=ALI`, and markup in the text stays text;
  - the squawks group's link is `ADMIN + "health/intercom?q=…"`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` and `scripts/app.sh` (budgets) → PASS.
- [ ] **Step 5: Commit** `feat(app): admin search with the title-bar box`.

### Task 6: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go` (`TestBrowserAdminDashboardSearch`), `docs/web-app.md`, `docs/usage/jam/ui-console.md`, and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminDashboardSearch`:
  - open the dashboard, save the Jam context YAML, and assert the store's Jam context;
  - press `/`, type a user's exact name, Enter, and wait for that user's page heading;
  - check there are no page problems.
- [ ] **Step 2:** `just app && go test -tags browser ./internal/jam/adminui/ ./internal/jam/userui/ -run Browser` → PASS.
- [ ] **Step 3:** Update the docs:
  - web-app.md: the routes, `appPath` and the new folders;
  - ui-console.md: the preview now covers every page.

  Then run the docs audit and check its counts haven't grown.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for the dashboard and search; docs`.
