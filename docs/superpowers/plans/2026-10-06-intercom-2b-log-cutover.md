# intercom 2b: the log cutover — plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Test-first throughout. Run the Postgres suites locally before pushing (the store tests are what CI can't show us).

**Goal:** the intercom runs on channels. Every squawk is in one channel, from one participant, with its audience recorded at append. The legacy log is frozen as read-only history. The agent wire, wake-on, nags, both relays, `/me` and the admin view all read and write the new log.

**Spec:** [`../specs/2026-10-06-intercom-slice2-channels-design.md`](../specs/2026-10-06-intercom-slice2-channels-design.md) §3–§8 and §11. Builds on 2a: the channel registry (#368), sources and `Intercom.Plan` (#369), and rooms (#370).

## Delivery

One draft PR to main, built as one commit per task below. CI runs the Postgres suites on every push, and nothing merges until the last task is green.

An integration branch was considered and rejected: PRs into a non-main branch get no CI here.

## Decisions

1. **No foreign keys from the log to the registry.**
   - Neither `squawks.channel_id` / `from_id` nor `squawk_deliveries.participant_id` references jam tables.
   - The two migration runners are independent (intercompg's integration tests run without jam's schema), and `Post` validates ids before it appends.
   - A row whose channel is gone renders as "(removed channel)".
2. **Project removal (decide in review; recommended (a)).**
   - (a) Removing a project still removes its channels; their squawks stay as history with no channel label. This is today's behaviour, made safe by decision 1.
   - (b) Pull 1b-3's project tombstones in here, so ids keep resolving. It touches the projects map's keying (by name) and every project lookup.
3. **The grandfathered union is permanent and cheap.**
   - An inbox read (`read`, wake-on) also returns legacy rows addressed to `actor:<session id>` with seq above the reader's cursor and below the cutover seq.
   - New `ses_` ids never appear in the legacy log, so no bookkeeping is needed.
   - The cutover seq is stored by intercompg `0004` (`intercom_settings.cutover_seq`).
4. **Origin suppression needs the ref.** A squawk records `origin_connection_id` and `origin_ref`. Egress skips exactly the surface it came from, so in a group chat a reply from alice's Discord inbox still reaches bob's.
5. **Ticket channels for sessions that predate 2a-2** are created at serve startup: the intercom's `Reconcile` calls `SetUp` for every live instance with a `Unit`. That way a Linear reply finds its channel before the session ever sends.
6. **Cleanup is a separate PR (2c), after the cutover:** deleting the `Roster.Channels` view, `RosterChannel`, `AddChannel`/`RemoveChannel`, the `Human` view (1a-3e), `DecideSend`/`ListTargets` and the legacy projection's dead paths. 2b keeps them where the History tab or tests still need them.

## Tasks

### 1. Log v2: `internal/intercom` and `intercompg`

- **`intercom`:**
  - `Squawk{Seq, ID, Channel, From, Body, At, ReplyTo, ContentType, Origin, OriginRef}`.
  - `Store`: `Append(m, audience)`, `InboxSince`/`InboxBefore(p, seq, limit)`, `ChannelSince`/`ChannelBefore(ch, seq, limit)`, `ListSince`, `ReadThread`, `SeqOf`, `TailSeq`, `SeenIDs`, `InboxChannels(p) []ChannelStat{Channel, LastSeq}`.
  - `LegacyStore`: today's read methods, plus `List(Filter)`, over the legacy tables.
  - Rewrite the in-memory `Log`.
  - `Notifier` wraps the v2 store unchanged.
- **intercompg `0004_channel_log.sql`:**
  - Rename to `legacy_squawks` / `legacy_squawk_recipients`.
  - New `squawks(seq bigint PK DEFAULT nextval('squawk_log_seq'), id text UNIQUE, channel_id, from_id, body, at, reply_to, content_type, origin_connection_id, origin_ref)` with indexes `(channel_id, seq)` and `(reply_to)`.
  - New `squawk_deliveries(participant_id, seq, PK(participant_id, seq))`.
  - `setval` from the legacy tail; record `cutover_seq`.
  - `SeqOf` and `SeenIDs` consult both tables.
- **Tests:** rewrite the `intercomtest` conformance (mem + pg), covering:
  - append with audience;
  - inbox and channel paging both ways;
  - seq continuity across a populated legacy log;
  - `SeqOf`/`SeenIDs` across both logs;
  - the legacy reader over renamed tables.

### 2. Posting: `Intercom.Post` and friends (`internal/jam`)

- **`Post(planned, from, body, contentType, replyTo, opts)`:** appends with `planned.Audience`.
- **Membership on post:** a user posting in a ticket or room joins it; an account posting through ingress joins a ticket.
- **`PostTrusted(ch, from, …)`** for relay ingress and Jam's notices. It skips `CanPost` but still takes the audience.
- **`PlanUserChat(user, project, members)`:** a person starting a conversation from `/me`, with a session (chat) or people (chat).
- **`Reconcile()`** at startup (decision 5).
- **`label(id)`** for wire and UI rendering: registry entries, sessions (`sessionLabel`), and "(removed channel)".

### 3. The agent wire: `/squawks`, `cove-master mcp`

- **`send`:** `Plan` → `Post`, answering `200 {"id","channel":{id,kind,label}}`.
  - Statuses are unchanged: 403 when denied, 404 when unresolved, 400 when there is no default, 502 when the append fails.
- **`read`:** `InboxSince`/`InboxBefore` plus the legacy union (decision 3). Entries keep `author` (now the from label) and gain `channel` and `from` objects.
- **`commit`:** `SeqOf` over both logs.
- **`targets`:** members as `user:<name>`, rooms as `channel:<name>`, and the session's own ticket as `ticket:<key>`. `studiofacts` uses the same list.
- **`cove-master`:** `squawkOut` gains `channel` and `from`; `send` prints the id and channel; tool descriptions are updated.
  - Old images keep working: every change is additive, and a 2xx with a body is still success to them.
- **Tests:**
  - handler: each address kind, every status, and the entry shape;
  - an old-client decode of the new JSON;
  - the union read for a grandfathered session.

### 4. Wake-on, nags, supervisor

- **wake-on:** reads `InboxSince(session, WaitSeq)` plus the union. Any delivery wakes the session. Keep/release requires `from == inst.OwnerID` and `IsNagReply`.
- **Nagger:** posts trusted from the session into its default channel (`Intercom.DefaultChannel(inst)`), which works after teardown from the instance snapshot. Nag ids are unchanged.
- **Supervisor:** unchanged apart from wiring.
- **Tests:**
  - wake on a `/me` post and on a co-member session's post;
  - no wake on the session's own post;
  - keep/release from the owner, and not from anyone else;
  - `NotifyEnded` after teardown.

### 5. Relays: binding-driven

- **`relay.Directory` becomes:**
  - `Surfaces(service, m) []Delivery`: the channel's bindings on this service's connection, minus the origin surface.
    - A chat yields each user member's delivery address on the project's chat connection, except the author's.
    - A Linear-only chat yields the egress-only `@mention` on its ticket session member's issue.
  - `Route(service, e) (channel, from, replyTo, ok)`.
- **`EgressMark.Pending`** is keyed by surface.
- **Bodies:**
  - Discord prefixes `<from label>: ` as today.
  - Linear prefixes `<from label>: ` for anyone but a session (mirroring, §9 q2).
- **Ingress:**
  - Linear: `(connection, issue)` → `ChannelByBinding`.
  - Discord: the receipt's squawk id → that squawk's channel. A legacy receipt routes to its actor's default channel while live. A non-reply post in a room's bound channel goes to that room.
  - The author is resolved through accounts, as today.
- **Receipts v2** store `msg id → squawk id`. Old entries are read as legacy.
- **Tests:**
  - egress per kind, with origin suppression and the group-chat case;
  - ingress per kind, including a legacy receipt;
  - the seeded egress mark across the cutover.

### 6. `/me` on channels

- **jam `0011_channel_reads.sql`:** `channel_reads(participant_id, channel_id, seq)`, with Store `ChannelRead`/`CommitChannelRead`. The old `intercom_unread_cursors` table is left unused (dropped in 2c).
- **Rail:** `ChannelsOf(user)` ∪ `InboxChannels(user)`, filtered by `CanSee`. Unread = deliveries above the read cursor. Attention: waiting when a session member is waiting or idled.
- **Conversation:** `ChannelBefore`, behind `CanSee`. `Mine` = from == viewer.
- **Picker:** people and live sessions (→ chat), live sessions' tickets, the project's rooms.
- **`/me/send`:**
  - `{channel}` → `PlanChannel`;
  - `{to}` → `PlanUserChat`, or a room or ticket by id;
  - then `Post` as the user's id.
- **History tab:** the legacy projection (`channels.go`, moved to `legacy_channels.go`) over `LegacyStore`, read-only and all read.
- **Tests:**
  - the rail and unread across kinds;
  - visibility revoked live;
  - a send waking a session;
  - History renders legacy rows.

### 7. Admin view

- **`/ui/intercom`:**
  - rows show from, channel and body;
  - filters: project (channel's project), channel, participant id, text;
  - a "Legacy" tab over `LegacyStore`.
- **Studio page:** the session's channels and recent squawks.
- **Search:** both logs.
- **Participant suggestions:** emit ids with labels.

### 8. Docs, all in the same PR

- `intercom.md`: the wire, inbox, wake-on, relays, the union and the cutover.
- `comms-addressing.md`: the grammar (`chat:`, `ticket:`), delivery semantics per kind, and the mirroring.
- `intercom-ui.md`: rail, unread, History.
- `discord.md`: bindings, receipts v2.
- `ui.md`, `ui-pages.md`.
- `personal-sessions.md`, `standing-sessions.md`: default channel, nags.
- `serve.md`: the upgrade note (no rollback, the cutover).
- `OVERVIEW.md`.
- The spec's status line.

## Review focus

1. **A Linear reply to a ticket whose session started before the upgrade.** Its channel must exist (Reconcile) or the reply is dropped.
2. **A personal session's nag after the owner left the project.** `DefaultChannel` refuses it (2a-2), so the nag fails. Log it at debug; don't crash the idle ladder.
3. **The first egress tick after the upgrade.** The mark's `LastSeq` is a legacy seq. Every new squawk is above it, and legacy rows must never be re-delivered.
4. **An agent on an old `cove-master` reading the new JSON** (extra fields), and sending to `user:x` in a Linear-only project. Both must behave exactly as before.
5. **Two Jams running across the cutover** (rolling restart). intercompg `0004` runs under the advisory lock. An old binary still appending to the renamed table would fail loudly; document stopping old serves first.
