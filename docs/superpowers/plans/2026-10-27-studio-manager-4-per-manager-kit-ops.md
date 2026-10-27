# Studio Manager, Plan 4: Per-Manager Kit Operations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every kit-, draft- and image-scoped operation name the manager it runs on, and give a remote manager its own container and volume names. With both done, `local` and remote managers can be compared side by side, and `local` can later be cut over.

**Architecture:**
- **The router gains per-manager forms:**
  - `BuildKitDraftOn`, `HasKitDraftImageOn` and `ImageFactsOn`;
  - `DraftManagers`, the draft-capable managers in placement order;
  - `AvailableNames`.

  The supervisor and the admin layer call these through two new `jam` interfaces, so `internal/jam` still never imports `fleet`.
- **Draft builds are keyed by (kit, manager).** A draft studio is placed on the manager that holds its draft image.
- **Image facts follow the image:** a studio's facts come from its own manager, and a role's from an optional `manager`.
- **Staleness compares a studio's tag with its own manager's current tag.** A new per-role images view has one row per connected manager.
- **A standing upgrade prepares on the session's pinned manager.**
- **A remote manager's containers and volumes carry its name.** That way a standing session that moves from `local` to a remote manager on a shared daemon starts fresh, as the spec requires.

**Tech Stack:** Go; the existing `jam.Store` (no migration: draft builds stay in memory, as today); the admin API, `adminclient`, at-jam CLI and adminui.

**Spec:** `docs/superpowers/specs/2026-10-26-studio-manager-design.md`. This plan implements:
- "Operations not tied to a running studio";
- "Constraints for the protocol": per-manager tags and staleness against the studio's own manager;
- "Placement → Where it applies": draft studios go to the manager holding the draft image;
- "Non-goals": no state carry-over at cutover. This was found in plan 3's final review.

Plans 1 to 3 (`docs/superpowers/plans/2026-10-26-studio-manager-{1,2,3}-*.md`) built the router, the protocol and the manager binary.

**Later (not this plan):**
- Plan 5: the Jam container image and compose file.
- Then the live test, and deleting `local`.

## Global Constraints

- **The name `local`:**
  - It lives in `jam` as `const LocalManager = "local"`, and `fleet.LocalName = jam.LocalManager`.
  - An instance or pin whose `Manager` is `""` means `local` everywhere a manager is resolved.
- **The default manager:** an omitted manager (`""`) on an admin or API call means the first available manager in router order. For drafts, that is the first draft-capable one. This is today's behaviour.
- **A named manager that is not registered** fails with an error wrapping `jam.ErrUnknownManager`, which the admin API returns as HTTP 404. One that is not connected fails wrapping `jam.ErrManagerUnavailable`, which is HTTP 503. Both name the manager.
- **Container names:**
  - With no manager name (`local`): `atcove-cove-<id>` (unchanged).
  - With one: `atcove-<manager>-cove-<id>`.
  - Volumes derive from the container name as today: `-agent-data`, `-workspace` and `-docker`.
- **Draft build records stay in memory** and keep only the latest build per (kit, manager). There is no migration.
- **A draft studio never runs on a manager other than the one whose build it uses.**
- **A pinned standing session's prepare runs only on its pinned manager**, never elsewhere. That matches plan 1's rule that a pinned session never moves.
- **Tests are hermetic:** fakes, `MemStore` and `httptest`. Use TDD.
- **Go commands** in this sandbox: prefix them with `GOPROXY=https://proxy.golang.org GOFLAGS=`.

## Review Focus

1. **The manager that built a draft has disconnected when its draft studio is raised.** The raise builds on the next draft-capable candidate rather than waiting or failing. Task 3 adds `TestEnsureSkipsUnavailableBuilder`.
2. **A pinned standing session's manager is disconnected during an upgrade.** The prepare fails with "manager unavailable", the upgrade stays pending and retries, and nothing is built elsewhere. Task 6 adds `TestUpgradePreparesOnlyOnPin`.
3. **A studio raised before plan 1** (with `Manager == ""`) is judged stale against `local`'s current tag, not the first available manager's. Task 4 adds `TestImageStatusLegacyInstanceUsesLocal`.
4. **A kit has builds on two managers, and `GET …/draft/build` names no manager.** It returns the most recently started build, and `manager` in the body says which one it is. Task 3 adds `TestDraftBuildStatusLatestAcrossManagers`.
5. **A role setup request names a manager that does not exist.** It returns 404 with `unknown manager: <name>`, not a 500 and not a silent fallback to the default. Task 5 adds `TestRoleSetupUnknownManager404`.

---

### Task 1: Manager-scoped container and volume names

**Files:**
- Modify: `internal/naming/naming.go` (`CoveContainerOn`)
- Modify: `internal/jam/launcher/launcher.go` (the `l.container(actorID)` helper used by Raise and PurgeState)
- Test: `internal/naming/naming_test.go`, `internal/jam/launcher/launcher_test.go`

**Interfaces:**
- Produces:
  - `func naming.CoveContainerOn(manager, actorID string) string`. It returns `CoveContainer(actorID)` when `manager == ""`, and otherwise `atcove-<manager>-cove-<sanitized id>`, using the same sanitizing as `CoveContainer`.
  - `func (l *Launcher) container(actorID string) string`, which is `naming.CoveContainerOn(l.cfg.Manager, actorID)`.
- Every `naming.CoveContainer(spec.ActorID)` or `naming.CoveContainer(actorID)` in `launcher.go` becomes `l.container(...)`.

- [ ] **Step 1: Write the failing tests**
  - `TestCoveContainerOn`:
    - `("", "w1")` gives `atcove-cove-w1`;
    - `("m1", "w1")` gives `atcove-m1-cove-w1`;
    - `("m1", "a/b")` gives `atcove-m1-cove-a-b`.
  - `TestRemoteManagerNamesItsStudio`. A launcher with `Manager: "m1"` runs a standing raise of `w1`:
    - the fake records container `atcove-m1-cove-w1`;
    - the volumes created are `atcove-m1-cove-w1-agent-data` and `atcove-m1-cove-w1-workspace`;
    - `PurgeState("w1")` removes `atcove-m1-cove-w1-agent-data`, `-workspace` and `-docker`.
  - `TestLocalNamesUnchanged`. The same raise with no manager name uses `atcove-cove-w1` and its volumes.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/naming/ ./internal/jam/launcher/ -run 'CoveContainerOn|NamesItsStudio|NamesUnchanged'`
Expected: FAIL (undefined: CoveContainerOn)

- [ ] **Step 3: Implement** `CoveContainerOn` and `l.container`, and replace the call sites. Update the `naming` package comment to give both forms.
- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/naming/ ./internal/jam/launcher/ ./internal/studiomanager/`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(launcher): a remote manager's studios and state carry its name`

---

### Task 2: Per-manager router operations

**Files:**
- Modify: `internal/jam/managers.go` (`LocalManager`), `internal/jam/fleet/local.go` (`LocalName = jam.LocalManager`)
- Create: `internal/jam/managed_ops.go` (the two interfaces)
- Modify: `internal/jam/fleet/router.go` (the new methods; `BuildKitDraft`, `HasKitDraftImage` and `ImageFacts` delegate to the `On` forms with `""`)
- Test: `internal/jam/fleet/router_test.go`

**Interfaces:**
- Produces:

```go
// internal/jam/managed_ops.go
// ManagedDraftBuilder builds kit drafts on named managers ((*fleet.Router)).
type ManagedDraftBuilder interface {
	DraftManagers() []string // available, draft-capable, in router order
	BuildKitDraftOn(ctx context.Context, manager string, def KitDefinition, tag string, log io.Writer) (string, error)
	HasKitDraftImageOn(manager, imageID string) (bool, error)
}
// ManagedImageReader reads image facts on a named manager; "" = the first available reader.
type ManagedImageReader interface {
	ImageFactsOn(ctx context.Context, manager, image string) (ImageFacts, error)
}
// OneDraftManager presents a single KitDraftBuilder as a ManagedDraftBuilder
// whose only manager is name (for a Jam without a router, and for tests).
func OneDraftManager(name string, b KitDraftBuilder) ManagedDraftBuilder
```

- And on `*fleet.Router`:
  - `func (r *Router) AvailableNames() []string`;
  - `func (r *Router) DraftManagers() []string`;
  - `BuildKitDraftOn`, `HasKitDraftImageOn` and `ImageFactsOn`, matching the interfaces.

  The `On` forms follow the default-manager and unknown/unavailable rules in Global Constraints. A named manager that lacks the capability fails with `manager <name> cannot build kit drafts` or `manager <name> cannot read image facts`, wrapping `ErrManagerUnavailable`.
- Compile-time checks: `var _ jam.ManagedDraftBuilder = (*Router)(nil)` and `var _ jam.ManagedImageReader = (*Router)(nil)`.

- [ ] **Step 1: Write the failing tests** (router_test):
  - `TestDraftManagersOrderAndCapability`. With `local` able to draft, m1 unable, and m2 able but unavailable, the result is `["local"]`.
  - `TestBuildKitDraftOnNamed`. `BuildKitDraftOn(ctx, "m2", …)` reaches m2's fake, not local's.
  - `TestOnFormsErrors`:
    - an unknown name gives `errors.Is(err, jam.ErrUnknownManager)`, with the name in the text;
    - an unavailable one gives `ErrManagerUnavailable`;
    - an incapable one gives the capability text, wrapping `ErrManagerUnavailable`.
  - `TestImageFactsOnDefaultIsFirstAvailable`. `""` reads from the first available reader, and existing `ImageFacts` behaviour is unchanged.
  - `TestOneDraftManager`. `DraftManagers()` is `["x"]`, and `BuildKitDraftOn(ctx, "x", …)` forwards. Any other name gives `ErrUnknownManager`.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/fleet/ ./internal/jam/ -run 'DraftManagers|BuildKitDraftOn|OnFormsErrors|ImageFactsOn|OneDraftManager'`
Expected: FAIL

- [ ] **Step 3: Implement** the items listed under Produces.
- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/...`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(fleet): per-manager draft, image-facts and availability operations`

---

### Task 3: Draft builds keyed by (kit, manager); draft studios placed on their builder

**Files:**
- Modify: `internal/jam/kit_draft_build.go`
- Modify: `internal/jam/kit_draft_build_admin.go`
- Modify: `internal/jam/supervisor.go` (`raiseDraft` and its caller, which sets the placement)
- Modify: `internal/jam/adminclient/kitdraft.go`, `cmd/at-jam/kitdraft.go` (`--manager`)
- Modify: `cmd/at-jam/main.go` (wire `NewKitDraftBuilds` with the router, or with `OneDraftManager("local", …)` when the launcher is not a `ManagedDraftBuilder`)
- Test: `internal/jam/kit_draft_build_test.go`, `internal/jam/kit_draft_build_admin_test.go`, `internal/jam/supervisor_draft_test.go` (or the existing draft raise test file), `cmd/at-jam/kitdraft_build_test.go`

**Interfaces:**
- Consumes: Task 2's `ManagedDraftBuilder` and `OneDraftManager`.
- Produces:
  - `NewKitDraftBuilds(ctx, store, builder ManagedDraftBuilder, log)`.
  - `Start(name, ifMatch, by, manager string) (KitDraftBuild, error)`:
    - `""` means `DraftManagers()[0]`;
    - with none it fails with `no manager can build kit drafts`, wrapping `ErrManagerUnavailable`;
    - a named manager not in `DraftManagers()` is handled per Global Constraints.
  - `Status(name, manager string, since int64)`, where `""` means the most recently started build of that kit on any manager.
  - `Cancel(ctx, name, manager string)`, where `""` means the most recently started running build of that kit.
  - `Ensure(ctx, def, ref, by string, managers []string) (KitDraftImage, string, KitObjects, error)`, where the `string` is the manager.
  - `KitDraftBuild.Manager string` with JSON `"manager"`.
  - One running build per (kit, manager): a second Start on the same pair is 409, as today. A Start for another manager runs alongside.
  - Admin API:
    - POST's JSON body accepts `{"manager": "<name>"}`;
    - GET and DELETE accept `?manager=<name>`;
    - an unknown manager is 404 and an unavailable one 503.
  - `adminclient` passes the manager through.
  - CLI: `at-jam kit draft build <kit> [--manager <name>]`.
- **`Ensure`:**
  - The candidates are `managers` intersected with `DraftManagers()`, in `managers` order. When `managers` is empty, they are all of `DraftManagers()`.
  - The first candidate whose latest job for this kit is reusable wins. Reusable means: succeeded, the same tree and digest, and `HasKitDraftImageOn` is true.
  - Otherwise it waits on or starts a build on `candidates[0]`.
  - With no candidates it fails with `no manager can build kit drafts`.
- **`raiseDraft`:**
  - It passes `spec.Placement.Managers` to `Ensure`.
  - The raise then runs with `spec.Placement.Managers = []string{manager}`. The caller's timeout is kept.

- [ ] **Step 1: Write the failing tests**
  - `TestDraftBuildKeyedByManager`. Starting builds of kit `k` on `local` and then `m1` gives two running jobs. A second Start on `(k, m1)` is 409. `Status(k, "m1", 0).Manager` is `"m1"`.
  - `TestDraftBuildStatusLatestAcrossManagers`. With a build on local at t0 and one on m1 at t1, `Status(k, "", 0)` returns m1's.
  - `TestDraftBuildStartDefaultManager`. `Start(k, etag, by, "")` builds on `DraftManagers()[0]`, and its view says so.
  - `TestDraftBuildStartUnknownManager404` (admin). POST with `{"manager":"nope"}` returns 404, and the body contains `unknown manager: nope`.
  - `TestEnsureReusesBuildOnLaterCandidate`. m1 has a reusable build and local has none. With managers `[]`, `Ensure` returns m1 and no new build starts.
  - `TestEnsureSkipsUnavailableBuilder`. m1 built the image, then m1 becomes unavailable, so `DraftManagers()` is `["local"]`. `Ensure` builds on local and returns local.
  - `TestEnsureRespectsRequestedManagers`. With managers `["m1"]` and local holding a reusable build, `Ensure` builds on m1.
  - `TestDraftStudioPlacedOnBuilder` (supervisor). A draft raise, where `Ensure` picks m1, calls `RaiseOn` with `Placement.Managers == ["m1"]`.
  - `TestKitDraftBuildManagerFlag` (cmd/at-jam). `--manager m1` sends `"manager":"m1"` in the POST body and `manager=m1` on the follow GET.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/ ./cmd/at-jam/ -run 'DraftBuild|Ensure|DraftStudioPlacedOnBuilder|KitDraftBuildManagerFlag'`
Expected: FAIL

- [ ] **Step 3: Implement.**
  - Re-key `jobs` by `kit + "\x00" + manager`.
  - Keep the existing logic for logs, cancelling and reuse, per job.
  - Update every existing test that builds a `KitDraftBuilds` to pass `OneDraftManager("local", fake)`.
- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/... ./cmd/at-jam/`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(jam): draft builds per manager; draft studios run where their image is`

---

### Task 4: Staleness per manager; the per-role images view

**Files:**
- Modify: `internal/jam/supervisor.go`:
  - `CurrentImage(project, role, manager string)`;
  - `RoleImages`;
  - `currentImage` uses `imageTagOn(manager, ref)`, where `""` keeps `imageTag(ref)`.
- Modify: `internal/jam/admin.go`:
  - `ImageResolver.CurrentImage` gets the new signature;
  - `imageStatus` passes the instance's manager, with `""` meaning `LocalManager`, and memoizes per (role, manager);
  - add the route `GET /admin/roles/{project}/{role}/images`.
- Modify: `internal/jam/adminclient/` (`RoleImages`), `cmd/at-jam/main.go` (`role images <project>/<role>`, in the existing `cmdRole` dispatcher)
- Test: `internal/jam/admin_image_test.go` (or the existing imageStatus tests), `internal/jam/supervisor_image_test.go`, `cmd/at-jam/main_test.go`

**Interfaces:**
- Consumes: Task 2's `AvailableNames`, reached by the supervisor through a narrow interface assertion: `interface{ AvailableNames() []string }`.
- Produces:
  - `func (s *Supervisor) CurrentImage(project, role, manager string) (CurrentImage, error)`;
  - `type RoleImage struct { Manager string \`json:"manager"\`; Kit KitRef \`json:"kit"\`; HasKit bool \`json:"has_kit"\`; Tag string \`json:"tag"\` }`;
  - `func (s *Supervisor) RoleImages(project, role string) ([]RoleImage, error)`. It has one row per `AvailableNames()` entry, in order. Without a router it has one row with `Manager: ""`.
  - The route returns `{"images": [...]}`.
  - The CLI prints one line per row: `<manager>\t<kit>\t<tag|->`.
  - The standing package's `Supervisor` interface is fully updated in Task 6, which consumes this signature.

- [ ] **Step 1: Write the failing tests**
  - `TestCurrentImagePerManager`. A fake `PlacingLauncher` gives a different tag per manager, so `CurrentImage(p, r, "m1").Tag` differs from `CurrentImage(p, r, "local").Tag`.
  - `TestImageStatusUsesInstanceManager`. An instance on m1 whose tag equals m1's current tag is `ok`, even though local's current tag differs.
  - `TestImageStatusLegacyInstanceUsesLocal`. An instance with `Manager ""` whose tag equals local's tag is `ok`, while the first available manager (m1, ordered before local) has another tag.
  - `TestRoleImagesOneRowPerConnectedManager`. With available managers `[m1, local]`, there are two rows in that order with their tags. A disconnected m2 is absent.
  - `TestRoleImagesRoute` and `TestRoleImagesCLI`. The JSON shape, and the CLI lines `m1\t<kit>\t<tag>`.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/ ./cmd/at-jam/ -run 'CurrentImagePerManager|ImageStatus|RoleImages'`
Expected: FAIL

- [ ] **Step 3: Implement.** Fix the callers, including `standing`, with a temporary `""` until Task 6. The build must stay green.
- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/... ./internal/standing/ ./cmd/at-jam/`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(jam): staleness against each studio's own manager; per-role images view`

---

### Task 5: Image facts on the image's manager

**Files:**
- Modify: `internal/jam/setup_render.go`:
  - `SetupDeps.Images` becomes a `ManagedImageReader`;
  - `RoleSetup` gains `manager string`;
  - `InstanceSetup` uses `inst.Manager`, with `""` meaning `LocalManager`;
  - the role's current tag comes from Task 4's `CurrentImage(project, role, manager)`.
- Modify: `internal/jam/setup_admin.go` (`?manager=`)
- Modify: `internal/jam/adminclient/` (the setup call takes a manager), `cmd/at-jam/inspect.go` (`role inspect --manager <name>`)
- Modify: `internal/jam/adminui/role_detail.go` and its Setup tab template: a manager selector when `AvailableNames()` has more than one entry
- Modify: `cmd/at-jam/main.go`, where `SetupDeps.Images` is the router, or a `ManagedImageReader` adapter over the plain reader when there is no router (manager is ignored)
- Test: `internal/jam/setup_render_test.go`, `internal/jam/setup_admin_test.go`, `cmd/at-jam/inspect_test.go`, `internal/jam/adminui/role_detail_test.go`

**Interfaces:**
- Consumes: Task 2's `ManagedImageReader` and `AvailableNames`; Task 4's `CurrentImage(project, role, manager)`.
- Produces:
  - `RoleSetup(ctx, d, project, role string, sections []string, manager string)`;
  - an `adminclient` setup method with a `manager` argument;
  - the CLI flag `--manager`;
  - the UI selector, which adds `manager=<name>` to the Setup tab's requests and shows the current one in bold.

- [ ] **Step 1: Write the failing tests**
  - `TestInstanceSetupReadsItsManager`. An instance on m1 reads facts through `ImageFactsOn(_, "m1", tag)`. One with `Manager ""` reads through `"local"`.
  - `TestRoleSetupOnManager`. `RoleSetup(..., "m1")` reads the tag `CurrentImage(p, r, "m1")` and the facts `ImageFactsOn(_, "m1", thatTag)`.
  - `TestRoleSetupUnknownManager404` (admin). `?manager=nope` returns 404, and the body contains `unknown manager: nope`.
  - `TestRoleInspectManagerFlag`. `role inspect p/r --manager m1` requests `manager=m1`.
  - `TestRoleSetupTabManagerSelector`. With two available managers, the tab renders both names, the current one bold. With one, it renders no selector.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/... ./cmd/at-jam/ -run 'SetupReadsItsManager|RoleSetupOnManager|RoleSetupUnknownManager|RoleInspectManagerFlag|ManagerSelector'`
Expected: FAIL

- [ ] **Step 3: Implement the changes.** An image-read error from an unknown or unavailable manager maps through the admin error helper to 404 or 503. It is not shown as a section reason.
- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/... ./cmd/at-jam/`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(jam): image facts come from the image's manager`

---

### Task 6: Standing upgrades prepare on the pinned manager

**Files:**
- Modify: `internal/jam/supervisor.go`. `PrepareImage(ctx, project, role, manager string)` uses `PrepareKitOn(manager)` when the launcher is a `PlacingLauncher` and `manager != ""`, and `s.launcher.PrepareKit` otherwise. The returned `CurrentImage` uses the same manager's tag.
- Modify: `internal/standing/standing.go`:
  - the `Supervisor` interface;
  - `advanceUpgrade` and `startPrepare` pass the session's pin, looked up from `ListStandingSessions()` by (project, role, name), to both `CurrentImage` and `PrepareImage`;
  - a pin of `""` means unpinned, which uses the default manager.
- Test: `internal/standing/standing_test.go`, `internal/jam/supervisor_image_test.go`

**Interfaces:**
- Consumes: Task 4's `CurrentImage(project, role, manager)`.
- Produces: `func (s *Supervisor) PrepareImage(ctx context.Context, project, role, manager string) (CurrentImage, KitStatus, error)`.

- [ ] **Step 1: Write the failing tests**
  - `TestPrepareImageOnNamedManager` (supervisor). `PrepareImage(ctx, p, r, "m1")` calls `PrepareKitOn(_, "m1", _)` and never `PrepareKit`.
  - `TestUpgradePreparesOnlyOnPin` (standing):
    - The session is pinned to m1, and the fake supervisor's `PrepareImage` for m1 returns `ErrManagerUnavailable`.
    - The upgrade stays in `preparing`, retries on the next pass, and never calls `PrepareImage` with any other manager.
    - Once m1 answers ready, the upgrade proceeds.
  - `TestUpgradeUnpinnedUsesDefault`. An unpinned session calls both with `""`.
- [ ] **Step 2: Run them and see them fail**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/ ./internal/standing/ -run 'PrepareImageOnNamedManager|Upgrade'`
Expected: FAIL

- [ ] **Step 3: Implement the changes.**
- [ ] **Step 4: Run the packages**

Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/... ./internal/standing/ ./cmd/at-jam/ && GOPROXY=https://proxy.golang.org GOFLAGS= go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**: `feat(standing): upgrades prepare on the session's pinned manager`

---

### Task 7: Docs

**Files:**
- Modify, routed by the docs-author skill to the owning docs:
  - `docs/usage/jam/kits-drafts.md` (builds per manager, `--manager`, and where a draft studio runs);
  - `docs/usage/jam/inspect.md` (`role inspect --manager`; a studio's facts come from its manager);
  - `docs/usage/jam/coves.md` or `studio-managers.md`, whichever owns staleness and the manager view (the per-manager image status, and `role images`);
  - `docs/usage/jam/standing-sessions.md` (upgrades prepare on the pin);
  - `docs/usage/studio-manager.md` (container and volume names with the manager name; a `local`→manager move starts fresh).
- Update INDEX rows if a doc's `read_when` changes.

- [ ] **Step 1: Make the edits.** Each fact goes in one doc, and the others link to it.
- [ ] **Step 2: Run the docs-audit checker.** Nothing new may appear outside `docs/superpowers/`.
- [ ] **Step 3: Commit**: `docs: per-manager kit operations`

---

## Coverage (spec to task)

| Spec item | Task |
|---|---|
| Draft build records keyed by (kit, manager); optional manager for admin draft builds | 3 |
| Draft studios go to the manager holding the draft image | 3 |
| Optional manager for image-facts inspection | 5 |
| Staleness per studio against its own manager's tag | 4 |
| Per-role current image: one row per connected manager | 4 |
| Standing upgrade prepares on the pinned manager; other prepares where placement would pick | 6 (other prepares already go through placement or the default) |
| No state carry-over at cutover (manager-scoped names) | 1 |
| Kit image tags are per manager (`ImageTag` answers are scoped to a manager) | 2, 4 |
