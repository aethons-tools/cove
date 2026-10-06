---
summary: The comms target space and access-graph — kind-prefixed user:/chat:/channel:/ticket: targets (human: alias), each naming a channel of the channel log; a Project's members and rooms, Scope.Addressing authz, and send(to=…) delivery/reply semantics.
read_when: You want a studio's agent to send to someone other than its own ticket (a named human or a channel), or you're granting/scoping who a studio may address, or managing a Project's members and rooms.
owns: the target space (user:<name|usr_id>/chat:/channel:<name>/ticket:<key> + globs; the human: alias) and which channel each names, a Project's members and rooms (incl. a user's `--login` link and `--oidc` identity bindings; Discord delivery profiles and reply attribution are owned by discord.md), the comms access-graph (Scope.Addressing/Override authz, 403 vs 404), send(to=…) delivery/reply semantics, GET /squawks/targets + list_targets, and the project/role --addressing operator commands
prereqs: intercom.md for the /squawks endpoint and cove-master mcp delivery this extends; roster.md for the Role/Grant/Scope model Addressing plugs into
tier: leaf
updated: 2026-10-06
---

# Comms addressing (target space & access-graph)

A studio's `send` can reach more than its default channel: a **person** or a
group of them (a chat), a **room**, or another **ticket**'s conversation —
each a channel of the [channel log](intercom.md#enabling-it) — gated by a **comms
access-graph** that mirrors the broker's `Scope`/`Grant` model.

## The target space

A target is **kind-prefixed**, and names a channel:

| Target | Channel |
|---|---|
| `user:<name>` / `user:<usr_id>` | a chat between the session and that person |
| `chat:user:<a>,user:<b>…` | a chat between the session and those people |
| `channel:<name>` | the project's room of that name (post-only: the session doesn't join) |
| `ticket:<key>` / `ticket:<connection>/<key>` | that ticket's conversation (its own is always allowed; another's is post-only) |

A person must be a member of the session's project. The same members are the
same chat, however they're listed. `human:<name>` — the pre-registry form — is
still accepted and read as `user:`. A bare name with no known prefix is
**malformed** and always denied.

Addressing allow-lists (`Scope.Addressing`, below) are **glob-capable**, matched
with `path.Match`: `user:*` (any member), `channel:eng-*`
(channels by prefix), `*` (everything). A person matches a glob by either form —
`user:<name>` or `user:<usr_id>` — so policy can name someone by id (rename-proof;
what Jam itself writes, e.g. a personal session's grant and migrated policy) or
by name. An id is matched only by that exact id, `user:*` or `*` — a name
pattern like `user:a*` never matches an id — so ids never widen a name glob. Globs match only within
their kind — a glob never crosses `user:`/`channel:` implicitly; write both
prefixes if you mean both.

## Project members and rooms

A **Project** (the same namespace a `Role` lives in — see [roster.md](roster.md))
has two kinds of addressable things:

- **Members** — users in the Jam-wide [identity registry](roster.md) with a
  membership in the project. A member is addressed by their user name or id
  (`user:<name|usr_id>`). Their tracker `@`-handle is the **account** linked to
  them on the `linear` connection, and their Discord user id is the account on
  the `discord` connection. Their per-project **delivery** addresses (e.g. a
  Discord inbox) live on the membership. A user's **logins** link them to
  their admin login (OIDC `sub`, or `local` on loopback), so Jam knows who is
  behind an admin request, e.g. to own a
  [personal session](personal-sessions.md). Their **OIDC identity bindings**
  `{Issuer, Subject}` are for a browser login, e.g. [`/me`](intercom-ui.md);
  they are opaque, never secrets, and both parts are non-empty.
- **Rooms** — the project's named channels (`channel:<name>`) in Jam's channel
  registry, each with an id and a **binding** of a `Ref` (a tracker issue
  identifier like `ACME-1`, or a Discord channel id) on a connection.

A ref receives replies for at most one room: binding a taken ref is refused
(`409`). Removing a room archives it.

**A person is one user Jam-wide.** The same name in two projects is one person.
A login, OIDC binding, tracker handle or Discord user id belongs to one person
Jam-wide; claiming another person's is **400**. A person's logins, OIDC
bindings and service accounts are managed on the **user**, and their
per-project delivery addresses on the **membership**.

**Upgrades from per-project rosters** migrated them once (don't roll back past
it). Humans were merged into users: the same login, OIDC binding or Discord id
makes one user, else the same name; on a clash the first keeps the name and the
other becomes `<name>-<project>`. Channels became rooms: if two shared a ref,
the first kept it and the other only posts there (re-saving it unchanged keeps
it so); a service other than `linear` or `discord` stays unused in the
project's record. The upgrade logs each of these.

A Project's members also back its **escalation policy** — ordered tiers
that get `@`-mentioned while a studio is Waiting; see [escalation.md](escalation.md).

Manage members and rooms with `at-jam`:

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
at-jam room add <project> <name> --ref ACME-1 [--connection <name|id|kind>]   # adds, or rebinds
at-jam room list <project> | rename <project> <room> <new> | rm <project> <room>
```

A `<user>` is a name or a `usr_` id; renaming a user changes nothing else
(nothing refers to names). `user rm` tombstones the user and ends their
memberships; it and `user rename` are refused (409) while the user owns a live
personal session. The admin API behind
these is `/admin/users`, `/admin/projects/{p}/members`, `/admin/accounts`,
`/admin/connections` and `/admin/projects/{p}/rooms` (the per-project `/humans`
and `/channels` routes, `/admin/projects/{p}/roster`, and `project roster`,
are gone).

A room's `--connection` is a connection name or id, or a kind (`linear`,
`discord`: that kind's connection, created if there is none); it defaults to
`linear`. A `<room>` is a name
or a `chn_` id, and renaming one changes nothing else but which `channel:`
addressing globs match it. `room list` marks a room that only posts to its ref
(another channel receives its replies) `post-only`. `--delivery` and `--oidc`
are both repeatable (one flag per binding). Because an OIDC issuer is commonly
a URL that itself contains colons, `--oidc` splits on the **final** colon: the
subject is the text after it, the issuer everything before. `user list` shows
each binding as `oidc=<issuer>:<subject>`. A malformed value (empty issuer or subject) exits `2`
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
**additive across the actor's grants in the session's project**. Everything is
**fail-closed**: an unknown actor, an expired token, a role with no addressing, or
a malformed target all deny. A chat needs every person in it allowed; a ticket
other than the studio's own needs a `ticket:<glob>` (e.g. `ticket:*`). The
studio's **default channel** (`to` empty) never consults the access-graph — only
its expiry.

**Authz is checked before existence.** A target whose form no grant's addressing
allows returns **403** — the send is denied without ever asking whether the target
exists. A target that *is* authorized in form but isn't in the resolving grant's
project (e.g. `user:*` is allowed but no member `bob` exists) returns **404**. This
ordering means a 403 never reveals whether a target would otherwise exist.

## `send(text, to=…)` — delivery and reply semantics

A squawk reaches its channel's members (in Jam: their inboxes, waking a waiting
session) and is rendered onto the channel's surfaces by the relays
([intercom.md](intercom.md#enabling-it)):

| Channel | Rendered | Replies come back |
|---|---|---|
| ticket | a comment on its Linear issue | comments on the issue → the ticket's conversation |
| chat | each person's Discord inbox (a Discord-chat project); a person with none is `@<handle>`-mentioned on the ticket of a session in the chat | a Discord reply to the post → the chat; a Linear reply lands in the ticket's conversation, which the session is in too |
| room | its bound surface (a Linear issue or a Discord channel) | from that surface → the room |

A person posting in a ticket or room (from `/me`, or a linked account on the
tracker) joins it and hears what follows; a person sees a project's tickets and
rooms while they are a member of it.

## Discovering targets: `GET /squawks/targets` / `list_targets`

An agent doesn't need to know its addressing in advance. `GET /squawks/targets`
(brokered, self-derived from the caller's identity — no parameters) returns the
actor's authorized-**and**-resolvable targets:

```json
{"targets": [{"target": "ticket:ACME-7", "kind": "ticket", "name": "ACME-7"},
             {"target": "user:alice", "kind": "user", "name": "alice"}]}
```

The studio's own ticket comes first (when it has one); then the people and rooms
its addressing allows. Handles are deliberately omitted — the agent addresses by
`user:<name>`, not by handle. The `cove-master mcp` server exposes this as the `list_targets` tool,
alongside `send`'s now-optional `to` argument; see
[intercom.md](intercom.md#what-the-tools-do) for the tool surface. The same list,
taken at raise, appears in the session's [session context](session-context.md).

## Not yet (later comms slices)

- **Session channels and call-in:** a channel per session, joined by those
  called in; addressing another session directly (intercom slice 3).
- **Escalation as call-in** (slice 4): today tiers are `@`-mentioned on the ticket.

Design rationale lives in
[`../../superpowers/specs/2026-09-14-harbor-comms-addressing.md`](../../superpowers/specs/2026-09-14-harbor-comms-addressing.md)
and, for channels,
[`../../superpowers/specs/2026-10-06-intercom-slice2-channels-design.md`](../../superpowers/specs/2026-10-06-intercom-slice2-channels-design.md).
