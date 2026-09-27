---
summary: The Requisitioner — Jam's always-on poll loop that turns ready tracker tickets into managed-cove raises, bounded by a concurrency cap. Covers the flow, the `runtime.requisitioner` serve-config block, and the elastic-raise-under-a-cap model.
read_when: You are enabling or operating Jam's automatic intake — having it poll a tracker (Linear) and raise a managed studio per ready ticket — or tuning its concurrency cap and poll interval.
owns: the operator-facing Requisitioner story — the poll→claim→raise flow, the `runtime.requisitioner` serve-config block, and the concurrency-cap model
prereqs: coves.md for what a raised managed studio does (the supervisor + Launcher own its lifecycle); serve.md for the `runtime.launcher` a raised studio needs; roster.md for the role tickets are raised for
tier: leaf
updated: 2026-09-26
---

# The Requisitioner

The **Requisitioner** is an always-on loop inside `at-jam serve` that
turns ready tracker tickets into managed-cove raises — the automatic counterpart
to `at-jam studio raise` ([coves.md](coves.md)). Enable it with a
`runtime.requisitioner` block; the supervisor + Launcher own everything after the
raise (run → report → teardown).

## The flow (per poll)

1. **List ready** — poll the tracker for issues in its READY state.
2. **Gate on the dispatch label** — skip any issue that does *not* carry a label
   matching `dispatch-label-prefix` (default `dispatch:`). Only tickets
   explicitly tagged for dispatch are worked; an unlabeled READY backlog is left
   alone. Presence-only — the value after the prefix is unused.
3. **Dedup** — skip any issue that already has a live Instance in the registry
   (its studio is `cove-<identifier>`), so a ticket is never raised twice.
4. **Cap** — ask Jam's Allocator for an ephemeral reservation; stop raising
   once it denies (the role's ephemeral cap is reached); the rest wait for the
   next poll. The cap is the role's roster `max-ephemeral`
   ([roster.md](roster.md#roles)), falling back to `max-concurrent` when the role
   sets none. The count is durable (the Postgres allocation ledger, else the
   Instance registry — see [serve.md](serve.md)), so the cap holds across a
   Jam restart.
5. **Claim** — transition the issue READY → IN PROGRESS *before* raising, so a
   crash between claim and raise leaves the ticket claimed (recoverable), never
   double-raised. The transition also drops it from the next `ListReady`.
6. **Raise** — build the prompt (the issue brief + a result protocol asking the
   agent to write `.at-task/worker-result.json`) and call the supervisor's raise
   with `role`/`project` from config and `unit = <identifier>`. On a raise
   failure the issue is moved to NEEDS INPUT (surfaced, not silently retried).

## The model: elastic raise under a cap

Studios are **one-shot ephemeral** — each raised studio does one ticket and tears
itself down — so there is no pool of idle actors to assign to; each ready ticket
is a fresh raise, bounded by `max-concurrent`. The tracker's READY column is the
durable queue; the Requisitioner is the bounded consumer. This is the middle ground
between the old single-task dispatcher and raising unboundedly.

## Config (`runtime.requisitioner`)

Add a `runtime.requisitioner` block to the serve config (see [serve.md](serve.md));
omit it and Jam runs no intake. The Requisitioner needs a real
[`runtime.launcher`](serve.md#the-launcher-runtimelauncher) to raise real studios
(against a placeholder launcher it exercises intake only).

```yaml
runtime:
  requisitioner:
    role: worker              # required — role raised coves get (must grant anthropic + git)
    project: acme             # optional
    max-concurrent: 5         # required, > 0 — the backpressure cap (fallback: the role's roster max-ephemeral wins when set)
    poll-interval: 30s        # optional; defaults to 30s
    tracker-token:            # Jam's own secret to call the tracker API (never injected into a cove)
      command: ["op", "read", "op://jam/linear/token"]
    linear:                   # the Linear team + lifecycle-state map
      team: AET
      class-label-prefix: "class:"
      dispatch-label-prefix: "dispatch:"   # only issues carrying a dispatch:* label are raised (default: dispatch:)
      states: { ready: "Ready", in-progress: "In Progress", in-review: "In Review", done: "Done", needs-input: "Needs Input", blocked: "Blocked" }
```

The block also accepts `wake-poll-interval`, `wait-max`, and `warm-timeout` (the
wake-on engine) and `escalation-poll-interval` (the escalation engine). The three wake
fields are now a **fallback**: the matching `runtime.wake` field wins when set — see
[intercom.md](intercom.md#waiting-for-a-reply-wake-on). The Requisitioner also brings the
escalation engine and the Linear relay, both of which need its tracker; the intercom
itself (`/squawks`, wake-on, the Discord relay) runs without a Requisitioner.

The role must grant the `anthropic` and `git` destinations so the raised studio's
agent can reach them ([roster.md](roster.md)).

**Dispatch is opt-in per ticket.** Only issues in the READY state that *also*
carry a label matching `dispatch-label-prefix` (default `dispatch:`) are raised —
so pointing `states.ready` at a shared column (e.g. "Todo") does not sweep the
whole backlog into studios; tag the specific tickets with `dispatch:*`. The gate is
presence-only (any `dispatch:<anything>` counts). It is distinct from
`class-label-prefix`, which parses a handler *class* but does not gate.

## Not yet (deferred)

- **Outcome → tracker writeback** — a ticket stays IN PROGRESS after its studio
  finishes; writing Done/Needs-Input back (with a result comment) on completion is
  the next slice (it needs the studio's outcome propagated over the Attach stream).
- **Webhook intake** — poll only for now.
- **Multiple Jam instances** — the Requisitioner is single-instance today (the
  tracker-transition claim + registry dedup); multi-instance ticket-leasing is
  deferred.
- **Per-role/class caps** and the GitHub-issues tracker (the same `Tracker`
  interface supports it) are not wired here.
