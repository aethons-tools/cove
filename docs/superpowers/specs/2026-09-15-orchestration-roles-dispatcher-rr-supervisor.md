# Orchestration roles: Dispatcher, Robot Resources, Harbor Master

**Status:** design conversation captured; conceptual model agreed; **pre-plan**.
This is a forward-looking role decomposition, not yet scoped for implementation —
three genuine open questions (below) remain before a plan can be written.
**Motivation:** the orchestration responsibilities inside `at-harbor serve` are
currently collapsed into two clusters with fuzzy edges. This doc names three
roles with clean boundaries, so capacity policy can grow without touching
matching or execution.
**Touches (eventual):** `internal/dispatch` / the resident dispatcher, the
supervisor + `runtime.launcher` (`internal/backend`), the `runtime.dispatcher`
config, and a new allocation layer. Nothing here is built yet.

## The problem

Today two clusters own everything (see [dispatcher.md](../../usage/harbor/dispatcher.md),
[coves.md](../../usage/harbor/coves.md)):

- **The resident dispatcher** polls ready tickets → dedups → **checks the
  `max-concurrent` cap** → claims the ticket → **calls raise**. It owns matching
  *and* admission *and* the trigger.
- **The supervisor + Launcher** own the Instance registry (Phase/Activity),
  leases, reconcile, self-heal, wake/idle, and the Colima execution.

The capacity concern (the cap) is buried inside the matcher, enforced on the
*live* instance count. That is why per-role caps, a warm pool, and standing coves
are all awkward today: there is no component that owns "what should exist."

## The three roles

| Role | Responsibility | One-line boundary |
|---|---|---|
| **Dispatcher** | Match a unit of work to a **role**, then request a cove for it. | Demand producer. No counting, no raising. |
| **Robot Resources (RR)** | The **allocator**: hold the reservation ledger, enforce per-project/per-role capacity, grant/deny/queue. | Source of truth for *what coves should exist*. |
| **Harbor Master** | Turn desired reservations into running coves and keep them alive. = **Supervisor** (reconcile brain) + a **pool of launchers** (one per cove-hosting mechanism). | Source of truth for *what coves actually exist*. |

## Core principle: the reservation is the only currency

Everything outside RR sees exactly one abstraction — a **reservation** (a handle
to a cove for a role, optionally bound to a unit of work). The dispatcher asks
for one and never learns *how* it was satisfied. The Supervisor consumes
reservations and never learns *why* they were allocated.

RR internally splits each project's per-role concurrency into **tiers** — for
example `role worker: 1 standing, 2 assignable, 4 ephemeral`:

- **standing** — always-on; a reservation with no unit and no expiry (a
  teammate/manager cove). Never released.
- **assignable** — a warm cove reused across units; a reservation *leases* it and
  **returns it to the pool** on release rather than tearing it down.
- **ephemeral** — one unit, then the cove is torn down on release.

These tiers are **RR's private concern**. Outside RR they are all just
reservations on a cove. RR can start as a trivial per-tier counter and grow
arbitrarily sophisticated internally — fairness, priority, demand prediction,
pre-warming — with zero blast radius, because the reservation abstraction hides
all of it. A single reservation object covers both transient (unit-bound) and
standing (unbound) demand: a standing teammate is just a reservation with no unit
and no expiry.

## Topology: desired-state in, actual-state out — no directing

The dispatcher talks to **only** RR. RR does **not** imperatively command the
Supervisor; it **records desired reservations in durable state**, and the
Supervisor **reconciles** reality toward them. Nobody issues "do this now" across
the RR↔Supervisor seam.

```
                 workload (role, unit, prompt-ref)
   Dispatcher ───────────────────────────────────▶ Robot Resources
      ▲                                              │  (admits → appends
      │ reservation handle / backpressure            │   ReservationGranted;
      └──────────────────────────────────────────────   ledger = a projection)
                                                      │
                        desired reservations (durable event stream)
                                                      │
                                                      ▼
                                              Harbor Master
                                        ┌───────────────────────┐
                                        │ Supervisor (reconcile) │  folds granted
                                        │        +               │  reservations vs
                                        │ launcher pool          │  cove lifecycle,
                                        │ (Colima / cloud / …)   │  converges reality,
                                        └───────────────────────┘  reports releases/
                                                      │             liveness back up
                                                      ▼
                                                   coves
```

**Why declarative, not "RR directs the Supervisor":** if RR imperatively
commanded the Supervisor, RR would inherit execution reliability — a Supervisor
crash mid-raise forces RR to track acks/retries, entangling allocation state with
execution state. With reconcile-from-durable-state, a crash anywhere is re-derived
on the next pass; self-heal is free. This is already how the supervisor behaves
(it re-adopts live Instances from the store on restart).

**Why the reconcile brain lives with the launchers, not with RR:** reconcile is
inseparable from execution ground truth (is the cove alive? lease expired?
reported `done`?). Co-locating it with the launchers that observe that truth keeps
each of the two feedback loops — allocation accounting (RR) and execution ground
truth (Supervisor) — sealed inside one component. The reservation ledger is the
seam between them: desired-state in, released/liveness counts out.

**Bonus — the assignable tier costs nothing extra in this shape:** "raise a fresh
cove" and "push a new workload onto warm cove W" are both just *converge cove W to
its desired workload*. The reconcile loop absorbs reuse with no special case; an
imperative "RR tells the Supervisor to raise" would not.

## Event-sourced substrate

The seam is an **append-only event stream**, and the durable stores become
**projections** over it. This fits because the codebase already builds this
substrate: the intercom/squawk log is append-only with a monotonic `Seq` and a
durable commit cursor + seekable reads. Event sourcing here is applying a proven
in-house pattern, not importing a foreign one. It also gives harbor (a security
boundary) a first-class audit trail — who reserved what, which cove ran which
unit, what scope its token got — aligned with the standing view that these logs
are durable, indefinitely-retained records.

**The discipline line — event-source decisions and lifecycle transitions, not
sensor readings:**

- **Events (retained, source of truth):** `ReservationRequested / Granted /
  Denied / Queued / Released`, `StandingDeclared`, `CoveRaiseIntended / Live /
  Lost / Idled / Woken / TornDown`, `WorkloadAssigned`, `TokenScoped`.
- **Current-state with a TTL (NOT an event per tick):** leases, heartbeats,
  liveness pings. Their history is worthless and would flood the stream; only
  their meaningful *transitions* (`Live→Lost`, `Idled→Live`) become events.
- **Referenced, not embedded:** the workload prompt and other large payloads —
  the event carries a reference, RR never parses it.

**Two consequences, both good:**

- **RR and the Instance registry become folds, not mutable stores.** RR folds
  demand + grant/release events into per-tier/per-role counts. It makes an
  admission decision by appending `ReservationGranted` **with an expected-version
  / version-pinned append** — the same primitive the squawk log already uses. That
  is what makes caps safe under concurrency: two racing grants cannot both win an
  append at the same version, so a cap cannot be overshot. This also pre-pays the
  multi-instance-harbor future the dispatcher doc flags as deferred. Without
  version-pinned appends, an event log alone would happily double-grant off a
  stale projection.
- **Keep the orchestration stream separate from the squawk/intercom comms log.**
  Same append-log machinery, different domain, different consumers and retention.
  Having just de-overloaded "message," do not re-overload "the log": one stream
  for coordination events, one for comms.

## Mapping to what exists today

- **Dispatcher** — slim the resident dispatcher down: keep poll/dedup/claim
  (matching a ticket to a role via handler class), drop the `max-concurrent` cap
  and the direct `raise` call.
- **Robot Resources** — new. The cap moves here and generalizes into per-tier,
  per-role, per-project budgets; the deferred per-role/class caps and any
  fairness/priority live here.
- **Harbor Master / Supervisor** — mostly already built: the Instance registry,
  leases, reconcile, self-heal, wake/idle. It changes its *input* from "dispatcher
  calls raise" to "reconcile the reservation ledger," and its launcher becomes a
  **pool** over the existing pluggable `internal/backend` seam (Colima now;
  cloud/k8s/remote later).

## Naming decisions (settled)

- **Robot Resources (RR)** = the allocator.
- **Supervisor** = Harbor Master's reconcile brain (kept, as today).
- **Harbor Master** = Supervisor + launcher pool (the executor umbrella).
- **Disambiguate "dispatcher":** there are currently two — the harbor-resident
  dispatcher (`runtime.dispatcher`) and the standalone `at-dispatch` /
  `internal/dispatch` scheduler. This design concerns the resident one; the
  standalone scheduler's relationship (or merge) is out of scope here and needs
  its own naming pass.

## Open questions (block a plan)

1. **Assignable-tier lifecycle.** On release, an assignable cove returns to the
   warm pool instead of being torn down — which introduces reuse across units.
   That forces (a) a **credential re-scoping** step per assignment (a reused cove
   must not carry unit A's brokered scope into unit B — this is a *security*
   property, not just hygiene, and interacts with the per-task minted token), and
   (b) a **workspace/context reset** decision between units (clear `/workspace`,
   agent context?). Neither is fully worked out.
2. **Stream topology.** One global stream, one per project, or per-aggregate?
   For coarse-grained coves, the leaning is one stream per project with a
   single-writer allocation aggregate, resisting sharding until there's a reason —
   but this is a real fork, not settled.
3. **Projection consistency details.** The exact expected-version/optimistic-
   concurrency protocol for admission, projection rebuild/versioning, and
   acceptable projection lag on RR's admission hot path.

Secondary: **backpressure semantics** — when RR cannot grant, does the dispatcher
get a denial (retry next poll, like today's cap) or a queued reservation RR
fulfills later? Leaning deny+retry first, queue later.

## Non-goals / out of scope (for now)

- Building any of this — no implementation until the open questions resolve and a
  plan is written.
- Merging or renaming the standalone `at-dispatch` scheduler.
- The intercom/squawk comms log (a separate stream and domain).
- Multi-instance harbor (the version-pinned-append design pre-pays for it, but it
  is not a goal of a first slice).

## A plausible first slice (tentative, not committed)

Extract RR as a thin allocator over an event stream with a single tier
(ephemeral), moving the existing `max-concurrent` cap into it as a per-role
budget, and re-point the Supervisor to reconcile the reservation ledger instead of
taking a direct `raise` call — behavior-preserving for today's one-shot flow, but
with the seam in place. Standing and assignable tiers, and their re-scoping/reset
lifecycle, come as later slices once open question 1 is resolved.
