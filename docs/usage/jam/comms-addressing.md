---
summary: The comms target space and access-graph — kind-prefixed user:/channel: targets (human: alias), a Project's Roster, Scope.Addressing authz, and send(to=…) delivery/reply semantics.
read_when: You want a studio's agent to send to someone other than its own ticket (a named human or a channel), or you're granting/scoping who a studio may address, or managing a Project's roster of humans and channels.
owns: the target space (user:<name|usr_id>/channel:<name> + globs; the human: alias), Project/Roster (Human/Channel, incl. a Human's `--login` link and `--oidc` identity bindings; Discord delivery profiles and reply attribution are owned by discord.md), the comms access-graph (Scope.Addressing/Override authz, 403 vs 404), send(to=…) delivery/reply semantics, GET /squawks/targets + list_targets, and the project/role --addressing operator commands
prereqs: intercom.md for the /squawks endpoint and cove-master mcp delivery this extends; roster.md for the Role/Grant/Scope model Addressing plugs into
tier: leaf
updated: 2026-10-06
---

# Comms addressing (target space & access-graph)

A studio's `send` can reach more than its own ticket: a named **human** or a shared
**channel**, drawn from its Project's **Roster**, gated by a **comms access-graph**
that mirrors the broker's `Scope`/`Grant` model. This is C1 of the comms-hub
escalation slice — the addressing foundation C2 (escalation policy) builds on.

## The target space

A target is **kind-prefixed**: `user:<name>` or `user:<usr_id>` for a person,
`channel:<name>` for a channel (e.g. `user:alice`, `channel:eng-help`).
`human:<name>` — the pre-registry form — is still accepted and read as `user:`
(for one release). A bare name with no known prefix is **malformed** and always
denied.

Addressing allow-lists (`Scope.Addressing`, below) are **glob-capable**, matched
with `path.Match`: `user:*` (any member), `channel:eng-*`
(channels by prefix), `*` (everything). A person matches a glob by either form —
`user:<name>` or `user:<usr_id>` — so policy can name someone by id (rename-proof;
what Jam itself writes, e.g. a personal session's grant and migrated policy) or
by name. An id is matched only by that exact id, `user:*` or `*` — a name
pattern like `user:a*` never matches an id — so ids never widen a name glob. Globs match only within
their kind — a glob never crosses `user:`/`channel:` implicitly; write both
prefixes if you mean both.

## The Project roster

A **Project** (the same namespace a `Role` lives in — see [roster.md](roster.md))
owns a **Roster** of addressable members:

- **Human** — `{Name, Handle, Login, Identity}`. `Name` is the roster-local target
  name (`user:<Name>`); `Handle` is the tracker `@`-mention handle used to deliver
  to them. `Login` (optional) links them to their **admin login** (OIDC `sub`, or
  `local` on loopback), so Jam knows who is behind an admin request, e.g. to
  own a [personal session](personal-sessions.md).
  `Identity` (optional) is a list of **OIDC identity bindings** `{Issuer, Subject}`
  — a browser OIDC subject, so a login authenticated at a provider can later map to
  this roster actor. Both parts are opaque identifiers, never secrets; both must be
  non-empty. (Data-model + CLI only for now; the auth/session mapping is a later
  slice.)
- **Channel** — `{Name, Service, Ref}`. `Name` is the roster-local target name
  (`channel:<Name>`); `Service` is the transport (`linear` or `discord`); `Ref` is
  the surface it posts to (a tracker issue identifier like `ACME-1`, or a Discord
  channel id).

**Roster channels are rooms.** Since intercom slice 2a, a project's channels are
*rooms* in Jam's channel registry, each with an id and a **binding** of its `Ref`
on the connection of its `Service` (the default connection of that kind, created
if there is none). On upgrade, each project's channels became rooms once. A ref
receives replies for at most one channel: adding a channel on a ref another
channel already holds is refused (`409`). If two existing channels shared a ref,
the first kept it and the other now only posts there; the upgrade logs which.
Removing a channel archives its room.

**Roster humans are Jam-wide users.** Since intercom slice 1a-3a, a human is
a user in the [identity registry](roster.md) plus a project membership: the
same name in two projects is one person, and a login, OIDC binding, tracker
handle or Discord user id belongs to one person Jam-wide (claiming another
person's is **400**). A person's logins, OIDC bindings and service accounts are
managed on the **user**; their per-project delivery addresses on the
**membership**. On upgrade, existing per-project
humans were merged into users once: same login/OIDC/Discord id → one user, else
same name → one user; two different people sharing a name keep it for the
first and the other becomes `<name>-<project>` (logged at startup, with that
project's exact `human:<name>` tiers, addressing and session owners rewritten; a later
upgrade step moved stored policy to `user:<usr_id>`).

A Project's roster of humans also backs its **escalation policy** — ordered tiers
that get `@`-mentioned while a studio is Waiting; see [escalation.md](escalation.md).

Manage a roster with `at-jam project`:

```
at-jam user add <name> [--login 'auth0|abc123']... [--oidc <issuer>:<subject>]...
at-jam user list | show <user> | rename <user> <new> | rm <user>
at-jam user login <user> [<login>...]        # replaces the set; none clears it
at-jam user oidc  <user> [<issuer>:<subject>...]
at-jam project member add  <project> <user> [--delivery discord:<inbox-channel>]...
at-jam project member list <project> | rm <project> <user>
at-jam account add --connection linear --handle alice.h --user <user>     # tracker @-handle
at-jam account list [--connection c] | link <account> <user> | unlink <account>
at-jam connection add --kind linear|discord --name <n> [--cred <credential>]
at-jam connection list | rename <c> <new> | cred <c> <credential> | rm <c>
at-jam project roster add-channel <project> --name eng-help --ref ACME-1 [--service linear]
at-jam project roster list        <project>
at-jam project roster rm-channel  <project> <name>
```

A `<user>` is a name or a `usr_` id; renaming a user changes nothing else
(nothing refers to names). `user rm` tombstones the user and ends their
memberships; it and `user rename` are refused (409) while the user owns a live
personal session. The admin API behind
these is `/admin/users`, `/admin/projects/{p}/members`, `/admin/accounts` and
`/admin/connections` (the per-project `/humans` routes are gone).

`--service` defaults to `linear`. `--delivery` and `--oidc` are both repeatable
(one flag per binding). Because an OIDC issuer is commonly a URL that itself
contains colons, `--oidc` splits on the **final** colon: the subject is the text
after it, the issuer everything before. `roster list` shows each binding as
`oidc=<issuer>:<subject>`. A malformed value (empty issuer or subject) exits `2`
(the admin route answers `400`). All subcommands take the same admin-client flags
(`--app`/`--admin-url`/`--token`) as every other `at-jam` verb — see
[operators.md](operators.md).

## Delivery profiles & per-project chat service

A Human may also carry per-service **delivery profiles** (a Discord inbox channel,
optionally bound to their Discord user id), and a Project a **chat service**; the
Discord egress, the reply loop, and who a Discord reply is attributed to live in
[discord.md](discord.md).

## The comms access-graph

A `Role`'s `Scope` gains `Addressing []string` — an allow-list of target globs,
alongside `Destinations`. A `Grant`'s `Override.Addressing`, when set,
**replaces** the role's addressing (no merge — same semantics as `Override.Destinations`).
Set it at role-creation with `role add --addressing`:

```
at-jam role add --project acme --name impl --addressing 'user:*,channel:eng-help'
```

`--addressing` is a comma-separated list of globs, mirroring `--destinations`.

**Authorization mirrors the broker's `Decide`:** resolved live at send time,
**additive across an actor's grants**, **per-grant existential** — a target must
be authorized *and resolvable* by some single grant's project (one grant's
addressing never recombines with another grant's roster). Everything is
**fail-closed**: an unknown actor, an expired token, a role with no addressing, or
a malformed target all deny. The studio's **own ticket** (`to` empty) never consults
the access-graph — it is always allowed, unchanged from before addressing existed.

**Authz is checked before existence.** A target whose form no grant's addressing
allows returns **403** — the send is denied without ever asking whether the target
exists. A target that *is* authorized in form but isn't in the resolving grant's
roster (e.g. `user:*` is allowed but no `bob` exists) returns **404**. This
ordering means a 403 never reveals whether a target would otherwise exist.

## `send(text, to=…)` — delivery and reply semantics

| `to` | Delivery | Reply |
|---|---|---|
| *(empty)* | own ticket (unchanged self-scoped `send`) | own ticket → existing wake-on |
| `user:<name or id>` | `@<handle>` mention posted on the studio's **own ticket** | own ticket → existing [wake-on](intercom.md#waiting-for-a-reply-wake-on) — **two-way, free** |
| `channel:<name>` | comment posted on the channel's own thread (`Channel.Ref`) | **none in C1 — post-only** |

A human target is delivered as an `@`-mention so the reply lands where the studio is
already listening — no new tracker method or wake-on wiring needed. A channel
target posts to a different ticket than the studio's own; C1 does not route replies
back (that's a later comms slice — see below).

## Discovering targets: `GET /squawks/targets` / `list_targets`

An agent doesn't need to know its addressing in advance. `GET /squawks/targets`
(brokered, self-derived from the caller's identity — no parameters) returns the
actor's authorized-**and**-resolvable targets:

```json
{"targets": [{"target": "user:alice", "kind": "user", "name": "alice"}]}
```

Handles are deliberately omitted — the agent addresses by `user:<name>`, not by
handle. (The message log still records a person as `human:<name>` until the
channel-centric log replaces it.) The `cove-master mcp` server exposes this as the `list_targets` tool,
alongside `send`'s now-optional `to` argument; see
[intercom.md](intercom.md#what-the-tools-do) for the tool surface. The same list,
taken at raise, appears in the session's [session context](session-context.md).

## Not yet (later comms slices)

- **Cross-thread reply-routing + merged inbox (Linear):** making Linear
  channel-sends two-way, and generalizing `read` into a merged, tagged
  multi-source inbox. (Discord already routes replies regardless of target
  kind — see [discord.md](discord.md#egress-the-reply-loop).)
- **Actor/role-to-actor addressing:** addressing another managed studio or Manager
  directly (waits on the Manager pillar).

Design rationale lives in
[`../../superpowers/specs/2026-09-14-harbor-comms-addressing.md`](../../superpowers/specs/2026-09-14-harbor-comms-addressing.md).
