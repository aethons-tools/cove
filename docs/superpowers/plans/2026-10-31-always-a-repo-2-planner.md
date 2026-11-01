# Always a repo, slice 2: the planner without drift — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The config planner applies the repo over anything Jam holds. Drift detection, force and release are gone, and Jam's own startup writes no longer change an object the repo manages.

**Architecture:**
- **The planner (`internal/jam/config_plan*.go`):** `classify` loses its drift and unmanaged branches, and `ConfigApplyOptions.Force` goes.
- **The drift checker and release go:** the periodic loop keeps its other two checks.
- **Startup's legacy connection binding only reads:** it never creates or rebinds.

**Tech Stack:** Go; web (React + Mantine, Vitest) for the Tracking page bits.

**Spec:** `docs/superpowers/specs/2026-10-31-always-a-repo-design.md`, §"Drift, force and release go" and §"Objects Jam creates itself".

## Global Constraints

- **Marks stay.** An apply still deletes only marked objects that the repo no longer holds. Marks are still written on create, update and adopt.
- **The unmanaged-objects list stays.** `ConfigPlan.Unmanaged` lists live objects the repo neither holds nor manages: Jam's own objects. These are shown, never deleted.
- **These names are removed everywhere:**
  - `ConfigApplyOptions.Force`, `ConfigPlanInput.Force`;
  - `ConfigOp.Drift`, `ConfigOp.Unmanaged`, `ConfigPlan.HasDrift`;
  - `seededConfigObjects`, `planner.adoptable`, `refuseDrift`;
  - `ConfigMarkStates`, `ConfigMarkState`, `ConfigInSync`/`ConfigDrifted`/`ConfigMissing`;
  - `CheckConfigDrift`, `checkConfigDriftLocked`, `ConfigDriftKey`, `configDriftFix`, `ConfigDriftEvery`;
  - `ReleaseConfigMark`, `config_release.go`;
  - `KitDraftPublish.Drift`, `DraftDriftWarning`;
  - the `force` query param on `/admin/config/{diff,apply,sync}`;
  - `--force` on `jam config apply` and `jam config sync`. `config write --force` is a different flag and stays.
- **The periodic loop is renamed** `RunConfigChecks(ctx, store, t, every, log)`, with `ConfigChecksEvery = time.Minute`. It keeps calling `kitMigrateConditions` and `ReservedDestinationEnvConditions`. Update the two comments that name `RunConfigDriftCheck` (`kit_migrate.go:151`, `destination_env_reserved.go:45`).
- **Legacy connection binding.** The deprecated `runtime.discord.bot-token-cred` and `runtime.requisitioner.tracker-token-cred` stay accepted, read-only.
  - They bind the existing connection of their kind when its cred equals the setting.
  - With no such connection, the error is `<field>: no <kind> connection (the deprecated *-token-cred no longer creates one; add connections/<kind>.yml to the config repo, or set <field>.connection)`.
  - With a different cred, the error is `<field>: the <kind> connection's credential is %q, not %q (set it in the config repo, or set <field>.connection)`.
  - Ruling (my call, changing the spec's "dropped"): an existing deployment keeps starting. Only the live create and rebind go.
- **Error texts naming removed CLI verbs** (`jam connection add`, `jam connection cred`) in `cmd/jam/connections.go` say `add it to the config repo (connections/<name>.yml)` instead.

## Review Focus

- A repo object identical to an unmanaged live one must still be an **adopt**: a mark is written and no data change. Test in Task 1.
- An unmanaged live **kit** that the repo also holds, with different content, must get a new kit version and a mark. It is not refused. Test in Task 1.
- A follower tick right after startup, where Jam created the requisitioner project and the repo also has `projects/<it>/project.yml`, must apply cleanly. Covered by the update path in Task 1's tests.
- `EnsureDefaultStudioKit` must keep the repo's version of a repo-managed default kit across a serve restart, with no new version pushed. Test in Task 3.
- The Tracking page must not show a Force checkbox or a Release button anywhere. Test in Task 2's web tests.

---

### Task 1: The planner applies over unmanaged objects; no drift, no force

**Files:**
- Modify: `internal/jam/config_plan.go`: `classify` (:185-213); delete `seededConfigObjects` (:124-127), `adoptable` (:137-139 and :156), `refuseDrift` (:215-218), `HasDrift` (:89-94), `ConfigOp.Drift`/`Unmanaged` (:40-43), `ConfigPlanInput.Force` (:120); the kit adoptable line (:472-474); the digest struct (:287-290) without the `Unmanaged` op flag.
- Modify: `internal/jam/config_plan_delete.go:36-46`: delete without a drift check.
- Modify: `internal/jam/config_apply.go`: `Force` (:31, :89, :164 log field); the drift re-check (:76-77).
- Modify: every caller that sets `Force` (`config_follow.go:123`, `config_branches.go:133`/`:350`, `config_admin.go:183-196`, `adminui/api_tracking.go`, `adminui/config_page.go:163-171`, `cmd/jam/configgit.go` flags and `printConfigPlan` :258-290, `internal/jam/adminclient` `configPath` force param :953-993).
- Test: `internal/jam/config_plan_test.go` (and the other `config_*_test.go` files that assert drift or refusal). Update or delete those assertions; grep `Drift\|Force\|not managed\|Unmanaged:` in `internal/jam/*_test.go`, `internal/jam/adminclient/*_test.go`, `cmd/jam/*_test.go`.

**Interfaces:**
- Produces: `classify(key, repo) (action string, ok bool)`, where `action` is one of `ConfigCreate`, `ConfigAdopt`, `ConfigUnchanged`, `ConfigUpdate`. The table:
  - not live → create;
  - unmanaged and equal → adopt;
  - unmanaged and different → update;
  - managed and equal → unchanged;
  - managed and different → update.
- `ok` stays for kit-specific refusals made by callers. If `classify` itself never refuses, drop `ok`, and the callers' `if !ok` branches with it.

- [ ] **Step 1: Write the failing tests** in `config_plan_test.go`:
  - `TestPlanUpdatesUnmanagedObject`: live user `bob` with no mark and `display: Bob`; the repo has `users/bob.yml` with `display: Robert`. The plan has one op `{Kind: user, Name: bob, Action: update}`, no refusals, and the apply writes the mark and `display: Robert`.
  - `TestPlanAdoptsEqualUnmanaged`: equal content → `adopt`, and a mark is written.
  - `TestPlanUpdatesUnmanagedKit`: an unmanaged live kit `k` at v1, and a repo `kits/k/kit.yml` with a different prompt. The op is `update`, v2 exists after the apply, and `k` is marked.
  - `TestPlanOverwritesEditedManaged`: a managed object edited in the store after its mark (the old "drift") updates with no refusal, and the plan carries no drift field (compile-time).
  - `TestPlanDeletesEditedManaged`: a managed object edited, then removed from the repo, is deleted with no refusal.

- [ ] **Step 2: Run them and see them fail.**
  Run: `go test ./internal/jam -run 'TestPlan(UpdatesUnmanaged|AdoptsEqual|OverwritesEdited|DeletesEdited)' -v`
  Expected: FAIL. The update is refused as "exists and is not managed", or the drift refusal fires.

- [ ] **Step 3: Implement it.** Remove the names listed in Global Constraints, as the Files block says. Fix every compile error across `./...` (`go build ./... && go vet ./internal/jam/... ./cmd/jam/...`).

- [ ] **Step 4: Run them and see them pass, with the suite.**
  Run: `go test ./internal/jam/... ./cmd/jam/... 2>&1 | tail -40`
  Expected: `ok` everywhere. Tests that asserted drift, force or unmanaged refusals are updated to the new table or deleted.

- [ ] **Step 5: Commit**
  `git commit -am "config: apply the repo over unmanaged and edited objects; drop drift refusals and force"`

### Task 2: The drift checker, release and the drift UI go

**Files:**
- Modify: `internal/jam/config_drift.go`: keep only `RunConfigChecks`/`ConfigChecksEvery`. Rename the file to `config_checks.go` with `git mv`.
- Delete: `internal/jam/config_release.go`, and its tests (`grep -l ReleaseConfigMark internal/jam/*_test.go`).
- Modify:
  - `internal/jam/config_admin.go:227` (the `DELETE /admin/config/managed/...` route);
  - `internal/jam/adminclient` (`ConfigRelease` :1012);
  - `cmd/jam/configgit.go` (`release` :103-118, its usage text, and `cmd/jam/main.go:141`'s `config` brief, which should list `write|diff|apply|sync|status`. `apply` goes in slice 3);
  - `cmd/jam/main.go:2175`.
- Modify:
  - `internal/jam/adminui/api_tracking.go`: drop `DELETE tracking/managed/...` (:128), `HasDrift`, `Forced`, and the row `Drift`/`Unmanaged`;
  - `adminui/config_page.go`: drop `configMarkStates` (:95-97), and any per-mark state the Tracking view shows;
  - `adminui/apitypes.go:187`;
  - `adminui/branches.go:108`: drop the `tracking/managed/` clause.
- Modify: `internal/jam/kit_draft_publish.go:26-34`, `:79-96` (no `Drift`); `kit_draft.go:52-54`; `kit_draft_admin.go:83`; `adminui/kit_draft_publish.go:12,28-29` (`draftDriftConfirm`); `adminui/api_kit_draft.go:61,327-333,344`; `adminui/kit_draft_view.go:52-54,163`.
- Modify (web):
  - `web/app/src/admin/tracking/TrackingPage.tsx`: Release (:77-78) and the "differ from the repo" badge (:69);
  - `tracking/PlanView.tsx`: the Force checkbox (:14, :23-24);
  - `kits/DraftPublish.tsx:15-37`: the drift checkbox;
  - `admin/api/types.ts`: the matching fields.
- Test: the Go tests touched above; `web/app/src/admin/tracking/tracking.test.tsx`; `kits/publish.test.tsx`.

**Interfaces:**
- Produces: `func RunConfigChecks(ctx context.Context, store Store, t *condition.Tracker, every time.Duration, log *slog.Logger)`. `log` may become unused; drop the parameter if so, and update the caller.

- [ ] **Step 1: Write the failing tests.**
  - Go, `adminui/api_tracking_test.go`, `TestTrackingHasNoRelease`: `DELETE /app/admin/api/tracking/managed/user/bob` → 404 or 405 (the mux has no route).
  - Go, `config_admin_test.go`: `DELETE /admin/config/managed/user/bob` → 404 or 405.
  - Go, `TestDraftPublishOfManagedKitPublishes`: in a store where kit `k` is marked, publish with no `drift` confirm in the body succeeds.
  - Web, `tracking.test.tsx`: a plan fixture once flagged drift renders no "Force" text and no "Release" button. `publish.test.tsx`: a managed kit's publish button is enabled with no checkbox.

- [ ] **Step 2: Run them and see them fail.**
  Run: `go test ./internal/jam/... -run 'TrackingHasNoRelease|DraftPublishOfManagedKit|ConfigManagedRoute' -v; cd web/app && npx vitest run src/admin/tracking src/admin/kits/publish`
  Expected: FAIL. The routes exist, and the checkbox is required.

- [ ] **Step 3: Implement it:** the removals, as the Files block says.

- [ ] **Step 4: Run the suites.**
  Run: `go build ./... && go test ./internal/jam/... ./cmd/jam/... 2>&1 | tail -30; cd web/app && npx tsc --noEmit && npx vitest run 2>&1 | tail -15`
  Expected: all pass.

- [ ] **Step 5: Commit**
  `git commit -am "config: remove drift checks, release and the drift UI"`

### Task 3: Jam's own startup writes leave repo-managed objects alone

**Files:**
- Modify: `internal/jam/studiokit.go:119-122`. `EnsureDefaultStudioKit` returns the current ref, without pushing, when the default kit is config-managed (`KitManaged(store, studio.DefaultStudioKitID)`, kit_draft.go:143) and has a current version.
- Modify: `cmd/jam/connections.go:18-60`. In `resolveServeConnection`'s legacy branch, no `CreateConnection` and no `SetConnectionCred`. Use the errors in Global Constraints, and the repo wording for the two messages naming `jam connection add` and `jam connection cred`.
- Test: `internal/jam/studiokit_test.go`; `cmd/jam/connections_test.go` (create it if absent; grep for existing `resolveServeConnection` tests first).

**Interfaces:**
- Consumes: `KitManaged(store Store, name string) bool`.

- [ ] **Step 1: Write the failing tests.**
  - `TestEnsureDefaultStudioKitKeepsManaged`: push a different default kit at v1 and write its mark (`store.ApplyConfig(ConfigWrite{...})`, the way existing tests seed marks; grep `PutMarks\|Marks:` in `internal/jam/*_test.go`). Then `EnsureDefaultStudioKit` returns `{ID: default, Version: 1}`, and no v2 exists.
  - `TestEnsureDefaultStudioKitSeedsUnmanaged`: the existing behavior on an empty store, v1 created; keep it, or note the existing test that covers it.
  - `TestLegacyTokenCredBindsExisting`: a connection `discord` with cred `bot` exists, and `resolveServeConnection(st, "runtime.discord", "discord", "", "bot", demanded)` returns it.
  - `TestLegacyTokenCredNoConnection`: an empty store gives exactly the no-connection error; no connection was created (`st.ConnectionOfKind` is false).
  - `TestLegacyTokenCredOtherCred`: an existing cred `old` gives exactly the different-credential error; the cred is unchanged.

- [ ] **Step 2: Run them and see them fail.**
  Run: `go test ./internal/jam -run EnsureDefaultStudioKit -v; go test ./cmd/jam -run LegacyTokenCred -v`
  Expected: FAIL.

- [ ] **Step 3: Implement it** as the Files block says.

- [ ] **Step 4: Run them and see them pass, with the suite.**
  Run: `go test ./internal/jam/... ./cmd/jam/... 2>&1 | tail -30`
  Expected: `ok`.

- [ ] **Step 5: Commit**
  `git commit -am "jam serve: startup seeding never edits repo-managed objects; legacy token creds bind read-only"`
