# Admin SPA slice 2 (Tracking and Users) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In the admin app at `/app/admin/next/`, the Config tree's **Tracking** node is today's Config page (the tracked source, the managed objects, Diff / Apply / Track / Untrack / Release). **Users** is the users list with New user. A user's page offers rename, logins, OIDC, add or unlink an account, and remove. All of these have parity with the old pages.

**Architecture:** `adminui` gains JSON twins of the Config page and user routes under `/app/admin/api/`. They call the same `jam` functions the HTML handlers call, and serialize new JSON view types in `apitypes.go`. The app gains the shared admin components the spec lists (§5), the first ones needed: `DataTable`, `Section`, `EditForm`, `ConfirmAction`, `ManagedBadge` and `DiffView`. The pages are built on them.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§3, §4, §5, §6 slice 2). Slice 1's plan holds the conventions: `docs/superpowers/plans/2026-10-28-admin-spa-1-foundation.md`.

## Global Constraints

- Writes are `POST` / `DELETE` with JSON bodies (`415` otherwise). The API's errors are `{"error","code"}` with the real status, mapped as follows:
  - 400 → `bad_request`
  - 403 → `forbidden`
  - 404 → `not_found`
  - 409 → `conflict`
  - 412 → `stale`
  - 413 → `too_large`
  - anything else → `internal`
- A user's ref in a URL is a name or a `usr_` id (`jam.ResolveRegistryRef`). The tree links by id.
- **Parity:** every action on the old Config, Users and user pages exists, with the same confirm texts:
  - "Stop following <label>? Managed objects and their marks stay."
  - "Stop managing <kind> <name> from the config repo? It stays in Jam, unmanaged."
  - "Apply N change(s) to this Jam[, overwriting the drifted objects]?" / "Record this plan's commit?"
  - "Remove user <name>? They leave every project; history keeps showing them as removed."
  - "Unlink this <connection> account from <name>?"
  - The drift note "This will drift from the config repo (managed · config @<commit>)." is prepended to the confirm on a managed user's rename, logins save and remove.
- The success messages of tracking writes (`applyMsg`, "config source untracked; …", "released …", "tracking …") show as a green notification.
- A page's links to other objects are app paths (`nodeHref`) when the object has a tree node. Otherwise they are the old admin URL.

## Review Focus

1. **A stale Apply.** The repo or Jam changed after Diff. Apply must answer 409 with `configPlanStaleMsg`, show it, and keep the plan visible. Tested in Task 4 (`TestTrackingApplyStale`) and Task 5.
2. **A Diff or Apply that takes seconds** (a git fetch). The buttons are disabled and show progress, and a double click sends one request. Tested in Task 5 (`disables Diff while planning`).
3. **A user ref that doesn't exist, or a removed user.** The page shows "User not found" or the read-only removed banner, and offers no write controls. Tested in Task 2 and Task 3.
4. **A rename to a name that's taken.** It answers 409, and the form keeps the typed value with the error shown inline. Tested in Task 3.
5. **Tree freshness after a write.** Creating, renaming or removing a user updates the tree, through the `["admin","tree"]` invalidation. Tested in Task 3.

---

### Task 1: Shared admin components and mutations

**Files:**
- Modify: `web/app/src/api/client.ts` (`apiAt(...).del`)
- Create, in `web/app/src/admin/ui/`:
  - `DataTable.tsx`, `Section.tsx`, `EditForm.tsx`, `ConfirmAction.tsx`, `ManagedBadge.tsx`, `DiffView.tsx`;
  - `useAdminMutation.ts`;
  - `ui.test.tsx`.

**Interfaces:**
- Produces:
  - `apiAt(base).del<T = void>(path: string, body?: unknown): Promise<T>`, sent with method `DELETE`. `request` accepts `"DELETE"`.
  - `DataTable<R>({ columns: { key: string; label: string; render: (r: R) => ReactNode }[]; rows: R[]; rowKey: (r: R) => string; empty: ReactNode })`: a Mantine `Table`; an empty `rows` renders one full-width cell with `empty`.
  - `Section({ title: string; sub?: ReactNode; actions?: ReactNode; children })`: a bordered `Card` with a header row.
  - `EditForm({ submit: string; onSubmit: () => Promise<unknown>; confirm?: string; resetOnSuccess?: () => void; children })`:
    - a `<form>` whose submit button shows `loading` and is disabled while pending;
    - with `confirm`, a modal "Confirm" must be accepted first;
    - a rejected promise's `ApiError.message` shows in a red `Alert` under the fields, and the fields keep their values;
    - success clears the error and calls `resetOnSuccess`.
  - `ConfirmAction({ label: string; confirm: string; onConfirm: () => Promise<unknown>; color?: string })`: a button. It opens a modal with `confirm` and buttons Cancel and `label`. Accepting runs `onConfirm` once (the button stays disabled while pending). A failure shows a red notification with the message.
  - `ManagedBadge({ managed?: Managed | null })` renders nothing without `managed`. Otherwise it renders a badge "managed · config @<commit>" linking `nodeHref("config/tracking")`, titled "Applied from the config repo <applied>; edits here drift from it".
  - `driftNote(m?: Managed | null): string`, "" or "This will drift from the config repo (managed · config @<commit>)."
  - `DiffView({ lines: DiffLine[] })`: a `<pre>` with one span per line, prefixed `+ `, `- `, `  … ` or `  `. Add and delete lines are coloured green and red.
  - `useAdminMutation<V, R>(fn: (v: V) => Promise<R>, opts?: { invalidate?: unknown[][]; onSuccess?: (r: R) => void })`: a TanStack `useMutation` that invalidates each key on success (always `["admin","tree"]` too).
  - The types `Managed { commit; applied }` and `DiffLine { op; text }` come from Task 2 / Task 4's `apitypes.go`. Task 1 defines `Managed` and `DiffLine` in `apitypes.go` first and regenerates.
- [ ] **Step 1: Write the failing tests** in `ui.test.tsx`:
  - `EditForm` with `confirm` doesn't call `onSubmit` until Confirm is clicked.
  - `EditForm` shows a rejected `ApiError("x", …)` message, and the input keeps its typed value.
  - `ConfirmAction` calls `onConfirm` once on a double click of the modal's action button.
  - `DataTable` renders `empty` with no rows.
  - `ManagedBadge` links `/app/admin/next/config/tracking`.
  - `DiffView` prefixes lines.
- [ ] **Step 2:** `npx vitest run src/admin/ui` → FAIL.
- [ ] **Step 3:** Add `Managed` and `DiffLine` to `apitypes.go`, then `go generate ./internal/jam/adminui/`. Implement the components and `del`.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): shared admin components`.

### Task 2: Users API

**Files:**
- Create: `internal/jam/adminui/api_users.go`, `internal/jam/adminui/api_users_test.go`
- Modify: `internal/jam/adminui/apitypes.go`, `internal/jam/adminui/api.go` (`decodeJSON`, `apiFail`, route registration)

**Interfaces:**
- Produces (types):
  ```go
  type Managed struct{ Commit, Applied string }                       // json commit, applied
  type AccountRow struct{ ID, Connection, ServiceUID, Handle string } // json id, connection, serviceUid, handle
  type UserRow struct {
      ID, Name string; Logins []string; Accounts []AccountRow; Projects []string
      Managed *Managed `json:"managed"` // nil = not managed by the config repo
  }
  type UsersResponse struct{ Users []UserRow }
  type ConnectionChoice struct{ Name, Kind string }
  type UserDetail struct {
      User UserRow; Removed bool; LoginSpec, OIDCSpec string; Connections []ConnectionChoice
  }
  type Created struct{ ID, Path string } // a create's id and its page (under the app mount)
  ```
  Every list is non-nil, and every field has a camelCase json tag.
- Produces (Go helpers, `api.go`):
  - `decodeJSON(w, r, v any) bool`: 415 `bad_request` unless `Content-Type: application/json`; 400 on bad JSON.
  - `apiFail(w, err error, fallback int)`: the status is `jam.WriteStatus(registryErr(err), fallback)`, with the code from Global Constraints.
  - The API's origin check answers in JSON. Replace `browserauth.RequireSameOrigin` around the API mux with an equivalent wrapper that answers 403 `{"error":"cross-origin request refused","code":"forbidden"}`. Today the text/plain 403 reads as "signed out" in the client (slice 1 review). Update `TestAdminAPICrossOrigin` to assert the JSON `code: "forbidden"`.
- Routes:
  - `GET api/users` → `UsersResponse`, sorted by name.
  - `POST api/users {name, logins, oidc}` → 201 `Created{ID, Path: "config/users/<id>"}`. `logins` and `oidc` are newline-separated, as on the old form.
  - `GET api/users/{user}` → `UserDetail`, or 404 `not_found`.
  - `POST api/users/{user}/name {name}`, `…/logins {logins}`, `…/oidc {oidc}`, `…/accounts {connection, uid, handle}` → `UserDetail`.
  - `DELETE api/users/{user}/accounts/{account}` → `UserDetail`. It's 404 when the account isn't this user's.
  - `DELETE api/users/{user}` → 204.

  Each write logs as the HTML route does (`"ui user …"`) and calls the same `jam`/store function.
- [ ] **Step 1: Write the failing tests** in `api_users_test.go`:
  - `TestUsersAPI` covers the cycle create → list → get by name and by id → rename → logins → OIDC → add account → unlink → remove. It checks each response's JSON. After the removal, a GET shows `removed: true`.
  - `TestUsersAPIRefusals`:
    - a duplicate name → 409 `conflict`;
    - an unknown user → 404 `not_found`;
    - unlinking another user's account → 404;
    - a form-encoded POST → 415.
  - `TestUsersAPIManaged`: with a config mark on user `bob`, `managed.commit` is set.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run UsersAPI` → FAIL.
- [ ] **Step 3:** Implement, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): users API`.

### Task 3: Users pages in the app

**Files:**
- Create: `web/app/src/admin/users/UsersPage.tsx`, `UserPage.tsx`, `users.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (routes `config/users` and `config/users/:user`)

**Interfaces:**
- Consumes: Task 1's components and Task 2's routes and types.
- Produces:
  - **UsersPage** has:
    - a `Section` "New user", collapsed by default, with Name (required), Logins and OIDC identities textareas. Create navigates to `nodeHref(created.path)`.
    - a `DataTable` with columns Name (a link to `nodeHref("config/users/"+id)`), Logins, Accounts (`connection` badge plus `@handle` or the uid) and Projects (a link to `nodeHref("config/projects/"+p)`). Its empty text is "No users.".
  - **UserPage** (`:user` = ref) has:
    - a heading with the name, `ManagedBadge` and the id;
    - "Remove user" (`ConfirmAction`, then navigate to `config/users`);
    - sections Name, Projects (links), Logins, OIDC identities and Accounts. Accounts holds a `DataTable` with Unlink on each row, plus an Add account form with a connection select.

    For a removed user it shows the banner "This user was removed. Their id still labels history as <name> (removed); nothing here can be changed." and no forms. An API 404 shows "User not found" with a link to the users page. Each write's response replaces the query data `["admin","user",ref]`.
- [ ] **Step 1: Write the failing tests** in `users.test.tsx` (MSW):
  - the list renders rows, and its empty text;
  - creating a user navigates to its page, and the tree query is refetched;
  - a rename answering 409 shows the message inline and keeps the typed name;
  - renaming a managed user asks to confirm with the drift note;
  - a removed user shows the banner and no "Rename" button;
  - an unknown ref shows "User not found";
  - Unlink asks "Unlink this discord account from alice?" and then shows the updated list;
  - Remove navigates to the users list.
- [ ] **Step 2:** `npx vitest run src/admin/users` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin users pages`.

### Task 4: Tracking API

**Files:**
- Create: `internal/jam/adminui/api_tracking.go`, `internal/jam/adminui/api_tracking_test.go`
- Modify: `internal/jam/adminui/apitypes.go`, `internal/jam/adminui/config_page.go` (`objectPath`)

**Interfaces:**
- Produces (types):
  ```go
  type TrackingSource struct{ Label, Repo, RepoLink, Dir, Ref, Commit, CommitLink, Applied string }
  type ManagedRow struct{ Kind, Name, Path, Commit, Applied, State string } // Path "" = no page in the app
  type ConfigKeyRow struct{ Kind, Name string }
  type KitSourceRow struct{ Path, Source, Commit string }
  type PlanOpRow struct {
      Action, Kind, Name, Path, File string; Drift, Unmanaged, TooLarge bool
      Lines []DiffLine; Diff string
  }
  type ServeDiff struct{ State string; Keys []string }
  type Plan struct {
      Source, Commit, Digest string; Forced, HasDrift bool; Changes, Unchanged int
      Ops []PlanOpRow; Forget, Unmanaged []ConfigKeyRow; KitSources []KitSourceRow
      Refusals []string; Serve ServeDiff
  }
  type Tracking struct {
      Source *TrackingSource `json:"source"`; Rows []ManagedRow; Drifted int; StatesErr bool
      LastCommit, LastApplied string; CanGit bool; Plan *Plan `json:"plan"`
  }
  type TrackingResult struct{ Tracking Tracking; Message string } // a write's page and its flash
  ```
- Produces (Go):
  - `objectPath(store jam.Store, kind, name string) string`, a config object's app path:
    - destination → `config/destinations/<n>`;
    - model-spec → `config/model-specs/<n>`;
    - user → `config/users/<id>`, resolving the name (the name itself when it can't be resolved);
    - kit → `config/kits/<n>`;
    - project → `config/projects/<p>`;
    - role `p/r` → `config/projects/<p>/agents/<r>`;
    - anything else → "".

    Segments are escaped.
  - `trackingOf(store, canGit, log) Tracking` converts `buildConfigPage`'s payload.
  - `planOf(planView) *Plan` converts `newPlanView`'s.
- Routes, on `registerConfig`'s inputs (`registerApp` takes `kitFetcher`, `cfgEnv` and `log`):
  - `GET api/tracking`;
  - `POST api/tracking/diff {force}` → `Plan`;
  - `POST api/tracking/apply {plan, force}` → `TrackingResult`. A missing plan is 400 with the old message. A stale plan is 409 `conflict` with `configPlanStaleMsg`.
  - `POST api/tracking/source {source}` → `TrackingResult` with `Tracking.Plan` set. When something is already tracked it's 409 with the old message.
  - `DELETE api/tracking/source` → `TrackingResult`;
  - `DELETE api/tracking/managed/{kind}/{name...}` → `TrackingResult`.

  Diff, apply and source answer 404 `not_found` "git actions are not available on this serve" without a fetcher. Errors go through `jam.ConfigStatus`.
- [ ] **Step 1: Write the failing tests** in `api_tracking_test.go`. Reuse the existing config page tests' fake fetcher and fixture (`config_page_test.go`).
  - `TestTrackingAPI`: GET with nothing tracked → `source: null`, `canGit` true. Then track, which returns a plan. Then diff, then apply with the digest, which answers the `applyMsg` message. The rows' `path` values are app paths.
  - `TestTrackingApplyStale`: apply with digest `"nope"` → 409 `conflict` and the stale message.
  - `TestTrackingRelease`: release a mark → 200 with the message, and the mark is gone.
  - `TestTrackingNoGit`: without a fetcher, diff → 404.
  - `TestObjectPath`: covers each kind, including a role with a space.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'Tracking|ObjectPath'` → FAIL.
- [ ] **Step 3:** Implement, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): tracking API`.

### Task 5: Tracking page in the app

**Files:**
- Create: `web/app/src/admin/tracking/TrackingPage.tsx`, `PlanView.tsx`, `tracking.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (route `config/tracking`)

**Interfaces:**
- Produces:
  - **TrackingPage** has:
    - a heading "Tracking", with the sub "This Jam's configuration in git — the repo it follows, the objects the repo manages, and the plan to apply it";
    - a `Section` "Source":
      - with a source: Repo, Dir, Ref and Last applied, plus the buttons Diff and Untrack (`ConfirmAction`);
      - without one, when `canGit`: the Track form;
      - otherwise: "Git actions are not available on this serve; use `jam config apply --dir`.";
    - a `Section` "Managed objects" with a `DataTable` of Object (kind badge plus a link when `path`), From, Applied, State and Release (`ConfirmAction`). It shows the StatesErr banner when set.
    - the Plan (`PlanView`), when the last Diff or Track returned one.
  - **PlanView** shows:
    - the refusals banner;
    - the Force checkbox when `hasDrift`, which re-diffs with `force`;
    - the kit sources;
    - the ops table, where each op with lines, a diff or `tooLarge` gets a `<details>` that's open for drift or unmanaged ops, holding `DiffView`, or a `<pre>` for `diff`;
    - the forget, unmanaged and serve lines;
    - "Apply this plan" (`ConfirmAction` with the parity confirm text) unless there are refusals.

    The plan is kept in component state. Diff and Apply are disabled while any tracking write is pending (one in flight).
- [ ] **Step 1: Write the failing tests** in `tracking.test.tsx`:
  - with nothing tracked, the Track form shows; Track shows the returned plan and the "tracking …" notification;
  - Diff shows the plan, and its button is disabled while pending (MSW delays the response);
  - a stale Apply shows the 409 message and keeps the plan;
  - Release asks the parity confirm and then shows the updated rows;
  - the Force checkbox re-diffs with `force: true`;
  - a refused plan shows no Apply button.
- [ ] **Step 2:** `npx vitest run src/admin/tracking` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin Tracking page`.

### Task 6: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go`, `docs/web-app.md` (the admin area's routes list), `docs/usage/jam/ui-console.md` (preview line: Tracking and Users work there), and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminUserRename`:
  - open `config/users/<alice id>`;
  - fill Name with `alicia` and click Rename;
  - wait for the heading `alicia`;
  - assert the tree's Users branch shows `alicia` after its next poll;
  - assert there are no page problems.
- [ ] **Step 2:** `just app && just test-browser` → PASS.
- [ ] **Step 3:** Update the docs, then run the docs audit.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for a user rename; docs`.

Member leaves (`config/projects/:p/members/:user`, with "Remove from project") come in slice 3 with the project Members page.
