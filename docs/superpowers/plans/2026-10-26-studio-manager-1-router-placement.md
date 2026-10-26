# Studio Manager, Plan 1: Router, Placement and Pinning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put a router of named managers in front of the supervisor's launcher. The router places each raise on a manager, using an ordered list and a timeout, and pins standing sessions to their manager. Today's in-process Colima launcher becomes the manager named `local`. Behaviour does not change when `local` is the only manager.

**Architecture:**
- A new package, `internal/jam/fleet`, holds `Router`. It implements `jam.Launcher` and its optional surfaces, plus a new optional `jam.PlacingLauncher`. Per-instance operations route by the new `Instance.Manager` field. `RaiseOn` walks the placement list.
- The supervisor detects `PlacingLauncher` by type assertion, the same idiom it already uses for `ImageTagger` and `StateKeeper`. It records which manager a raise landed on, and prepares a cold kit on that same manager.
- Standing-session pins live in a new `manager` column on `standing_sessions`.

**Tech Stack:** Go; Postgres migrations (`internal/jam/migrations`); hermetic tests on `MemStore` and fake launchers. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-26-studio-manager-design.md`, sections "Seam shape inside Jam", "Placement (routing)", "Pinning (standing sessions)", and the `local` row of "Components".

**Later plans (not this one):**
- **Plan 2:** the gRPC manager protocol, auth and registry; remote managers that can be disconnected; reconcile on Hello. Also the per-manager handling of draft builds, image facts, image staleness, and `PrepareImage` for standing upgrades. Those only diverge once there is more than one real manager.
- **Plan 3:** the Studio Manager binary.
- **Plan 4:** the Jam container and its docs.

## Global Constraints

- The manager name `local` is reserved for the in-process launcher: `fleet.LocalName = "local"`.
- "Do not wait" is a placement timeout of `0`. That is also the default whenever a caller omits the timeout.
- With no manager list, candidates are all managers in **registration order, with `local` last**.
- A standing session is never raised on a manager other than its pin.
  - If a request's list excludes the pin, the raise fails immediately.
  - If the pin names a manager that is not configured, the pin is cleared and the session is placed fresh. Log this at Warn, with the message `standing session's pinned manager is not configured; placing fresh, state not carried over`.
- A raise that returns `ErrKitNotReady` is not a decline. Jam prepares the kit on *that* manager and retries the raise there. Build time is not counted against the placement timeout.
- Probe on an instance whose manager is registered but unavailable returns `LivenessUnknown, nil`, never an error, because an error gets the cove reaped. Probe on an instance whose manager is not registered at all returns `LivenessDead`.
- Per-raise secrets (`LaunchCreds`) are never logged (AGENTS.md: secrets never hit logs).
- Tests stay hermetic: `MemStore`, fakes, no Docker. Use TDD: write the failing test first.
- Docs are updated in the same change as the behaviour (AGENTS.md).

## Review Focus

1. **A legacy instance with `Manager == ""`**, raised before this change, must keep routing to `local`. Teardown, probe, pause and egress must not lose it.
2. **A raise whose `ctx` is cancelled while waiting for a manager** (an HTTP client hangs up) must return promptly with `ctx.Err()`, not sleep out the timeout.
3. **A standing session with a backfilled `local` pin on a Jam with no launcher configured** (placeholder mode) must not error forever. Placeholder mode has no router, so pin logic is skipped.
4. **A request with `managers` containing an unknown name** must fail with a message naming it, not wait out the timeout.
5. **A negative or unparsable `placement_timeout`** must get a 400 response, not be treated as zero.

Tests for each are added to the owning task below.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/placement.go` (new) | `Placement`, `Placed`, `PlacingLauncher`, placement error types |
| `internal/jam/supervisor.go` (modify) | `RaiseSpec.Placement`; raise through `PlacingLauncher`; prepare on manager; pin logic |
| `internal/jam/instance.go` (modify) | `Instance.Manager` |
| `internal/jam/fleet/router.go` (new) | `Router` membership and per-instance routing |
| `internal/jam/fleet/place.go` (new) | `RaiseOn` placement algorithm and wakeups |
| `internal/jam/fleet/local.go` (new) | `Local`: wraps an in-process `jam.Launcher` as an always-available manager |
| `internal/jam/migrations/0021_standing_session_manager.sql` (new) | pin column plus `local` backfill |
| `internal/jam/standing_sessions.go`, `pgstore_registry.go`, `storetest/` (modify) | pin get/set |
| `internal/jam/admin.go`, `sessions.go`, `adminclient/adminclient.go`, `cmd/at-jam/main.go` (modify) | request fields, CLI flags, `CoveSummary.Manager`, wiring |
| `docs/usage/jam/coves.md`, `docs/usage/jam/standing-sessions.md`, `docs/usage/jam/serve.md` (modify) | docs |

---

### Task 1: Placement types and `Instance.Manager`

**Files:**
- Create: `internal/jam/placement.go`
- Modify: `internal/jam/supervisor.go:34` (`RaiseSpec`); `internal/jam/instance.go:57` (beside `Backend`)
- Test: `internal/jam/placement_test.go`

**Interfaces:**
- Produces (package `jam`):

  ```go
  // Placement is where a raise may land: Managers in preference order ("" / nil = any, registration order),
  // and how long to look for one that accepts (0 = do not wait).
  type Placement struct {
      Managers []string
      Timeout  time.Duration
  }
  type Placed struct{ Manager, Location string }

  // PlacingLauncher is optionally implemented by a Launcher that spreads raises across named managers (fleet.Router).
  type PlacingLauncher interface {
      Launcher
      RaiseOn(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (Placed, error)
      PrepareKitOn(ctx context.Context, manager string, def KitDefinition) (KitStatus, error)
      ImageTagOn(manager string, ref KitRef) string
      HasManager(name string) bool
  }

  // KitNotReadyError: manager Manager needs def prepared before it can raise. errors.Is(err, ErrKitNotReady) holds.
  type KitNotReadyError struct{ Manager string }
  // NoManagerError: no manager accepted before the timeout; Reasons is "<name>: <why>" per candidate, in order.
  type NoManagerError struct{ Reasons []string }
  var ErrManagerUnavailable = errors.New("manager unavailable")
  var ErrUnknownManager = errors.New("unknown manager")
  var ErrPinExcluded = errors.New("standing session is pinned to a manager the request excludes")
  ```

- `RaiseSpec` gains a `Placement Placement` field. `Instance` gains ``Manager string `json:"manager,omitempty"` ``, with the comment: `the manager that raised it (fleet); "" = raised before managers, routed to local`.

- [ ] **Step 1: Write the failing tests**

  ```go
  func TestKitNotReadyErrorIsErrKitNotReady(t *testing.T) {
      err := fmt.Errorf("raise: %w", &KitNotReadyError{Manager: "mac-1"})
      if !errors.Is(err, ErrKitNotReady) { t.Fatal("want errors.Is ErrKitNotReady") }
      var k *KitNotReadyError
      if !errors.As(err, &k) || k.Manager != "mac-1" { t.Fatalf("got %v", k) }
  }
  func TestNoManagerErrorListsReasons(t *testing.T) {
      e := &NoManagerError{Reasons: []string{"mac-1: at capacity", "local: unavailable"}}
      want := "no manager could host it: mac-1: at capacity; local: unavailable"
      if e.Error() != want { t.Fatalf("got %q", e.Error()) }
  }
  func TestInstanceManagerRoundTripsJSON(t *testing.T) // Marshal/Unmarshal Instance{Manager:"local"}; field survives; "" is omitted
  ```

- [ ] **Step 2: Run them and confirm they fail**

  Run `go test ./internal/jam -run 'KitNotReadyError|NoManagerError|InstanceManager'`. Expected: FAIL (undefined names).

- [ ] **Step 3: Implement the types in `placement.go`**

  - `KitNotReadyError.Error()` is `"manager " + Manager + ": " + ErrKitNotReady.Error()`.
  - `KitNotReadyError.Unwrap()` returns `ErrKitNotReady`.
  - `NoManagerError.Error()` must produce exactly the string in the test above.
  - Add the two struct fields.

- [ ] **Step 4: Run them and confirm they pass**

  Run `go test ./internal/jam -run 'KitNotReadyError|NoManagerError|InstanceManager'`. Expected: PASS. Then run `go test ./internal/jam/...` to confirm nothing else broke.

- [ ] **Step 5: Commit**

  `git commit -m "feat(jam): placement types and Instance.Manager"`

---

### Task 2: `fleet.Router`: membership and per-instance routing

**Files:**
- Create: `internal/jam/fleet/router.go`, `internal/jam/fleet/local.go`
- Test: `internal/jam/fleet/router_test.go`, plus `fleet_test.go` holding a `fakeManager` shared by Tasks 2–3

**Interfaces:**
- Consumes: Task 1's types; `jam.Launcher`, `jam.StateKeeper`, `jam.ImageTagger`, and `jam.KitDraftBuilder` (`internal/jam/kit_draft_build.go:38`).
- Produces (package `fleet`):

  ```go
  const LocalName = "local"
  // Manager is one named place studios run.
  type Manager interface {
      jam.Launcher
      Name() string
      Available() bool // connected and serving now; Local is always true
  }
  func Local(l jam.Launcher) Manager                // Name() == LocalName; forwards optional interfaces of l via Unwrap
  type Router struct{ /* mu, ordered members, wake chan */ }
  func New(log *slog.Logger) *Router
  func (r *Router) Add(m Manager) error             // error on duplicate name; a non-Local manager named "local" is refused
  func (r *Router) Remove(name string)
  func (r *Router) Kick()                           // wakes raises waiting for a manager (Task 3); Add calls it
  func (r *Router) Names() []string                 // candidate order: registration order, local last
  func (r *Router) HasManager(name string) bool
  ```

- To reach a member's optional interfaces, `Router` asserts `m.(jam.StateKeeper)`, and so on, on the manager value. `Local` must therefore expose the wrapped launcher's optional methods. Have `Local` return a type that embeds `jam.Launcher` and provides `Unwrap() jam.Launcher`. The router asserts on `Unwrap()` when present.
- `Router` implements `jam.Launcher`, `jam.StateKeeper`, `jam.ImageTagger` and `jam.KitDraftBuilder`. Add compile-time checks: `var _ jam.PlacingLauncher = (*Router)(nil)`, and the same for the others. `RaiseOn`, `PrepareKitOn` and `ImageTagOn` come in Task 3. Add stubs here that return `errors.New("not implemented")` so this task compiles.

**Routing rules.** `route(inst)` resolves the manager: name = `inst.Manager`, or `LocalName` if that is empty.

| Method | Manager not registered | Registered, `!Available()` | Available |
|---|---|---|---|
| `Probe` | `LivenessDead, nil` | `LivenessUnknown, nil` | forward |
| `Teardown` | `nil`, plus a Warn log `teardown: manager not configured; nothing to remove` | `ErrManagerUnavailable` (the supervisor's `Reconcile` resumes Terminating teardowns every tick) | forward |
| `Pause`, `Unpause`, `ApplyEgress` | `fmt.Errorf("%w: %s", ErrUnknownManager, name)` | `ErrManagerUnavailable` | forward |

Plain `Launcher` methods that don't route by instance:
- `Raise(ctx, spec, creds)` calls `RaiseOn` and returns `.Location`.
- `PrepareKit(ctx, def)` calls `PrepareKitOn` on the first available manager in `Names()` order.

The optional surfaces:

| Method | Behaviour |
|---|---|
| `StateOwners` | Union, sorted, over available managers that implement `StateKeeper`. |
| `PurgeState(id)` | Calls `PurgeState` only on available managers whose `StateOwners` contains `id`. Returns the first error. Returns nil if no manager owns `id`. |
| `ImageTag(ref)` | `ImageTagOn(first available name, ref)`; "" if none. |
| `BuildKitDraft`, `HasKitDraftImage` | Forward to the first available manager implementing `KitDraftBuilder`. With none, return `errors.New("no manager can build kit drafts")`. Multi-manager draft keying is Plan 2. |

- [ ] **Step 1: Write the failing tests**

  Shared fixture (`fakeManager` in `fleet_test.go`): `name`, `avail bool`, `raiseErr error`, `owners []string`, and recorders `tornDown`, `probed`, `purged []string`. It implements `Manager` and `jam.StateKeeper`.

  Tests:
  - `TestRouterNamesRegistrationOrderLocalLast`: add `Local(x)`, then `a`, then `b`. `Names()` is `["a","b","local"]`.
  - `TestRouterAddRefusesDuplicateAndReservedLocal`: adding `a` twice errors. A `fakeManager{name:"local"}` errors.
  - `TestRouterRoutesByInstanceManager`: teardown of `Instance{Manager:"b"}` lands only on `b`.
  - `TestRouterLegacyInstanceRoutesToLocal` (Review Focus 1): an `Instance{Manager:""}`'s Teardown and Probe land on the Local-wrapped fake.
  - `TestRouterProbeUnavailableIsUnknownNotError`: `b.avail=false` gives `(LivenessUnknown, nil)`.
  - `TestRouterProbeUnregisteredIsDead`: `Instance{Manager:"gone"}` gives `(LivenessDead, nil)`.
  - `TestRouterTeardownUnavailableErrorsUnregisteredNoops`: `errors.Is(err, jam.ErrManagerUnavailable)` for an unavailable manager; `nil` for an unregistered one.
  - `TestRouterPauseUnknownManagerErrors`: `errors.Is(err, jam.ErrUnknownManager)`.
  - `TestRouterStateOwnersUnionAndPurgeOnlyOwner`: `a` owns `[x]` and `b` owns `[y]`. `StateOwners` is `[x y]`. `PurgeState("y")` purges only on `b`.
  - `TestLocalExposesWrappedOptionalInterfaces`: a launcher that implements `jam.StateKeeper`, wrapped in `Local`, is reached through `Router.StateOwners`.

- [ ] **Step 2: Run them and confirm they fail**

  Run `go test ./internal/jam/fleet/`. Expected: FAIL (package missing).

- [ ] **Step 3: Implement `router.go` and `local.go`** following the routing rules above.

- [ ] **Step 4: Run them and confirm they pass**

  Run `go test ./internal/jam/fleet/`. Expected: PASS.

- [ ] **Step 5: Commit**

  `git commit -m "feat(fleet): Router membership and per-instance routing; Local manager"`

---

### Task 3: `Router.RaiseOn`: the placement algorithm

**Files:**
- Create: `internal/jam/fleet/place.go`
- Test: `internal/jam/fleet/place_test.go`

**Interfaces:**
- Consumes: Task 2's `Router`, `Manager`, `fakeManager` and `Kick`.
- Produces: `RaiseOn`, `PrepareKitOn` and `ImageTagOn` on `*Router`, with the signatures given in Task 1's `PlacingLauncher`.

**Algorithm (the one part the tests don't fully determine):**

```
candidates := spec.Placement.Managers, or r.Names() if empty
any name in candidates not registered → return fmt.Errorf("%w: %s", jam.ErrUnknownManager, name) immediately
deadline := now + spec.Placement.Timeout
loop:
    reasons = nil
    for name in candidates (in order):
        m := member(name); if !m.Available() { reasons += name+": unavailable"; continue }
        loc, err := m.Raise(ctx, spec, creds)
        switch {
        case err == nil:                     return Placed{name, loc}, nil
        case errors.Is(err, jam.ErrKitNotReady): return Placed{}, &jam.KitNotReadyError{Manager: name}
        default:                             reasons += name+": "+err.Error()   // a decline
        }
    if now >= deadline: return Placed{}, &jam.NoManagerError{Reasons: reasons}
    wait for the first of: Kick, a 1s poll tick, the deadline, ctx.Done (→ return ctx.Err())
```

Other methods:
- `PrepareKitOn(ctx, name, def)` forwards to the named manager. It returns `ErrUnknownManager` or `ErrManagerUnavailable` if that manager is not registered or not available.
- `ImageTagOn(name, ref)` forwards if the manager is registered and implements `jam.ImageTagger`; otherwise it returns "".

Inject the clock and poll interval through unexported `Router` fields (`now func() time.Time`, `poll time.Duration`) so tests stay fast.

- [ ] **Step 1: Write the failing tests**
  - `TestRaiseOnFirstAvailableInOrder`: list `[a,b]`, `a` unavailable, gives `Placed{Manager:"b"}`.
  - `TestRaiseOnDeclineMovesToNext`: `a.raiseErr = errors.New("at capacity")` gives `b`.
  - `TestRaiseOnNoListUsesNamesOrder`: no list, with `a` and `local` both available, gives `a`.
  - `TestRaiseOnZeroTimeoutFailsAtOnce`: all unavailable and `Timeout:0`. Assert `errors.As(err, &noMgr)`, `noMgr.Reasons == ["a: unavailable","b: unavailable"]`, and that it returns in under 50ms.
  - `TestRaiseOnWaitsForKick`: `Timeout: 5s`, all unavailable. A goroutine sets `a.avail=true` and calls `Kick()` after 20ms. The raise succeeds on `a` in well under 1s.
  - `TestRaiseOnTimeoutExpires`: `Timeout: 30ms`, with the poll interval set to 10ms. The result is a `NoManagerError`.
  - `TestRaiseOnCtxCancelReturnsPromptly` (Review Focus 2): `Timeout: 10s`, cancel after 20ms. `errors.Is(err, context.Canceled)`, returning in under 200ms.
  - `TestRaiseOnKitNotReadyNamesManager`: `a` returns `jam.ErrKitNotReady`. Assert `errors.As(err, &k)` and `k.Manager == "a"`, and that `b` was never called.
  - `TestRaiseOnUnknownNameFailsFast` (Review Focus 4): list `[nope]` with `Timeout: 10s`. `errors.Is(err, jam.ErrUnknownManager)` holds, the message contains `nope`, and it returns at once.
  - `TestPrepareKitOnRoutesToNamed`: only `b` receives `PrepareKit`.

- [ ] **Step 2: Run them and confirm they fail**

  Run `go test ./internal/jam/fleet/ -run 'RaiseOn|PrepareKitOn'`. Expected: FAIL.

- [ ] **Step 3: Implement `place.go`** and remove the Task 2 stubs.

- [ ] **Step 4: Run them and confirm they pass**

  Run `go test -race ./internal/jam/fleet/`. Expected: PASS.

- [ ] **Step 5: Commit**

  `git commit -m "feat(fleet): ordered placement with timeout, kick wakeups and per-manager prepare"`

---

### Task 4: Supervisor raises through `PlacingLauncher`

**Files:**
- Modify: `internal/jam/supervisor.go`. The touch points are:
  - `Raise` at 452 (the call), 470-483 (Instance build) and 482 (`ImageTag`);
  - `prepareKitAndRetry` at 791-831;
  - `imageTag` at 724-731.
- Test: `internal/jam/supervisor_placement_test.go`. Build a real `fleet.Router` over two `fakeLauncher`s (`supervisor_test.go:25`), wrapped as managers. If importing `fleet` from `jam`'s internal test files causes an import cycle, put the test in package `jam_test`, or give it a minimal in-test `PlacingLauncher` fake instead.

**Interfaces:**
- Consumes: `jam.PlacingLauncher` and `KitNotReadyError` (Task 1); `fleet.Router` (Tasks 2–3), in tests only.
- Produces: `Instance.Manager` set on every raise through a `PlacingLauncher`.

Steps 3 to 5 below describe the supervisor's behaviour.

- [ ] **Step 1: Write the failing tests**
  - `TestRaiseRecordsPlacedManager`: router with `a` and `b`, spec `Placement{Managers: ["b"]}`. The stored `Instance.Manager == "b"` and `Location` comes from `b`.
  - `TestRaiseWithoutPlacingLauncherLeavesManagerEmpty`: a plain `fakeLauncher` gives `Instance.Manager == ""`. This guards against regressions in placeholder mode.
  - `TestPrepareKitRunsOnManagerThatNeededIt`: `b.notReadyOnce = true` and no list. `a` is unavailable, `b` is available. Assert `PrepareKit` was called on `b` only, the retry raised on `b`, and `Instance.Manager == "b"`.
  - `TestRaiseRecordsImageTagFromPlacedManager`: `a` and `b` have different asm strings. `Instance.ImageTag` carries `b`'s asm.
  - `TestRaiseNoManagerRollsBackActor`: a `NoManagerError` results in the actor being removed and no instance stored. This mirrors `TestRaiseRollsBackIdentityWhenLauncherFails` (`supervisor_test.go:202`).

- [ ] **Step 2: Run them and confirm they fail**

  Run `go test ./internal/jam -run 'PlacedManager|WithoutPlacingLauncher|ManagerThatNeededIt|ImageTagFromPlaced|NoManagerRollsBack'`. Expected: FAIL.

- [ ] **Step 3: Implement the raise call**
  - Add `func (s *Supervisor) raiseOn(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (Placed, error)`. It calls `RaiseOn` when `s.launcher` is a `PlacingLauncher`, and otherwise returns `Placed{Location: loc}` from `Raise`.
  - Use `raiseOn` at line 452 and in `prepareKitAndRetry`.

- [ ] **Step 4: Implement prepare-on-manager**
  - On `errors.As(err, &kitNotReady)`, `prepareKitAndRetry` takes the manager name.
  - It calls `PrepareKitOn(ctx, manager, def)` when the launcher is a `PlacingLauncher`, and otherwise `PrepareKit` as today.
  - It retries with a copy of the spec whose `Placement` is `{Managers: []string{manager}, Timeout: 0}`.
  - Change its return to `(Placed, KitDefinition, error)`.

- [ ] **Step 5: Record the manager and its image tag**
  - Set `Instance.Manager = placed.Manager`.
  - Change `imageTag(ref)` to `imageTagOn(manager string, ref KitRef)`. It uses `ImageTagOn` when the launcher is a `PlacingLauncher`, and the existing `ImageTagger` path otherwise.
  - The raise path uses `placed.Manager`. Every other caller (`CurrentImage`) passes `""`, which means "the default", i.e. `ImageTag(ref)`.

- [ ] **Step 6: Run the tests and confirm they pass**

  Run `go test ./internal/jam/...`. Expected: all PASS, including the existing `TestSupervisorPreparesKitOnNotReadyThenRaises`.

- [ ] **Step 7: Commit**

  `git commit -m "feat(jam): supervisor places raises and prepares kits on the chosen manager"`

---

### Task 5: Pin standing sessions to their manager

**Files:**
- Create: `internal/jam/migrations/0021_standing_session_manager.sql`
- Modify:
  - `internal/jam/standing_sessions.go`: `StandingSessionRef`, `StandingSessionStore`, and the memState and MemStore implementations;
  - `internal/jam/pgstore_registry.go:502-560`;
  - `internal/jam/storetest/` (conformance);
  - `internal/jam/supervisor.go`, in the `Raise` preamble (after the project is resolved at 367 and before `Enroll` at 379) and after `PutInstance`.
- Test: `internal/jam/supervisor_pin_test.go`; a conformance case in `storetest`.

**Interfaces:**
- Produces:

  ```go
  // StandingSessionRef gains:
  Manager string `json:"manager,omitempty"` // the manager holding its state; "" = not yet pinned
  // StandingSessionStore gains:
  StandingSessionManager(project ident.ID, role, name string) string           // "" if unpinned or absent
  SetStandingSessionManager(project ident.ID, role, name, manager string) error // no-op if the entry is absent
  ```

- Migration:

  ```sql
  -- Studio Manager (plan 1): the manager a standing session's state lives on.
  -- Existing sessions were raised by the in-process launcher, now the manager "local".
  ALTER TABLE standing_sessions ADD COLUMN manager text NOT NULL DEFAULT '';
  UPDATE standing_sessions SET manager = 'local';
  ```

**Raise rules.** These apply only when `spec.SessionKind == SessionKindStanding`, `spec.Name != ""`, and `s.launcher` is a `PlacingLauncher`:

- **Pin set, manager no longer configured:** if `pin != ""` and `!HasManager(pin)`, log Warn with the exact message from Global Constraints and the fields `id`, `project`, `role`, `name`, `pin`. Call `SetStandingSessionManager(…, "")` and continue unpinned.
- **Pin excluded by the request:** if the pin is set and `len(spec.Placement.Managers) > 0` without containing it, return `fmt.Errorf("raise: %w: %s", ErrPinExcluded, pin)` *before* `Enroll`, so there is nothing to roll back.
- **Pin set otherwise:** set `spec.Placement.Managers = []string{pin}` and keep the timeout.
- **After `PutInstance` succeeds:** if the session was unpinned and `placed.Manager != ""`, call `SetStandingSessionManager(…, placed.Manager)`. A failure is logged at Warn and does not fail the raise.
- **Reset:** this needs no code. `finishReset` already calls `RemoveStandingSession`, which deletes the row and with it the pin. Add a test anyway.

- [ ] **Step 1: Write the failing conformance case**

  `StandingSessionManager round-trips`:
  - Put a session, then `Set(…, "mac-1")`, then `Get` returns `"mac-1"`.
  - `ListStandingSessions` carries `Manager`.
  - `Remove` followed by `Get` returns `""`.
  - Setting a pin on an absent entry is a no-op with a nil error.

- [ ] **Step 2: Run it and confirm it fails**

  Run `go test ./internal/jam -run Conformance`. Expected: FAIL.

- [ ] **Step 3: Implement the store side**
  - Add the migration.
  - In memState, the value becomes `{sessionID, manager}`.
  - In pgstore: an `UPDATE … SET manager` statement, and `loadStandingSessions` reads the column.

- [ ] **Step 4: Run it and confirm it passes**

  Run `go test ./internal/jam -run Conformance`. Expected: PASS. If `JAM_TEST_POSTGRES_DSN` is set, also run `just integration-jam`.

- [ ] **Step 5: Write the failing supervisor tests**

  Use a router with `a` and `b`, plus a standing role, seeded with `PutStandingSession`.
  - `TestStandingFirstRaisePinsPlacedManager`: unpinned with no list, `a` unavailable. The session raises on `b`, and the pin is now `"b"`.
  - `TestStandingRaiseGoesOnlyToPin`: pinned to `a`, `a` unavailable, `b` available, `Timeout:0`. The result is a `NoManagerError`, and `b` never received a raise.
  - `TestStandingRaiseExcludedPinFailsBeforeEnroll`: pinned to `a`, list `[b]`. `errors.Is(err, ErrPinExcluded)` holds, the actor does not exist, and no raise happened.
  - `TestStandingPinToUnconfiguredManagerIsCleared`: pinned to `gone`. The session raises on `a`, the pin becomes `"a"`, and the log captured through a `slog` buffer handler contains `placing fresh, state not carried over`.
  - `TestStandingResetClearsPin`: after `RemoveStandingSession`, `StandingSessionManager` returns `""`.
  - `TestPlaceholderLauncherIgnoresPin` (Review Focus 3): a plain `fakeLauncher` with the pin `"local"` raises normally, and the pin is untouched.
  - `TestPersonalSessionNotPinned`: a raise with `SessionKindPersonal` writes no pin.

- [ ] **Step 6: Run them and confirm they fail, then implement**

  Run `go test ./internal/jam -run 'Standing.*Pin|PlaceholderLauncherIgnoresPin|PersonalSessionNotPinned'`. Confirm FAIL, implement the raise rules above, and run again. Expected: PASS.

- [ ] **Step 7: Commit**

  `git commit -m "feat(jam): pin standing sessions to the manager holding their state"`

---

### Task 6: Request surface: `managers` and `placement_timeout`

**Files:**
- Modify:
  - `internal/jam/admin.go`: `CoveRaiseBody` at 229; the `POST /admin/coves` handler at ~990; `CoveSummary` at 253 and the code that fills it;
  - `internal/jam/sessions.go`: `PersonalSessionBody` at 62; `RequestPersonalSession` at 177/215;
  - `internal/jam/adminclient/adminclient.go`: `CoveRaiseParams` at 460; `RequestPersonalSession` at 519;
  - `cmd/at-jam/main.go`: the `studio raise` flags near 1240/1288; the `session request` flags near 1381;
  - `internal/jam/adminui/role_request.go:39`: pass an empty placement.
- Test: `internal/jam/admin_placement_test.go`; `cmd/at-jam/main_test.go`.

**Interfaces:**
- Consumes: `RaiseSpec.Placement` (Task 1).
- Produces:
  - JSON fields on both `CoveRaiseBody` and `PersonalSessionBody`:
    - ``Managers []string `json:"managers,omitempty"` ``
    - ``PlacementTimeout string `json:"placement_timeout,omitempty"` ``, a Go duration string where "" means 0.
  - `func ParsePlacement(managers []string, timeout string) (Placement, error)` in `placement.go`. It errors on an unparsable or negative timeout.
  - `CoveSummary` gains ``Manager string `json:"manager,omitempty"` ``.
  - `adminclient.CoveRaiseParams` gains `Managers []string` and `PlacementTimeout string`.
  - `adminclient.RequestPersonalSession` changes to take a `jam.PersonalSessionBody` (one caller, main.go:1381).
  - CLI, on both `studio raise` and `session request`:
    - `--manager <name>`, repeatable, order preserved. Reuse the existing `multiFlag` (`cmd/at-jam/main.go:850`).
    - `--placement-timeout <duration>`.
  - `at-jam studio list` adds a MANAGER column if the list is tabular. Match the existing column style.

- [ ] **Step 1: Write the failing tests**
  - `TestParsePlacement`:
    - `(["a","b"], "2m")` gives `Placement{[a b], 2*time.Minute}`.
    - `(nil, "")` gives a zero `Placement`.
    - `"-1s"` errors, and so does `"soon"` (Review Focus 5).
  - `TestPostCovesPassesPlacement`: post `{"id":…,"role":…,"managers":["b"],"placement_timeout":"0s"}` to a supervisor over a recording placing fake. The recorded `RaiseSpec.Placement.Managers` is `["b"]`.
  - `TestPostCovesBadPlacementTimeout400` (Review Focus 5): `"placement_timeout":"-5s"` gives 400.
  - `TestPersonalSessionPassesPlacement`: the same check through `POST /admin/sessions/personal`.
  - `TestCoveSummaryCarriesManager`: an `Instance{Manager:"b"}` gives `CoveSummary.Manager == "b"`.
  - `TestStudioRaiseManagerFlagsOrdered` in `cmd/at-jam`: `--manager b --manager a --placement-timeout 30s` posts `managers:["b","a"]` and `placement_timeout:"30s"`. Use the existing httptest admin pattern in `main_test.go`.

- [ ] **Step 2: Run them and confirm they fail**

  Run `go test ./internal/jam ./cmd/at-jam -run 'Placement|CoveSummaryCarriesManager|ManagerFlags'`. Expected: FAIL.

- [ ] **Step 3: Implement**
  - The handlers parse the body with `ParsePlacement`. On an error they return `http.Error(w, err.Error(), http.StatusBadRequest)`. Otherwise they set `spec.Placement`.
  - `RequestPersonalSession` passes the placement into its `RaiseSpec`.
  - The requisitioner and standing reconciler are unchanged; they keep the zero placement.

- [ ] **Step 4: Run them and confirm they pass**

  Run `go test ./internal/jam/... ./cmd/at-jam/...`. Expected: PASS.

- [ ] **Step 5: Commit**

  `git commit -m "feat(jam): managers and placement_timeout on raise and personal-session requests"`

---

### Task 7: Wire the router in `at-jam serve`, then update the docs

**Files:**
- Modify: `cmd/at-jam/main.go:2126-2190`
- Docs: `docs/usage/jam/coves.md` (new section `### Managers and placement` under "The model", plus the `studio raise` flags); `docs/usage/jam/standing-sessions.md` ("State across restarts": the pin); `docs/usage/jam/serve.md` (launcher block: it is the `local` manager)
- Test: `cmd/at-jam/main_test.go`

**Interfaces:**
- Consumes: `fleet.New`, `fleet.Local` and `Router.Add` (Task 2).

**Wiring.** When `cfg.Runtime.Launcher != nil`:
- Build the Colima launcher exactly as today.
- Then:

  ```go
  router := fleet.New(log); router.Add(fleet.Local(colimaLauncher)); lch = router
  ```

- The existing `lch.(jam.KitDraftBuilder)` assertion at 2182 now succeeds through the router's forwarding.
- `imagefacts` stays bound to the same `col` as today; per-manager image facts are Plan 2.
- Log `"Jam managers", "names", router.Names()`.

With no launcher block, `lch` stays `placeholderLauncher{}`. There is no router in that case.

- [ ] **Step 1: Write the failing test**

  `TestServeWiresRouterWithLocal`: factor the launcher construction at 2126-2169 into `func buildLauncher(cfg serveConfig, …) (jam.Launcher, jam.ImageFactsReader, …)`, if it isn't already separable. With a launcher block whose Colima runner is fake, the result is a `*fleet.Router` with `Names() == ["local"]`. With no block, the result is `placeholderLauncher`.

- [ ] **Step 2: Run it and confirm it fails**

  Run `go test ./cmd/at-jam -run ServeWiresRouter`. Expected: FAIL.

- [ ] **Step 3: Implement the wiring**

- [ ] **Step 4: Run the full suite**

  Run `just test` and `just lint`. Expected: both pass with no new lint findings.

- [ ] **Step 5: Update the docs** (use the docs-author skill)

  Write these in user terms:
  - Jam raises studios on named managers; today's launcher block is the manager `local`.
  - The `managers` and `placement_timeout` fields and flags, including that `0` means do not wait.
  - The default order (registration order, `local` last).
  - Standing sessions are pinned to their manager and never raised elsewhere; a reset clears the pin, and so does a pin to a manager that is no longer configured.
  - The pinned-manager-excluded error.

  Then run the docs-audit skill's checker and fix any findings.

- [ ] **Step 6: Commit**

  `git commit -m "feat(at-jam): route studios through the manager router; document managers and placement"`

---

## Self-review notes

- **Spec coverage.** This plan covers these spec sections: "Seam shape", Placement (every rule), Pinning (every rule), the `local` component, the request fields, and admin visibility of `Instance.Manager`.
  - **Deferred to Plan 2 by design:** the protocol, auth and registry; disconnect and reconnect reconciliation; manager-name labels; per-manager draft builds, image facts, staleness and `PrepareImage`; the admin manager list.
  - **Deferred to Plan 3:** the manager binary.
  - **Deferred to Plan 4:** the container.
- **Teardown retry on reconnect.** This plan returns `ErrManagerUnavailable` from Teardown. The existing `Reconcile` already resumes Terminating and Lost teardowns every tick (`supervisor.go:1512`), which supplies the retry the spec requires.
