---
summary: The resident dispatcher — harbor's always-on poll loop that turns ready tracker tickets into managed-cove raises, bounded by a concurrency cap. Covers the flow, the `runtime.dispatcher` serve-config block, and the elastic-raise-under-a-cap model.
read_when: You are enabling or operating harbor's automatic intake — having it poll a tracker (Linear) and raise a managed cove per ready ticket — or tuning its concurrency cap and poll interval.
owns: the operator-facing resident-dispatcher story — the poll→claim→raise flow, the `runtime.dispatcher` serve-config block, and the concurrency-cap model
prereqs: coves.md for what a raised managed cove does (the supervisor + Launcher own its lifecycle); serve.md for the `runtime.launcher` a raised cove needs; roster.md for the role tickets are raised for
tier: leaf
updated: 2026-09-13
---

# The resident dispatcher

The **resident dispatcher** is an always-on loop inside `at-harbor serve` that
turns ready tracker tickets into managed-cove raises — the automatic counterpart
to `at-harbor cove raise` ([coves.md](coves.md)). Enable it with a
`runtime.dispatcher` block; the supervisor + Launcher own everything after the
raise (run → report → teardown).

## The flow (per poll)

1. **List ready** — poll the tracker for issues in its READY state.
2. **Dedup** — skip any issue that already has a live Instance in the registry
   (its cove is `cove-<identifier>`), so a ticket is never raised twice.
3. **Cap** — stop raising once the number of live Instances reaches
   `max-concurrent`; the rest wait for the next poll. The count is read from the
   **durable Instance registry** each pass, so the cap holds across a harbor
   restart (and is the hook for a future multi-instance dispatcher).
4. **Claim** — transition the issue READY → IN PROGRESS *before* raising, so a
   crash between claim and raise leaves the ticket claimed (recoverable), never
   double-raised. The transition also drops it from the next `ListReady`.
5. **Raise** — build the prompt (the issue brief + a result protocol asking the
   agent to write `.at-task/worker-result.json`) and call the supervisor's raise
   with `role`/`project` from config and `unit = <identifier>`. On a raise
   failure the issue is moved to NEEDS INPUT (surfaced, not silently retried).

## The model: elastic raise under a cap

Coves are **one-shot ephemeral** — each raised cove does one ticket and tears
itself down — so there is no pool of idle actors to assign to; each ready ticket
is a fresh raise, bounded by `max-concurrent`. The tracker's READY column is the
durable queue; the dispatcher is the bounded consumer. This is the middle ground
between the old single-task dispatcher and raising unboundedly.

## Config (`runtime.dispatcher`)

Add a `runtime.dispatcher` block to the serve config (see [serve.md](serve.md));
omit it and harbor runs no intake. The dispatcher needs a real
[`runtime.launcher`](serve.md#the-launcher-runtimelauncher) to raise real coves
(against a placeholder launcher it exercises intake only).

```yaml
runtime:
  dispatcher:
    role: worker              # required — role raised coves get (must grant anthropic + git)
    project: acme             # optional
    max-concurrent: 5         # required, > 0 — the backpressure cap
    poll-interval: 30s        # optional; defaults to 30s
    tracker-token:            # harbor's own secret to call the tracker API (never injected into a cove)
      command: ["op", "read", "op://harbor/linear/token"]
    linear:                   # the Linear team + lifecycle-state map
      team: AET
      class-label-prefix: "class:"
      states: { ready: "Ready", in-progress: "In Progress", in-review: "In Review", done: "Done", needs-input: "Needs Input", blocked: "Blocked" }
```

The role must grant the `anthropic` and `git` destinations so the raised cove's
agent can reach them ([roster.md](roster.md)).

## Not yet (deferred)

- **Outcome → tracker writeback** — a ticket stays IN PROGRESS after its cove
  finishes; writing Done/Needs-Input back (with a result comment) on completion is
  the next slice (it needs the cove's outcome propagated over the Attach stream).
- **Webhook intake** — poll only for now.
- **Multiple harbor instances** — the dispatcher is single-instance today (the
  tracker-transition claim + registry dedup); multi-instance ticket-leasing is
  deferred.
- **Per-role/class caps** and the GitHub-issues tracker (the same `Tracker`
  interface supports it) are not wired here.
