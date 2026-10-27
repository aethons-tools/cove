# Studio Manager, Plan 6: Delete the In-Process `local` Manager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the in-process Colima launcher from Jam now that the live test has passed. That means removing:
- the `local` manager;
- the `runtime.launcher` config block;
- the `at-jam colima` verbs.

After this, Studio Managers (`at-studio-manager`) are the only way Jam hosts studios.

**Architecture:**
- **at-jam** loses the launcher wiring:
  - `addLocalManager`, `localLauncher` and `launcher_key.go`;
  - the `colima` command;
  - the single-builder draft fallback.

  A config that still has `runtime.launcher` fails at load with a migration message.
- **`fleet` loses `Local`** and every special case for the name `local`:
  - it was sorted last;
  - it was always asked to purge state;
  - its Router entry name was reserved;
  - `Hub.Statuses` reported it as always connected.

  `jam.LocalManager` and `ValidManagerName`'s reservation of `local` stay. They still name legacy records, and stop an operator from registering a remote manager under the old name.
- **The cutover follows plan 1's rules,** which the spec requires (no state carry-over):
  - A `local` (or `""`) instance probes Dead because no manager by that name is registered. It is reaped: identity revoked and record dropped. Nothing is called on the host.
  - A standing pin to `local` is cleared and placed fresh, with a loud log line.

  The operator removes the old containers and volumes by hand. The docs give the commands.
- **Unchanged:** `internal/jam/launcher`, `internal/backend/colima`, `internal/jam/imagefacts` and `internal/colimacmd`. `at-studio-manager` uses them.

**Tech Stack:** Go; the existing tests and fakes.

**Spec:** `docs/superpowers/specs/2026-10-26-studio-manager-design.md`. This plan implements:
- Phasing step 6, the follow-up: delete `local`, `runtime.launcher` and the `at-jam colima` verbs after live troubleshooting. The live run passed on 2026-10-27.
- Non-goal: "Preserving existing standing sessions' state across the cutover".

**Merge:** the user asked that this PR **not be merged until we coordinate**. Push it and stop.

## Global Constraints

- **The `runtime.launcher` error.** When the YAML has `runtime.launcher` (or its deprecated host fields), loading fails with exactly this message:

  `runtime.launcher was removed: Jam no longer runs studios itself. Run at-studio-manager next to Colima (docs/usage/studio-manager.md) and set runtime.studio-managers: true. Studios the old launcher raised are dropped on startup and their standing sessions start fresh (docs/usage/studio-managers.md#retiring-the-in-process-launcher)`

  No silent ignore, and no deprecation period.
- **`runtime.studio-managers`** keeps its meaning:
  - `true` builds the router and the hub;
  - otherwise Jam uses the placeholder launcher (dev spine only), unchanged.
- **The name `local`:**
  - `jam.LocalManager` stays;
  - `ValidManagerName` still refuses `local`;
  - legacy `Manager == ""` still maps to `local`, which is now never registered.
- **Router order:** registration order only. There is no "local last".
- **`Router.PurgeState`** asks only the available managers that list the actor in `StateOwners`.
- **Tests are hermetic.** Use TDD, and give each deletion a test that pins the new behaviour.
- **Go commands** in this sandbox: prefix them with `GOPROXY=https://proxy.golang.org GOFLAGS=`.
- **Do not merge the PR.**

## Review Focus

1. **An old config with `runtime.launcher` meets the new binary.** It must fail at startup with the migration message, and must not run with the block ignored. Task 1 adds `TestServeConfigRejectsRuntimeLauncher`.
2. **A Jam with live `local` studios restarts on the new binary.** They are reaped without calling any manager, their identities are revoked, and nothing panics on the missing manager. Task 3 adds `TestCutoverReapsLocalInstances`.
3. **A standing session pinned to `local`.** Its next raise clears the pin, places it on a connected manager, and logs at Warn that its state is not carried over. Task 3 adds `TestCutoverResetsLocalPin`.
4. **`at-jam colima …` from muscle memory.** It is an unknown command, and the error points at `at-studio-manager colima`. Task 1 adds `TestColimaVerbMoved`.
5. **`Router.Add` with a manager named `local`.** It is no longer special in the router. The hub's `ValidManagerName` still refuses it at registration. Task 2 adds `TestRouterHasNoLocalSpecialCase`.

---

### Task 1: at-jam without the launcher

**Files:**
- Modify: `cmd/at-jam/config.go`:
  - delete `launcherConfig`, `validateLauncher` and the `harbor-host` fold for the launcher;
  - `Runtime.Launcher` becomes a `yaml.Node`-typed sentinel field (or equivalent) used only to detect the block and fail.
- Modify: `cmd/at-jam/main.go`:
  - delete `addLocalManager` and `localLauncher`;
  - `buildLauncher` builds the router only when `StudioManagers` is set;
  - drop the `colima` command from `run`'s command list;
  - drop the `OneDraftManager` fallback at the draft-builds wiring (the placeholder is not a draft builder).
- Delete: `cmd/at-jam/launcher_key.go`, `cmd/at-jam/launcher_key_test.go`.
- Modify: `cmd/at-jam/` tests that build a `launcherConfig` or call `addLocalManager`, including `serve_wiring_test.go`, `config_test.go` and `rename_test.go`. Those tests move to `runtime.studio-managers`, or are deleted where they only tested the launcher block.
- Modify: `dev/jam.dev.yml.example`: replace the `runtime.launcher` block with `runtime.studio-managers: true`, plus a comment pointing to `docs/usage/studio-manager.md`.
- Modify: `cmd/at-jam/main.go`'s unknown-command handling (or `internal/cli`) so that `at-jam colima` exits 2 with `at-jam: colima moved to at-studio-manager: run "at-studio-manager colima setup-docker|check-docker"`.

**Interfaces:**
- Produces: `buildLauncher(cfg, st, sshUserCA, log)` keeps its signature, and the `sshUserCA` parameter goes away if it is now unused. With `StudioManagers` set, the result is `{launcher: router, router: router, images: router, defaultKit…}`; otherwise it is the placeholder.

- [ ] **Step 1: Write the failing tests**
  - `TestServeConfigRejectsRuntimeLauncher`. The YAML has a minimal valid config plus `runtime: {launcher: {runtime-addr: x:443, jam-host: x}}`. Loading fails, and the error equals the Global Constraints message.
  - `TestColimaVerbMoved`. `run([]string{"colima"}, …)` returns 2, and stderr contains `colima moved to at-studio-manager`.
  - `TestBuildLauncherStudioManagersOnly`:
    - With `studio-managers: true`, the launcher is a `*fleet.Router` with no managers and the default kit is seeded.
    - With neither setting, it is the placeholder.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-jam/ -run 'RejectsRuntimeLauncher|ColimaVerbMoved|BuildLauncherStudioManagersOnly'`
Expected: FAIL

- [ ] **Step 3: Delete and rewire.** Run `go build ./...` and `go vet ./cmd/...`, and remove whatever becomes unused (imports, helpers such as `ensureKnownHostsDir` and `atCoveConfigDir`, if they are now dead).
- [ ] **Step 4: Run the package and the build**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go build ./... && GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/...`
Expected: PASS

- [ ] **Step 5: Commit**: `feat!: Jam no longer runs studios itself (runtime.launcher and at-jam colima removed)`

---

### Task 2: `fleet` without `Local`

**Files:**
- Delete: `internal/jam/fleet/local.go`, except `LocalName`, which moves into `router.go` as `const LocalName = jam.LocalManager` because the hub and tests use it.
- Modify: `internal/jam/fleet/router.go`:
  - `Add` drops the reserved-name check;
  - `ordered`/`Names` use registration order only;
  - `optional` loses the `Unwrap` branch, so `m` itself is asserted;
  - `PurgeState` loses the local branch;
  - `route` keeps `"" → LocalName`.
- Modify: `internal/jam/managers/hub.go`: `Statuses` drops the always-connected `local` entry. It still counts `""`/`local` instances under the name `local`, but lists only registered managers.
- Modify: the tests that use `fleet.Local(...)` (`grep -rn 'Local(' --include=*_test.go internal/`). Replace each with the package's plain fake manager registered under a non-reserved name (for example `m0`), and keep each test's intent.

**Interfaces:**
- Produces: `fleet.LocalName` is still exported. `fleet.Local` is gone.

- [ ] **Step 1: Write the failing tests**
  - `TestRouterHasNoLocalSpecialCase`:
    - Add `m1`, then a fake named `local`. `Names()` is `["m1","local"]`, not local last.
    - `Add` of a fake named `local` succeeds; the hub's `ValidManagerName` is what refuses it.
  - `TestPurgeStateAsksOnlyOwners`. An available manager that does not list the actor is not asked to purge.
  - `TestHubStatusesHasNoLocal`. With no remote managers registered, `Statuses()` is empty.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/fleet/ ./internal/jam/managers/ -run 'NoLocalSpecialCase|PurgeStateAsksOnlyOwners|StatusesHasNoLocal'`
Expected: FAIL

- [ ] **Step 3: Delete, simplify, and migrate the tests.**
- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/... ./cmd/...`
Expected: PASS

- [ ] **Step 5: Commit**: `refactor(fleet): drop the in-process local manager and its special cases`

---

### Task 3: Pin the cutover behaviour

**Files:**
- Test: `internal/jam/fleet/supervisor_integration_test.go`, with a real `jam.Supervisor` over a Router that has only `m1`
- Modify (only if a test fails): `internal/jam/supervisor.go`'s cutover paths

**Interfaces:** none new.

- [ ] **Step 1: Write the tests**
  - `TestCutoverReapsLocalInstances`:
    - Store two Live instances with leases this supervisor owns: `Manager: "local"` and `Manager: ""`.
    - Run at most two `Reconcile` passes.
    - Both are removed (or `PhaseGone`), their actors are revoked, and m1's fake saw no Teardown, Probe or Pause call for them.
  - `TestCutoverResetsLocalPin`:
    - Store a standing session pinned `local`, then raise it.
    - It lands on m1 and the pin becomes `m1`.
    - The log has the Warn line from `applyStandingPin` for a pin to a manager that is not configured (assert on its message text as it exists today).
- [ ] **Step 2: Run them**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/fleet/ -run Cutover`
Expected: PASS. Plan 1's reset and plan 3's reap already cover this, so these tests pin it. If either fails, fix the supervisor path that fails, and note it in the commit.

- [ ] **Step 3: Commit**: `test(jam): pin the cutover from the in-process launcher`

---

### Task 4: Docs

**Files** (use the docs-author skill; one owner per fact):
- `docs/usage/jam/studio-managers.md`. A new section, `## Retiring the in-process launcher`. The Task 1 error message links to this anchor. It covers:
  - what happens to old `local` studios and pins;
  - the operator's cleanup commands for the old containers and state on the Colima daemon: `docker --context colima ps -aq --filter name=^atcove-cove- | xargs -r docker --context colima rm -f`, and the matching `docker volume ls -q --filter name=^atcove-cove-` for volumes, which are removed only if their state is not wanted;
  - that old `local` image builds can be pruned.

  Drop every mention of `local` being a live manager.
- `docs/usage/jam/serve.md`: delete the `runtime.launcher` section and table row, and link to `studio-manager.md`.
- `docs/usage/jam/coves.md`: placement no longer mentions `local`, or "local last".
- `docs/usage/docker-in-sandbox.md`: the verbs are `at-studio-manager colima …` only.
- `docs/usage/jam/container.md`, `docs/usage/studio-manager.md` and `docs/usage/jam/kits-drafts.md`: remove the "transitional", "fallback" and "`local` too" wording.
- `docs/usage/jam/studio-manager-live-test.md`: rewrite the last item as "this run gated the removal of the in-process launcher (done in plan 6)". The runbook stays, for validating new managers.
- `docs/usage/jam/renamed-from-harbor.md`, `dispatch-runbook.md`, `requisitioner.md` and `studio-kit-migration.md`: fix each `runtime.launcher` mention, pointing to Studio Managers or removing it where it is history.
- `docs/OVERVIEW.md`: the architecture lists no in-process launcher.
- `dev/README.md`: the launcher block is gone.
- `AGENTS.md`'s quick orientation, if it mentions the launcher.

- [ ] **Step 1: Make the edits**, verifying each against the code after Tasks 1–3.
- [ ] **Step 2: Run the docs-audit checker** (nothing new outside `docs/superpowers/`). Also run `grep -rn 'runtime.launcher\|at-jam colima' docs --include=*.md | grep -v superpowers`, which should show only intentional history lines.
- [ ] **Step 3: Commit**: `docs: retire the in-process launcher`

---

## Coverage

| Spec item | Task |
|---|---|
| Delete `local` | 1, 2 |
| Delete `runtime.launcher` | 1 |
| Delete the `at-jam colima` verbs | 1 |
| No state carry-over at cutover; pins to an unconfigured manager reset; studios reaped, not adopted | 3 (pinned), 4 (operator cleanup) |
| Docs no longer describe the fallback | 4 |
