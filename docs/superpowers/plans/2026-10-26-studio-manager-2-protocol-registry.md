# Studio Manager, Plan 2: Protocol, Registry and Remote Managers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a Studio Manager connect to Jam and host studios. That needs:
- a gRPC stream on the cove-facing mux;
- a registry of per-name manager tokens;
- a remote adapter that the plan 1 router drives like any other manager;
- disconnect and reconnect handling;
- the manager side of the protocol, so plan 3's binary only adds config and Colima wiring.

**Architecture:**
- **The proto is only an envelope:** Hello, Welcome, heartbeats, and `Frame{id, op, data, more}`.
- **Payloads are JSON** of the existing Go types, in a `wire` package that both sides share. Jam and the manager run the same version, so no proto mirror of `RaiseSpec` or `KitDefinition` is needed. A `Frame` carries at most 1 MiB, so large kit definitions are chunked.
- **Jam side:** `managers.Hub` serves `StudioManager.Connect`. It authenticates the bearer token against a new `managers` table, checks Hello (name and exact version), reconciles the inventory through the supervisor, and attaches the stream to that name's `managers.Remote`.
  - A `Remote` is a `fleet.Manager` and stays registered while its token exists.
  - When disconnected it is unavailable, so its studios probe as Unknown and are never reaped.
- **Manager side:** `managers/client.Run` dials, sends Hello, and serves requests against a `jam.Launcher` backend.

**Tech Stack:** Go; gRPC (`google.golang.org/grpc` v1.84, already a dependency); buf v1.45.0 with protoc-gen-go and protoc-gen-go-grpc (`just buf-gen`); `google.golang.org/grpc/test/bufconn` for hermetic stream tests; Postgres migration `0022`; `github.com/go-git/go-billy/v5/memfs` (already a dependency).

**Spec:** `docs/superpowers/specs/2026-10-26-studio-manager-design.md`. This plan implements these sections:
- "Connection, protocol, and auth"
- "Disconnects and reconciliation", except the labels
- "Admin visibility"
- "Deployment assumptions" (the version check)

Plan 1 (`docs/superpowers/plans/2026-10-26-studio-manager-1-router-placement.md`, merged in #514) built the router this plan plugs into.

**Later plans (not this one):**
- **Plan 3, the Studio Manager binary.** It covers:
  - config, and a reconnect loop around `client.Run`;
  - Colima wiring and inventory from docker labels;
  - manager-name labels on studios and state;
  - the cove-master log tail returned on teardown;
  - the `colima setup/check-docker` verbs;
  - treating teardown of an absent container as success.
- **Plan 4, per-manager kit operations.** It covers:
  - draft build records keyed by (kit, manager);
  - an optional manager for admin draft builds and image facts;
  - staleness computed against each studio's own manager;
  - `PrepareImage` on the pinned manager.

  These are not needed for the live test, because `local` and the first remote manager share one Colima daemon.
- **Plan 5, the Jam container image, compose file, and docs.**

## Global Constraints

- **Proto:**
  - package `cove.manager.v1`;
  - service `StudioManager`;
  - one RPC, `Connect(stream ManagerUp) returns (stream ManagerDown)`;
  - Go package `github.com/aethons-tools/cove/internal/jam/managers/managerpb`.
  - Generated code is committed and CI never regenerates it.
- **Auth:**
  - The manager sends metadata `authorization: Bearer <token>`.
  - The token comes from `jam.MintToken()`. Jam stores only `jam.HashToken(token)`.
  - The token is shown once, in the add or rotate response, and is never logged.
- **Hello:** `Hello.name` must equal the token's registered name, otherwise the stream is refused with `PermissionDenied`. `Hello.version` must equal Jam's version string exactly, otherwise `FailedPrecondition` with the message `version mismatch: jam <J>, manager <M>`.
- **One stream per name:** a new authenticated stream for a name closes the old one.
- **Reserved name:** `local` (`fleet.LocalName`) can never be registered.
- **Valid names:** `^[a-z0-9][a-z0-9-]{0,62}$`.
- **A disconnected manager:**
  - Probe returns `LivenessUnknown, nil`.
  - Teardown, Pause, Unpause and ApplyEgress return an error wrapping `jam.ErrManagerUnavailable`.
  - Its `Remote` is never removed from the router while its token exists.
- **A Raise whose stream drops mid-call** returns `jam.ErrRaiseInterrupted`. The router does **not** try another manager with the same credentials.
- **Wire constants** (in package `wire`):

  | Constant | Value |
  |---|---|
  | `MaxFrameData` | `1 << 20` |
  | `MaxMessageBytes` | `256 << 20` |
  | `HeartbeatEvery` | `10 * time.Second` |
  | `DeadAfter` | `30 * time.Second` |
  | `HelloTimeout` | `10 * time.Second` |
- **What reaches logs:** `LaunchCreds`, manager tokens and frame payloads are never logged. Errors from the far side are logged as their message text only.
- **Location is opaque.** No docker or Colima concept appears in `wire` or the proto.
- **Tests are hermetic:** `MemStore`, fakes, and `bufconn`. No Docker and no network. Use TDD.
- **Postgres paths:** the sandbox has no Postgres, so Postgres store conformance runs in CI's `store-integration` job.
- **Go commands in this sandbox** need the prefix `GOPROXY=https://proxy.golang.org GOFLAGS=`.
- **Docs** are updated in the same change, kept progressively disclosed, and checked with docs-audit.

## Review Focus

1. **The stream drops while a Raise is in flight.** Jam must not raise the same actor on a second manager with the same credentials. Expect `ErrRaiseInterrupted`, with no fall-through. Tests: Task 3 (router) and Task 6 (Remote).
2. **Jam restarts while the remote manager is not yet connected.** Instances on that manager must probe Unknown and must not be reaped. That requires `Hub.Start` to register every stored manager *before* `sup.Run`. Tests: Task 7 (hub) and Task 9 (wiring order).
3. **A replaced stream sends a late result.** A manager reconnects while its old stream is still open. A result arriving on the old stream must not resolve a call made on the new one. Calls are owned by their connection. Test: Task 7.
4. **A build context with an executable script and a symlink** must cross the wire byte-for-byte, mode and link included. Otherwise the remote build differs from the local one. Test: Task 1.
5. **Launch secrets stay out of Jam's log.** A full raise over the wire must not leave the launch secret or identity token anywhere in Jam's log output. Test: Task 7.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/managers/proto/manager.proto`, `buf.yaml`, `buf.gen.yaml` (new) | the envelope proto |
| `internal/jam/managers/managerpb/` (generated) | Go stubs |
| `internal/jam/managers/wire/` (new: `frame.go`, `ops.go`, `kitdef.go`, `errors.go`) | constants, chunking, per-op JSON payloads, KitDefinition codec, error codes |
| `internal/jam/managers.go` (new) | `ManagerRecord`, `ManagerStore`, `ValidManagerName`, `ManagerStatus`, `ManagedStudio`, `ManagerInventory`, `ManagerReconcile` |
| `internal/jam/manager_reconcile.go` (new) | `Supervisor.ReconcileManager` |
| `internal/jam/managers_store.go` (new), `memstate.go`, `memstore_registry.go`, `pgstore_registry.go`, `store.go` (modify) | the store table |
| `internal/jam/migrations/0022_managers.sql` (new) | `managers` table |
| `internal/jam/storetest/managers.go` (new), `conformance.go` (modify) | conformance |
| `internal/jam/placement.go`, `internal/jam/fleet/place.go`, `router.go` (modify) | `ErrRaiseInterrupted`, broadcast wake, decline backoff, `ImageFacts` forwarding |
| `internal/jam/managers/client/` (new) | manager side: `Run` |
| `internal/jam/managers/conn.go`, `remote.go` (new) | Jam side: per-stream calls; the `Remote` manager |
| `internal/jam/managers/hub.go` (new) | the gRPC service, auth, Hello, registry operations, statuses |
| `internal/jam/managers/admin.go` (new) | `/admin/managers` routes |
| `internal/jam/adminclient/adminclient.go`, `cmd/at-jam/managers.go` (new), `cmd/at-jam/main.go` (modify) | client and `at-jam manager` verbs |
| `cmd/at-jam/config.go`, `cmd/at-jam/main.go` (modify) | `runtime.studio-managers`, router and hub wiring, metrics |
| `internal/jam/adminui/` (modify) | managers panel, manager column |
| `docs/usage/jam/studio-managers.md` (new), `docs/usage/jam/INDEX.md`, `coves.md`, `serve.md` (modify) | docs |
| `justfile` (modify) | `buf-gen` also generates `managerpb` |

---

### Task 1: The envelope proto and the `wire` package

**Files:**
- Create: `internal/jam/managers/proto/manager.proto`, `internal/jam/managers/buf.yaml`, `internal/jam/managers/buf.gen.yaml` (copy `internal/jam/attach`'s two buf files, same plugins, `out: managerpb`)
- Generate: `internal/jam/managers/managerpb/*.pb.go`
- Create: `internal/jam/managers/wire/frame.go`, `ops.go`, `kitdef.go`, `errors.go`, with matching `_test.go` files
- Modify: `justfile` recipe `buf-gen`, so that it also runs `buf generate` in `internal/jam/managers`

**Interfaces:**
- Produces:
  - **managerpb:** `ManagerUp{oneof msg: Hello hello; Frame frame; Heartbeat heartbeat}`, `ManagerDown{oneof msg: Welcome welcome; Frame frame; Heartbeat heartbeat}`, `Hello{name, version, capacity int32, repeated Studio studios, repeated string state_owners}`, `Studio{actor_id, location}`, `Frame{uint64 id, string op, bytes data, bool more}`. Server interface `managerpb.StudioManagerServer`, client `managerpb.NewStudioManagerClient`.
  - **Constants:** the wire constants from Global Constraints.
  - **Op names:** `OpRaise="raise"`, `OpTeardown="teardown"`, `OpProbe="probe"`, `OpPause="pause"`, `OpUnpause="unpause"`, `OpApplyEgress="apply-egress"`, `OpPrepareKit="prepare-kit"`, `OpPurgeState="purge-state"`, `OpStateOwners="state-owners"`, `OpImageTag="image-tag"`, `OpBuildKitDraft="build-kit-draft"`, `OpHasKitDraftImage="has-kit-draft-image"`, `OpImageFacts="image-facts"`.
  - **Control ops:** `OpResult="result"` (manager→Jam, id = the request's), `OpLog="log"` (manager→Jam, one raw draft-log chunk per frame, never assembled), `OpCancel="cancel"` (Jam→manager, empty).
  - **Chunking:** `func Split(id uint64, op string, data []byte) []*managerpb.Frame`, which always returns at least one frame; every frame except the last has `more=true`. `type Assembler struct{…}` (zero value usable), with `func (a *Assembler) Add(f *managerpb.Frame) (data []byte, done bool, err error)`. Frames are keyed by id. An `OpLog` frame is returned as is. Exceeding `MaxMessageBytes` for one id is an error.
  - **Payloads (JSON):** `RaiseRequest{Spec jam.RaiseSpec; Creds jam.LaunchCreds}` / `RaiseResult{Location string}`; `InstanceRequest{Instance jam.Instance}`; `ProbeResult{Liveness jam.Liveness}`; `EgressRequest{Instance jam.Instance; Policy *jam.EgressPolicy}`; `KitRequest{Def KitDef}` (result `jam.KitStatus`); `ActorRequest{ActorID string}`; `OwnersResult{ActorIDs []string}`; `ImageTagRequest{Ref jam.KitRef}` / `TagResult{Tag string}`; `DraftBuildRequest{Def KitDef; Tag string}` / `DraftBuildResult{ImageID string}`; `DraftImageRequest{ImageID string}` / `HasResult{Has bool}`; `ImageFactsRequest{Image string}` (result `jam.ImageFacts`); `Empty{}`.
  - **Result envelope:** `Result{Error *Error \`json:"error,omitempty"\`; Body json.RawMessage \`json:"body,omitempty"\`}`. `func EncodeResult(body any, err error) ([]byte, error)`, `func DecodeResult(data []byte, body any) error`. `DecodeResult` returns the remote `*Error` when one is set.
  - **Errors:** `type Error struct{Code, Message string}` with `Error() string` (= Message) and `Is(target error) bool`. The code mapping is: `"kit-not-ready"`↔`jam.ErrKitNotReady`, `"image-not-present"`↔`jam.ErrImageNotPresent`, `"image-unreadable"`↔`jam.ErrImageUnreadable`, `""` = none. `func EncodeError(err error) *Error` (nil for nil) picks the code by `errors.Is`.
  - **Kit definitions:** `type KitDef struct{Ref jam.KitRef; Kit studio.StudioKit; BuildContext string; Chain []KitLink; Harness harnessinstall.Install}`, `type KitLink struct{Ref jam.KitRef; Kit studio.StudioKit; BuildContext string; Follow bool; Files []File}` (`Files == nil` ⇔ the link's `Files` is nil), `type File struct{Path string; Mode uint32; Data []byte; Link string}`. `func EncodeKitDefinition(def jam.KitDefinition) (KitDef, error)`, `func (d KitDef) Definition() (jam.KitDefinition, error)`. `Definition()` rebuilds each link's `Files` as a `memfs` tree, and restores `StudioKit.BuildContext`, which `studio` tags `json:"-"`.

- [ ] **Step 1: Write the proto** at `internal/jam/managers/proto/manager.proto`, with exactly the messages above. Add a file comment: payloads are JSON (`internal/jam/managers/wire`), and Jam and manager versions must match.

- [ ] **Step 2: Generate.** Extend `buf-gen` with a second `cd internal/jam/managers && … buf generate` line. Run: `GOPROXY=https://proxy.golang.org GOFLAGS= just buf-gen`. Expected: `internal/jam/managers/managerpb/manager.pb.go` and `manager_grpc.pb.go` exist, and `go build ./internal/jam/managers/...` succeeds.

- [ ] **Step 3: Write the failing tests** in `internal/jam/managers/wire`:
  - `TestSplitAssembleRoundTrip`: for sizes 0, 1, `MaxFrameData`, `MaxFrameData+1`, and `3*MaxFrameData+7`, `Split` returns `ceil(n/MaxFrameData)` frames (minimum 1), each with `len(data) <= MaxFrameData`, and only the last has `more == false`. Feeding them to `Assembler.Add` yields `done` only on the last frame, with bytes equal to the input.
  - `TestAssemblerInterleavedIDs`: frames of id 1 and id 2 interleave and both reassemble.
  - `TestAssemblerCap`: frames for one id beyond `MaxMessageBytes` produce an error. Build them with a small `Assembler.max` override, an unexported field, set in the test.
  - `TestResultErrorCodes`: `EncodeResult(nil, fmt.Errorf("x: %w", jam.ErrKitNotReady))`, then `DecodeResult`, gives an error where `errors.Is(err, jam.ErrKitNotReady)` holds and `err.Error() == "x: launcher: kit not prepared"`. The same check runs for `ErrImageNotPresent` and `ErrImageUnreadable`. A plain `errors.New("boom")` decodes with `Error() == "boom"` and matches no sentinel. A success decodes `RaiseResult{Location: "loc"}`.
  - `TestRaiseRequestRoundTrip`: a `RaiseRequest` has every exported `RaiseSpec` field set: `Connector` with `Env` and `ModelSpec`, `Context` with `Files`, `DraftImage`, `Egress`, `Kit`, and `Creds`. JSON round-trips it `reflect.DeepEqual`.
  - `TestKitDefinitionRoundTrip` (Review Focus 4): a two-link chain. The top link's `Files` is a `memfs` holding:
    - `Dockerfile` (0644);
    - `bin/run.sh` (0755);
    - a symlink `latest -> bin/run.sh`.

    `Kit.BuildContext` is `"abc"` on both the def and the link, and the root link has `Files == nil`. After `EncodeKitDefinition`, JSON, and `Definition()`, the test checks:
    - `kittree.ContextTar` of the decoded top-link FS is byte-equal to that of the original;
    - the root link's `Files` is nil;
    - `def.Kit.BuildContext == "abc"` and `Chain[1].Kit.BuildContext == "abc"`;
    - `Ref` and `Harness` are equal.

- [ ] **Step 4: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/managers/wire/`. Expected: build failure (undefined `Split` and others).

- [ ] **Step 5: Implement** `frame.go`, `ops.go`, `errors.go` and `kitdef.go`.
  - **Encoding:** `EncodeKitDefinition` walks each link's FS with `util.Walk` from go-billy. It uses `Lstat` and `Readlink`, so symlinks are recorded as links, not followed.
  - **Decoding:** `Definition` writes files with `OpenFile(path, O_CREATE|O_WRONLY, mode)`, and links with `Symlink`.

- [ ] **Step 6: Run the tests to make sure they pass.** Same command. Expected: `ok`.

- [ ] **Step 7: Commit.**
```bash
git add justfile internal/jam/managers
git commit -m "feat(jam): Studio Manager envelope proto and wire codec"
```

---

### Task 2: The manager registry in the store

**Files:**
- Create: `internal/jam/managers.go`, `internal/jam/managers_store.go`, `internal/jam/migrations/0022_managers.sql`, `internal/jam/storetest/managers.go`
- Modify: `internal/jam/store.go` (embed `ManagerStore`), `memstate.go` (map field), `memstore_registry.go`, `pgstore_registry.go` (methods and a `loadManagers` call in load), `internal/jam/storetest/conformance.go` (run `runManagerConformance`)

**Interfaces:**
- Produces, in package `jam`:
  - `type ManagerRecord struct{Name, TokenHash string; CreatedAt time.Time}`
  - `type ManagerStore interface`, with these methods:
    - `AddManager(m ManagerRecord) error` (`ErrManagerExists`, or an invalid-name error);
    - `ListManagers() []ManagerRecord` (by `CreatedAt`, then `Name`);
    - `ManagerByTokenHash(hash string) (ManagerRecord, bool)`;
    - `SetManagerToken(name, tokenHash string) error` (wraps `ErrNotFound`);
    - `RemoveManager(name string) error` (wraps `ErrNotFound`).
  - `var ErrManagerExists = errors.New("manager already registered")`.
  - `func ValidManagerName(name string) error`. It refuses `"local"` with `manager name "local" is reserved`, and anything not matching `^[a-z0-9][a-z0-9-]{0,62}$` with `invalid manager name %q`. Package `jam` cannot import `fleet`, so the literal `"local"` gets a comment pointing at `fleet.LocalName`.
  - Types used by later tasks:
    - `type ManagedStudio struct{ActorID string \`json:"actor_id"\`; Location string \`json:"location"\`}`
    - `type ManagerInventory struct{Studios []ManagedStudio; StateOwners []string}`
    - `type ManagerReconcile struct{Lost []string; Strays []ManagedStudio; MissingState []string}`
    - `type ManagerStatus struct{Name string \`json:"name"\`; Local bool \`json:"local,omitempty"\`; Connected bool \`json:"connected"\`; Version string \`json:"version,omitempty"\`; Capacity int \`json:"capacity,omitempty"\`; Studios int \`json:"studios"\`; ConnectedAt time.Time \`json:"connected_at,omitzero"\`; LastSeen time.Time \`json:"last_seen,omitzero"\`; Strays []ManagedStudio \`json:"strays,omitempty"\`}`
    - `type ManagerToken struct{Name string \`json:"name"\`; Token string \`json:"token"\`}`
- Migration:
```sql
-- Studio Manager registry (docs/usage/jam/studio-managers.md): one row per
-- registered manager name; only the token's hash is stored.
CREATE TABLE managers (
    name       text        PRIMARY KEY,
    token_hash text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL
);
```
- Not part of `ExportConfig` or `ImportConfig`. A restored Jam re-registers its managers. Document this in Task 10.

- [ ] **Step 1: Write the failing conformance tests** in `storetest/managers.go`, in the style of `runPushConformance`:
  - `manager_add_list_order`: add `b` (t=2) and then `a` (t=1). `ListManagers` returns `a, b`.
  - `manager_add_duplicate`: `errors.Is(err, jam.ErrManagerExists)`.
  - `manager_add_invalid_name`: `"local"`, `""`, `"Bad"` and `"-x"` each error, and nothing is stored.
  - `manager_lookup_by_hash`: a hit, plus a miss for an unknown hash.
  - `manager_set_token`: the old hash misses, the new one hits, and an unknown name wraps `ErrNotFound`.
  - `manager_remove`: removed, then a second remove wraps `ErrNotFound`.

  Add `TestValidManagerName` in `internal/jam/managers_test.go`. It checks the exact messages for `local` and `Bad`, and that `mac-1` passes.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/ -run 'Conformance|ValidManagerName'`. Expected: build failure.

- [ ] **Step 3: Implement.**
  - Follow the push-subscription pattern: validation in `memState` `prepare…`, then `apply…` mutators shared by `MemStore` and `PostgresStore`, then the Postgres `registryTx`.
  - Load the rows in `PostgresStore`'s load path.

- [ ] **Step 4: Run them to make sure they pass.** Same command. Expected: `ok` (MemStore). Postgres runs in CI.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam
git commit -m "feat(jam): manager registry table and store conformance"
```

---

### Task 3: Router: interrupted raises, broadcast wake, decline backoff, image facts

**Files:**
- Modify: `internal/jam/placement.go`, `internal/jam/fleet/router.go`, `internal/jam/fleet/place.go`
- Test: `internal/jam/fleet/place_test.go`, `router_test.go`

**Interfaces:**
- Consumes: plan 1's `Router`, `RaiseOn`, `Kick`, and `optional(m)`.
- Produces:
  - `jam.ErrRaiseInterrupted = errors.New("manager connection lost mid-raise; outcome unknown")`.
  - `RaiseOn` returns an error wrapping it at once, with no further candidates and no waiting.
  - **`Kick` wakes every waiting `RaiseOn`.** Replace the 1-buffered `wake` channel with a channel that `Kick` closes and replaces under `mu`. A waiter takes the current channel before its attempt round.
  - **Decline backoff.** Within one `RaiseOn`, a manager that *declined* (returned an error) is next tried after a per-manager delay. The delay starts at `r.poll` and doubles on each further decline, capped at `maxDeclineBackoff = 30 * time.Second`. A Kick makes every manager eligible on the next round. A manager that was *unavailable* stays eligible every round.
  - `func (r *Router) ImageFacts(ctx context.Context, image string) (jam.ImageFacts, error)`. It forwards to the first available manager whose `optional(m)` implements `jam.ImageFactsReader`. With none, it returns an error wrapping `jam.ErrManagerUnavailable`. Also add `var _ jam.ImageFactsReader = (*Router)(nil)`.

- [ ] **Step 1: Write the failing tests:**
  - `TestRaiseOnInterruptedDoesNotFallThrough` (Review Focus 1): managers `a` and `b`. `a.Raise` returns `fmt.Errorf("x: %w", jam.ErrRaiseInterrupted)`. `RaiseOn` with timeout 5s returns `errors.Is(err, jam.ErrRaiseInterrupted)` within 100ms, and `b` sees 0 Raise calls.
  - `TestKickWakesAllWaiters`: three goroutines in `RaiseOn`, each with timeout 5s and `poll` set to 1h, on an unavailable `a`. Mark `a` available and `Kick()` once. All three return a placement within 1s.
  - `TestDeclineBackoff`: `poll = 10ms`. `a` always declines. `RaiseOn` with timeout 200ms makes at most 6 Raise calls on `a`, not about 20. Then add a second waiter in which `Kick()` after the first decline causes an immediate retry; assert the call count increments within 5ms of the Kick.
  - `TestUnavailableNotBackedOff`: `a` is unavailable for 30ms and then available. With `poll = 10ms`, `RaiseOn` places on `a` within 60ms, without any Kick.
  - `TestRouterImageFacts`: it forwards to the first available reader. It skips an unavailable one. With none, `errors.Is(err, jam.ErrManagerUnavailable)` holds.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/fleet/`. Expected: FAIL or a build failure.

- [ ] **Step 3: Implement** the changes in `place.go` and `router.go`. Keep plan 1's tests passing unchanged.

- [ ] **Step 4: Run them to make sure they pass.** Same command, then `go test ./internal/jam/`. Expected: `ok`.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/placement.go internal/jam/fleet
git commit -m "feat(fleet): interrupted raises, broadcast wake, decline backoff, image facts"
```

---

### Task 4: `Supervisor.ReconcileManager`

**Files:**
- Create: `internal/jam/manager_reconcile.go`, `internal/jam/manager_reconcile_test.go`

**Interfaces:**
- Consumes: Task 2's `ManagedStudio`, `ManagerInventory` and `ManagerReconcile`; `StandingSessionStore.ListStandingSessions`.
- Produces: `func (s *Supervisor) ReconcileManager(name string, inv ManagerInventory) ManagerReconcile`. It considers instances with `inst.Manager == name` and does four things:
  - **Lost:** every instance in phase `Live` or `Idled` with no inventory entry matching **both** `ActorID` and `Location` is patched to `PhaseLost`, through `patchInstance`. It is listed in `Lost` and logged at Warn as `studio missing from its manager's inventory; marked lost` (`id` and `manager`). The next `Reconcile` tick tears it down.
  - **Raising:** an instance in `Raising` is left alone, because the raise path owns it. An inventory entry with its `ActorID` is not a stray.
  - **Strays:** an inventory entry that matches no non-`Gone` instance on `name` by (actor, location), and is not covered by the Raising rule, goes in `Strays` and is logged at Warn as `manager reports a studio Jam does not know; left running` (`manager`, `actor`, `location`). It is never torn down.
  - **MissingState:** a `ListStandingSessions` entry with `Manager == name` whose `SessionID` is not in `inv.StateOwners` goes in `MissingState`. It is logged at Warn as `pinned standing session's state is missing on its manager; it will start fresh there`. The pin is kept.

- [ ] **Step 1: Write the failing tests** (MemStore, fake launcher):
  - `TestReconcileManagerMarksMissingLost`: instance `x1` (Live, manager `m`, location `L1`), an empty inventory. Then `x1` is `PhaseLost`, and `Lost == ["x1"]`.
  - `TestReconcileManagerLocationMismatch`: `x1` has location `L1` while the inventory has `{x1, L2}`. `x1` is Lost and `{x1, L2}` is a stray.
  - `TestReconcileManagerIgnoresOtherManagers`: `y1` on manager `n` and `z1` with `Manager ""` are untouched.
  - `TestReconcileManagerKeepsIdledPresent`: an Idled instance in the inventory stays Idled.
  - `TestReconcileManagerRaisingUntouched`.
  - `TestReconcileManagerStrays`: an unknown `{q, Lq}` is returned and no teardown is called on the launcher.
  - `TestReconcileManagerTerminatingNotStray`: a Terminating instance present in the inventory is not a stray.
  - `TestReconcileManagerMissingState`: a standing pin to `m` with session `s1`, and `StateOwners` of `[]`. Then `MissingState == ["s1"]` and the pin is still `m`.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/ -run ReconcileManager`. Expected: build failure.

- [ ] **Step 3: Implement.** Use `patchInstance`'s fresh-read pattern, and re-check the phase inside the patch function.

- [ ] **Step 4: Run them to make sure they pass.** Same command. Expected: `ok`.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/manager_reconcile.go internal/jam/manager_reconcile_test.go
git commit -m "feat(jam): reconcile a manager's inventory on connect"
```

---

### Task 5: The manager side: `client.Run`

**Files:**
- Create: `internal/jam/managers/client/client.go`, `client_test.go`

**Interfaces:**
- Consumes: Task 1's `managerpb` and `wire`.
- Produces:
```go
type Config struct {
	Name, Token, Version string
	Capacity  int
	Launcher  jam.Launcher         // required; StateKeeper, ImageTagger, KitDraftBuilder asserted per request
	Images    jam.ImageFactsReader // nil: image-facts requests fail
	Inventory func(ctx context.Context) ([]jam.ManagedStudio, error) // nil: none
	HeartbeatEvery, DeadAfter time.Duration // 0: wire defaults
	Log *slog.Logger // nil: discard
}
// Run opens one Connect stream on cc, sends Hello and waits for Welcome, then
// serves Jam's requests until the stream ends. It returns the stream's error
// (a gRPC status from Jam when refused), or ctx.Err() after ctx ends.
func Run(ctx context.Context, cc grpc.ClientConnInterface, cfg Config) error
```
- Behaviour:
  - **Hello:** `state_owners` comes from `StateKeeper.StateOwners`, when the launcher implements it.
  - **Requests:** each request runs in its own goroutine, with a per-id context. `OpCancel` cancels it.
  - **Sending:** all sends go through one mutex. A multi-frame `Split` is sent under one lock hold, so its frames are never interleaved.
  - **Unsupported operations:** a request the backend cannot serve (no `KitDraftBuilder`, nil `Images`) gets the result error `manager does not support <op>`. An unknown op gets `unknown op <op>`.
  - **Draft logs:** `OpBuildKitDraft` passes the launcher an `io.Writer` that sends each `Write` as `OpLog` frames, at most `MaxFrameData` each, with the request id.
  - **Heartbeats:** it sends a heartbeat every `HeartbeatEvery`, and ends the stream with `heartbeat timeout` after `DeadAfter` with nothing received.
  - **Logging:** request payloads are never logged.

- [ ] **Step 1: Write the failing tests.** They use a tiny in-test `managerpb.StudioManagerServer` over `bufconn`, which plays Jam: it records the Hello, sends Welcome, then sends requests and reads results.
  - `TestRunSendsHelloWithInventoryAndOwners`: checks the name, version, capacity, studios, owners, and that the bearer metadata equals `"Bearer tok"`.
  - `TestRunServesRaise`: a fake launcher returns `"loc-1"` and receives the exact `Spec.ActorID` and `Creds`. The result decodes to `RaiseResult{"loc-1"}`.
  - `TestRunMapsKitNotReady`: the result error satisfies `errors.Is(_, jam.ErrKitNotReady)`.
  - `TestRunStreamsDraftLog`: the launcher writes `"a"` and then `"b"`. The fake Jam sees `OpLog` frames `"a"` and `"b"`, then the result `DraftBuildResult{"img"}`, in that order.
  - `TestRunCancel`: the launcher blocks on ctx. An `OpCancel` from the fake Jam unblocks it within 1s.
  - `TestRunUnsupported`: a launcher without `KitDraftBuilder` gives `manager does not support build-kit-draft`.
  - `TestRunRefused`: the fake Jam returns `status.Error(codes.FailedPrecondition, "version mismatch: jam a, manager b")` before Welcome. `Run` returns that status.
  - `TestRunHeartbeatTimeout`: `DeadAfter = 50ms`, and the fake Jam goes silent after Welcome. `Run` returns within 500ms with an error containing `heartbeat timeout`.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/managers/client/`. Expected: build failure.

- [ ] **Step 3: Implement** `client.go`.

- [ ] **Step 4: Run them to make sure they pass.** Same command. Expected: `ok`.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/managers/client
git commit -m "feat(managers): manager-side Connect client"
```

---

### Task 6: Jam side: per-stream calls and the `Remote` manager

**Files:**
- Create: `internal/jam/managers/conn.go`, `remote.go`, `remote_test.go`

**Interfaces:**
- Consumes: Task 1's `wire`; Task 3's `jam.ErrRaiseInterrupted`; `fleet.Manager`.
- Produces:
```go
// conn is one accepted stream. send is serialized by conn.
type conn struct{ /* send func(*managerpb.ManagerDown) error; pending map[uint64]*call; asm wire.Assembler; done chan struct{} … */ }
func newConn(send func(*managerpb.ManagerDown) error) *conn
// call sends op/req as frames, awaits the OpResult and decodes it into resp
// (nil = ignore body). log (may be nil) receives OpLog chunks for this call.
// ctx done: sends OpCancel, returns ctx.Err(). conn closed: returns an error
// wrapping jam.ErrManagerUnavailable.
func (c *conn) call(ctx context.Context, op string, req, resp any, log io.Writer) error
func (c *conn) deliver(f *managerpb.Frame) error // recv loop; unknown ids are dropped
func (c *conn) close(err error)                  // fails every pending call

type Remote struct{ /* name; log; mu; cur *conn; tags map[jam.KitRef]string */ }
func NewRemote(name string, log *slog.Logger) *Remote
func (r *Remote) attach(c *conn) // makes c current; closes the previous conn; clears tags
func (r *Remote) detach(c *conn) // clears cur only if it is c
```
- `*Remote` implements `fleet.Manager` (`Available()` is true while it has a current conn), `jam.StateKeeper`, `jam.ImageTagger`, `jam.KitDraftBuilder` and `jam.ImageFactsReader`. Add compile-time `var _` assertions.
- **Disconnected:** every method fails with `fmt.Errorf("%w: %s", jam.ErrManagerUnavailable, name)`, except two: `Probe` returns `LivenessUnknown, nil`, and `ImageTag` returns `""`.
- **Raise:** a `Raise` whose conn closes before its result arrives returns `fmt.Errorf("%w: manager %s", jam.ErrRaiseInterrupted, name)` (Review Focus 1). Plain `ErrManagerUnavailable` is reserved for calls made while there was no conn. Spec `Placement` is zeroed before sending.
- **ImageTag and HasKitDraftImage** have no ctx, so they use `context.WithTimeout` of `5s`. `ImageTag` caches the tag per `KitRef` for the current conn, because the answer is a pure function of the ref and the manager's build. A failed lookup is not cached and returns `""`.
- **Probe:** a Probe call error returns `LivenessUnknown, nil` (never reap on a transport error).

- [ ] **Step 1: Write the failing tests.** They drive `conn` with a fake `send`, and a goroutine plays the manager: it decodes requests and `deliver`s results.
  - `TestRemoteDisconnected`: no conn, so `Available()==false`, `Probe` gives `(LivenessUnknown, nil)`, `Teardown` gives `errors.Is(_, jam.ErrManagerUnavailable)`, and `ImageTag` gives `""`.
  - `TestRemoteRaiseRoundTrip`: the location is returned. The sent `RaiseRequest.Spec.Placement` is zero.
  - `TestRemoteRaiseInterrupted`: the conn closes while the manager goroutine holds the request. The error satisfies `errors.Is(_, jam.ErrRaiseInterrupted)`.
  - `TestRemoteCallCancel`: ctx is cancelled mid-call. The call returns `context.Canceled`, an `OpCancel` frame with the same id was sent, and a late result for that id is dropped without panicking.
  - `TestRemoteDraftLog`: `OpLog` frames `"a"` and `"b"` reach the passed writer, in order, before the call returns the image id.
  - `TestRemoteImageTagCached`: two `ImageTag` calls for the same ref send one request. After `attach` of a new conn, the cache is cleared.
  - `TestRemoteAttachReplacesAndFailsOld` (Review Focus 3): there is a pending call on conn A, then `attach(B)`. The A call fails with `ErrManagerUnavailable` (not Raise), and a call on B succeeds. A result delivered to A afterwards does not resolve B's call. Ids are per conn, so B's id 1 and A's id 1 are independent.
  - `TestRemoteLargeKitDefinition`: `PrepareKit` with a 5 MiB file in a link sends more than one `OpPrepareKit` frame, and the manager side reassembles it equal to the original.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/managers/ -run Remote`. Expected: build failure.

- [ ] **Step 3: Implement** `conn.go` and `remote.go`.

- [ ] **Step 4: Run them to make sure they pass.** Same command. Expected: `ok`.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/managers/conn.go internal/jam/managers/remote.go internal/jam/managers/remote_test.go
git commit -m "feat(managers): remote manager adapter over a framed stream"
```

---

### Task 7: The `Hub`: auth, Hello, replacement, revoke, statuses

**Files:**
- Create: `internal/jam/managers/hub.go`, `hub_test.go`, `e2e_test.go`

**Interfaces:**
- Consumes: Task 2's store and types; Task 4's `ReconcileManager` (through an interface); Task 5's `client.Run` (tests only); Task 6's `Remote` and `conn`; `fleet.Router`.
- Produces:
```go
type Reconciler interface {
	ReconcileManager(name string, inv jam.ManagerInventory) jam.ManagerReconcile
}
type Config struct {
	Store      jam.Store
	Router     *fleet.Router
	Reconciler Reconciler // nil: no reconcile
	Instances  func() []jam.Instance // studio counts; nil = Store.ListInstances
	Version    string
	Log        *slog.Logger
	Now        func() time.Time
	HelloTimeout, HeartbeatEvery, DeadAfter time.Duration // 0: wire defaults
}
var ErrManagerHasStudios = errors.New("manager hosts studios")
func NewHub(cfg Config) *Hub
func (h *Hub) Start() error // Router.Add(NewRemote) for each ListManagers record, in order
func (h *Hub) Connect(stream managerpb.StudioManager_ConnectServer) error
func (h *Hub) Add(name string) (jam.ManagerToken, error)    // ValidManagerName, MintToken, AddManager, Router.Add
func (h *Hub) Rotate(name string) (jam.ManagerToken, error) // SetManagerToken; drops the live stream
func (h *Hub) Revoke(name string, force bool) error         // see below
func (h *Hub) Statuses() []jam.ManagerStatus                // Router.Names() order; local included as {Local, Connected: true}
// Studios counts non-Gone instances by manager ("" counts as local).
```
- **The `Connect` sequence:**
  1. Bearer lookup through `ManagerByTokenHash`; a miss is `Unauthenticated` (`unknown manager token`).
  2. The first message must be `Hello` within `HelloTimeout`. Otherwise `InvalidArgument` (`expected hello`), or `DeadlineExceeded`.
  3. The name and version checks from Global Constraints.
  4. `Reconciler.ReconcileManager(name, inventory)`; store the `Strays` in the status.
  5. `Remote.attach(newConn)`, then send `Welcome`, then `Router.Kick()`.
  6. A recv loop, which `deliver`s frames and updates `LastSeen` on every message, and a heartbeat sender. End the stream on recv error or `DeadAfter` silence.
  7. On exit, `Remote.detach` and record `LastSeen`.

  The status for a name records the connected flag, version, capacity, `ConnectedAt`, `LastSeen` and strays.
- **Revoke:**
  - If any non-`Gone` instance has `Manager == name` and `!force`, it returns an error wrapping `ErrManagerHasStudios`, with the message `manager <name> hosts <n> studios; revoke with force to forget them`.
  - Otherwise it calls `RemoveManager`, closes the live conn, and calls `Router.Remove(name)`. The router then probes those instances Dead, so the reconciler forgets them.
  - It logs at Warn when force dropped studios.
- `Add` and `Rotate` never log the token.

- [ ] **Step 1: Write the failing tests.** `hub_test.go` runs a real gRPC server on `bufconn` with `Hub` registered, a `MemStore`, and `fleet.New(nil)`. `e2e_test.go` adds `client.Run` with a fake launcher.
  - `TestHubRejectsBadToken`: `codes.Unauthenticated`.
  - `TestHubRejectsNameMismatch`: a token for `a` with Hello name `b` gives `codes.PermissionDenied`.
  - `TestHubRejectsVersionMismatch`: `codes.FailedPrecondition`, with the message `version mismatch: jam v1, manager v2`.
  - `TestHubHelloTimeout`: `HelloTimeout=50ms` and no message sent; the stream ends with a non-OK status.
  - `TestHubStartRegistersStoredManagersDisconnected` (Review Focus 2): store records `m1` and `m2`, then `Start()`. `Router.Names()` is `[m1 m2]`, and `Router.Probe` of an instance on `m1` gives `(LivenessUnknown, nil)`.
  - `TestHubConnectMakesAvailableAndReconciles`: a fake `Reconciler` receives the Hello's inventory. After Welcome, `Router.HasManager("m1")` holds and `Statuses()` shows `Connected` with the version and capacity. A `RaiseOn` that was waiting on `m1` with timeout 5s completes after connect, which shows the Kick.
  - `TestHubReplacesStream` (Review Focus 3): connect client A, then client B with the same token. A's `Run` returns, B serves a Raise, and Statuses shows one connection.
  - `TestHubDisconnectProbesUnknown`: cancel the client. `Probe` gives Unknown, Statuses shows `Connected=false`, and `LastSeen` is set.
  - `TestHubHeartbeatTimeout`: `DeadAfter=50ms`, with a fake client that sends Hello and then nothing. The hub ends the stream and the manager is unavailable within 500ms.
  - `TestHubRevoke`:
    - With a live instance on `m1` and no force, it returns `ErrManagerHasStudios`.
    - With force, the stream is closed, `Router.HasManager("m1")==false`, and a reconnect with the old token is `Unauthenticated`.
  - `TestHubRotate`: the old stream closes, the old token is refused, and the new token connects.
  - `TestHubAddRefusesLocal`: `Add("local")` errors with `manager name "local" is reserved`.
  - `TestE2ERaiseTeardownViaSupervisor`:
    - Setup: `jam.NewSupervisor` drives the router, with only remote `m1` connected through `client.Run` and a fake launcher.
    - `sup.Raise` gives an Instance with `Manager == "m1"` and the fake's location.
    - `sup.Teardown` reaches the fake.
    - Kill the client and redo `sup.Reconcile` with an expired lease: the instance is not removed (Unknown).
  - `TestE2ESecretsNotLogged` (Review Focus 5): the same raise path, with the hub's and supervisor's logger writing to a buffer at Debug. After the raise, the buffer contains neither `creds.LaunchSecret` nor `creds.IdentityToken`. Read them from the fake launcher's received `LaunchCreds`.
  - `TestE2EDraftBuildLogStreams`: `Router.BuildKitDraft` returns the image id, and the writer got the fake's log bytes.
  - `TestE2EReconnectReconciles`: an instance on `m1` whose actor is not in the new Hello's inventory becomes `PhaseLost` after reconnect. Use the real Supervisor as the `Reconciler`.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/managers/`. Expected: build failure.

- [ ] **Step 3: Implement** `hub.go`.

- [ ] **Step 4: Run them to make sure they pass.** Run `go test ./internal/jam/managers/... ./internal/jam/fleet/ ./internal/jam/`. Expected: `ok`.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/managers
git commit -m "feat(managers): StudioManager.Connect hub with token auth and reconnect handling"
```

---

### Task 8: Admin API, adminclient and `at-jam manager` verbs

**Files:**
- Create: `internal/jam/managers/admin.go`, `admin_test.go`, `cmd/at-jam/managers.go`, `cmd/at-jam/managers_test.go`
- Modify: `internal/jam/adminclient/adminclient.go`, `cmd/at-jam/main.go` (the command table: `{Name: "manager", Brief: "manage Studio Managers (add|list|rotate|revoke) via the admin API", Run: cmdManager}`)

**Interfaces:**
- Consumes: Task 7's `Hub`.
- Produces:
  - `func AdminRoutes(h *Hub) []jam.AdminOption`, mounted through `jam.WithAdminRoute`. The routes:

    | Route | Responses |
    |---|---|
    | `GET /admin/managers` | 200 `[]jam.ManagerStatus` |
    | `POST /admin/managers` with body `{"name":"…"}` | 201 `jam.ManagerToken`; 409 on `ErrManagerExists`; 400 on an invalid name |
    | `POST /admin/managers/{name}/rotate` | 200 `jam.ManagerToken`; 404 for an unknown name |
    | `DELETE /admin/managers/{name}?force=true` | 204; 404; 409 on `ErrManagerHasStudios`, with its message |

    Errors use the admin API's existing JSON error shape. Look at how `admin.go` writes them and reuse that helper; export it if needed.
  - adminclient: `Managers() ([]jam.ManagerStatus, error)`, `AddManager(name string) (jam.ManagerToken, error)`, `RotateManager(name string) (jam.ManagerToken, error)`, `RevokeManager(name string, force bool) error`.
  - CLI `at-jam manager`:
    - `add <name>` and `rotate <name>` print `manager <name> token (shown once): <token>` to stdout, plus one hint line: `put it in the manager's config; Jam stores only its hash`.
    - `list` prints one line per manager: `<name>\tconnected=<bool>\tversion=<v>\tstudios=<n>\tcapacity=<c>\tlast-seen=<RFC3339|->\tstrays=<n>`.
    - `revoke <name> [--force]` prints `revoked <name>`.
    - The flags follow `cmdModelSpec` (`--app`, `--admin-url`, `--token`).

- [ ] **Step 1: Write the failing tests.**
  - `admin_test.go` uses an `httptest` server over `jam.NewAdminHandler(… , AdminRoutes(hub)...)` with `LoopbackAuthenticator`. It checks each route's status codes as listed, that the POST body's `token` is non-empty, and that GET then lists the name.
  - `cmd/at-jam/managers_test.go` runs `cmdManager` against an `httptest` admin server. It checks the add output line format, the list line format for one connected and one disconnected manager, the revoke 409 message on stderr with exit 1, and `--force` succeeding.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/managers/ ./cmd/at-jam/ -run 'Admin|Manager'`. Expected: build failure.

- [ ] **Step 3: Implement** the handlers, client methods and verbs.

- [ ] **Step 4: Run them to make sure they pass.** Same command. Expected: `ok`.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/managers/admin.go internal/jam/managers/admin_test.go internal/jam/adminclient cmd/at-jam
git commit -m "feat(jam): manager registry admin API and at-jam manager verbs"
```

---

### Task 9: Wire it into `at-jam serve`

**Files:**
- Modify: `cmd/at-jam/config.go` (`Runtime.StudioManagers bool \`yaml:"studio-managers"\``), `cmd/at-jam/main.go` (`buildLauncher`, serve wiring), `cmd/at-jam/serve_wiring_test.go`

**Interfaces:**
- Consumes: Tasks 3, 7 and 8.
- Produces:
  - **`builtLauncher` gains `router *fleet.Router`.** `buildLauncher` builds a Router when `runtime.launcher` is set **or** `runtime.studio-managers: true`. `placeholderLauncher` remains only when neither is set.
  - **Local images.** With a launcher block, the local manager is `fleet.Local(localLauncher{Launcher: colimaLauncher, ImageFactsReader: imagefacts.New(…)})`. `localLauncher` is a new two-field struct in `main.go`.
  - **Defaults whenever there is a router:** `images` is the router, and the default studio kit is seeded.
  - **With `studio-managers`, serve:**
    1. builds `managers.NewHub(managers.Config{Store: st, Router: router, Reconciler: sup, Version: version, Log: log})`;
    2. calls `hub.Start()` **before** `go sup.Run(…)` (Review Focus 2);
    3. registers it with `managerpb.RegisterStudioManagerServer(gs, hub)`;
    4. appends `managers.AdminRoutes(hub)...` to the admin options;
    5. adds the gauges `jam_managers` ("Studio Managers registered.") and `jam_managers_connected` ("Studio Managers connected.") to the metrics gauge list.

    Without it, none of these happen, and `/admin/managers` is not mounted (404).
  - **Log** `Jam studio managers: accepting connections` with `registered=<n>`.

- [ ] **Step 1: Write the failing tests** in `serve_wiring_test.go`:
  - `TestBuildLauncherStudioManagersOnly`: with `studio-managers: true` and no launcher block, `got.launcher` is a `*fleet.Router` with no names, `got.router != nil`, and `haveDefaultKit` holds.
  - The existing no-block test still gets `placeholderLauncher`.
  - `TestStartManagersBeforeRun`: serve calls a new helper, `func startManagers(hub *managers.Hub, run func()) error`, which calls `hub.Start()`, returns its error without calling `run`, and otherwise calls `run`, which `go`es `sup.Run`. The test seeds a stored manager `m1` and passes a `run` that asserts `router.HasManager("m1")`.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-jam/ -run 'BuildLauncher|Hub'`. Expected: FAIL.

- [ ] **Step 3: Implement** the wiring.

- [ ] **Step 4: Run them to make sure they pass.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./cmd/at-jam/ ./internal/jam/...`. Expected: `ok`.

- [ ] **Step 5: Commit.**
```bash
git add cmd/at-jam
git commit -m "feat(at-jam): serve accepts Studio Manager connections (runtime.studio-managers)"
```

---

### Task 10: Admin UI managers panel, then the docs

**Files:**
- Modify: `internal/jam/adminui/adminui.go` (`WithManagers`), `templates/dashboard.html`, `templates/coves.html` (Manager column), `adminui_test.go`; `cmd/at-jam/main.go` (pass `adminui.WithManagers(hub.Statuses)` when the hub exists)
- Create: `docs/usage/jam/studio-managers.md`
- Modify: `docs/usage/jam/INDEX.md`, `docs/usage/jam/coves.md` ("Managers and placement": remote managers exist; link the new doc), `docs/usage/jam/serve.md` (the `runtime.studio-managers` row)

**Interfaces:**
- Consumes: `jam.ManagerStatus`; `CoveSummary.Manager` from plan 1.
- Produces: `func WithManagers(list func() []jam.ManagerStatus) Option`.
  - The dashboard renders a "Managers" panel when the option is set. It has one row per status: name, connected or disconnected, version, studios/capacity, last seen, strays count, and the strays' actor ids in a `title` attribute.
  - Without the option, there is no panel.
  - The studios table gains a Manager column.
  - The UI is read-only; rotate and revoke stay on the CLI and API.

- [ ] **Step 1: Write the failing tests:**
  - `TestDashboardManagersPanel`: with `WithManagers` returning `mac-1` (connected, 2 studios, capacity 4) and `local`, the page contains `mac-1`, `connected` and `2/4`.
  - Without the option, the page has no `Managers` heading.
  - `TestCovesTableManagerColumn`: a summary with `Manager: "mac-1"` renders `mac-1`.

- [ ] **Step 2: Run them to make sure they fail.** Run: `GOPROXY=https://proxy.golang.org GOFLAGS= go test ./internal/jam/adminui/ -run 'Managers|ManagerColumn'`. Expected: FAIL.

- [ ] **Step 3: Implement** the option, the templates and the main wiring.

- [ ] **Step 4: Run them to make sure they pass.** Same command. Expected: `ok`.

- [ ] **Step 5: Write the docs** with the docs-author skill. `studio-managers.md` gets frontmatter (what it is, plus "read when you register, rotate or revoke a Studio Manager, or debug a manager connection") and covers:
  - enabling it with `runtime.studio-managers`;
  - the `at-jam manager` verbs, and that the token is shown once;
  - how a manager connects: the cove-facing TLS port, bearer token, exact version match, and one stream per name;
  - what happens on disconnect: Probe Unknown with no reap; Teardown and Pause fail until it is back; the reconciler retries teardown;
  - reconnect reconciliation: missing studios become Lost, and strays are reported and left running;
  - that revoke refuses while studios are hosted, and that `--force` forgets them;
  - that managers are not in config export, so a restored Jam re-registers them;
  - the metrics names.

  The doc states that the manager binary arrives in plan 3, and links it from coves.md, serve.md and INDEX.md. Facts already in coves.md (placement) are linked, not repeated.

- [ ] **Step 6: Run docs-audit.** Run it on the repo (docs-audit skill). Expected: no orphans, no dangling links, and no oversize docs.

- [ ] **Step 7: Commit.**
```bash
git add internal/jam/adminui cmd/at-jam docs
git commit -m "feat(adminui): managers panel; docs for Studio Managers"
```

---

## Self-review notes

- **Spec coverage:**

  | Spec requirement | Task |
  |---|---|
  | Transport, Hello and requests | 1, 5, 6, 7 |
  | Kit definitions and chunking | 1, 6 |
  | Secrets | 7 (Review Focus 5) |
  | Auth token, one stream per name | 2, 7, 8 |
  | TLS: the cove-facing mux | 9 (the manager's own CA config is plan 3) |
  | Disconnects and reconciliation | 4, 6, 7 |
  | Teardown retry: the existing `Reconcile` re-drives Terminating and Lost every tick | covered by existing behaviour |
  | Admin visibility and metrics | 8, 9, 10 |
  | Version match | 7 |
  | Stays in-process until removed | 9 (`local` unchanged) |

- **Deferred, and named in the header:** labels, the log tail, and per-manager kit operations. The spec's async events (a kit build finishing, a studio dying) are also deferred. Colima prepares synchronously, and a dead studio is already caught by Attach and Probe. The op namespace allows adding them later.
