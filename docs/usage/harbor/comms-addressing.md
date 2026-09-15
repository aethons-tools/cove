---
summary: The comms target space and access-graph — kind-prefixed human:/channel: targets, a Project's Roster, Scope.Addressing authz, and send(to=…) delivery/reply semantics.
read_when: You want a cove's agent to send to someone other than its own ticket (a named human or a channel), or you're granting/scoping who a cove may address, or managing a Project's roster of humans and channels.
owns: the target space (human:<name>/channel:<name> + globs), Project/Roster (Human/Channel), the comms access-graph (Scope.Addressing/Override authz, 403 vs 404), send(to=…) delivery/reply semantics, GET /messages/targets + list_targets, and the project/role --addressing operator commands
prereqs: messaging.md for the /messages endpoint and cove-master mcp delivery this extends; roster.md for the Role/Grant/Scope model Addressing plugs into
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

This is the **data foundation** for a second messaging service beyond the
tracker — Discord egress/ingress arrive in later slices. Today, setting these
fields changes nothing about delivery: `send(to=…)` still only ever posts an
`@`-mention or a tracker comment, as described above.

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
| `human:<name>` | `@<handle>` mention posted on the cove's **own ticket** | own ticket → existing [wake-on](messaging.md#waiting-for-a-reply-wake-on) — **two-way, free** |
| `channel:<name>` | comment posted on the channel's own thread (`Channel.Ref`) | **none in C1 — post-only** |

A human target is delivered as an `@`-mention so the reply lands where the cove is
already listening — no new tracker method or wake-on wiring needed. A channel
target posts to a different ticket than the cove's own; C1 does not route replies
back (that's a later comms slice — see below).

## Discovering targets: `GET /messages/targets` / `list_targets`

An agent doesn't need to know its addressing in advance. `GET /messages/targets`
(brokered, self-derived from the caller's identity — no parameters) returns the
actor's authorized-**and**-resolvable targets:

```json
{"targets": [{"target": "human:alice", "kind": "human", "name": "alice"}]}
```

Handles are deliberately omitted — the agent addresses by `human:<name>`, not by
handle. The `cove-master mcp` server exposes this as the `list_targets` tool,
alongside `send`'s now-optional `to` argument; see
[messaging.md](messaging.md#what-the-tools-do) for the tool surface.

## Not yet (later comms slices)

- **Cross-thread reply-routing + merged inbox:** making channel-sends two-way, and
  generalizing `read` into a merged, tagged multi-source inbox.
- **C3 — Discord / multi-channel:** a real chat `Service` so a channel target can
  be a Discord channel (generalizing at-switchboard).
- **Actor/role-to-actor addressing:** addressing another managed cove or Manager
  directly (waits on the Manager pillar).

Design rationale lives in
[`../../superpowers/specs/2026-09-14-harbor-comms-addressing.md`](../../superpowers/specs/2026-09-14-harbor-comms-addressing.md).
