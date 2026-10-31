# Config branch editor, slice 2b: the branch editor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the admin console's config pages work in the context of one branch of the followed config repo.
- **The followed branch** is the default context.
- **On a branch,** every save of the existing forms becomes a commit to that branch.
- **Branch actions:** Create branch, Open branch, Merge and PR. Merge applies the result at once.

**Architecture:**
- **`jam.ConfigBranches`** wraps a `configrepo.Repo`. For a branch it builds a **workspace**: a scratch `MemStore` holding the branch's config tree, applied with `ApplyConfigTree` and cached by branch head.
- **The router.** In adminui, the `X-Jam-Config-Branch: <name>` header sends a config API request to a handler built over the workspace's store, by the same factory as the live handler. After a write (non-GET, 2xx), `Save` exports the scratch store and commits the difference to the branch. The response is buffered, so a failed save is answered instead of the form's success.
- **Writing the export.** It goes into a temp copy of the branch's files with the same `WriteConfigTree` that `jam config write` uses, moved into `internal/jam`. That keeps its kit-dir mirror and `.dockerignore` rules. The diff of that dir against the branch is the commit.
- **Merge** plans the merged tree against the live Jam first: refusals refuse it. After the push it refreshes the follower and starts a tick, so the result applies now.
- **PR** goes through `configrepo.GitHub`.

**Tech Stack:** Go (internal/jam, adminui, configrepo), React/Mantine (web/app admin).

**Spec:** `docs/superpowers/specs/2026-10-31-config-branch-editor-design.md` (phase 2).

## Global Constraints

- Header: `X-Jam-Config-Branch`. With it absent or empty, the request is live, as today.
- These are routed to the workspace: the adminui config API prefixes `tree`, `users`, `connections`, `projects`, `roles`, `destinations`, `model-specs`, `kits` and `jam/context`, under `/app/admin/api/`. Everything else stays live.
- The followed branch can't be a branch context: 400 "the followed branch is read-only; create or open a branch".
- The commit author is the operator, `Name: OperatorID`, `Email: <OperatorID>@jam`. The message is `<METHOD> <api path>` (for example `POST projects/app/roles`), prefixed with `jam: `.
- No credential in any answer or log.
- The default branch name for Create is `jam/<operator>`, with the operator id lowercased and anything outside `[a-z0-9._-]` replaced by `-`.

## Review Focus

1. **Two tabs edit the same branch.** The second save, based on a stale head, is refused (409 `stale`, "reload"), and its scratch state is dropped (Task 2 test `stale save`).
2. **A branch whose tree doesn't plan** (a hand-pushed broken file). Opening it is a clear 409 with the refusals, not a 500 (Task 2 test `broken branch`).
3. **A save that changes nothing** (the form re-saved as is). No commit is made (Task 2 test `no-op save`).
4. **Merge of a branch whose result would be refused** against the live Jam. Merge is refused with the refusals, and nothing is pushed (Task 3 test).
5. **A kit dir file excluded by its `.dockerignore`** (e.g. `kits/web/node_modules/x`) survives a save (Task 2 test `dockerignored survives`).

---

### Task 1: `WriteConfigTree` moves into internal/jam

**Files:**
- Create `internal/jam/config_writetree.go`, moving `writeConfigTree`, `readTreeDockerignore`, `writeTreeFile`, `checkWritePath`, `isKitDir` and `mirrorKitDir` out of `cmd/jam/configgit.go`. `WriteConfigTree` is exported; the rest stay unexported.
- Modify `cmd/jam/configgit.go` to call `jam.WriteConfigTree`.
- Move their tests (grep `writeConfigTree\|mirrorKitDir` in `cmd/jam/*_test.go`) into `internal/jam/config_writetree_test.go`, keeping the CLI-level tests in cmd/jam.

**Interfaces:**
- Produces: `func WriteConfigTree(dir string, exp ConfigTreeExport) error`, unchanged in behaviour.

- [ ] Move. `go test ./cmd/jam/ ./internal/jam/` passes with the moved tests. Commit `refactor(config): WriteConfigTree lives in internal/jam`.

### Task 2: Branch workspaces and saves

**Files:** Create `internal/jam/config_branches.go`, `config_branches_test.go`.

**Interfaces:**
- Produces:
  ```go
  type ConfigBranches struct {
      Repo    *configrepo.Repo
      GitHub  *configrepo.GitHub // nil = no PRs
      Dir     string             // the config dir in the repo (Followed.Dir)
      Fetcher KitFetcher         // for .source.yml kits while loading
      Env     ConfigEnv          // CredExists, PoolConfigured, Serve, Followed, Follower
      Live    Store
      Log     *slog.Logger
      // private: mu, workspaces map[string]*ConfigWorkspace
  }
  type ConfigWorkspace struct { Branch, Head string; Store *MemStore }
  var ErrBranchStale = configrepo.ErrStale   // re-exported for status mapping
  func (b *ConfigBranches) Workspace(ctx context.Context, branch string) (*ConfigWorkspace, error)
  func (b *ConfigBranches) Save(ctx context.Context, ws *ConfigWorkspace, author configrepo.Signature, msg string) error // updates ws.Head; ErrStale drops the cached ws
  func (b *ConfigBranches) Drop(branch string)
  ```
- **Workspace.**
  - The cached one is returned while its Head equals the branch head (`Repo.Branches`). Otherwise it is rebuilt:
    1. `Repo.Files`, with the files under `Dir` written into a temp dir.
    2. `LoadConfigTree`.
    3. `NewMemStore()` plus `ApplyConfigTree(scratch, Fetcher, tree, envWithoutFollower, {Source: Followed with Ref=branch, Commit: head, Now}, nil, "branch:"+branch)`.
  - Refusals → `writeErr(409, "branch <b> does not plan: …")`.
  - The followed branch is `configrepo.ErrFollowedBranch`.
- **Save.**
  1. Export with `ExportConfigTree(ws.Store.configSnapshot(), ws.Store.ListConfigMarks(), Env.Serve, true)`.
  2. Write the branch's files under `Dir` into a temp dir, then `WriteConfigTree(tmp, exp)`.
  3. The changes are the files whose bytes or mode differ (Put) and the files gone (Delete), with paths prefixed by `Dir`.
  4. `Repo.Commit(ctx, ws.Branch, ws.Head, ch, author, msg)`. The new head goes on ws, and the cache is kept.
  5. ErrStale → Drop and return.
- Status mapping: `ConfigStatus` maps `configrepo.ErrStale` → 409 `"the branch moved since it was read; reload it"`, `ErrNoBranch` → 404, `ErrFollowedBranch` and `ErrBranchExists` → 400/409, and `*configrepo.ConflictError` → 409.

- [ ] Tests (gittest WithPush, a repo with `jam/` as Dir holding `users/bob.yml` and `kits/web/kit.yml` plus `kits/web/.dockerignore` (`node_modules`) and `kits/web/node_modules/x`):
  - Workspace loads bob.
  - A second Workspace call is the same pointer (cached).
  - After a hand push to the branch it is rebuilt.
  - Save after `PutUser`-style edits (use the store API, e.g. `ws.Store.CreateUser`) commits `jam/users/<new>.yml`; gittest.Show sees it, and the author is right.
  - **no-op save** leaves the head as it was.
  - **stale save**: a hand push between Workspace and Save → ErrStale, and the next Workspace is fresh.
  - **dockerignored survives**: after a save, `kits/web/node_modules/x` is still on the branch.
  - **broken branch**: a hand-pushed `jam/users/eve.yml` with `version: 9` → Workspace 409 naming the refusal.
- [ ] Fail, implement, pass. Commit `feat(config): branch workspaces and saves`.

### Task 3: List, Create, Merge, PR

**Files:** `internal/jam/config_branches.go`, `config_branches_test.go`.

**Interfaces:**
- Produces:
  ```go
  type ConfigBranchInfo struct { Name, Head string; Followed bool; PR string /* "" = none known */ }
  func (b *ConfigBranches) List(ctx) ([]ConfigBranchInfo, error)                   // the followed branch first, then by name; no PR lookups (cheap)
  func (b *ConfigBranches) Create(ctx, name string) (ConfigBranchInfo, error)
  func (b *ConfigBranches) Merge(ctx, branch, head string, author configrepo.Signature) (configrepo.MergeResult, error)
  func (b *ConfigBranches) PR(ctx, branch string) (configrepo.PullRequest, error)  // OpenPR(branch → Followed.Ref, title "Jam config: <branch>")
  func (b *ConfigBranches) FindPR(ctx, branch string) (configrepo.PullRequest, bool, error)
  func DefaultBranchName(operator string) string                                   // jam/<sanitized>
  ```
- **Merge.**
  - The check loads the merged files under Dir into a tree and runs `PlanConfig` against the live store (as a dry run: `ApplyConfigTree(..., DryRun)` on Live, with `Env`). Refusals → `ErrConfigRefused` with them.
  - Message `jam: merge <branch>`.
  - On success: `Drop(branch)`, then `Env.Follower.Tick` in a goroutine with `context.WithoutCancel`. The follower applies the new head now; it's already confirmed.
- **PR** without GitHub → `writeErr(501, "pull requests need config.github")`.

- [ ] Tests:
  - List gives main first (Followed true) then jam/alice.
  - Create with the default name.
  - Merge after a Save fast-forwards main, and gittest.Head(main) moves.
  - **Merge refused** when the merged tree has a role naming a kit that doesn't exist (pushed to the branch by hand): main is unchanged.
  - A Merge conflict → ConflictError.
  - PR against an httptest GitHub returns the URL; with no GitHub → 501.
  - DefaultBranchName table (`Alice@Example` → `jam/alice-example`).
- [ ] Fail, implement, pass. Commit `feat(config): list, create, merge and PR branches`.

### Task 4: The adminui branch router and branch API

**Files:**
- Create `internal/jam/adminui/branches.go`, `branches_test.go`.
- Modify `adminui.go`: `WithConfigBranches(b *jam.ConfigBranches, factory func(jam.Store) http.Handler) Option`. The Handler wraps itself with the router when set.

**Interfaces:**
- **Router.** `X-Jam-Config-Branch` is set and the path is under one of the Global Constraints' prefixes →
  1. `b.Workspace` (errors as API errors through `jam.ConfigStatus`).
  2. A handler for `ws.Store`, cached per workspace pointer.
  3. Serve into a recorder.
  4. If the method isn't GET/HEAD and the status is 2xx, `b.Save` with the operator's signature. A failed save writes its API error instead.
  5. Then copy the recorder out.
- **API** (on the live handler):
  - `GET /app/admin/api/config/branches` → `{branches: [...], followed, defaultName, canPR}`.
  - `POST …/config/branches` `{name}` → ConfigBranchInfo.
  - `POST …/config/branches/merge` `{branch, head}` → `{merge: MergeResult, message}`.
  - `POST …/config/branches/pr` `{branch}` → `{url}`.
  - `GET …/config/branches/pr?branch=` → `{url}` ("" = none).
  - All operator-only like the rest, and audit-logged (`ui config branch created|merged|pr`).
- Types go in `apitypes.go` (regenerated).

- [ ] Tests (adminui with a gittest repo and a factory that builds `adminui.Handler(store, …)`):
  - GET tree with the header shows the branch's users, while without it it shows the live ones.
  - A POST to create a user with the header commits to the branch and leaves live unchanged.
  - The header naming the followed branch → 400.
  - A stale save → 409 `stale`.
  - The branches API round trip: list, create, merge (live gets the user after the follower tick: call `Tick` directly in the test via env.Follower), pr 501 without GitHub.
- [ ] Fail, implement, pass. Commit `feat(admin): config pages edit a branch`.

### Task 5: The console's branch context

**Files:**
- Create `web/app/src/admin/branch/BranchBar.tsx`, `branch.ts` (the store: localStorage `jam.admin.configBranch`, a tiny subscribable), and `branch.test.tsx`.
- Modify `web/app/src/admin/paths.ts` (`adminApi` adds the header from `branch.ts` when set) and the Config layout that hosts the config pages (find where `TrackingPage` and the config tree render), so `BranchBar` shows above every config page.

**Behaviour:**
- **On the followed branch:** "Viewing **main** (what's running; read-only)", with **Create branch** (a modal with the default name, editable) and **Open branch** (a menu from `GET config/branches`).
- **On a branch:** "Editing **jam/alice** @abc1234", a link to the branch on GitHub (`<repo>/tree/<branch>`), and a PR link when one exists. **Merge** confirms, then posts, then reloads the queries. A conflict says "Merge refused: … — open a PR instead", with a **PR** button. **PR** opens or links the pull request. **Back to main** leaves the branch.
- Changing the context invalidates all admin queries.

- [ ] Tests (vitest + msw):
  - The header is sent only while a branch is open.
  - Create sets it.
  - The Merge conflict message offers PR.
  - The followed view shows read-only.
- [ ] Fail, implement, `npm run check` passes. Commit `feat(admin): the branch bar`.

### Task 6: Wiring and docs

**Files:**
- `cmd/jam/main.go`: with `config.github` set, build:
  - `configrepo.Repo{URL, Branch, Creds: kitFetcher.Creds, Path: <state-dir>/config-repo}`;
  - `GitHub{URL, Token: the cred's resolver}`;
  - `jam.ConfigBranches`;
  - `adminui.WithConfigBranches(b, factory)`, where the factory is the same `adminui.Handler(…)` call with the branch store.
  - Find the state-dir resolver used for relay state.
- Docs:
  - **Create** `docs/usage/jam/config-branches.md` (leaf): the branch context, saves as commits, Create/Open, Merge (per-file conflicts, planned first, applied at once), PR, the header, the workspace cache.
  - **INDEX row.**
  - **Links** from ui-config.md and config-git-sources.md.
  - The **spec Status** line.
- [ ] `go build ./... && go test ./...` for the touched packages; docs-audit is clean. Commit `feat(serve): the config branch editor; docs`.
