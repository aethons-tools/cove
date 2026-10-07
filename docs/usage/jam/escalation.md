---
summary: The escalation engine — a per-project, category-keyed ordered policy of human tiers + per-tier timeouts that calls people into a waiting session's channel when it asks for a person, opt-in and independent of wake-on's reply/max-wait clock.
read_when: You want a waiting session that asked for a person to actively reach people instead of passively waiting — configuring ordered tiers of people to call in with per-tier timeouts, routing by block category, or operating/tuning the resident escalation engine.
owns: the per-project escalation policy (ordered human tiers + per-tier timeout, category-keyed via `EscalationByCategory`), when an escalation opens (needs-input or `escalate`), tiers as call-ins and the escalation notice (immediate tier 0, advance-on-timeout, advance-on-empty-tier), the brokered `escalate(category)` tool, the `runtime.escalation-poll-interval` config, and the `at-jam project escalation set|list|clear [--category]` commands.
prereqs: intercom.md for the wake-on engine, the Waiting/suspend model, home channels and call-in escalation builds on, and the other brokered studio tools `escalate` sits alongside; comms-addressing.md for the Project members tiers resolve against
tier: leaf
updated: 2026-10-07
---

# Escalation (human tiers)

While a session is **Waiting** and has asked for a person, a separate resident
**escalation engine** can actively bring people in instead of leaving it to wait
passively. A **Project** opts in with an ordered **escalation policy**: tiers of
people, each with a timeout. Each tier is a **call-in**: its people join the
session's [home channel](intercom.md#channel-membership) (its ticket's, or its
own) and an escalation notice is posted there. Tier 0 is called the moment the
session starts waiting, the next tier if nobody answers in time. It is opt-in — a
Project with no policy behaves exactly as before (see
[intercom.md](intercom.md#waiting-for-a-reply-wake-on)). Every kind of session
escalates: ticket, standing, personal and manual.

## The policy: ordered tiers + per-tier timeout

A Project's `Escalation` is an ordered list of tiers, each `{Targets, Timeout}`.
`Targets` name people (`user:<name>` or `user:<usr_id>`; `human:<name>` is read as `user:`), resolved against the
same Project [members](comms-addressing.md#project-members-and-rooms) used for addressed
`send(to=…)`. Tiers are
**people only**: a `channel:` or `session:` target (or anything malformed) in a tier is skipped
with a logged warning, never a hard failure. `Timeout` is how long the engine
waits after calling that tier in before moving to the next one.

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
it (only the per-tier state below resets there). Calling `escalate` also **asks for
a person** until the session is next woken, which opens an escalation once it
waits (below). A session typically calls `escalate` before ending its turn, so the
category is set before Jam evaluates who to call in.

Operator commands take a matching `--category <name>` flag — see [Operator
commands](#operator-commands-at-jam-project-escalation) below. Jam
**auto-detecting** a category itself (e.g. stamping `infra` on an egress-wall
denial or a `401`, without the studio calling `escalate`) is not implemented yet
— see [Not yet](#not-yet-deferred).

## When it opens: Waiting and asking for a person

Every turn ends in Waiting, so an escalation opens only when the waiting session
**asked for a person**: a ticket session's **`needs-input`**
[report](turn-end.md#reporting-a-ticket), or **any** session's `escalate` call
since it was last woken (a resume after background work, Holding, doesn't count as
a wake). A session that asked to `end`, or that is ending or gone, is never
escalated. Then:

1. **Tier 0 is called in immediately**, with no grace period. If you want a delay
   before the first nudge, give tier 0 a longer timeout — there is no separate
   pre-tier-0 wait.
2. If tier 0's timeout elapses with no reply, the engine calls in tier 1, and so on.
3. Once the **last** tier has been called in, the engine stops advancing — it does
   not loop, re-call, or tear the session down. Abandoning a studio with no reply at
   all is wake-on's `wait-max`, not escalation's job (below).

Each fresh Waiting-entry starts a new escalation from tier 0 — the engine's
per-cove tier state resets whenever a studio re-enters Waiting.

## Two independent clocks

Escalation's per-tier timer and wake-on's `wait-max` are **two independent
clocks**, run by two separate engines:

- **Escalation** only calls tiers in on a schedule; it never reads replies, never
  wakes a session, and never tears one down.
- **Wake-on** (see [intercom.md](intercom.md#waiting-for-a-reply-wake-on)) owns
  reply-detection, waking, and `wait-max` teardown — unchanged by escalation.

A person's answer — in the session's channel, on any surface, at *any* tier —
wakes it like any other reply. Escalation has no reply handling of its own; the
two engines share only the `Activity == Waiting` gate.

## Delivery: a call-in and an escalation notice

A tier's `user:` targets are called into the session's home channel (people
already in it stay in; called-in people stay members afterwards and may leave),
and the session posts an **escalation notice** there: "Needs input on ACME-7 —
called in alice, bob (escalation tier 1, infra). Replies here go to ACME-7's
conversation, its issue included." The relays render it wherever the channel goes:

- a **ticket channel**: a comment on its issue that `@`-mentions the tier's people
  by their tracker handles (as before — also when a room holds the issue's
  binding), and each called-in person's Discord inbox in a Discord-chat project.
  A Discord reply to it lands in the ticket's conversation, so it is also posted
  on the issue; the session's answer goes to the issue (and `/me`), not back to
  that inbox;
- a **session channel**: each member's Discord inbox, like any post there; a
  person with only Linear sees it in [`/me`](intercom-ui.md).

Jam acts on the operator's policy here, so the comms access-graph
(`Scope.Addressing`) is not consulted. The notice is for people: it never wakes
another session. The engine needs no Requisitioner — it runs whenever Jam does.

## A mis-configured tier can't wedge a session

A tier whose targets don't resolve to any known human (a typo'd name, a
`channel:` target, an empty tier) posts nothing — but the engine still **advances
the timer** as if it had called someone in, so escalation keeps moving to the next tier
instead of getting stuck forever on a broken one.

## Config: `runtime.escalation-poll-interval`

How often Jam checks waiting sessions for a tier due to call in:

```yaml
runtime:
  escalation-poll-interval: 30s   # default 30s
```

`runtime.requisitioner.escalation-poll-interval` (where it used to live) is still
read; the top-level key wins when both are set, and an invalid one stops `serve`
at startup. The engine only does anything
for a Project that has an escalation policy set.

## Operator commands: `at-jam project escalation`

```
at-jam project escalation set   <project> [--category <name>] --tier 'user:alice,user:bob@15m' [--tier 'user:carol@1h' …]
at-jam project escalation list  <project>
at-jam project escalation clear <project> [--category <name>]
```

The admin UI's project Escalation page edits the same chains ([ui-projects.md](ui-projects.md#sections)).

- `set` **replaces** the whole ordered policy for one chain. Each `--tier` is
  `comma,separated,targets@duration` — a comma-separated list of `user:<name>`
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
  `at-jam project escalation set acme --category infra --tier 'user:sre@10m'`.

All three take the same admin-client flags (`--app`/`--admin-url`/`--token`) as
every other `at-jam` verb — see [operators.md](operators.md).

## Not yet (deferred)

- **Jam auto-detection** — the *implicit* setter: Jam stamping a
  category itself (e.g. `infra` on an egress-wall denial or a `401`) without
  the studio calling `escalate`. Only the agent-declared setter (`escalate`,
  [above](#categories-routing-by-block-kind)) has shipped so far. Likewise,
  populating human tiers from CODEOWNERS or a tracker's owner/assignee field is
  not wired — tiers are set by hand today.
- **Session and room tiers** — a tier that calls another session in (an on-call
  agent) or posts into a room.
- **The reserved Attach `TierChanged` control** — the session itself gets no signal
  about its escalation state; this is the hook for a future model where it reacts
  to its own escalation tier.
- **Escalation observability** — surfacing a studio's current tier in
  `at-jam studio list` or the UI is a nice-to-have follow-up, not done yet.

Design rationale lives in
[`../../superpowers/specs/2026-09-14-harbor-escalation.md`](../../superpowers/specs/2026-09-14-harbor-escalation.md)
(the default-chain engine), for categories,
[`../../superpowers/specs/2026-09-14-harbor-escalation-categories.md`](../../superpowers/specs/2026-09-14-harbor-escalation-categories.md),
and for call-in
[`../../superpowers/specs/2026-10-07-intercom-slice4-escalation-call-in-design.md`](../../superpowers/specs/2026-10-07-intercom-slice4-escalation-call-in-design.md).
