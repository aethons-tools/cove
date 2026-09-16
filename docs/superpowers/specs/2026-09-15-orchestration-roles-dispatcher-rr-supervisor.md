# Orchestration roles: Dispatcher, Robot Resources, Harbor Master

**Status:** design conversation captured; conceptual model agreed; **pre-plan**.
A forward-looking role decomposition + supporting ontology and security model.
Two open questions (stream topology, projection consistency) remain before a plan
can be written; the assignable-tier question that was open earlier has been
resolved by dropping the tier (below).
**Motivation:** the orchestration responsibilities inside `at-harbor serve` are
currently collapsed into two clusters with fuzzy edges, and "security" and the
runtime nouns (Actor/Instance/Cove) are scattered. This doc names three roles
with clean boundaries, pins the domain ontology behind them, and consolidates
security policy — so capacity and permissions can each grow in one place without
touching matching, execution, or the image.
**Touches (eventual):** `internal/dispatch` / the resident dispatcher, the
supervisor + `runtime.launcher` (`internal/backend`), the `runtime.dispatcher`
config, the roster/store (roles), the hardening layer (egress delivery), and a
new allocation layer. Nothing here is built yet.

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
| **Dispatcher** | Match a unit of work to a **role**, then request a cove for it. | Demand producer. No counting, no raising. |
| **Robot Resources (RR)** | The **allocator**: hold the reservation ledger, enforce per-project/per-role capacity, grant/deny. | Source of truth for *what coves should exist*. |
| **Harbor Master** | Turn desired reservations into running coves and keep them alive. = **Supervisor** (reconcile brain) + a **pool of launchers** (one per cove-hosting mechanism). | Source of truth for *what coves actually exist*. |

## Core principle: the reservation is the only currency

Everything outside RR sees exactly one abstraction — a **reservation** (a handle
to a cove for a role, optionally bound to a unit of work). The dispatcher asks
for one and never learns *how* it was satisfied. The Supervisor consumes
reservations and never learns *why* they were allocated.

RR internally splits each project's per-role concurrency into **kinds**:

- **standing** — always-on; a reservation with no unit and no expiry (a
  teammate/manager cove). Never released.
- **ephemeral** — one unit, then the cove is torn down on release.

A single reservation object covers both: a standing teammate is just a
reservation with no unit and no expiry. These kinds are **RR's private concern** —
outside RR everything is just a reservation on a cove. RR can start as a trivial
per-kind counter and grow arbitrarily sophisticated internally — fairness,
priority, demand prediction — with zero blast radius, because the reservation
abstraction hides all of it.

> **Deferred: a "warm/assignable" reuse kind** (a pooled cove leased across units
> and returned rather than torn down). It is intentionally *not* modeled now: it
> introduces cross-unit reuse, which forces per-assignment credential re-scoping
> (a security property) and workspace/context reset. It will fall out as a third
> `kind` if a real need appears; until then, ephemeral coves are fresh actors and
> standing coves are dedicated, so neither problem exists.

## The allocation aggregate (RR's storage)

RR's durable state is an **event-sourced aggregate keyed by *(project, role)*** —
"this role's allocation in this project." It is role-shaped, with two facets
folding over one stream:

- **static facet — the role's capacity config:** `RoleBudgetConfigured`,
  `RoleBudgetRetired` (the per-kind budget, e.g. `worker: 1 standing, 4
  ephemeral`).
- **dynamic facet — the reservations:** the events below.

Keeping both facets in one aggregate is what makes the cap invariant local:
`granted ≤ budget` is checked inside a single consistency boundary that holds
both the budget and the outstanding count. **RR is the sole writer** of this
stream; a version-pinned append is the admission gate.

**This aggregate owns capacity, not authorization.** A role's *authorization*
(grants/scope) already has a source of truth — the roster Role in the
control-plane store (see [roster.md](../../usage/harbor/roster.md)). The
allocation aggregate **references** the roster Role by id (fail-closed if absent,
exactly like enroll) and never duplicates its grants. Capacity is per-*(project,
role)*; authorization is the global roster Role — two facets of "role," two homes,
one id. (The larger alternative — fully event-sourcing the Role as its canonical
store with the roster projecting from it — is coherent but reopens the
just-migrated control-plane store and puts authorization on the event path; it is
a separate, deliberate decision, not part of this design.)

### Events on the allocation stream (RR, sole writer)

- **Config:** `RoleBudgetConfigured{project, role, perKindLimits}` ·
  `RoleBudgetRetired{project, role}`. On-stream (not read from live config) so
  "how many slots existed at time T" is replayable and audited.
- **Demand:** `ReservationRequested{reservationId, role, project, kind, unit?,
  workloadRef?, priority?}` · `ReservationWithdrawn{reservationId, reason}`
  (demand evaporated before grant; standing revoked). Recording demand — not just
  decisions — is what later fairness/prediction folds over.
- **Decision:** `ReservationGranted{reservationId, budgetVersion}` — the
  cap-critical, version-pinned append · `ReservationDenied{reservationId, reason}`
  (no slot now → dispatcher retries).
- **Completion:** `ReservationReleased{reservationId, reason: completed | failed |
  lost | revoked}` — the only thing that decrements occupancy.

RR's ledger is the fold `Granted − Released` per *(project, role)*; the cap is
enforced at `Granted` time by the version-pinned append. (Deferred: a
`ReservationQueued` / `ReservationExpired` pair if backpressure moves from
deny+retry to real queueing.)

### What is NOT on this stream

- **Cove lifecycle** — `CoveRaiseIntended / Live / Lost / Idled / Woken /
  TornDown` — lives on the **Supervisor's execution stream**. The allocation
  stream is allocation-only; it never learns which cove or how it ran.
- **Leases / heartbeats / liveness** — current-state with a TTL, never events
  (their history is worthless and would flood the stream; only meaningful
  transitions like `Live→Lost` become events).

### The coupling (two thin cross-subscriptions)

- The **Supervisor** subscribes to `ReservationGranted` with no live cove yet →
  emits `CoveRaiseIntended` on *its* stream and materializes it.
- **RR** subscribes to the execution stream's terminal (`TornDown` / `Lost`) for a
  cove bound to reservation R → emits `ReservationReleased`, freeing the slot.

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
(it re-adopts live coves from the store on restart).

**Why the reconcile brain lives with the launchers, not with RR:** reconcile is
inseparable from execution ground truth (is the cove alive? lease expired?
reported `done`?). Co-locating it with the launchers that observe that truth keeps
each of the two feedback loops — allocation accounting (RR) and execution ground
truth (Supervisor) — sealed inside one component. The reservation ledger is the
seam between them: desired-state in, released/liveness counts out.

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
sensor readings.** Decisions and transitions (the reservation and cove-lifecycle
events above) are retained source-of-truth; continuous signals (leases,
heartbeats, liveness) are current-state with a TTL; large payloads (the workload
prompt) are referenced, not embedded.

**Two consequences, both good:**

- **RR and the cove registry become folds, not mutable stores.** Admission is a
  `ReservationGranted` appended **with an expected-version / version-pinned
  append** — the same primitive the squawk log already uses. That is what makes
  caps safe under concurrency: two racing grants cannot both win an append at the
  same version, so a cap cannot be overshot. This also pre-pays the
  multi-instance-harbor future the dispatcher doc flags as deferred.
- **Keep the orchestration stream separate from the squawk/intercom comms log.**
  Same append-log machinery, different domain, different consumers and retention.
  Having just de-overloaded "message," do not re-overload "the log": one stream
  for coordination events, one for comms.

## The domain ontology (three planes)

The nouns aren't a single deep stack; they are **three orthogonal planes** that
meet when a cove is raised. The test for a right-sized boundary: *what question
does each answer?* Two concepts that answer the same question should merge.

| Plane | Concepts | The question each answers |
|---|---|---|
| **Authorization / identity (RBAC)** | Project → Role ← Grant → Actor | Project: *which namespace?* · Role: *what may it reach / how many may exist?* · Grant: *who holds which role?* · Actor: *who is it?* |
| **Build** | Kit | *What is it made of, and how is it sealed?* |
| **Runtime** | Cove | *Is it running, and where?* |

- **The RBAC plane is a correct, standard model** — leave it. **Actor is
  deliberately general** ("a cove *or a standing teammate* is an Actor," and Grant
  is M:N): it is the one identity abstraction serving both the comms/escalation
  plane (humans) and the runtime plane (coves). Collapsing Actor into Cove would
  fork identity.
- **Instance dissolves into a projection.** "Instance" and "Cove" answered the
  same question (is it running / what's its state). In the event-sourced model,
  Instance is no longer a stored noun — it is the **current-state fold of the
  execution stream**, with harbor-owned **Phase** and cove-reported **Activity** as
  two facets of the one runtime entity. So "Actor → Instance → Cove" tightens to:
  *an Actor has at most one live Cove; the Cove's tracked state is a projection.*
- **The reservation sits above as the "why":** a reservation is granted, and the
  Supervisor raises a **Cove** (running thing) for an **Actor** (who) of a **Role**
  (what it may reach + how many may exist), built from that role's **Kit** (what it
  is made of), within a **Project** (namespace). Every noun answers its own
  question.

Note: **Role is the busiest concept** — it touches all three planes
(authorization scope + a bound Kit + capacity budget). That is a natural join
point ("the class of cove"); not a problem to split now, but the fault line, if it
ever needs one, is authorization-scope vs a "cove type" (kit + capacity).

## Security: split mechanism from policy

There are two security surfaces today, and they feel alike but aren't:

- **Role security** = *brokered authorization* — credentialed destinations, repo
  globs, comms targets. Enforced by harbor at request time, keyed to the actor's
  identity, live-editable.
- **Kit security** = *sandbox egress* — the squid/nftables allow-list of domains
  the box may reach at all (including non-brokered raw egress like pypi/apt).
  Enforced inside the VM, today baked into the image via `config.yml` + a
  human-gated `at-cove recreate`.

The same word "security" spans identity-scoped brokered access and the box's raw
network perimeter, in two places. The fix is **not** "move all security into the
Role" literally — it is to split **mechanism** from **policy**:

- **Kit keeps the enforcement mechanism** — the sealed hardening layer
  (nftables/squid/sshd/credential-helper) — plus pure contents. It is the lockbox;
  it can't leave the image, and it's the same for every cove (reviewed once,
  sealed from inside).
- **Role owns all security *policy*** — the brokered destinations/repos/comms it
  already has, **plus the raw egress allow-list** promoted out of the kit. A
  reviewer asking "what can this cove touch?" then reads exactly one thing: the
  Role.

Two guardrails keep that from being a downgrade:

1. **A kit-level egress *ceiling* for defense-in-depth.** The image bounds what is
   *possible*; the Role's list is the *effective* grant and must be ⊆ the ceiling.
   A Role misconfiguration can never punch past the image's perimeter — two
   independent layers preserved. (Decision: ceiling model — the Role narrows
   *within* the kit's bound — not full supersession.)
2. **Egress policy delivered at raise, not baked at build.** Harbor injects the
   Role's egress list into the sandbox at boot, applied *before* the agent runs,
   over the same trusted channel that delivers the connector/identity, and the
   "sealed from inside" property must survive it (the box still cannot widen its
   own list). This is the one real hardening-layer change the split implies.

**Scope caveat:** this consolidation applies only where a Role exists — the
**harbor-managed plane**. A standalone `.at-cove/` dev sandbox (the `dev/` world,
no harbor) has no Role, so its egress stays kit-owned. Precisely: the kit's egress
config is the **default + ceiling**; when a cove is harbor-managed, its **Role's
list is the effective policy** within that ceiling.

Net division: **Kit = what it is made of and how it is sealed; Role = who it is
and everything it is permitted.**

## Mapping to what exists today

- **Dispatcher** — slim the resident dispatcher down: keep poll/dedup/claim
  (matching a ticket to a role via handler class), drop the `max-concurrent` cap
  and the direct `raise` call.
- **Robot Resources** — new. The cap moves here and generalizes into per-kind,
  per-role, per-project budgets; the deferred per-role/class caps and any
  fairness/priority live here.
- **Harbor Master / Supervisor** — mostly already built: the cove registry,
  leases, reconcile, self-heal, wake/idle. It changes its *input* from "dispatcher
  calls raise" to "reconcile the reservation ledger," and its launcher becomes a
  **pool** over the existing pluggable `internal/backend` seam (Colima now;
  cloud/k8s/remote later).
- **Roster Role** — gains a per-*(project, role)* capacity budget (referenced by
  RR's aggregate) and the promoted egress allow-list; keeps ownership of
  authorization scope.

## Naming decisions (settled)

- **Robot Resources (RR)** = the allocator.
- **Supervisor** = Harbor Master's reconcile brain (kept, as today).
- **Harbor Master** = Supervisor + launcher pool (the executor umbrella).
- **Instance** = retired as a stored noun; it is the Cove's tracked-state
  projection (Phase + Activity facets). (Whether to keep the *word* for "the record
  view of a cove" is cosmetic and deferred.)
- **Disambiguate "dispatcher":** there are currently two — the harbor-resident
  dispatcher (`runtime.dispatcher`) and the standalone `at-dispatch` /
  `internal/dispatch` scheduler. This design concerns the resident one; the
  standalone scheduler's relationship (or merge) needs its own naming pass.

## Settled during design

- Drop the warm/assignable reuse tier for now (falls out later as a third `kind`
  if needed).
- The allocation aggregate is keyed *(project, role)* and references — does not
  fork — the roster Role.
- `RoleBudget*` events live **on** the allocation stream (replayable/audited).
- **Record demand** (`ReservationRequested`/`Withdrawn`) as first-class events,
  not just RR's decisions.
- Backpressure: **deny + retry** first; real queueing deferred.
- Security: **mechanism/policy split**, egress promoted to the Role, kit sets the
  **ceiling**, policy **delivered at raise**, harbor-managed plane only.

## Open questions (block a plan)

1. **Stream topology.** One global stream, one per project, or per-aggregate? For
   coarse-grained coves, the leaning is one stream per project with a single-writer
   allocation aggregate, resisting sharding until there's a reason — a real fork,
   not settled.
2. **Projection consistency details.** The exact expected-version/optimistic-
   concurrency protocol for admission, projection rebuild/versioning, and
   acceptable projection lag on RR's admission hot path.

Implementation implication to design when planning: the **egress-policy delivery
at raise** hardening-layer change (preserving "sealed from inside").

## Non-goals / out of scope (for now)

- Building any of this — no implementation until the open questions resolve and a
  plan is written.
- The warm/assignable reuse tier and its re-scoping/reset lifecycle.
- Fully event-sourcing the roster Role (roster stays the authorization source of
  truth; RR references it).
- Merging or renaming the standalone `at-dispatch` scheduler.
- The intercom/squawk comms log (a separate stream and domain).
- Multi-instance harbor (the version-pinned-append design pre-pays for it, but it
  is not a goal of a first slice).

## A plausible first slice (tentative, not committed)

Extract RR as a thin allocator over a per-*(project, role)* event stream with a
single kind (ephemeral), moving the existing `max-concurrent` cap into it as a
per-role budget, and re-point the Supervisor to reconcile the reservation ledger
instead of taking a direct `raise` call — behavior-preserving for today's one-shot
flow, but with the seam in place. Standing coves, the egress-policy promotion, and
the two open questions come as later slices.
