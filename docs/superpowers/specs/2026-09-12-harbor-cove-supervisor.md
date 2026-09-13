# harbor: managed-cove supervisor spine (COV-149)

**Status:** design approved, pre-plan
**Issue:** COV-149 (blocks COV-146)
**Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` — the
resident dispatcher, the durability/always-on driver, and "one thin runtime for
every managed actor (Worker *and* Manager)."

## Summary

Build the **supervisor spine**: the durable, leased **runtime registry** and
**lifecycle state machine** that harbor uses to manage a cove from the moment it
is raised to the moment it is torn down. This is the foundation the resident
webhook dispatcher (COV-146) and standing Managers sit on — dispatch becomes one
*trigger* that calls `raise`.

This slice is **pure, kit-free, grpc-free, and hermetic**: a fake `Launcher`
stands in for a real backend, status arrives through an in-memory seam rather
than a gRPC stream, and the whole thing is driven by `at-harbor cove` admin
verbs. The gRPC `Attach` transport, the real backend raise, and the webhook
dispatcher are explicitly deferred to later slices.

## Why this comes first

The shipped `internal/dispatch/scheduler.Engine` cannot be made "resident in
harbor" as-is, because the new runtime model breaks three of its load-bearing
assumptions:

| Shipped scheduler | New managed-cove model |
|---|---|
| **Synchronous** — `exec.Run` blocks on `at-cove work` until the subprocess exits, then brokers the result. | The cove is **self-managing and async** — it may wait, suspend, converse, or escalate overnight and self-terminate when its unit resolves. Raise returns a *location*; status arrives *later*. |
| **In-process state** — the `live` set and concurrency semaphores are per-process. | **Multiple harbor instances** may run; ownership must be shared state with **leases**, not channels in one process. |
| **Restart orphans work** — a restart loses the in-flight set; the reaper parks orphaned IN-PROGRESS issues to NEEDS-INPUT (gives up). | **In-progress work survives restart** — the registry persists each live cove, and harbor **re-adopts** on restart instead of abandoning. |

All three land on one missing thing: a durable, shared, **leased runtime
registry + a lifecycle supervisor**. One supervisor serves every managed actor —
Worker and Manager alike.

## The one fault line

**Identity and runtime are separate entities.** The roster `Actor` (COV-143) is
a durable *identity* — `{id, tokenHash, grants[], expiry}`, the thing that holds
a token and is authorized at the broker. A managed cove's *runtime* is a new,
ephemeral `Instance` — `{location, phase, activity, lease, …}` — keyed by the
Actor's id. `raise` composes `enroll` (mint identity) **+** create `Instance`
(record runtime); `teardown` composes `Launcher.Teardown` **+** deregister
`Instance` **+** `revoke` (remove identity). The roster stays identity-only; a
standing Manager is one durable `Actor` with one long-lived `Instance`; an
ephemeral Worker is both, torn down together. This keeps the security surface
(identity, tokens, grants) from entangling with the operational surface
(location, status, leases).

## Data model

### The `Instance` record (new)

```go
// Phase is supervisor-owned: only the supervisor writes it, via raise / teardown
// / reconcile. It is never set from a cove-reported value.
type Phase string
const (
    PhaseRaising     Phase = "raising"     // raise in flight; Launcher.Raise called, not yet confirmed Live
    PhaseLive        Phase = "live"        // running and leased; Activity is meaningful
    PhaseTerminating Phase = "terminating" // teardown decided (Done, or Lost); Launcher.Teardown in flight
    PhaseGone        Phase = "gone"        // torn down + deregistered (terminal; row removed)
    PhaseLost        Phase = "lost"        // reconciler declared it dead (lease expired + Probe dead) → Terminating
)

// Activity is cove-reported: it is only meaningful while Phase == Live. The
// supervisor records what the cove says it is doing; it never drives teardown
// except via the single rule Activity==Done ⇒ Phase=Terminating.
type Activity string
const (
    ActivityRunning Activity = "running"
    ActivityWaiting Activity = "waiting"
    ActivityBlocked Activity = "blocked"
    ActivityDone    Activity = "done"
)

type Lease struct {
    Holder string    `json:"holder"` // holderID of the harbor process that owns this Instance
    Expiry time.Time `json:"expiry"` // lease valid until; past expiry, any process may steal
}

type Instance struct {
    ActorID  string    `json:"actor_id"` // FK → roster Actor.ID (stable id, not tokenHash)
    Project  string    `json:"project"`
    Role     string    `json:"role"`
    Unit     string    `json:"unit,omitempty"`     // unit of work, e.g. "AET-42"; "" for a standing Manager
    Backend  string    `json:"backend"`            // which backend raised it (teardown routing)
    Location string    `json:"location,omitempty"` // opaque handle from Launcher.Raise (endpoint/machine-id)
    Phase    Phase     `json:"phase"`
    Activity Activity  `json:"activity,omitempty"`
    Lease    Lease     `json:"lease"`
    RaisedAt time.Time `json:"raised_at"`
    LastSeen time.Time `json:"last_seen"` // last status report / heartbeat
}
```

`Location` is an **opaque string** this slice (the fake `Launcher` returns a
synthetic handle). Later slices give it structure per backend; the supervisor
never parses it, it only hands it back to the `Launcher`.

### Store (v5, additive)

`storeFile` gains one field, loaded like `Kits` was (structural, additive — a v4
file with no `instances` key loads with an empty map):

```go
type storeFile struct {
    Roles        map[string]map[string]Role `json:"roles"`
    Actors       map[string]Actor           `json:"actors"`
    Destinations map[string]Destination     `json:"destinations"`
    Kits         map[string]Kit             `json:"kits"`
    Instances    map[string]Instance        `json:"instances"` // NEW — keyed by Instance.ActorID
}
```

New `Store` interface methods (mirroring the kit/role method shape; all
deep-copy on read, like `GetKit`/`ListKits`, to avoid handing out live map
references):

```go
PutInstance(i Instance) error              // upsert by ActorID
GetInstance(actorID string) (Instance, bool)
ListInstances() []Instance                 // deep-copied
RemoveInstance(actorID string) error
```

**Single-node caveat (stated honestly).** `FileStore` is documented
single-node, sole-writer. This slice designs the **lease fields and steal
semantics into the schema** so horizontal scaling is a later *flip*, not a
rewrite — but it does **not** make the JSON `FileStore` safe to share across
processes. True multi-instance needs a concurrent-safe store backend, which is
its own future concern. The lease *logic* (acquire/renew/steal/expiry) is fully
built and tested now against the single-process store; a shared store later
reuses it unchanged.

## Lease model

- Each harbor process mints a **`holderID`** at startup (`hostname + "/" + pid +
  "/" + 4 hex`, via `crypto/rand` — same idiom as the scheduler's `runID`).
  Passed into the supervisor at construction.
- **Acquire:** `raise` writes `Lease{Holder: self, Expiry: now + TTL}`.
- **Renew:** the owning process renews `Expiry = now + TTL` on a ticker (every
  `TTL/3`) for every Instance it holds, and opportunistically on each status
  report it processes.
- **Steal:** any process may take over an Instance whose `Lease.Expiry < now`
  (the holder is presumed dead). Stealing rewrites `Lease.Holder = self`. This
  same path is the slice-2 reconnect handoff (a cove that reconnects to a
  different harbor process causes that process to steal the lease).
- Defaults live in the serve config (see below): `lease-ttl ≈ 60s`,
  `reconcile-interval ≈ 30s`. `TTL` must be comfortably larger than the
  reconcile interval so a live owner never loses its own lease between renews.

## Lifecycle state machine

`Supervisor` is a struct holding the `Store`, a `Launcher`, the `holderID`, a
clock seam (`now func() time.Time`, for hermetic tests), and the durations. Its
operations:

- **`Raise(ctx, RaiseSpec) (Instance, error)`**
  1. `enroll` the identity Actor (reuse COV-143 `Enroll`: id, project, role) —
     yields the Actor + token.
  2. `Launcher.Raise(ctx, spec)` → `Location` (fake this slice).
  3. `PutInstance` with `Phase:Raising → Live`, lease held by `self`,
     `RaisedAt = now`, `Activity: Running`.
  4. If `Launcher.Raise` fails, the enroll is rolled back (`revoke`) so a failed
     raise leaves no dangling identity. (Fail-closed, mirrors enrollment.)

- **`Report(ctx, actorID, Activity) error`** — the cove (admin verb this slice;
  stream in slice 2) reports activity. Sets `Activity`, `LastSeen = now`, renews
  the lease. **Ownership rule:** a report never writes `Phase`; the *only* phase
  effect is `Activity==Done ⇒ Phase=Terminating` (then teardown proceeds). A
  report for an Instance this process does not hold steals the lease first (the
  cove is talking to us now).

- **`Teardown(ctx, actorID) error`** — `Phase:Terminating`, `Launcher.Teardown`,
  then `RemoveInstance` + `revoke` the Actor. Idempotent: tearing down an
  already-Gone/absent Instance is a no-op (mirrors the broker's idempotent
  single-writer transitions).

- **`Reconcile(ctx) error`** (the generalized reaper) — for every Instance whose
  `Lease.Expiry < now` **and** that no live local run owns:
  `Launcher.Probe(ctx, inst)`; if dead → `Phase:Lost` → teardown; if alive →
  steal + renew (adopt it). Run on the reconcile ticker and **once at startup**
  — startup reconcile is exactly the **restart re-adoption** that makes
  in-progress work survive a restart.

Transition table (the test oracle):

```
                     Launcher      Store effect
raise:      —      → Raise()     → PutInstance{Raising→Live}, lease=self
report(A):  Live   → —           → Activity=A, LastSeen, renew (steal if not ours)
report(Done):Live  → —           → Phase=Terminating  (then teardown)
teardown:   any    → Teardown()  → RemoveInstance + revoke   (idempotent)
reconcile:  expired+Probe=dead   → Teardown()/Phase=Lost → RemoveInstance + revoke
reconcile:  expired+Probe=alive  → steal + renew (adopt)
```

## The `Launcher` seam

The kit-aware/backend-aware raise is **not** in this slice. The supervisor
depends only on an interface, so it stays kit-free and grpc-free and fully
hermetic:

```go
type Liveness int
const ( LivenessAlive Liveness = iota; LivenessDead; LivenessUnknown )

type RaiseSpec struct {
    ActorID, Project, Role, Unit string
    // later slices add: Kit, image, backend selection, connector env
}

type Launcher interface {
    Raise(ctx context.Context, spec RaiseSpec) (location string, err error)
    Teardown(ctx context.Context, inst Instance) error
    Probe(ctx context.Context, inst Instance) (Liveness, error)
}
```

The **fake** `Launcher` (test-only) records calls and returns scripted
locations/liveness, so tests drive the full state machine deterministically. The
real launcher (backend + Role→Kit→image + connector injection, reusing COV-142)
is a later slice, wired from `cmd/at-harbor` where importing `internal/kit` and
`internal/backend` is allowed.

## Package placement & boundary

- The `Instance` type, `Store` methods, and `Supervisor` live in
  **`internal/harbor`** (new files `instance.go`, `supervisor.go`). This package
  is already the home of the store, broker, and admin API, and the supervisor is
  **kit-free and grpc-free**, so the boundary — *`internal/harbor` never imports
  `internal/kit`* — is preserved.
- The `Launcher` interface is defined in `internal/harbor`; its **fake** impl is
  test-only; its **real** impl arrives in a later slice in `cmd/at-harbor`.
- Verified the same way as the go-oidc gate: `go list -deps ./internal/harbor |
  grep -iE 'internal/kit|grpc'` stays empty for this slice.

## Admin surface (slice-1 driver)

New `at-harbor cove` verb group, client of the admin API like every other verb
(`--app`/`--admin-url`/`--token`; loopback-or-OIDC gated; `operator=` logged on
mutations):

```
at-harbor cove raise    --id spider-42 --project acme --role guest [--unit AET-42]
at-harbor cove list                          # actorID  role  unit  phase  activity  lease-holder  age
at-harbor cove status   --id spider-42 --activity waiting   # report activity (stands in for the stream)
at-harbor cove teardown --id spider-42
```

- `cove raise` uses the **fake** launcher this slice — it exercises the registry
  + state machine end-to-end without a real cove. (A doc note makes clear this is
  the spine; real raise lands with the backend slice.)
- Wire types mirror the existing admin shape (`…Body`/`…Summary`, never leaking
  a token or hash). `cove list`/`status` are read/much like `roster`.
- Admin routes: `POST /admin/coves` (raise), `GET /admin/coves` (list),
  `POST /admin/coves/{id}/status`, `DELETE /admin/coves/{id}` (teardown).

## Serve config

Two optional keys under a new `runtime:` block (bootstrap-only, like the rest of
the serve config), with the defaults above when omitted:

```yaml
runtime:
  lease-ttl: 60s            # how long a lease is valid without renewal
  reconcile-interval: 30s   # how often the reconciler runs (and the renew cadence base)
```

The reconciler + renew ticker start with the serve process when `admin-listen`
is configured (the supervisor only matters where the admin API / future stream
lives). A serve process with no `runtime:` block still runs the supervisor with
the defaults — the registry is always available to the admin verbs.

## Testing

Hermetic, no network, no real backend — `internal/runner.Fake` is not even
needed because the `Launcher` is faked directly:

- **Store (v5):** additive load of a v4 file (empty `instances`); round-trip
  `PutInstance`/`GetInstance`/`ListInstances`/`RemoveInstance`; deep-copy on read
  (mutating a returned `Instance` does not corrupt the store — the same race-class
  bug fixed in `GetKit`).
- **Lease:** acquire on raise; renew extends expiry; steal only when expired;
  a live owner never loses its lease across a reconcile tick.
- **State machine:** the full transition table above, using the fake `Launcher`
  and the injected clock — including `Activity==Done ⇒ Terminating ⇒ teardown`,
  idempotent teardown, and the ownership rule (a report never writes Phase).
- **Reconciler:** expired + `Probe` dead → Lost → teardown + revoke; expired +
  `Probe` alive → adopt (steal + renew); a non-expired Instance is untouched.
- **Restart re-adoption:** construct a fresh `Supervisor` over a store
  pre-seeded with a Live Instance (simulating a restart); the startup reconcile
  adopts the alive one and reaps the dead one.
- **Admin verbs:** against the in-memory admin server (the existing admin-test
  harness), asserting the routes + that no token/hash is ever emitted, and that
  pre-existing admin tests still pass (append, don't overwrite).

`go test -race` is unavailable in the sandbox (no C toolchain); concurrency
correctness is argued by construction (store methods hold the mutex; deep-copy
on read) and by a functional concurrent-access test, as in COV-144.

## Deferred (explicitly out of scope for COV-149)

1. **gRPC `Attach` stream** (slice 2): bidi, lifecycle-control only. Cove
   streams `StatusUp` (Running/Waiting/Blocked/Done/heartbeat); harbor streams
   `ControlDown` (Wake/Teardown/TierChanged). Stream-liveness ↔ lease;
   reconnect ↔ lease-steal; `:443`-multiplexed alongside the broker; squid
   CONNECT-tunnels it opaquely. The comms hub (COV-145) later reuses the same
   stream for message payloads.
2. **Real backend raise** (slice 3): `Launcher` backed by a backend +
   Role→Kit→image + connector injection (reuse COV-142). Colima/local first;
   **Fly is its own slice**.
3. **COV-146** (slice 4): resident webhook dispatcher as a *trigger* → `raise` a
   Worker for a Role; tracker-as-queue nudge; broker-on-terminal-status (single
   writer).
4. **Concurrent-safe shared store** for true multi-instance operation (the
   lease *model* is ready; the store backend is not).

## Docs

Per AGENTS.md, the same change updates `docs/usage/harbor/` — a new leaf
`coves.md` (the runtime/supervisor operator story + `at-harbor cove` verbs),
linked from `docs/usage/harbor/INDEX.md`, with `operators.md`'s verb list and
`serve.md`'s config table updated for the `runtime:` block. Design rationale
stays here (linked design history), not copied into the manual.
