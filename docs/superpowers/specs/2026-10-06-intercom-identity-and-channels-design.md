# intercom: identity registry, channels & channel sources — architecture overview

**Status:** model approved in brainstorming (2026-10-05/06); overview spec, pre-plan. Each slice below gets its own detailed spec → plan → PRs.
**Owner:** intercom subsystem.
**Supersedes (when built):** the name-keyed target space of [`2026-09-14-harbor-comms-addressing.md`](2026-09-14-harbor-comms-addressing.md) and the per-project `Human` roster; the "owner" / "default recipient" comms semantics of personal sessions.

## Summary

Today every intercom reference is a **name**: `human:<name>`, `channel:<name>`,
`squawks.project = <project name>`, escalation tiers, unread cursors,
`Instance.Owner`, allocator streams, relay cursor keys. Renaming breaks history,
remove-then-re-add silently merges two entities, and `human:alice` in two
projects shares one inbox key (the recipient index has no project). Session ids
are deterministic (`standing-<project>-<role>-<name>`, `cove-<ISSUE>`), so a
re-raised session inherits its predecessor's inbox.

This design moves everything to **surrogate ids** owned by one **registry**, and
re-centres the intercom on **channels** whose behaviour is supplied by pluggable
**channel sources**. The intercom becomes *mechanism* (identity, append, storage,
cursors, fan-out, enforcement); sources are *policy* (which channels exist, who
may post/see, who is notified).

## Decisions (brainstorming record)

1. **Surrogate ids everywhere a reference is stored** — the log, cursors,
   escalation, owners, allocator and relay state. Names are labels, resolved at
   the edges. Role `Scope.Addressing` globs stay **name-based policy**.
2. **A session is a session.** Every raise mints a fresh id; teardown ends it
   forever; recreating with the same name is an unrelated session.
3. **Unrostered external senders** are identified by their service id
   (an *account*), with the display name as a label.
4. **Existing data:** config and live sessions migrate to ids; the pre-cutover
   squawk log is **frozen read-only** as legacy history; new traffic starts on ids.
5. **One registry** (approach 1 of 3): a single owner and lookup for identity,
   with tombstones instead of deletes.
6. **`human` → `user`.** A *user* is a person registered with Jam who talks to
   agents; an *operator* (unchanged term) manages configuration. Service-side
   identities are *accounts*, which a user may have.
7. **Users are Jam-wide**, with project **memberships**. User names are Jam-wide;
   no per-project nicknames.
8. **External services are not singletons** — each configured instance is a
   *connection*.
9. **Slack model:** every squawk belongs to exactly **one channel**.
10. **Channel kind = what the conversation is anchored to**; each kind is a
    **channel source** that owns its policy.
11. **Chats have fixed membership** (a Slack group DM); an evolving group is a room.
12. **Session channels:** every session has a channel it is always in, addressed
    by referring to the session; others are *called in*. No "owner"; no
    "default recipient" beyond "my own channel".
13. **Call-in:** any current member may invite; anyone whose addressing allows the
    session may enter on their own; an inviter must also be allowed to address
    the invitee.

## 1. Identity

### Ids

Opaque, kind-prefixed, time-sortable random: `<prefix>_<base32>` (crypto/rand,
the repo's existing pattern). Never derived from a name, never reused.

| Entity | Prefix | Scope | Unique live name |
|---|---|---|---|
| Project | `prj_` | Jam | Jam-wide |
| User | `usr_` | Jam | Jam-wide |
| Connection | `con_` | Jam | Jam-wide |
| Account | `acc_` | Jam | — (unique `(connection_id, service_uid)`) |
| Channel | `chn_` | project | per source (room: name per project; ticket: `(connection, key)`; chat: member set) |
| Session | `ses_` | project | — (label only) |

### Registry

- `participants(id PK, kind)` — a supertable every entity inserts into, so
  columns in other tables (`from_id`, `participant_id`, `project_id`, …) can be
  real foreign keys across entity kinds.
- `projects(id, name, status, doc)` — the doc keeps context, resources and
  escalation; its references become ids.
- `users(id, name, status, …)` — logins and OIDC bindings move here from the
  project roster.
- `memberships(project_id, user_id, status, delivery)` — the project roster;
  per-project delivery stays here (chat service is per project).
- `connections(id, kind, name, status, credential_ref)` — `linear`, `discord`,
  `github`, … Requisitioner, relay and `ChatService` config reference a
  connection by name.
- `accounts(id, connection_id, service_uid, label, user_id NULL)` — absorbs
  `Human.Handle` and `DeliveryProfile.UserID`. Unlinked = an external sender.
  Linking later attributes it without rewriting the log.
- `instances` — keyed by `ses_…`; `Project` → `prj_`; today's deterministic id,
  standing name and ticket become labels/lookup columns.

**Lifecycle:** `status ∈ {live, removed}`. Removal is a tombstone, so an id
always resolves ("alice (removed)"). Partial unique indexes enforce live-name
uniqueness, so **rename is a one-row update** with no fixups.

**`Directory`:** `Resolve(id) → {id, kind, label, status}` and
`Lookup(scope, kind, name) → id` (live only). Every edge — send, read, UIs, CLI,
export — goes through it; nothing below the edges handles names.

## 2. Channels and channel sources

A **channel** (`chn_`) is a conversation. Its **kind** is what it is anchored to:

| Kind | Anchored to | Lives | Membership | Address |
|---|---|---|---|---|
| `session` | an agent at work | the session | the session always; others called in, may leave | `session:<label>` |
| `ticket` | a work item | the ticket | sessions working it (join at raise, leave at teardown) + tracker participants | `ticket:<key>` / `ticket:<connection>/<key>` |
| `room` | a topic | until removed | configured | `channel:<name>` |
| `chat` | just the people | while its members exist | fixed member set (sender included) | `chat:<member>[,<member>…]`; `user:<name>` ≡ `chat:user:<name>` |

A channel has zero or more **bindings** `(connection_id, ref)` — the surfaces it
is rendered on / ingested from (a Linear issue, a Discord channel, a user's
Discord inbox). Today's `Channel{Service, Ref}` becomes a binding.

### The source interface (in-process, registered at serve startup)

```go
type Source interface {
    Kind() string
    Resolve(scope, addr string) (ChannelID, error)    // may create on demand
    CanPost(p ParticipantID, ch ChannelID) Decision
    CanSee(p ParticipantID, ch ChannelID) bool
    Audience(m Squawk) []ParticipantID               // inbox / wake fan-out
    Bindings(ch ChannelID) []Binding
}
// lifecycle is pushed: Created(ch) / Deactivated(ch) → intercom records it
```

**Invariants the intercom enforces regardless of source:**

- **The intercom owns the channel row** (id, kind, source, status); sources own
  everything else. Ids and foreign keys stay stable whatever a source does.
- **Fail-closed composition:** role `Scope.Addressing` is the operator's
  ceiling; a source's `CanPost` can only narrow it.
- **Audience is decided at append time and recorded**
  (`squawk_deliveries(squawk_id, participant_id)`). Inboxes, wake-on and unread
  counts read that table, so policy changes move forward but never rewrite
  history below a commit cursor.
- **Visibility is live:** `CanSee` is evaluated at read time (history browsing,
  `read` from `start`), so revocation is immediate.

## 3. The log and the wire

- `squawks(seq, id, channel_id, from_id, body, at, reply_to, content_type)` —
  one channel per squawk; `To[]`, `squawk_recipients` and `Squawk.Project` go away
  (the channel carries the project). Seq and ids continue from the legacy tail so
  wire cursors stay monotonic across the cutover.
- `channel_members(channel_id, participant_id, joined_seq, left_seq)`.
- Legacy `squawks`/`squawk_recipients` are renamed `legacy_*`, frozen, and served
  by a separate read-only path (UI history).
- **A session's inbox** = deliveries to it (from others), after it joined.
  `read` stays one queue with one commit cursor; entries gain
  `channel: {id, kind, label}` and `from: {id, kind, label}` (replacing the
  kind-less `author`).
- **`send` with no `to`** posts to the sender's own session channel.
- **Egress** renders each squawk to its channel's bindings (labels and handles
  resolved at delivery). **Ingress** maps `(connection, ref)` → binding → channel,
  and the author → account (or its linked user, at receipt time).
- Unread cursors become `(participant_id, channel_id)`; the UI's synthesized
  `named:`/`studio:`/`dm:` ids are replaced by real channel ids.

## 4. What goes away

- `Human` per project, `Handle`, `DeliveryProfile.UserID` (→ users, memberships, accounts).
- Deterministic actor ids as identity (→ labels).
- "Owner" as a comms concept (`Instance.Owner` remains as "raised by", for audit)
  and "default recipient" special cases.
- The relay's `Instance.Unit` matching for ticket replies (→ the ticket source).
- Escalation `@`-mentions elsewhere (→ calling tiers into the session channel, slice 4).

## Slices

Each is independently shippable, in dependency order, with its own spec/plan.

1. **Identity registry** — ids for projects, users (incl. the human→user
   rename), memberships, accounts, connections; tombstones + rename; ids in every
   non-log reference (instances, cursors, session/alloc events, relay state);
   config export/import v2. The current log keeps working through the resolver.
2. **Channel-centric intercom** — channel registry, the source interface,
   recorded audience + live visibility, `chat`/`ticket`/`room` sources, the new
   log (legacy frozen at this cutover — the only log migration), new
   `send`/`read` wire shapes, binding-driven relays. Until slice 3, an omitted
   `to` keeps today's targets (a ticket session → its ticket channel; a personal
   session → a chat with the user who raised it; standing → `400`).
3. **Session channels** — the session source, call-in / self-join / invite, removal
   of owner and default-recipient semantics, agent↔agent collaboration.
4. **Escalation as call-in** — tiers call users into the session channel.

## Out of scope

- Role ids (roles stay keyed `(project_id, name)`); role rename can follow the same pattern later.
- Renaming "operator" to "admin" (cosmetic; separate if ever wanted).
- Room membership for sessions (rooms are post-only for sessions until a later slice decides role-based membership or join tools).
- Per-message label snapshots (tombstones keep history readable; addable later).
