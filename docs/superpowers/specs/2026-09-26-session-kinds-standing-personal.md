# Session kinds: ephemeral, standing, personal

**Status:** design agreed; **pre-plan**. Builds on the orchestration-roles design
([`2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md))
and Slices 1–5 (the Allocator, the authoritative OCC ledger, release on teardown,
the reconcile sweep).
**Motivation:** today the Allocator only knows one kind of session — **ephemeral**,
raised by the dispatcher for one ticket. Two more kinds are needed: **standing**
sessions (named, long-lived teammates) and **personal** sessions (ad-hoc sessions
a *human* asks for to do their own work). The immediate need is personal.
**Touches (eventual):** the reservation/event model (`internal/allocator`,
`allocpg`), the per-`(project, role)` allocation policy (authored in roster), the
Supervisor's reconcile loop, a new operator request surface (CLI/UI), and the
escalation engine (for idle nags). Nothing here is built yet.

## The three kinds

The axis that separates them is **who requests the session** and **what ends it**:

| Kind | Requester | Ends when |
|---|---|---|
| **ephemeral** | the dispatcher (a tracker ticket) | its unit of work completes |
| **standing** | an operator (declared, **named**) | it is **dismissed** |
| **personal** | **a human**, ad hoc | the **owner releases it** (with an idle backstop) |

All three remain **reservations** — the reservation is still the only currency
between requesters, the Allocator, and the Supervisor. The reservation grows:

- `kind` — `ephemeral | standing | personal`
- `name` — standing only: a stable identity (addressable, shows in the roster,
  resurrects under the same name)
- `owner` — personal only: the requesting human's Actor id

Humans are already Actors in the RBAC plane, so "owner" needs no new identity
concept.

## The per-(project, role) allocation policy

The budget generalizes from one ephemeral cap to a policy per `(project, role)`:

| Setting | Meaning |
|---|---|
| `max-ephemeral` | cap on concurrent ephemeral sessions (today's `max-concurrent`) |
| `standing` | the **named set** of standing sessions (a list of names, not a count) |
| `max-personal` | pool cap on concurrent personal sessions of this role, across all requesters |
| `idle` settings | the personal-session idle ladder (below) |

Plus a **requester-side grant**: a role may be granted the ability to request
personal sessions of other roles, with a per-requester count — e.g. a human
holding `engineer` in `acme` *may request* up to 2 personal `worker` sessions.

- The **type** ("which roles may I request") is **authorization** → a grant on the
  requester's Role, in the RBAC plane, like destinations.
- The **counts** are **capacity** → enforced by the Allocator: the per-requester
  count from the grant, and the pool cap (`max-personal`) on the target role.
  Two caps; a personal request must satisfy both.

**Where it lives:** the policy is administration, so — exactly like the budget
decision in the orchestration design — the **roster/control-plane store is the
source of truth**, and the Allocator **observes** it onto its ledger for atomic
enforcement. Capacity tolerates eventual consistency; the *type* grant is
authorization and is checked live, never merely observed.

## Personal sessions (the immediate need)

A personal session is, in effect, a **governed `at-harbor cove raise`**: today a
human can already raise a cove by hand, but it is ungoverned, not tracked against a
budget, and not owned. Personal sessions put that under the Allocator.

- **Request.** A human asks through an operator surface (CLI verb; UI later),
  authenticated by harbor's existing operator auth. The requester's identity is the
  **owner**. No in-cove API is involved.
- **Admission.** The Allocator checks the requester's grant (type, live) and both
  caps (per-requester count, `max-personal` pool) and grants via the same OCC
  append the ledger already uses (`kind: personal`, `owner`).
- **Lifetime.** Undetermined — the session lives until the **owner releases it**.
  Release is explicit (CLI/UI).
- **Idle ladder (the backstop).** Humans forget, so an idle personal session is
  handled humanely rather than silently killed. Because idle is nearly free and
  activity is expensive:
  1. **idle → pause.** After `idle-after`, the Studio is paused (cheap; the
     Session's context is preserved).
  2. **pester.** The owner is **squawked via the intercom** (`human:<owner>`) —
     "your personal session *X* has been idle for *N*; keep it or release it" — and
     re-nagged every `nag-every`.
  3. **reclaim.** After an optional final `reclaim-after`, the session is released
     (the Slice-5 sweep can carry this).

  All three thresholds are **settings** in the `(project, role)` policy
  (`reclaim-after` may be unset = never auto-reclaim). The nag mechanism **reuses
  the escalation engine**, which already pings humans over the intercom on
  per-tier timers — a personal-idle nag is a single-tier escalation to the owner.
  v1: the nag is a notification; the owner keeps or releases via CLI/UI.
  Replying to the squawk to act ("keep"/"release") is deferred.

## Standing sessions

- **Declaration.** An operator declares named standing sessions per
  `(project, role)` (roster/admin surface; a config block may seed it). Each name
  is a reservation with **no unit and no expiry**.
- **Keep-alive = the Supervisor reconciles the ledger.** This is the first place
  the design's "Supervisor reconciles desired reservations" is built directly
  (ephemeral is dispatcher-driven). Each pass: a standing reservation with no live
  Session → **raise** it; a standing Session that died → its reservation
  **persisted**, so the next pass **resurrects** it under the same name.
- **Teardown ≠ release** for standing. A standing Studio dying must not release its
  reservation (else it would not be resurrected). A standing reservation releases
  **only on dismissal** — the name is removed — after which the Supervisor tears it
  down and releases.

## Deferred (not in this design)

- **Session-requested personal sessions** (a cove asking harbor for a helper).
  Introduces an in-cove request API, a spawn-privilege boundary, ownership cascade
  on owner-death, and a recursion bound — needs its own design.
- **Reply-to-act on nags** (answer the squawk to keep/release) — builds on
  intercom reply routing.
- The warm/assignable reuse kind (still deferred from the orchestration design).

## Open question (resolve in the plan)

- **What "idle" means for a human-driven session.** Ephemeral sessions report
  Activity (`running`/`waiting`) from their agent loop; a personal session is driven
  by a human who may attach, work, and walk away. Candidate definition: no
  `running` Activity **and** no attached human connection for `idle-after`. Pin
  this down against what the Attach stream actually observes.

## Slice sequence (tentative)

Personal first, since it is the immediate need; standing after.

1. **Generalize the reservation + policy.** `kind`/`name`/`owner` on reservations;
   the per-`(project, role)` policy (authored in roster, observed by the
   Allocator); `max-ephemeral` replaces today's `StaticBudget` semantics
   (behavior-preserving for ephemeral).
2. **Personal: request + ownership.** The operator request verb, the requester
   grant (type, live) + both caps, OCC grant with `owner`, explicit release.
3. **Personal: idle ladder.** Idle → pause, intercom pestering via the escalation
   engine, optional reclaim via the sweep.
4. **Standing.** Named declarations, keep-alive/resurrect reconcile,
   dismissal-only release.
