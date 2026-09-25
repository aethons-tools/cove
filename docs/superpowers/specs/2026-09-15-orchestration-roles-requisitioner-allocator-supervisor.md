# Orchestration roles: Requisitioner, Allocator, Supervisor

**Status:** design conversation captured; conceptual model agreed; **pre-plan**.
A forward-looking role decomposition + supporting ontology and security model.
One open question (projection consistency) remains before a plan can be written;
stream topology and the assignable-kind question that were open earlier are now
resolved (below).
**Motivation:** the orchestration responsibilities inside `at-harbor serve` are
currently collapsed into two clusters with fuzzy edges, "security" is split across
two layers, and the runtime nouns (Actor/Cove, and the unnamed **Context**) are
scattered. This doc names three roles with clean boundaries, promotes **Context**
to a first-class entity, pins the domain ontology, and consolidates security
policy — so capacity and permissions can each grow in one place without touching
matching, execution, or the image.
**Naming:** golden-age-of-computing style — plain, functional, no theme.
**Touches (eventual):** `internal/dispatch` (the resident matcher), the supervisor
+ `runtime.launcher` (`internal/backend`), the `runtime.dispatcher` config, the
roster/store (roles), the hardening layer (egress delivery), and a new allocation
layer. Nothing here is built yet.

## The problem

Today two clusters own everything (see [dispatcher.md](../../usage/harbor/dispatcher.md),
[coves.md](../../usage/harbor/coves.md)):

- **The resident dispatcher** polls ready tickets → dedups → **checks the
  `max-concurrent` cap** → claims the ticket → **calls raise**. It owns matching
  *and* admission *and* the trigger.
- **The supervisor + Launcher** own the Instance registry (Phase/Activity),
  leases, reconcile, self-heal, wake/idle, and the Colima execution.

The capacity concern (the cap) is buried inside the matcher, enforced on the
*live* instance count. That is why per-role caps and standing coves are awkward
today: no component owns "what should exist." Separately, permission policy is
split across the Role (brokered destinations) and the Kit (sandbox egress
allow-list) — two homes for one question.

## The three roles

| Role | Responsibility | One-line boundary |
|---|---|---|
| **Requisitioner** | Match a unit of work to a **role**, then requisition a **Context** for it. | Demand producer. No counting, no raising. |
| **Allocator** | Ration Context lifetimes: hold the reservation ledger, enforce per-project/per-role capacity, grant/deny. | Source of truth for *what Contexts should exist*. |
| **Supervisor** | Reconcile reservations into running Contexts on Coves and keep them alive. Owns a **pool of launchers** (one per cove-hosting mechanism). | Source of truth for *what Contexts actually exist*. |

## Core principle: the reservation is the only currency

Everything outside the Allocator sees exactly one abstraction — a **reservation**
(a handle to a **Context** for a role, optionally bound to a unit of work). The
Requisitioner asks for one and never learns *how* it was satisfied. The Supervisor
consumes reservations and never learns *why* they were allocated.

The Allocator internally splits each project's per-role concurrency into **kinds**:

- **standing** — always-on; a reservation with no unit and no expiry (a
  teammate/manager Context). Never released.
- **ephemeral** — one unit, then the Context is discarded (the Cove torn down).

A single reservation object covers both: a standing teammate is just a reservation
with no unit and no expiry. These kinds are **the Allocator's private concern** —
outside it, everything is just a reservation on a Context. The Allocator can start
as a trivial per-kind counter and grow arbitrarily sophisticated internally —
fairness, priority, demand prediction — with zero blast radius, because the
reservation abstraction hides all of it.

**The Allocator and the Supervisor hone independently, behind the reservation.**
Because the seam is the only contract, each side can grow without the other
knowing. Likely early moves, none touching the seam: the Allocator puts a **TTL**
on reservations from the start (an abandoned or unfulfilled reservation
self-expires); later a **priority** carried on the reservation and honored by the
Supervisor, so that when it is backed up the hot raises skip the line. TTL and
admission live in the Allocator (allocation policy); placement order lives in the
Supervisor — the reservation hides both.

> **Deferred: a "warm/assignable" reuse kind** (a pooled Cove leased across units
> and returned rather than torn down). It is intentionally *not* modeled now: it
> introduces cross-unit reuse, which forces per-assignment credential re-scoping (a
> security property) and workspace/Context reset. It falls out as a third `kind` if
> a real need appears; until then, ephemeral Contexts are fresh actors and standing
> ones are dedicated, so neither problem exists.

## The allocation aggregate (the Allocator's storage)

The Allocator's durable state is an **event-sourced aggregate keyed by *(project,
role)*** — "this role's allocation in this project." It is role-shaped, with two
facets folding over one stream:

- **static facet — the *observed* budget:** `RoleBudgetObserved` (the per-kind
  budget, e.g. `worker: 1 standing, 4 ephemeral`) — the Allocator's materialization
  of the admin-authored budget, *not* an authoring surface (see below).
- **dynamic facet — the reservations:** the events below.

Keeping both facets in one aggregate is what makes the cap invariant local:
`granted ≤ budget` is checked inside a single consistency boundary that holds both
the observed budget and the outstanding count. **The Allocator is the sole writer**
of this stream; a version-pinned append is the admission gate.

**This aggregate *enforces* capacity; it does not *author* it.** A budget is
administration — CRUD role config an operator sets — so the **source of truth for a
role's budget is the roster/control-plane store**, next to the role's scope,
grants, and kit (see [roster.md](../../usage/harbor/roster.md)). The Allocator
**observes** budget changes and **materializes** them onto its own stream
(`RoleBudgetObserved`) so admission folds the budget locally and enforces the cap
atomically. The transfer mechanism degrades gracefully: an **active ping** from the
admin path today; a plain **event-observer** once the admin/control-plane
definitions are themselves event-sourced. So the budget has **one authoring home
(roster) and one enforcement home (the allocation aggregate)** — the *policy vs
mechanism* split again (policy authored centrally, enforced where the invariant
lives). This is safe *because capacity tolerates eventual consistency*: a brief
window admitting against a just-lowered budget self-corrects as the Allocator
drains — a tolerance a *security* scope never has, which is why authorization stays
live-checked and is never merely observed.

Authorization itself (grants/scope) likewise lives in the roster Role; the
allocation aggregate **references** it by id (fail-closed if absent, exactly like
enroll) and never duplicates it. (The larger alternative — fully event-sourcing the
Role as its canonical store — is coherent but reopens the just-migrated store and
puts authorization on the event path; a separate, deliberate decision.)

### Events on the allocation stream (the Allocator, sole writer)

- **Observed config:** `RoleBudgetObserved{project, role, perKindLimits,
  sourceVersion}` — the Allocator's materialization of the admin-authored budget
  (source of truth: roster). Retirement is an observed removal. On its stream so
  admission folds it locally and "what budget applied at time T" stays
  replayable/audited.
- **Demand:** `ReservationRequested{reservationId, role, project, kind, unit?,
  workloadRef?, priority?}` · `ReservationWithdrawn{reservationId, reason}` (demand
  evaporated before grant; standing revoked). Recording demand — not just decisions
  — is what later fairness/prediction folds over.
- **Decision:** `ReservationGranted{reservationId, budgetVersion}` — the
  cap-critical, version-pinned append · `ReservationDenied{reservationId, reason}`
  (no slot now → the Requisitioner retries).
- **Completion:** `ReservationReleased{reservationId, reason: completed | failed |
  lost | revoked}` — the only thing that decrements occupancy.

The ledger is the fold `Granted − Released` per *(project, role)*; the cap is
enforced at `Granted` time by the version-pinned append. (Deferred: a
`ReservationQueued` / `ReservationExpired` pair if backpressure moves from
deny+retry to real queueing.)

### What is NOT on this stream

- **Cove lifecycle** — `CoveRaising / CoveLive / CoveIdled / CoveWoke /
  CoveTerminating / CoveGone / CoveLost` (the Phase vocabulary from coves.md) —
  lives on the **Supervisor's execution stream**. The allocation stream is
  allocation-only; it never learns which Cove or how it ran.
- **Leases / heartbeats / liveness** — current-state with a TTL, never events
  (their history is worthless and would flood the stream; only meaningful
  transitions like `CoveLive → CoveLost` become events).

### The coupling (two thin cross-subscriptions)

- The **Supervisor** subscribes to `ReservationGranted` with no live Cove yet →
  emits `CoveRaising` on *its* stream and materializes it.
- The **Allocator** subscribes to the execution stream's terminal (`CoveGone` /
  `CoveLost`) for a Cove bound to reservation R → emits `ReservationReleased`,
  freeing the slot.

## Topology: desired-state in, actual-state out — no directing

The Requisitioner talks to **only** the Allocator. The Allocator does **not**
imperatively command the Supervisor; it **records desired reservations in durable
state**, and the Supervisor **reconciles** reality toward them. Nobody issues "do
this now" across the Allocator↔Supervisor seam.

```
                 workload (role, unit, prompt-ref)
   Requisitioner ─────────────────────────────────▶ Allocator
      ▲                                              │  (admits → appends
      │ reservation handle / backpressure            │   ReservationGranted;
      └──────────────────────────────────────────────   ledger = a projection)
                                                      │
                        desired reservations (durable event stream)
                                                      │
                                                      ▼
                                               Supervisor
                                        ┌───────────────────────┐
                                        │ reconcile loop         │  folds granted
                                        │        +               │  reservations vs
                                        │ launcher pool          │  cove lifecycle,
                                        │ (Colima / cloud / …)   │  converges reality,
                                        └───────────────────────┘  reports releases/
                                                      │             liveness back up
                                                      ▼
                                            Contexts on Coves
```

**Why declarative, not "the Allocator directs the Supervisor":** if the Allocator
imperatively commanded the Supervisor, it would inherit execution reliability — a
Supervisor crash mid-raise forces the Allocator to track acks/retries, entangling
allocation state with execution state. With reconcile-from-durable-state, a crash
anywhere is re-derived on the next pass; self-heal is free. This is already how the
supervisor behaves (it re-adopts live Coves from the store on restart).

**Why the reconcile loop lives with the launchers, not with the Allocator:**
reconcile is inseparable from execution ground truth (is the Cove alive? lease
expired? reported `done`?). Co-locating it with the launchers that observe that
truth keeps each of the two feedback loops — allocation accounting (Allocator) and
execution ground truth (Supervisor) — sealed inside one component. The reservation
ledger is the seam between them: desired-state in, released/liveness counts out.

## Event-sourced substrate

The seam is an **append-only event stream**, and the durable stores become
**projections** over it. This fits because the codebase already builds this
substrate: the intercom/squawk log is append-only with a monotonic `Seq` and a
durable commit cursor + seekable reads. Event sourcing here is applying a proven
in-house pattern, not importing a foreign one. It also gives harbor (a security
boundary) a first-class audit trail — who reserved what, which Context ran which
unit, what scope its token got — aligned with the standing view that these logs are
durable, indefinitely-retained records.

**The discipline line — event-source decisions and lifecycle transitions, not
sensor readings.** Decisions and transitions (the reservation and cove-lifecycle
events above) are retained source-of-truth; continuous signals (leases, heartbeats,
liveness) are current-state with a TTL; large payloads (the workload prompt) are
referenced, not embedded.

**Two consequences, both good:**

- **The Allocator and the cove registry become folds, not mutable stores.**
  Admission is a `ReservationGranted` appended **with an expected-version /
  version-pinned append** — the same primitive the squawk log already uses. That is
  what makes caps safe under concurrency: two racing grants cannot both win an
  append at the same version, so a cap cannot be overshot. This also pre-pays the
  multi-instance-harbor future the dispatcher doc flags as deferred.
- **Keep the orchestration stream separate from the squawk/intercom comms log.**
  Same append-log machinery, different domain, different consumers and retention.
  Having just de-overloaded "message," do not re-overload "the log": one stream for
  coordination events, one for comms.

## Stream topology (settled)

The earlier "global vs per-project vs per-aggregate" framing conflated **two
orthogonal axes**; separating them settles it.

- **Consistency unit (fixed by the invariant).** A version-pinned append is scoped
  to the **(project, role)** aggregate — that is where `granted ≤ budget` is
  enforced. Grants for the same (project, role) race for one revision; grants for
  different pairs share no invariant and must not contend. Not a choice.
- **Grouping / subscription / tenancy / shard (chosen): the project.** Roles,
  budgets, and the roster are already project-namespaced, so retention, replay, and
  access align to a project. Crucially, **project is the multi-instance shard**: a
  harbor instance that owns a project owns *both* its allocation category *and* its
  execution category, so the whole Allocator↔Supervisor seam for a project lives
  under one owner with zero cross-instance coordination (no invariant crosses
  projects).
- **Global ordering (cheap, orthogonal): a `BIGINT` serial per row.** A monotonic
  stamp assigned at persist time — the squawk log's `Seq` — giving a total order for
  *observation* (audit, cross-stream "what happened around T", catch-up cursors). It
  is **never** used for consistency; that is the per-stream revision's job, so it
  adds no write bottleneck. (Tailing a serial across concurrent commits has the
  known in-flight-gap window the squawk log already handles.)

Concretely this is the squawk log generalized into a **categorized event store** —
one events table with:

- `global_seq BIGSERIAL` — total order, observation only, never consistency;
- `category` = **project** — grouping / subscription / tenancy / shard axis;
- `stream_id` = **(project, role)** for allocation (per-Cove for execution) — the
  aggregate;
- `stream_revision` with `UNIQUE(stream_id, stream_revision)` — the consistency unit
  a version-pinned append checks.

Consistency per stream, ordering global-and-free, grouping-and-sharding by project —
three independent knobs, each set on its own axis.

## Capacity ceilings — policy vs mechanism

The per-(project, role) budgets in the Allocator are capacity *policy*. A *global*
physical ceiling (a backend can only run so many Coves) is **not** a global
Allocator aggregate — that would un-shard the Allocator and reintroduce a
bottleneck. It decomposes into limits **each component enforces against its own
state**:

- **each launcher** — its administrative cap and physical reality, against its own
  running-Cove count;
- **the Supervisor** — an optional administrative cap across its launcher pool,
  against its own fold of the pool.

No component needs a global view it doesn't already have — the policy/mechanism
split again (per-role budget = policy in the Allocator; host capacity = mechanism at
the launchers/Supervisor).

When the Allocator has **granted** a reservation (policy: yes) but every
launcher/Supervisor is **full** (mechanism: not now), **the Supervisor holds the
reservation and reconciles it when capacity frees** — no bounce back to the
Allocator, because a granted reservation is desired state and reconcile is exactly
"make this real when you can." Transient over-capacity just means a granted
reservation waits. *Persistent* over-capacity — the sum of the budgets structurally
exceeding host capacity — is config incoherence, not incorrectness: it surfaces as a
**health signal** ("budgets over-subscribe capacity"), and the system stays correct
(reservations wait).

## The domain ontology (three planes)

The nouns aren't a single deep stack; they are **three orthogonal planes** that meet
when a Context is run. The test for a right-sized boundary: *what question does each
answer?* Two concepts that answer the same question should merge.

| Plane | Concepts | The question each answers |
|---|---|---|
| **Authorization / identity (RBAC)** | Project → Role ← Grant → Actor | Project: *which namespace?* · Role: *what may it reach / how many may exist?* · Grant: *who holds which role?* · Actor: *who is it?* |
| **Build** | Kit | *What is it made of, and how is it sealed?* |
| **Runtime** | Context, Cove | Context: *what has it learned / what is it for?* · Cove: *where does it run?* |

- **Context is the entity with a lifetime** — an **Actor in flight**: its identity
  plus what it has accumulated plus the goal it serves. Idle **freezes its Cove**
  (Context preserved); dismiss **discards the Context** (Cove torn down); rehydrate
  (later) reattaches the Context to a fresh Cove. Standing vs ephemeral is just
  *whether the Context outlives one unit of work*.
- **Cove is the substrate** a Context runs on (the hardened sandbox). It is
  fungible; the durable thing is the Context. So "reuse a warm Cove" (deferred) is
  *recycle the empty substrate, attach a fresh Context* — never re-scope a live one.
- **The RBAC plane is a correct, standard model** — leave it. **Actor is
  deliberately general** ("a cove *or a standing teammate* is an Actor," and Grant is
  M:N): it is the one identity abstraction serving both the comms/escalation plane
  (humans) and the runtime plane (Contexts). Collapsing Actor into Cove would fork
  identity — and Context is the Actor *in flight*, so it sits naturally between them.
- **Instance dissolves into a projection.** "Instance" and the running thing
  answered the same question. In the event-sourced model, Instance is no longer a
  stored noun — it is the **current-state fold of the execution stream**, with
  harbor-owned **Phase** and cove-reported **Activity** as two facets of a Context on
  its Cove.
- **The reservation sits above as the "why":** a reservation is granted, and the
  Supervisor runs a **Context** (what it's for) — an **Actor** (who) of a **Role**
  (what it may reach) — on a **Cove** (where), built from that role's **Kit** (what
  it's made of), within a **Project** (namespace). Every noun answers its own
  question.

Note: **Role is the busiest concept** — it touches all three planes (authorization
scope + a bound Kit + capacity budget). That is a natural join point ("the class of
Context"); not a problem to split now, but the fault line, if it ever needs one, is
authorization-scope vs a "Context type" (kit + capacity).

## Security: split mechanism from policy

There are two security surfaces today, and they feel alike but aren't:

- **Role security** = *brokered authorization* — credentialed destinations, repo
  globs, comms targets. Enforced by harbor at request time, keyed to the actor's
  identity, live-editable.
- **Kit security** = *sandbox egress* — the squid/nftables allow-list of domains the
  box may reach at all (including non-brokered raw egress like pypi/apt). Enforced
  inside the VM, today baked into the image via `config.yml` + a human-gated
  `at-cove recreate`.

The same word "security" spans identity-scoped brokered access and the box's raw
network perimeter, in two places. The fix is **not** "move all security into the
Role" literally — it is to split **mechanism** from **policy**:

- **Kit keeps the enforcement mechanism** — the sealed hardening layer
  (nftables/squid/sshd/credential-helper) — plus pure contents. It is the lockbox; it
  can't leave the image, and it's the same for every Cove (reviewed once, sealed from
  inside).
- **Role owns all security *policy*** — the brokered destinations/repos/comms it
  already has, **plus the raw egress allow-list** promoted out of the kit. A reviewer
  asking "what can this Context touch?" then reads exactly one thing: the Role.

Two guardrails keep that from being a downgrade:

1. **A kit-level egress *ceiling* for defense-in-depth.** The image bounds what is
   *possible*; the Role's list is the *effective* grant and must be ⊆ the ceiling. A
   Role misconfiguration can never punch past the image's perimeter — two independent
   layers preserved. (Decision: ceiling model — the Role narrows *within* the kit's
   bound — not full supersession.)
2. **Egress policy delivered at raise, not baked at build.** Harbor injects the
   Role's egress list into the Cove at boot, applied *before* the agent runs, over
   the same trusted channel that delivers the connector/identity, and the "sealed
   from inside" property must survive it (the box still cannot widen its own list).
   This is the one real hardening-layer change the split implies.

**Scope caveat:** this consolidation applies only where a Role exists — the
**harbor-managed plane**. A standalone `.at-cove/` dev sandbox (the `dev/` world, no
harbor) has no Role, so its egress stays kit-owned. Precisely: the kit's egress
config is the **default + ceiling**; when a Cove is harbor-managed, its **Role's list
is the effective policy** within that ceiling.

Net division: **Kit = what it is made of and how it is sealed; Role = who it is and
everything it is permitted.**

## Mapping to what exists today

- **Requisitioner** — slim today's resident dispatcher (`internal/dispatch`): keep
  poll/dedup/claim (matching a ticket to a role via handler class), drop the
  `max-concurrent` cap and the direct `raise` call. (The standalone `at-dispatch`
  scheduler is unaffected — see naming below.)
- **Allocator** — new. The cap moves here and generalizes into per-kind, per-role,
  per-project budgets; the deferred per-role/class caps and any fairness/priority
  live here.
- **Supervisor** — mostly already built: the cove registry, leases, reconcile,
  self-heal, wake/idle. It changes its *input* from "dispatcher calls raise" to
  "reconcile the reservation ledger," and its launcher becomes a **pool** over the
  existing pluggable `internal/backend` seam (Colima now; cloud/k8s/remote later).
- **Roster Role** — becomes the authoring home for the per-*(project, role)* capacity
  budget (observed by the allocation aggregate) and the promoted egress allow-list,
  alongside its authorization scope.

## Naming decisions (settled)

Golden-age-of-computing style: plain, functional, no theme.

- **Requisitioner** = the demand producer (was the resident "dispatcher"). Renamed to
  kill the collision with the standalone `at-dispatch` and to describe its narrowed
  job — it *requisitions* a Context; it no longer dispatches/raises.
- **Allocator** = the capacity rationer (was "Robot Resources"). The HR framing is
  dropped: bot labor inverts the human cost model (idle is nearly free, activity is
  expensive), so "staffing/headcount" intuitions mislead.
- **Supervisor** = the reconciler + launcher pool (was "Harbor Master"). With the
  theme gone, the reconcile loop and the umbrella collapse into one; "Supervisor" is
  both golden-age and already the codebase term for this thing.
- **Context** = first-class runtime entity (new): the durable, goal-bound state — an
  Actor in flight. The thing whose lifetime idle/dismiss/standing/ephemeral all
  describe.
- **Cove** = the substrate a Context runs on (kept; the product's hardened sandbox).
- **Instance** = retired as a stored noun; it is the Cove's tracked-state projection
  (Phase + Activity facets).
- **Kept:** Actor, Kit, Role, Project, reservation, launcher.
- The standalone `at-dispatch` / `internal/dispatch` scheduler keeps its name; only
  the resident role was renamed (Requisitioner), which removes the collision.

## Settled during design

- Drop the warm/assignable reuse kind for now (falls out later as a third `kind` if
  needed).
- **Reservation = existence, not an activity slot** — modeling it as an activity slot
  drags toward preemptive turn-scheduling (agents-as-threads), which is a non-goal
  (below). Concurrency is admission-gated; idle is cooperative (the Context's own
  wait), never scheduler-driven.
- The allocation aggregate is keyed *(project, role)* and references — does not fork —
  the roster Role.
- Budget's **source of truth is roster** (admin CRUD); the Allocator **observes** it
  onto its stream (`RoleBudgetObserved`; active ping today → event-observer once admin
  config is event-sourced) and enforces the cap atomically against the observed value.
  Capacity **tolerates eventual consistency**; authorization does not.
- **Record demand** (`ReservationRequested`/`Withdrawn`) as first-class events.
- Backpressure: **deny + retry** first; real queueing deferred.
- Security: **mechanism/policy split**, egress promoted to the Role, kit sets the
  **ceiling**, policy **delivered at raise**, harbor-managed plane only.
- **Stream topology:** consistency per **(project, role)** stream revision; group by
  **project** (tenancy + multi-instance shard); a cheap global `BIGINT` serial for
  ordering/observation only. A categorized event store — the squawk log generalized.
- **Capacity ceilings** are mechanism, enforced per launcher / per Supervisor against
  each one's own state — never a global Allocator aggregate. A granted-but-unplaceable
  reservation is **held by the Supervisor and reconciled when capacity frees** (no
  bounce); structural over-subscription surfaces as a health signal.
- **Context** is first-class; **Cove** is its (fungible) substrate; **Instance** is
  retired to a projection. Golden-age naming, no theme.

## Open questions (block a plan)

1. **Projection consistency details.** The exact expected-version/optimistic-
   concurrency protocol for admission, projection rebuild/versioning, and acceptable
   projection lag on the Allocator's admission hot path.

Implementation implication to design when planning: the **egress-policy delivery at
raise** hardening-layer change (preserving "sealed from inside").

## Non-goals / out of scope (for now)

- Building any of this — no implementation until the open question resolves and a
  plan is written.
- **Preemptive turn-scheduling** — the Allocator/Supervisor never pause a *running*
  Context to reallocate its slot; agents run to a natural boundary, and idle is
  cooperative only. Concurrency is admission-gated, not time-sliced.
- The warm/assignable reuse kind and its re-scoping/reset lifecycle.
- Portable/rehydratable Context (freeze-in-place first; serialize-and-reattach later).
- Fully event-sourcing the roster Role (roster stays the authorization source of
  truth; the Allocator references it).
- Merging or renaming the standalone `at-dispatch` scheduler.
- The intercom/squawk comms log (a separate stream and domain).
- Multi-instance harbor (the version-pinned-append design pre-pays for it, but it is
  not a goal of a first slice).

## A plausible first slice (tentative, not committed)

Extract the **Allocator** as a thin capacity layer over a per-*(project, role)* event
stream with a single kind (ephemeral), moving the existing `max-concurrent` cap into
it as a per-role budget, and re-point the **Supervisor** to reconcile the reservation
ledger instead of taking a direct `raise` call from the **Requisitioner** —
behavior-preserving for today's one-shot flow, but with the seam in place. Standing
Contexts, the egress-policy promotion, and projection-consistency hardening come as
later slices.
