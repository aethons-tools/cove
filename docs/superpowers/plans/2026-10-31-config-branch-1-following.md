# Config branch editor, slice 1: following `config.github` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a Jam whose serve config sets `config.github` follows that repo's branch. It checks for a new commit about every minute and applies it on its own, once an operator has confirmed the first apply. `config track|untrack` are gone.

**Architecture:**
- **What gets followed.** The serve config names the repo, credential, branch and dir. `cmd/jam` turns it into a `gitsource.Source` (`ConfigEnv.Followed`) and adds its credential to the one kit fetcher.
- **What the stored record means now.** The store's `ConfigSource` record becomes "the last commit applied from the followed repo". A record that matches `Followed` means the first apply was confirmed.
- **The follower.** A new `ConfigFollower` (`internal/jam/config_follow.go`) runs `Tick` every `ConfigFollowEvery`:
  - It resolves the branch head.
  - It applies through the existing `ApplyConfigSource`, with `Force: true` because the repo wins.
  - It keeps an in-memory status and raises `config.follow:jam` on failure.
- **Admin and CLI.** The admin API gains `GET /admin/config/follow`. The Tracking page shows that status read-only, with **Confirm and apply** while awaiting confirmation.

**Tech Stack:** Go (internal/jam, cmd/jam, adminui), go-git through gitsource, React/Mantine (web/app admin).

**Spec:** `docs/superpowers/specs/2026-10-31-config-branch-editor-design.md` (phase 1).

## Global Constraints

- The serve-config key is `config.github` with `url` (required), `cred` (required), `branch` (optional) and `dir` (optional). No `config.git` key.
- `cred` must be a demanded credential. It may be any source except a gcp-exchange or pool credential.
- No credential value or URL userinfo in any error, log or API body (gitsource redaction rules).
- An unconfirmed followed source is never applied by the follower.
- Lock order stays configMu → kitMu → store mu. The follower takes configMu only through `ApplyConfigSource`.
- Docs are updated in the same change.

## Review Focus

1. **The repo is unreachable at serve start.** Serve still starts. The follower reports `failed` with the redacted error and retries (Task 3 test `fetch error is reported and retried`).
2. **The serve config moves to a different repo or branch** while a record of the old one exists. The follower treats it as unconfirmed, and the Tracking page offers Confirm (Task 3 test `another source is unconfirmed`).
3. **A commit fails to apply.** The follower retries it on the next tick even though the head hasn't moved (Task 3 test `failed apply is retried at the same head`).
4. **No `config.github`.** `config sync` and the follow route answer 409 "no config repo is configured", and the follower doesn't run (Tasks 2 and 4).
5. **A digest-bound confirm races a new commit.** It is refused as a stale plan, as today (existing `ErrConfigPlanChanged` path; Task 4 test).

---

### Task 1: `config.github` in the serve config

**Files:**
- Modify: `cmd/jam/config.go`: `serveConfig.Config *configRepoConfig \`yaml:"config"\``, with `configRepoConfig{GitHub *configGitHub \`yaml:"github"\`}` and `configGitHub{URL, Cred, Branch, Dir string}`. Wire `validateConfigRepo()` into the serve-config validation that already calls `validateKits`.
- Modify: `cmd/jam/main.go`: `resolveKitFetcher` also appends the config repo's cred (`Prefix: URL`, the same Resolve pattern). A new `followedSource(cfg) gitsource.Source` sets `cfgEnv.Followed`.
- Test: `cmd/jam/config_test.go`

**Interfaces:**
- Produces: `func (c serveConfig) followedSource() gitsource.Source`. It returns zero without `config.github`, else `{Repo: URL, Dir: Dir, Ref: Branch or "main"}`.

- [ ] Tests:
  - A valid block yields that source.
  - `branch` defaults to `main`.
  - These are refused, each error naming the key and never echoing the URL:
    - a missing url;
    - a non-https url (loopback http allowed, as gitsource);
    - userinfo in the url;
    - a missing cred;
    - an undemanded cred;
    - a pool cred;
    - a dir that isn't clean and relative.
  - `resolveKitFetcher` adds a Cred for the URL.
- [ ] Run, fail; implement. Validate with `gitsource.Parse(url + "//" + dir + "@" + branch)`, which owns the URL rules. Run, pass. Commit `feat(serve): config.github names the followed config repo`.

### Task 2: The followed source replaces the tracked one

**Files:**
- Modify: `internal/jam/config_plan.go`: `ConfigEnv.Followed gitsource.Source`.
- Modify: `internal/jam/config_source.go`:
  - `applyConfigSourceLocked` uses `env.Followed` when `src.Repo == ""`, else `ErrConfigNotTracked`, reworded: `"no config repo is configured (config.github in the serve config)"`.
  - `trackedRecord(store, env, opt, changes)` records the run when `opt.Source == env.Followed`. A missing record or one for another source is replaced by `{Followed…, Commit, AppliedAt}`.
  - Delete `TrackConfigSource` and `UntrackConfigSource`.
- Test: `internal/jam/config_source_test.go` (rewrite the track/untrack cases)

**Interfaces:**
- Produces: `func ConfigFollowConfirmed(store Store, env ConfigEnv) bool`, true when the stored record's `Source()` equals `env.Followed`.

- [ ] Tests:
  - An apply of the followed source with no record writes the record with its commit.
  - A record for another repo is replaced.
  - An apply of a different source (a `--git` diff of some other repo) doesn't touch the record.
  - A source-less apply with no `Followed` is `ErrConfigNotTracked`.
  - `ConfigFollowConfirmed` before and after.
- [ ] Run, fail; implement; `go test ./internal/jam/` passes (fix callers). Commit `feat(jam): the followed config source replaces track/untrack`.

### Task 3: The follower

**Files:**
- Create: `internal/jam/config_follow.go`, `internal/jam/config_follow_test.go`

**Interfaces:**
- Produces:
  ```go
  const ConfigFollowKey = "config.follow:jam"
  const ConfigFollowEvery = time.Minute
  type ConfigFollowStatus struct {
      State     string    `json:"state"` // awaiting-confirm | in-sync | failed | off
      Source    string    `json:"source,omitempty"`  // configSourceLabel(Followed)
      Head      string    `json:"head,omitempty"`    // the branch head last seen
      Applied   string    `json:"applied,omitempty"` // the last commit applied from it
      Error     string    `json:"error,omitempty"`
      CheckedAt time.Time `json:"checkedAt,omitzero"`
  }
  type ConfigFollower struct { Store Store; Fetcher KitFetcher; Env ConfigEnv; Log *slog.Logger; Now func() time.Time /* … private state */ }
  func (f *ConfigFollower) Tick(ctx context.Context)
  func (f *ConfigFollower) Status() ConfigFollowStatus
  func (f *ConfigFollower) Run(ctx context.Context, every time.Duration)
  ```
- Tick:
  - `Followed` zero → `off`.
  - `Resolve` the branch; an error → `failed` with the error and the condition raised (Warn).
  - Not confirmed → `awaiting-confirm` and the condition raised (Info, "confirm the first apply on the Tracking page").
  - Confirmed, the record's commit equals the head, and the last tick didn't fail → `in-sync`, condition cleared.
  - Otherwise `ApplyConfigSource(ctx, Store, Fetcher, Followed, Env, {Force: true, Now}, Log, "config-follow")`. An error → `failed` with `ConfigStatus(err)`'s message and the condition (Warn); else `in-sync`, cleared.
  - Status guarded by a mutex.

- [ ] Tests (fake KitFetcher over an in-memory tree, as `config_source_test.go` does):
  - `off` without a source.
  - `awaiting-confirm` doesn't apply.
  - After a confirmed apply, a new head applies; the same head is a no-op (the fake counts Fetch calls).
  - **fetch error is reported and retried**: the next Tick resolves again.
  - **failed apply is retried at the same head**: a refused tree, then fixed in the fake at the same commit, applies.
  - **another source is unconfirmed.**
  - The condition is raised and cleared (condition.Tracker).
- [ ] Run, fail; implement; pass. Commit `feat(jam): follow the config repo's branch`.

### Task 4: Admin API and CLI

**Files:**
- Modify: `internal/jam/config_admin.go`:
  - Delete the `PUT|DELETE /admin/config/source` routes.
  - Add `GET /admin/config/follow` → `ConfigFollowStatus` (`WithConfig` gains a `follow func() ConfigFollowStatus` parameter; nil → `{State: "off"}`).
  - `POST /admin/config/sync` (now "confirm / apply now") unchanged otherwise. After a successful sync it calls `follow tick` (a second func) so the status refreshes.
  - Update the route comment block.
- Modify: `cmd/jam/configgit.go`:
  - Remove `track` and `untrack`.
  - `config sync` keeps working.
  - Add `config status`, which prints the follow status.
  - Update the usage text.
- Modify: `internal/jam/adminclient` (whatever client call `track`/`untrack` used): remove those and add `ConfigFollow()`.
- Tests: `internal/jam/config_admin_test.go`, `cmd/jam/configgit_test.go`

- [ ] Tests:
  - `PUT /admin/config/source` → 404 or 405.
  - `GET /admin/config/follow` returns the status JSON.
  - A sync with a stale digest → 409.
  - A sync with no Followed → 409 with the new text.
  - The CLI `config track` → unknown-subcommand usage error; `config status` prints state and commits.
- [ ] Run, fail; implement; pass. Commit `feat(config): follow status route; track/untrack removed`.

### Task 5: Wire the follower into serve

**Files:**
- Modify: `cmd/jam/main.go`:
  - Build `follower := &jam.ConfigFollower{…}` after `cfgEnv`.
  - With `Followed` set, `go follower.Run(ctx, jam.ConfigFollowEvery)`.
  - Pass `follower.Status`/`Tick` to `WithConfig` and to adminui (Task 6).
- Test: the cmd/jam serve wiring test, if one covers `cfgEnv`. Otherwise this is covered by Tasks 3 and 4, plus a `go build`. Ledger the ruling if there's no test.

- [ ] Implement; `go build ./... && go test ./cmd/jam/` passes. Commit `feat(serve): run the config follower`.

### Task 6: The Tracking page

**Files:**
- Modify: `internal/jam/adminui/api_tracking.go`, `config_page.go`:
  - Delete the `tracking/source` POST and the untrack route.
  - The tracking JSON gains `follow: ConfigFollowStatus`.
  - The source card reads `Followed` (label) instead of the stored record.
  - Its Apply/"Sync" path is labelled Confirm when `follow.state == "awaiting-confirm"`.
- Modify: `web/app/src/admin/tracking/TrackingPage.tsx`, `tracking.test.tsx`, and `src/admin/api/types.ts` (regenerated).
  - The source card shows the source, state, head, applied commit, the error, and when it was checked.
  - No Track or Untrack.
  - **Confirm and apply** (or "Apply now") runs the existing digest-bound apply.
  - Without a source: "No config repo is configured: set `config.github` in the serve config."
- [ ] Tests (Go adminui + vitest):
  - Each state renders.
  - No Track/Untrack controls.
  - Confirm posts the digest-bound apply.
- [ ] Run, fail; implement; `go generate ./internal/jam/userui/`; `npm run check` in web/app; `go test ./internal/jam/adminui/`. Commit `feat(admin): the Tracking page follows config.github`.

### Task 7: Docs

- Modify:
  - `docs/usage/jam/serve.md`: the `config.github` block and its keys.
  - `docs/usage/jam/config-git-sources.md`: following replaces track/untrack (the state machine, the first-apply confirm, `config status`, the `config.follow` condition, `/admin/config/follow`). `.source.yml` kits are unchanged.
  - `docs/usage/jam/ui-config.md`: the source card.
  - `docs/usage/jam/credentials.md`: the demand table row for `config.github.cred`.
  - `docs/usage/jam/INDEX.md` rows, if read_when changes.
- [ ] docs-audit is clean for the touched docs. Commit `docs: following config.github`.
