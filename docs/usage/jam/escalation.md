---
summary: The escalation engine — a per-project, category-keyed ordered policy of human tiers + per-tier timeouts that actively pings while a studio is Waiting, opt-in and independent of wake-on's reply/max-wait clock.
read_when: You want a raised studio's Waiting state to actively nudge humans instead of passively waiting — configuring ordered tiers of people to @-mention with per-tier timeouts, routing by block category, or operating/tuning the resident escalation engine.
owns: the per-project escalation policy (ordered human tiers + per-tier timeout, category-keyed via `EscalationByCategory`), the auto-on-Waiting behavior (immediate tier-0 ping, advance-on-timeout, advance-on-empty-tier), the brokered `escalate(category)` tool, the `runtime.requisitioner.escalation-poll-interval` config, and the `at-jam project escalation set|list|clear [--category]` commands.
prereqs: intercom.md for the wake-on engine, the Waiting/suspend model escalation pings into, and the other brokered studio tools `escalate` sits alongside; comms-addressing.md for the Project roster (Human) and handle model tiers resolve against
tier: leaf
updated: 2026-10-05
---

# Escalation (human tiers)

While a raised studio is **Waiting** for input, a separate resident **escalation
engine** can actively nudge people instead of leaving the studio to wait passively.
A **Project** opts in with an ordered **escalation policy**: tiers of humans, each
with a timeout. The engine pings tier 0 the moment a studio starts waiting, and
escalates to the next tier if nobody answers in time. It is opt-in — a Project
with no policy behaves exactly as before (see
[intercom.md](intercom.md#waiting-for-a-reply-wake-on)). Pings land on the studio's own ticket, so the
engine skips a studio with **no ticket** — e.g. a [personal session](personal-sessions.md),
which waits on its owner instead.

## The policy: ordered tiers + per-tier timeout

A Project's `Escalation` is an ordered list of tiers, each `{Targets, Timeout}`.
`Targets` are kind-prefixed **human** names (`human:<name>`), resolved against the
same Project [Roster](comms-addressing.md#the-project-roster) used for addressed
`send(to=…)` — the roster's `Handle` is what gets `@`-mentioned. C2 v1 is
**human-only**: a `channel:` target (or anything malformed) in a tier is skipped
with a logged warning, never a hard failure. `Timeout` is how long the engine
waits after pinging that tier before moving to the next one.

## Categories: routing by block kind

A Project's escalation policy is **category-keyed**, additive over the single
chain above. `Project.Escalation` (the policy described above) is specifically
the **default/uncategorized** chain; `EscalationByCategory` maps a **free-form**
category name (e.g. `infra`, `ticket-blocked`, `code-architecture`) to its own
ordered tier chain, same `{Targets, Timeout}` shape. An unset category, an
unknown category, or a category with no configured chain all fall back to the
default chain — a cove-supplied category can only ever select among
operator-configured chains, never a recipient the operator didn't set up.

A studio declares its current block's category with the brokered **`escalate`**
tool (`escalate(category)`), one of the studio's [brokered intercom
tools](intercom.md#what-the-tools-do): Jam stamps
`Instance.EscalationCategory` on the caller's *own* instance — self-scoped, like
`read`; there's no actor/target parameter. The category **persists** until the
studio re-declares it or the instance tears down — entering Waiting does not clear
it (only the per-tier state below resets there). Calling `escalate` **only
categorizes** the block; it does not itself open an escalation — pinging still
starts solely on entering Waiting with a `needs-input` report, as above. A studio
typically calls `escalate` before `report(needs-input)` and ending its turn, so the
category is set before Jam evaluates who to ping.

Operator commands take a matching `--category <name>` flag — see [Operator
commands](#operator-commands-at-jam-project-escalation) below. Jam
**auto-detecting** a category itself (e.g. stamping `infra` on an egress-wall
denial or a `401`, without the studio calling `escalate`) is not implemented yet
— see [Not yet](#not-yet-deferred).

## Auto-on-Waiting: immediate tier-0, then advance on timeout

Escalation isn't triggered by a separate command — it starts the moment a studio is
**Waiting** with a **`needs-input`** [report](turn-end.md#reporting-a-ticket) (every
turn ends in Waiting, so only that report means it needs a person):

1. **Tier 0 is pinged immediately**, with no grace period. If you want a delay
   before the first nudge, give tier 0 a longer timeout — there is no separate
   pre-tier-0 wait.
2. If tier 0's timeout elapses with no reply, the engine pings tier 1, and so on.
3. Once the **last** tier has been pinged, the engine stops advancing — it does
   not loop, re-ping, or tear the studio down. Abandoning a studio with no reply at
   all is wake-on's `wait-max`, not escalation's job (below).

Each fresh Waiting-entry starts a new escalation from tier 0 — the engine's
per-cove tier state resets whenever a studio re-enters Waiting.

## Two independent clocks

Escalation's per-tier timer and wake-on's `wait-max` are **two independent
clocks**, run by two separate engines:

- **Escalation** only pings tiers on a schedule; it never reads comments, never
  wakes a studio, and never tears one down.
- **Wake-on** (see [intercom.md](intercom.md#waiting-for-a-reply-wake-on)) owns
  reply-detection, waking, and `wait-max` teardown — unchanged by escalation.

A human's answer — a comment on the studio's own ticket, posted in response to a
ping at *any* tier — is picked up by the existing wake-on engine exactly as any
other reply would be. Escalation itself never reads comments and has no reply
handling of its own; the two engines share only the `Activity == Waiting` gate.

## Delivery: an `@`-mention on the studio's own ticket

A ping resolves the tier's `human:<name>` targets to `@<handle>` (via the
Project's [Roster](comms-addressing.md#the-project-roster)) and posts a single
comment on the **studio's own ticket** — not a separate thread. Because the ping
lands where wake-on is already watching, a reply needs no new routing; see
[comms-addressing.md](comms-addressing.md) for the human/handle model this
reuses. Jam is the sender here (an operator-configured policy), so the comms
access-graph (`Scope.Addressing`) is not consulted — that gates a *studio's*
outbound `send`, not Jam's own escalation pings.

## A mis-configured tier can't wedge a studio

A tier whose targets don't resolve to any known human (a typo'd name, a
`channel:` target, an empty tier) posts nothing — but the engine still **advances
the timer** as if it had pinged, so escalation keeps moving to the next tier
instead of getting stuck forever on a broken one.

## Config: `runtime.requisitioner.escalation-poll-interval`

The engine lives alongside the [Requisitioner](requisitioner.md) and reuses
its tracker/Linear client. Tune how often it checks Waiting studios for tiers due
to ping:

```yaml
runtime:
  requisitioner:
    # …role / max-concurrent / linear / wake-poll-interval / wait-max as before…
    escalation-poll-interval: 30s   # how often Jam checks Waiting studios for a due tier (default 30s)
```

Omitting it keeps the 30s default. The engine only does anything for a Project
that has an escalation policy set — leaving policies unset costs nothing.

## Operator commands: `at-jam project escalation`

```
at-jam project escalation set   <project> [--category <name>] --tier 'human:alice,human:bob@15m' [--tier 'human:carol@1h' …]
at-jam project escalation list  <project>
at-jam project escalation clear <project> [--category <name>]
```

The admin UI's project page edits the same chains ([ui-pages.md](ui-pages.md#project-pages)).

- `set` **replaces** the whole ordered policy for one chain. Each `--tier` is
  `comma,separated,targets@duration` — a comma-separated list of `human:<name>`
  targets, an `@`, then a positive `time.ParseDuration` timeout (e.g. `15m`,
  `1h`); a tier with no targets is refused. Repeat
  `--tier` in order; the first is tier 0.
- `list` prints the default chain (labeled `default`), then each configured
  category's chain (labeled by category name), each tier's index, targets, and
  timeout.
- `clear` removes one chain — the Project reverts to today's passive wait for
  that chain.
- `--category <name>` on `set`/`clear` targets that category's chain (see
  [Categories](#categories-routing-by-block-kind) above) instead of the
  default; omitted on either, it's the default chain. Example:
  `at-jam project escalation set acme --category infra --tier 'human:sre@10m'`.

All three take the same admin-client flags (`--app`/`--admin-url`/`--token`) as
every other `at-jam` verb — see [operators.md](operators.md).

## Not yet (deferred)

- **Jam auto-detection** — the *implicit* setter: Jam stamping a
  category itself (e.g. `infra` on an egress-wall denial or a `401`) without
  the studio calling `escalate`. Only the agent-declared setter (`escalate`,
  [above](#categories-routing-by-block-kind)) has shipped so far. Likewise,
  populating human tiers from CODEOWNERS or a tracker's owner/assignee field is
  not wired — tiers are set by hand today.
- **Channel tiers** — a tier that pings a `channel:` target needs cross-thread
  reply-routing (a reply on a channel's own thread waking the studio), which C2 v1
  doesn't have; see [comms-addressing.md](comms-addressing.md#not-yet-later-comms-slices).
- **The reserved Attach `TierChanged` control** — v1 pings the *ticket*, not the
  *studio*, so the studio itself needs no signal about escalation state; this is the
  hook for a future model where the studio reacts to its own escalation tier.
- **Escalation observability** — surfacing a studio's current tier in
  `at-jam studio list` or the UI is a nice-to-have follow-up, not done yet.

Design rationale lives in
[`../../superpowers/specs/2026-09-14-harbor-escalation.md`](../../superpowers/specs/2026-09-14-harbor-escalation.md)
(the default-chain engine) and, for categories,
[`../../superpowers/specs/2026-09-14-harbor-escalation-categories.md`](../../superpowers/specs/2026-09-14-harbor-escalation-categories.md).
