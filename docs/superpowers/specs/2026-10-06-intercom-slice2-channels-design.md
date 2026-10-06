# intercom slice 2: channel-centric intercom — design

**Status:** approved (2026-10-06); §9 questions decided as recommended.
**Parent:** [`2026-10-06-intercom-identity-and-channels-design.md`](2026-10-06-intercom-identity-and-channels-design.md), slice 2 of 4. The parent holds the vocabulary, the decisions record and the source interface sketch. Slice 1 ([`…-slice1-identity-registry-design.md`](2026-10-06-intercom-slice1-identity-registry-design.md)) gave every participant a surrogate id.
**Delivers:**
- a channel registry;
- the source interface;
- `chat`, `ticket` and `room` sources;
- recorded audience with live visibility;
- the new log (the legacy log is frozen at this cutover);
- new `send`/`read` wire shapes;
- binding-driven relays;
- `/me` and the admin intercom view on real channels.

**Deferred:** session channels, call-in and the end of owner semantics (slice 3); escalation as call-in (slice 4); project-id references and project rename (1b-2/1b-3).

## 1. What changes, in one paragraph

Today a squawk is `{From, To[], Project}` with name-keyed targets. Readers rebuild conversations from `To[]`: the `/me` rail synthesizes `studio:`, `named:` and `dm:` ids, and the relays guess a surface from the target kind.

After this slice, every squawk belongs to **one channel** (`chn_`), and its author is a **participant id** (`ses_`, `usr_` or `acc_`). Who receives it is decided once, at append, by the channel's **source** and recorded as **deliveries**. An inbox is just deliveries. The relays render a channel to its **bindings** and ingest from them. Nothing below the edges handles a name.

## 2. Channels (`internal/jam`, jam migration `0010_channels.sql`)

```sql
channels(id text PK → participants, project_id → projects, kind text, key text,
         label text, status text, created_at, archived_at)
  UNIQUE (project_id, kind, key) WHERE status = 'live'
channel_bindings(channel_id → channels, connection_id → connections, ref text,
                 mode text CHECK (mode IN ('both','egress')))
  PK (channel_id, connection_id, ref)
  UNIQUE (connection_id, ref) WHERE mode = 'both'    -- ingress resolves to one channel
channel_members(channel_id → channels, participant_id → participants,
                joined_seq bigint, left_seq bigint NULL)
  PK (channel_id, participant_id, joined_seq)
channel_reads(participant_id → participants, channel_id → channels, seq bigint)
  PK (participant_id, channel_id)                     -- the /me unread cursor
```

- `channels` rows are inserted into `participants` (kind `chn`), so a channel id can be a foreign key like any other id. (This is used by deliveries, reads and slice 3's call-in.)
- **The intercom owns the row** (id, kind, status, membership rows). The source decides what the row *means*:
  - the `key` (its identity within the project and kind);
  - the `label`;
  - who is a member;
  - who may post or see;
  - the bindings.
- `status ∈ {live, archived}`. An archived channel keeps its history readable and accepts no posts.
- `Directory.Resolve(chn_…)` returns `{id, kind: "channel", label, status}`, plus the channel kind. `ident.Channel` (`chn`) stops being reserved.

### The source interface

As sketched in the parent, now concrete:

```go
type Source interface {
    Kind() string                                              // "chat" | "ticket" | "room"
    Resolve(ctx Ctx, addr string) (ChannelID, error)           // may create; ErrNoChannel, ErrDenied
    CanPost(p ParticipantID, ch Channel) error                 // narrows the addressing ceiling
    CanSee(p ParticipantID, ch Channel) bool                   // evaluated at read time
    Audience(ch Channel, from ParticipantID) []ParticipantID   // decided at append, recorded
    Bindings(ch Channel) []Binding                             // for egress; read at delivery
}
```

`Ctx` carries the poster (a participant id), their project and, for a session, its addressing ceiling. Sources are registered at serve startup. The registry and the members are store state; the sources are stateless policy over them.

### The three sources in this slice

| | `chat` | `ticket` | `room` |
|---|---|---|---|
| Key | sorted member ids, `,`-joined | `<connection id>/<issue key>` | name (unique per project) |
| Label | the other members' labels, from the viewer's side | the issue key | the name |
| Created | on first `Resolve` | when a session is set up on the ticket (the dispatcher); an ingress comment never creates one | by an operator (`at-jam room add`) |
| Members | fixed: the member set | sessions working it (they join at setup and leave at end) and users who posted or were posted to here | users who joined (`/me` join, or by posting) |
| `CanSee` | members | members, and every member of the project | members of the project |
| `CanPost` | members | members, and project members | project members; sessions if their addressing allows `channel:<name>` (post-only, as today) |
| Audience | members except the author; ended sessions skipped | current members except the author | current members except the author |
| Bindings | each user member's delivery on the project's chat connection (`both`); otherwise, for a ticket session member, `(tracker, its issue)` with an `@`-mention (`egress`, §6) | `(tracker connection, issue key)` (`both`) | its configured binding (`both`), or none |
| Archived | when a user member is removed | never (a re-dispatch rejoins the same channel) | `at-jam room rm` |

- **Ticket channels outlive sessions.** A re-dispatched ticket's new session joins the *same* channel. Its inbox still starts empty: the 1b decision holds because deliveries are recorded per participant, and the new session joins after the old traffic. The channel's history is the ticket's whole conversation.
- **Rooms replace `Roster.Channels`.** The migration turns each roster channel `{Name, Service, Ref}` into a room with that name and one binding `(the connection of kind Service, Ref)`. `Roster.Channels` is then dropped from the project doc.

## 3. The log (intercompg migration `0004_channel_log.sql`)

```sql
ALTER TABLE squawks RENAME TO legacy_squawks;                 -- frozen; read-only path
ALTER TABLE squawk_recipients RENAME TO legacy_squawk_recipients;
squawks(seq bigint PK, id text UNIQUE, channel_id → channels, from_id → participants,
        body, at, reply_to, content_type, origin_connection_id NULL → connections)
squawk_deliveries(seq → squawks, participant_id → participants, PK (participant_id, seq))
```

- **Seq continues from the legacy tail** (a new sequence started at `max(legacy seq) + 1`). That keeps `CommitSeq`, `WaitSeq`, the relay egress marks and wire cursors monotonic across the cutover. `SeqOf` and `SeenIDs` consult both tables, so a cursor holding a legacy id resolves and an already-ingested `in:linear:<uuid>` is not ingested again.
- **`origin_connection_id`** marks an ingested squawk. It is used for echo suppression (§6) and replaces the `Classify(From)` echo guard.
- The FKs reach the jam tables. intercompg migrations already run after the jam store opens on the same pool (`cmd/at-jam/main.go`), and the integration harness will open both.

**`intercom` package (stays stdlib plus `internal/ident`):**

```go
type Squawk struct { Seq int64; ID string; Channel, From ident.ID; Body string; At time.Time
                     ReplyTo, ContentType string; Origin ident.ID }
type Store interface {
    Append(m Squawk, audience []ident.ID) (Squawk, error)   // one tx: squawk + deliveries
    InboxSince(p ident.ID, afterSeq int64, limit int) []Squawk
    InboxBefore(p ident.ID, beforeSeq int64, limit int) []Squawk
    ChannelSince(ch ident.ID, afterSeq int64, limit int) []Squawk
    ChannelBefore(ch ident.ID, beforeSeq int64, limit int) []Squawk
    ListSince(afterSeq int64, limit int) []Squawk            // relays, admin view
    ReadThread(rootID string) []Squawk
    SeqOf(id string) (int64, bool); TailSeq() (int64, bool); SeenIDs(prefix string) []string
    Close() error
}
type LegacyStore interface { /* today's read methods over legacy_*, unchanged */ }
```

`Target`, `To[]`, `Project`, `Filter.Project` and `Classify` are deleted. The in-memory `Log` and the conformance suite in `intercomtest` are rewritten to match.

## 4. Posting: one path for every writer

```go
func (ic *Intercom) Post(from ident.ID, ch ident.ID, body, contentType, replyTo string, o PostOpts) (Squawk, error)
```

`Post` is the only appender:
- the agent's `send`;
- `/me/send`;
- relay ingress;
- the nagger.

`Post` does five things in order:
1. Loads the channel and refuses an archived one.
2. Checks `CanPost`, unless `o.Trusted` is set. Trusted is for relay ingress, which was already resolved by binding, and for Jam's own notices.
3. Takes `Audience`.
4. Appends the squawk with its deliveries.
5. Records membership: a user posting in a ticket or room becomes a member.

**Authorization is unchanged in spirit and fail-closed.** A session's role `Scope.Addressing` is the ceiling, matched against the *address it used*: `user:alice`, `channel:eng-help`, or `ticket:ACME-2` (a new glob kind). `CanPost` can only narrow it. Authorization is checked before existence, so a forbidden address is a 403 and an authorized but unknown one is a 404. The empty `to` never consults addressing (as today).

## 5. The agent wire (`/squawks`, `cove-master mcp`)

**Addresses** (`send(to=…)`, `list_targets`):

| `to` | Channel |
|---|---|
| *(empty)* | the default (§5.1) |
| `user:<name\|usr_id>` (`human:` alias kept) | `chat` {self, user} |
| `chat:user:<a>[,user:<b>…]` | `chat` {self, the users} |
| `channel:<name>` | `room` |
| `ticket:<key>` / `ticket:<connection>/<key>` | `ticket` (its own ticket always; any other needs `ticket:` addressing; post-only unless a member) |

Session-to-session addressing (`session:`) arrives with slice 3.

### 5.1 The default target until slice 3

A ticket session's default is **its ticket channel**. A personal session's default is **the chat with the user who started it**. A standing session has no default and gets `400`, as today. This is exactly today's behaviour, now expressed as channels.

**`read`** is the session's deliveries, as before. It is one queue with one commit cursor, and the anchors, dirs and limits are unchanged. Entries are **additive**, because studios run the `cove-master` that is baked into their image and it can be older than Jam:

```json
{"id": "…", "author": "alice", "body": "…", "at": "…", "content_type": "text/markdown",
 "channel": {"id": "chn_…", "kind": "ticket", "label": "ACME-12"},
 "from":    {"id": "usr_…", "kind": "user",   "label": "alice"}}
```

`author` stays and holds the from label. **`send`** answers `200 {"id", "channel": {…}}` instead of `204`; old clients accept any 2xx. **`list_targets`** keeps `{target, kind, name}` and adds `ticket:` entries.

**Grandfathered inboxes.** A session that was live at the cutover may have replies in the legacy log that it has not committed. Until it ends, its `read` also returns legacy rows addressed to `actor:<its id>` with `CommitSeq < seq < cutover seq`, mapped to the same entry shape:
- `channel` is its default channel;
- `from` is resolved through the account and alias tables.

The bound is fixed, and the union disappears once no grandfathered session is live.

## 6. Relays: binding-driven

- **Egress.** For each squawk after the engine's mark, the engine looks up `Bindings(channel)` for its own connection kind. It skips the binding the squawk came in through: same `origin_connection_id` and, for `both` bindings, the same ref. It then renders the squawk:
  - **Linear:** a comment on `ref`. An `egress`-mode chat binding prefixes `@<handle>` of the chat's user members on that tracker connection, which keeps today's "`user:alice` from a ticket session is an `@`-mention on its ticket".
  - **Discord:** a post to `ref` (a user's inbox channel, or a room's channel), prefixed `<from label>: `.
  - **Delivery is recorded per `(seq, binding)`.** `EgressMark.Pending` is keyed by binding instead of target string.
  - **Every author is rendered**, users included. Today only agent squawks leave the log. In the new model, a `/me` post into a ticket channel also appears on the Linear issue, as `alice: …` from the bot. (§9, question 2.)
- **Ingress.** For each event the relay finds the channel and author, then posts with `Trusted`:
  - **Linear:** `(connection, issue key)` → the ticket channel holding that `both` binding; no channel means the event is dropped, as today.
  - **Discord:** the reply's receipt → the answered squawk's channel; a non-reply post in a room's own channel → that room. A non-reply post in a user inbox is still dropped, because inboxes are shared across chats.
  - **Author:** the author maps to an account (slice 1a-3d-2, unchanged). `from` is the linked user if they are a project member, else the account.
  - The squawk's `ReplyTo` and its ingress id (`in:<svc>:<foreign id>`) are unchanged.
- **Receipts.** New receipts store `discord msg id → squawk id`, and the reply routes to that squawk's channel. A legacy receipt (actor, legacy squawk id) routes to that actor's default channel (§5.1) while the session is live; otherwise it is dropped with a log line.
- **No more instance matching in relays.** `routeLinear`'s `Instance.Unit` match, `resolveHuman`'s live-instance lookup and the "non-roster `channel:` name is my ticket" fallback are all replaced by bindings.

## 7. Wake-on, nags, supervisor

- **Wake-on** reads `InboxSince(session, WaitSeq)`. Any delivery wakes, because a delivery never goes to its own author. That replaces "external-origin": a user's `/me` post and another session's post into a shared ticket channel both wake. The keep/release check compares `from` with the session's `OwnerID` (a user id) instead of `human:<Owner>`.
- **Nags and notices** post `Trusted` from the session into its default channel (§5.1), which for a personal session is the chat with its owner. A chat keeps accepting Jam's notices after the session ends, so `NotifyEnded` still reaches the owner. The `nag:<session>:` id prefix and `IsNagReply` are unchanged.
- **Supervisor:** the `CommitSeq`/`WaitSeq` baselines at setup are unchanged (global seq). At setup, a ticket session joins its ticket channel, with `joined_seq` = tail. At teardown it leaves.

## 8. People's surfaces

- **`/me`**:
  - **Rail:** the viewer's channels — the ones they are a member of, plus the ones they have deliveries in. Grouped by attention as today: a channel is *waiting* when one of its session members is waiting or idled.
  - **Unread:** comes from deliveries above `channel_reads`.
  - **Ids:** channel ids are real `chn_` ids, replacing the synthetic `studio:`, `named:` and `dm:` ids.
  - **New Message:** offers people and sessions, giving chat {self, …}, plus live sessions' tickets and the project's rooms.
  - **`/me/send`:** takes `{channel}` for an existing conversation or `{to}` for a new one, and posts through `Post` as the viewer's **user id**. This removes the per-project `human:<name>` guessing.
  - **History tab:** a read-only "History" section serves the legacy log with today's projection code, moved into a `legacy` file. Its old unread cursors are not migrated: all history reads as read.
- **Admin `/ui/intercom`:**
  - **Rows:** from, channel (kind and label) and body, filtered by project (`channels.project_id`), channel, participant id and text.
  - **Legacy tab:** the same view over `legacy_*`.
  - **Pages:** the studio page lists the session's channels, and search covers both logs.
  - **Participant suggestions:** emit ids with labels.
- **Session context** (`studiofacts`): lists the default channel and the targets in the new grammar.

## 9. Decided in review (2026-10-06): each as recommended

1. **`user:alice` in a project with no chat service.** Do we keep rendering it as an `@`-mention on the session's ticket, via an `egress` binding on the chat (rec.)? Or should we refuse it, because there is no surface to reach alice on?
   - With the rec., alice's reply on Linear lands in the **ticket** channel, not the chat. The session still gets it, because it is a member of the ticket channel.
   - In `/me`, alice sees her question in the chat and her answer in the ticket. That split goes away once she uses `/me` or Discord.
2. **Mirror users' `/me` posts to Linear and Discord.** Rec.: yes; one conversation, every surface. Today they never leave Jam.
3. **Rooms' user membership.** Rec.: join by posting or with a `/me` "Join" button; no operator-managed lists in this slice. Visibility is project-wide, like a public Slack channel.
4. **Grandfathered inbox union (§5).** Rec.: keep it. The alternative is to tell operators to let sessions drain before upgrading.

## 10. Config and admin plane

- `at-jam room add <project> <name> [--connection c --ref r] | list | rename | rm` and `/admin/projects/{p}/rooms` replace `project roster add-channel|list|rm-channel`. There is no alias, matching slice 1 §8.
- **Config snapshot v3** carries rooms and their bindings. Chats and tickets are runtime state and are not exported, like the log. A v1 or v2 snapshot's `Roster.Channels` is imported as rooms.
- The squawk log, deliveries, channel reads and memberships stay out of the export, as the log does today.

## 11. Cutover and migration

The cutover is a single serve upgrade. Under the advisory locks:
1. jam `0010` creates the channel tables. A Go data step:
   - turns roster channels into rooms (a `roster_schema` step 5);
   - creates a ticket channel for each live instance with a `Unit`, and joins its session.
2. intercompg `0004` renames the legacy tables and creates the new log.

Seq and wire cursors stay monotonic. Nothing in the legacy tables is rewritten (parent decision 4). The relays' egress marks carry over, because seq is global.

## 12. Plans (tentative; the plan doc decides)

- **2a — channel registry.**
  - Contents: tables, the source interface, the `chat`/`ticket`/`room` sources (behind `Post`, unused by the wire yet), rooms from `Roster.Channels`, `at-jam room`, snapshot v3.
  - The old log keeps running, so 2a ships on its own.
- **2b — log cutover.**
  - Contents: intercompg `0004`, `intercom.Store` v2, `Post`, `/squawks` wire, wake-on, nagger, supervisor joins, binding-driven relays, and `/me` and the admin view on the new log with the legacy tab.
  - It has to land as one release. It is built as stacked PRs into an `intercom-2b` integration branch, each green on its own, and merged to main together.

## 13. Testing

- `intercomtest` conformance (mem + pg): append with deliveries; inbox and channel paging both ways; seq continuity across the cutover; `SeqOf` and `SeenIDs` across both logs.
- Source tables: one table-driven test per source, covering resolve, `CanPost`, `CanSee`, audience and bindings, including ended sessions and archived channels.
- `Post`: fail-closed ordering (403 before 404), the addressing ceiling and `Trusted`.
- Relays: egress per binding with echo suppression; ingress by binding and receipt; legacy receipts. Wake-on: any delivery wakes; keep/release by owner id.
- Migration fixtures: roster channels → rooms; live ticket instances → channels and members; the grandfathered union. A pg integration test runs `0004` against a populated legacy log.
- All tests are hermetic (`MemStore`, the in-memory `Log`, `runner.Fake`).

## 14. Docs updated with the change

- `intercom.md`: the wire, inbox, wake-on and relays.
- `comms-addressing.md`: the target grammar, rooms and `ticket:` globs.
- `intercom-ui.md`: the rail, unread, history.
- `discord.md`: bindings and receipts.
- `ui.md` and `ui-pages.md`: the admin intercom view.
- `personal-sessions.md` and `standing-sessions.md`: the default channel.
- `backup.md`: snapshot v3.
- `serve.md`.
- `OVERVIEW.md` and `INDEX.md` rows.
