---
summary: The escalation engine — a per-project ordered policy of human tiers + per-tier timeouts that actively pings while a cove is Waiting, opt-in and independent of wake-on's reply/max-wait clock.
read_when: You want a raised cove's Waiting state to actively nudge humans instead of passively waiting — configuring ordered tiers of people to @-mention with per-tier timeouts, or operating/tuning the resident escalation engine.
owns: the per-project escalation policy (ordered human tiers + per-tier timeout), the auto-on-Waiting behavior (immediate tier-0 ping, advance-on-timeout, advance-on-empty-tier), the `runtime.dispatcher.escalation-poll-interval` config, and the `at-harbor project escalation set|list|clear` commands.
prereqs: messaging.md for the wake-on engine and the Waiting/suspend model escalation pings into; comms-addressing.md for the Project roster (Human) and handle model tiers resolve against
tier: leaf
updated: 2026-09-14
---

# Escalation (human tiers)

While a raised cove is **Waiting** for input, a separate resident **escalation
engine** can actively nudge people instead of leaving the cove to wait passively.
A **Project** opts in with an ordered **escalation policy**: tiers of humans, each
with a timeout. The engine pings tier 0 the moment a cove starts waiting, and
escalates to the next tier if nobody answers in time. It is opt-in — a Project
with no policy behaves exactly as before (see
[messaging.md](messaging.md#waiting-for-a-reply-wake-on)).

## The policy: ordered tiers + per-tier timeout

A Project's `Escalation` is an ordered list of tiers, each `{Targets, Timeout}`.
`Targets` are kind-prefixed **human** names (`human:<name>`), resolved against the
same Project [Roster](comms-addressing.md#the-project-roster) used for addressed
`send(to=…)` — the roster's `Handle` is what gets `@`-mentioned. C2 v1 is
**human-only**: a `channel:` target (or anything malformed) in a tier is skipped
with a logged warning, never a hard failure. `Timeout` is how long the engine
waits after pinging that tier before moving to the next one.

## Auto-on-Waiting: immediate tier-0, then advance on timeout

Escalation isn't triggered by a separate command — it starts the moment a cove's
Activity transitions to **Waiting** (typically after the agent reports
`needs-input`; see [messaging.md](messaging.md#waiting-for-a-reply-wake-on)):

1. **Tier 0 is pinged immediately**, with no grace period. If you want a delay
   before the first nudge, give tier 0 a longer timeout — there is no separate
   pre-tier-0 wait.
2. If tier 0's timeout elapses with no reply, the engine pings tier 1, and so on.
3. Once the **last** tier has been pinged, the engine stops advancing — it does
   not loop, re-ping, or tear the cove down. Abandoning a cove with no reply at
   all is wake-on's `wait-max`, not escalation's job (below).

Each fresh Waiting-entry starts a new escalation from tier 0 — the engine's
per-cove tier state resets whenever a cove re-enters Waiting.

## Two independent clocks

Escalation's per-tier timer and wake-on's `wait-max` are **two independent
clocks**, run by two separate engines:

- **Escalation** only pings tiers on a schedule; it never reads comments, never
  wakes a cove, and never tears one down.
- **Wake-on** (see [messaging.md](messaging.md#waiting-for-a-reply-wake-on)) owns
  reply-detection, waking, and `wait-max` teardown — unchanged by escalation.

A human's answer — a comment on the cove's own ticket, posted in response to a
ping at *any* tier — is picked up by the existing wake-on engine exactly as any
other reply would be. Escalation itself never reads comments and has no reply
handling of its own; the two engines share only the `Activity == Waiting` gate.

## Delivery: an `@`-mention on the cove's own ticket

A ping resolves the tier's `human:<name>` targets to `@<handle>` (via the
Project's [Roster](comms-addressing.md#the-project-roster)) and posts a single
comment on the **cove's own ticket** — not a separate thread. Because the ping
lands where wake-on is already watching, a reply needs no new routing; see
[comms-addressing.md](comms-addressing.md) for the human/handle model this
reuses. Harbor is the sender here (an operator-configured policy), so the comms
access-graph (`Scope.Addressing`) is not consulted — that gates a *cove's*
outbound `send`, not harbor's own escalation pings.

## A mis-configured tier can't wedge a cove

A tier whose targets don't resolve to any known human (a typo'd name, a
`channel:` target, an empty tier) posts nothing — but the engine still **advances
the timer** as if it had pinged, so escalation keeps moving to the next tier
instead of getting stuck forever on a broken one.

## Config: `runtime.dispatcher.escalation-poll-interval`

The engine lives alongside the [resident dispatcher](dispatcher.md) and reuses
its tracker/Linear client. Tune how often it checks Waiting coves for tiers due
to ping:

```yaml
runtime:
  dispatcher:
    # …role / max-concurrent / linear / wake-poll-interval / wait-max as before…
    escalation-poll-interval: 30s   # how often harbor checks Waiting coves for a due tier (default 30s)
```

Omitting it keeps the 30s default. The engine only does anything for a Project
that has an escalation policy set — leaving policies unset costs nothing.

## Operator commands: `at-harbor project escalation`

```
at-harbor project escalation set   <project> --tier 'human:alice,human:bob@15m' [--tier 'human:carol@1h' …]
at-harbor project escalation list  <project>
at-harbor project escalation clear <project>
```

- `set` **replaces** the whole ordered policy. Each `--tier` is
  `comma,separated,targets@duration` — a comma-separated list of `human:<name>`
  targets, an `@`, then a `time.ParseDuration` timeout (e.g. `15m`, `1h`). Repeat
  `--tier` in order; the first is tier 0.
- `list` prints each tier's index, targets, and timeout.
- `clear` removes the policy — the Project reverts to today's passive wait.

All three take the same admin-client flags (`--app`/`--admin-url`/`--token`) as
every other `at-harbor` verb — see [operators.md](operators.md).

## Not yet (deferred)

- **Categories + agent-declared `escalate(category)`, and auto-detection** —
  today's policy is one fixed default per Project, pinged only on Waiting; an
  agent choosing *what* to escalate, and harbor auto-detecting an infra failure
  (an egress-wall denial or a `401`) to escalate on its own, are later slices.
  Likewise, populating human tiers from CODEOWNERS or a tracker's
  owner/assignee field is not wired — tiers are set by hand today.
- **Channel tiers** — a tier that pings a `channel:` target needs cross-thread
  reply-routing (a reply on a channel's own thread waking the cove), which C2 v1
  doesn't have; see [comms-addressing.md](comms-addressing.md#not-yet-later-comms-slices).
- **The reserved Attach `TierChanged` control** — v1 pings the *ticket*, not the
  *cove*, so the cove itself needs no signal about escalation state; this is the
  hook for a future model where the cove reacts to its own escalation tier.
- **Escalation observability** — surfacing a cove's current tier in
  `at-harbor cove list` or the UI is a nice-to-have follow-up, not done yet.

Design rationale lives in
[`../../superpowers/specs/2026-09-14-harbor-escalation.md`](../../superpowers/specs/2026-09-14-harbor-escalation.md).
