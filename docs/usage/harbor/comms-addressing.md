---
summary: The comms target space and access-graph — kind-prefixed human:/channel: targets, a Project's Roster, Scope.Addressing authz, and send(to=…) delivery/reply semantics.
read_when: You want a cove's agent to send to someone other than its own ticket (a named human or a channel), or you're granting/scoping who a cove may address, or managing a Project's roster of humans and channels.
owns: the target space (human:<name>/channel:<name> + globs), Project/Roster (Human/Channel), the comms access-graph (Scope.Addressing/Override authz, 403 vs 404), send(to=…) delivery/reply semantics, GET /squawks/targets + list_targets, and the project/role --addressing operator commands
prereqs: intercom.md for the /squawks endpoint and cove-master mcp delivery this extends; roster.md for the Role/Grant/Scope model Addressing plugs into
tier: leaf
updated: 2026-09-15
---

# Comms addressing (target space & access-graph)

A cove's `send` can reach more than its own ticket: a named **human** or a shared
**channel**, drawn from its Project's **Roster**, gated by a **comms access-graph**
that mirrors the broker's `Scope`/`Grant` model. This is C1 of the comms-hub
escalation slice — the addressing foundation C2 (escalation policy) builds on.

## The target space

A target is a **kind-prefixed name**: `human:<name>` or `channel:<name>` (e.g.
`human:alice`, `channel:eng-help`). A bare name with no known prefix is
**malformed** and always denied.

Addressing allow-lists (`Scope.Addressing`, below) are **glob-capable**, matched
with `path.Match` exactly like `Scope.Repos`: `human:*` (any human), `channel:eng-*`
(channels by prefix), `*` (everything). Globs match only within their kind — a
glob never crosses `human:`/`channel:` implicitly; write both prefixes if you mean
both.

## The Project roster

A **Project** (the same namespace a `Role` lives in — see [roster.md](roster.md))
owns a **Roster** of addressable members:

- **Human** — `{Name, Handle}`. `Name` is the roster-local target name
  (`human:<Name>`); `Handle` is the tracker `@`-mention handle used to deliver to
  them.
- **Channel** — `{Name, Service, Ref}`. `Name` is the roster-local target name
  (`channel:<Name>`); `Service` is the transport (`linear` in C1); `Ref` is a
  tracker issue identifier (e.g. `ACME-1`) the channel posts to.

A Project's roster of humans also backs its **escalation policy** — ordered tiers
that get `@`-mentioned while a cove is Waiting; see [escalation.md](escalation.md).

Manage a roster with `at-harbor project`:

```
at-harbor project roster add-human   <project> --name alice --handle alice.h
at-harbor project roster add-channel <project> --name eng-help --ref ACME-1 [--service linear]
at-harbor project roster list        <project>
at-harbor project roster rm-human    <project> <name>
at-harbor project roster rm-channel  <project> <name>
```

`--service` defaults to `linear`. All subcommands take the same admin-client flags
(`--app`/`--admin-url`/`--token`) as every other `at-harbor` verb — see
[operators.md](operators.md).

## Delivery profiles & per-project chat service

**Discord egress and reply-routing are both live.** A discord-project's
`send(to=human:<name>)` posts to that human's Discord **inbox channel** when
they have a `discord` delivery profile (falling back to the Linear
`@`-mention when they don't); `send(to=channel:<name>)` posts to a discord
roster channel's own `Ref` when the channel's `Service` is `discord`. Every
Discord post is prefixed `"<cove>: "` (the sending cove's identity —
`Delivery.BodyPrefix`, no webhook this slice; per-sender webhook
username/avatar is a future polish). Delivery is exactly-once (the resident
Discord relay engine's own `EgressMark`, seeded to the Log tail on first
enable so turning it on never redelivers the backlog) — see
[intercom.md](intercom.md#enabling-it) for the engine and
[serve.md](serve.md) for the `runtime.discord.bot-token` config that enables
it (requires an intercom-log; without one the engine doesn't run).

**The reply loop:** when a human **replies** (Discord's own reply-to-message
feature, not a bare follow-up post) to a cove's Discord post, harbor routes
that reply back to the cove that sent the original squawk — the same
[wake-on](intercom.md#waiting-for-a-reply-wake-on) a Linear reply triggers,
so a Waiting cove resumes with the reply already in its inbox. Routing works
by a small **receipt**: on every Discord post the engine records the posted
squawk's id against the sending cove (`discord-msg-id → actor`); an inbound
squawk is matched to that receipt by the id it *replies to*. Two
consequences follow directly from that mechanism:

- **Only a reply routes.** A bare (non-reply) squawk posted into a shared
  inbox channel carries no id to look up against, so it can't be attributed
  to any cove — it is silently dropped, by construction (this also means
  harbor's own outbound Discord posts, echoed back on the same channel,
  never mis-route to themselves; no separate self-post filter is needed).
- **Receipts are currently unpruned.** The receipt store grows by one entry
  per Discord post and is never garbage-collected — a known follow-up, not a
  correctness issue today (an unbounded map on local disk, not a leak
  visible to any cove).

A **Human** additionally carries `Delivery []{Service, Address}` — one entry per
non-tracker service the human can be reached on. For `Service: "discord"`,
`Address` is the id of the **inbox channel** harbor posts that human's DMs into
(never a bot token or other secret — see [operators.md](operators.md) for where
credentials actually live). Look up a human's profile for a service with
`Human.DeliveryFor(service)`.

A **Project** additionally carries `ChatService string` — the service backing
that project's human DMs (e.g. `"discord"`); empty means tracker `@`-mentions
only, same as before this field existed.

Set a human's delivery profiles with `--delivery service:address` on
`add-human` (repeatable — one flag per service):

```
at-harbor project roster add-human <project> --name alice --handle alice.h \
  --delivery discord:123456789
```

Each `--delivery` value splits on the first `:`; both the service and the
address must be non-empty, or the command exits `2` (e.g. `discord:`, `:123`,
or a value with no `:` are all rejected).

Manage a project's chat service with `at-harbor project chat-service`:

```
at-harbor project chat-service set   --project <project> --service discord
at-harbor project chat-service show  --project <project>
at-harbor project chat-service clear --project <project>
```

`set` requires `--project` and `--service`; `clear` is `set` with `""` under
the hood; `show` prints the configured service or `(none)`. All three take the
same admin-client flags as every other `at-harbor` verb.

## The comms access-graph

A `Role`'s `Scope` gains `Addressing []string` — an allow-list of target globs,
alongside `Destinations`/`Repos`. A `Grant`'s `Override.Addressing`, when set,
**replaces** the role's addressing (no merge — same semantics as `Override.Repos`).
Set it at role-creation with `role add --addressing`:

```
at-harbor role add --project acme --name impl --addressing 'human:*,channel:eng-help'
```

`--addressing` is a comma-separated list of globs, mirroring `--destinations`/`--repos`.

**Authorization mirrors the broker's `Decide`:** resolved live at send time,
**additive across an actor's grants**, **per-grant existential** — a target must
be authorized *and resolvable* by some single grant's project (one grant's
addressing never recombines with another grant's roster). Everything is
**fail-closed**: an unknown actor, an expired token, a role with no addressing, or
a malformed target all deny. The cove's **own ticket** (`to` empty) never consults
the access-graph — it is always allowed, unchanged from before addressing existed.

**Authz is checked before existence.** A target whose form no grant's addressing
allows returns **403** — the send is denied without ever asking whether the target
exists. A target that *is* authorized in form but isn't in the resolving grant's
roster (e.g. `human:*` is allowed but no `bob` exists) returns **404**. This
ordering means a 403 never reveals whether a target would otherwise exist.

## `send(text, to=…)` — delivery and reply semantics

| `to` | Delivery | Reply |
|---|---|---|
| *(empty)* | own ticket (unchanged self-scoped `send`) | own ticket → existing wake-on |
| `human:<name>` | `@<handle>` mention posted on the cove's **own ticket** | own ticket → existing [wake-on](intercom.md#waiting-for-a-reply-wake-on) — **two-way, free** |
| `channel:<name>` | comment posted on the channel's own thread (`Channel.Ref`) | **none in C1 — post-only** |

A human target is delivered as an `@`-mention so the reply lands where the cove is
already listening — no new tracker method or wake-on wiring needed. A channel
target posts to a different ticket than the cove's own; C1 does not route replies
back (that's a later comms slice — see below).

## Discovering targets: `GET /squawks/targets` / `list_targets`

An agent doesn't need to know its addressing in advance. `GET /squawks/targets`
(brokered, self-derived from the caller's identity — no parameters) returns the
actor's authorized-**and**-resolvable targets:

```json
{"targets": [{"target": "human:alice", "kind": "human", "name": "alice"}]}
```

Handles are deliberately omitted — the agent addresses by `human:<name>`, not by
handle. The `cove-master mcp` server exposes this as the `list_targets` tool,
alongside `send`'s now-optional `to` argument; see
[intercom.md](intercom.md#what-the-tools-do) for the tool surface.

## Not yet (later comms slices)

- **Cross-thread reply-routing + merged inbox (Linear):** making Linear
  channel-sends two-way, and generalizing `read` into a merged, tagged
  multi-source inbox. (Discord already routes replies regardless of target
  kind — see the reply loop above.)
- **Discord receipt pruning:** the discord-msg-id→cove receipt store (above)
  is currently unpruned — an unbounded, never-garbage-collected map on local
  disk.
- **Actor/role-to-actor addressing:** addressing another managed cove or Manager
  directly (waits on the Manager pillar).

Design rationale lives in
[`../../superpowers/specs/2026-09-14-harbor-comms-addressing.md`](../../superpowers/specs/2026-09-14-harbor-comms-addressing.md).
