# intercom slice 3: session channels and call-in — design

**Status:** approved (2026-10-07); §9 questions decided as recommended. Built: 3a (#376), 3b (#377), 3c (#378); see each plan for review decisions.
**Parent:** [`2026-10-06-intercom-identity-and-channels-design.md`](2026-10-06-intercom-identity-and-channels-design.md), slice 3 of 4 (decisions 12 and 13). Slice 2 ([`…-slice2-channels-design.md`](2026-10-06-intercom-slice2-channels-design.md)) put every squawk in one channel and gave us `chat`, `ticket` and `room` sources; this slice adds the fourth.
**Delivers:**
- a `session` channel source, and every session's **home channel**;
- `session:` addresses for agents and people;
- **call-in**, self-join and leave;
- the end of "owner" and "default recipient" as comms concepts;
- agent↔agent conversations that wake each other, with a loop breaker.

**Deferred:** escalation as call-in (slice 4); room membership for sessions; project-id references (1b-2/1b-3).

## 1. What changes, in one paragraph

Today a session's `send` with no `to` goes to a channel picked by its kind: a ticket session's ticket, a personal session's chat with its owner, and a `400` for a standing session. People reach a session through that one channel. Sessions can't talk to each other, and their posts never wake another session.

After this slice, every session has a **home channel**, the channel it is always in. For a ticket session that is its ticket channel. For every other session it is a new **session channel** made for it. Anyone may be **called in** to a home channel by a member who may address them. People and sessions who are allowed may also **join** on their own, and anyone but the session itself may **leave**. `session:<label>` names a session's home channel, so agents and people can address any session they are allowed to. "Owner" stays only as *started by*, which is still used for allocation caps and release.

## 2. The session source

| | `session` |
|---|---|
| Key | the session id (`ses_…`, or a grandfathered id) |
| Label | the session's label (`sessionLabel`: its declared name, else its id) |
| Created | when the session is created (supervisor `SetUp`). At upgrade, `Reconcile` creates one for each live non-ticket session (§8). |
| Members | the session (joined at creation; it never leaves), plus participants who were called in or joined |
| `CanSee` | members. For a standing or manual session, every member of the project too; a personal session's channel is members-only (§9 Q2). |
| `CanPost` | members, plus anyone who may join (§3): posting joins, as in a room |
| Audience | current members except the author; ended sessions skipped (as in a chat) |
| Bindings | as a chat's: each user member's delivery on the project's chat connection, egress-only, with replies routed back by receipt. No tracker binding. |
| Archived | when the session ends, after Jam's end notices are posted (§6) |

Ticket sessions get **no** session channel. Their ticket channel already is the channel they are always in while they are live (§9 Q1).

**Home channel.** `HomeChannel(inst)` returns the ticket channel for a session with a `Unit`, and the session channel otherwise. It replaces `DefaultChannel`:
- `send` with no `to` posts here;
- the nagger and Jam's notices post here;
- the grandfathered legacy inbox maps here.

## 3. Membership: call-in, join, leave

All three are plain membership changes (`channel_members` rows with `joined_seq`/`left_seq`). There are no new tables.

**Call-in** (`inviter` calls `invitee` into channel `ch`):
- The inviter must be a **current member** of `ch`.
- The inviter must be allowed to **address the invitee**:
  - a session uses its role's addressing ceiling, matched against `user:<name>` or `session:<label>`;
  - a user may call in any live member of the project, or any live session in it.
- The invitee must be able to **see** the project: a live project member, or a live session of the project.
- `ch` must accept members. Session, ticket and room channels do; a **chat refuses** (its member set is fixed, decision 11).
- On success, the invitee joins at the tail. The inviter posts a notice: "*inviter* called in *invitee*". The invitee is in that notice's audience, so it reaches their surfaces and wakes a session.
- Calling in a current member is a no-op (`200`, no notice).

**Join** (a participant joins `ch` on its own): allowed when `CanSee` holds and, for a session, its addressing ceiling allows the channel's address (`session:<label>`, `channel:<name>`, `ticket:<key>`). A user joins a standing session's channel (or a room, or a ticket) by posting or with `/me` **Join**. A personal session's channel is invite-only for users (§9 Q2).

**Leave:** any member but the channel's own session may leave. A user leaves with `/me` **Leave**. A session leaves with the new `leave` tool. A ticket channel's sessions still join at setup and leave at end, as in slice 2. A left member gets no further deliveries but keeps what was delivered.

**Fail-closed order** is as in slice 2: authorization is checked before existence, so 403 comes before 404, and an unknown session is indistinguishable from a forbidden one.

## 4. Addresses and the agent wire

| `to` | Channel |
|---|---|
| *(empty)* | the sender's home channel (§2) |
| `session:<label\|ses_id>` | that session's home channel; the sender joins by posting |
| `user:…`, `chat:…`, `channel:…`, `ticket:…` | unchanged from slice 2 |

- **Addressing ceiling:** `session:<glob>` is a new glob kind, matched against the target's label. A session may always address itself, so the empty `to` never consults addressing, as before. A glob never crosses kinds.
- **New tools** in `cove-master mcp`, backed by new `/squawks` routes:
  - `call_in(who, channel?)` → `POST /squawks/call-in {who, channel}`. `channel` defaults to the caller's home channel.
  - `leave(channel)` → `POST /squawks/leave {channel}`.
  - An older `cove-master` simply lacks them; everything else stays additive.
- **`list_targets`** adds `session:` entries for the live sessions the caller may address.
- **`read`** entries already carry `channel {id, kind, label}`; `kind` can now be `"session"`.
- **Standing sessions** no longer get `400` on an empty `to`.

## 5. Wake-on: sessions wake sessions, with a loop breaker

Slice 2 never woke a session for another session's post, to avoid wake loops. Collaboration needs those wakes, so:
- **Any delivery wakes**, including one from a session.
- **The loop breaker:** a channel counts its **consecutive session-authored deliveries** since the last post by a user or account. Past **N** (rec. 8), deliveries from sessions in that channel no longer wake. Jam posts one notice into the channel: "paused agent-to-agent wakes until someone replies". The next user post resets the count. Deliveries are still recorded; only the wake is withheld (§9 Q3).
- The count is derived from the log (`ChannelBefore`), not stored, so there is nothing to migrate.

## 6. Owner semantics: what's left of "owner"

`Instance.Owner`/`OwnerID` becomes **started by**:
- It is used for the per-owner allocation caps, `session list` and owner-only release, all unchanged.
- **At start**, the starter is called in to the personal session's channel, by Jam. This is the only special treatment in comms. After that the starter is a member like any other and may leave; leaving does not end the session.
- **Nags and notices** post into the home channel. All members see them; a channel with no user members gets nags nobody reads, as a standing session would.
- **Keep/release replies** act only when the reply is from the **starter** (attributed as today, `DiscordAuthorOf`), because they change the starter's allocation (§9 Q4). A "keep" from anyone else is an ordinary reply.
- **End:** `NotifyEnded` and `NotifyReclaimed` post into the session channel, and then it is archived, in that order. The members, the starter included, still see why the session ended.
- `personalDeliveryProblem` is unchanged: a personal session in a Discord-chat project still needs its starter to have an inbox, because the starter is called in and nags must reach them.

## 7. People's surfaces

- **`/me`:**
  - The rail lists session channels like any other. A session channel whose session is waiting groups under *Waiting on you*, as today.
  - **New Message → a session** posts into its home channel (joining it). Slice 2 made a chat {user, session} instead. The picker offers the sessions the viewer may join.
  - **Join** (on a visible channel the viewer isn't in) and **Leave** (except their own chats).
  - **Call in…** on session, ticket and room channels: a picker of project members and live sessions.
- **Discord:** a session channel renders to its user members' inboxes like a chat, prefixed with the author. A reply comes back by receipt. Being called in delivers the call-in notice to the invitee's inbox.
- **Admin:** the studio page lists the session's home channel and its members. `/ui/intercom` can filter by kind `session`.
- **Session context** (`studiofacts`): names the home channel, and says other sessions and people can be called in with `call_in`.

## 8. Migration and upgrade

There is no log migration and no schema change; the channel kind is new data. At serve startup, `Reconcile` (already run after tracker resolution) also does three things:
1. creates a session channel for each live session without a `Unit`, and joins the session;
2. for a personal session, calls its starter in (no notice);
3. leaves existing chats {session, owner} alone. Their history stays, the session stays a member, and an owner's reply to an old Discord message (by receipt) still reaches the session. New traffic goes to the session channel.

A rollback to slice 2 leaves `session` channels unused but harmless. A `chn` row of an unknown kind is ignored by slice 2 readers, and the plan must check that it is.

## 9. Decided in review (2026-10-07): each as recommended

1. **Ticket sessions' home channel.**
   - Rec.: their **ticket channel**, with no session channel. A ticket session's `send()` keeps commenting on its ticket, which is what tracker users read today. Calling someone into a ticket session adds them to the ticket's conversation.
   - Alternative: every session gets a session channel, ticket sessions included. Their empty `to` would then stop reaching Linear unless we bind the session channel to the ticket. That splits a ticket's conversation in two (the agent's posts in one channel, the replies in the other), which is the `/me` split slice 2 §9 Q1 accepted only for one corner case.
2. **Who may see and join a personal session's channel.**
   - Rec.: **members only, invite-only for users**. A personal session is someone's own collaborator and its channel is their private conversation until they call someone in.
   - Sessions may still join it if the operator's addressing allows `session:` (operator policy; `session:` is a new glob kind, so no existing ceiling allows it until an operator adds it).
   - Standing and manual sessions' channels are project-visible and joinable, like rooms.
3. **Agent↔agent wakes.**
   - Rec.: sessions wake sessions, with the consecutive-delivery breaker (N = 8 per channel, reset by any user or account post).
   - Alternatives: (a) never wake on a session's post, as in slice 2; then a session asking another must set an alarm to collect the answer. (b) Wake only on a direct reply (`reply_to` one's own squawk). That still loops, just more slowly.
4. **Keep/release from members other than the starter.**
   - Rec.: **starter only**, because it is their allocation that is kept or released.
   - Alternative: any user member. Simpler to explain, but anyone called in could release someone else's session.
5. **Operator call-in.**
   - Rec.: **not in this slice**. Operators can call themselves in through `/me` like anyone, and an `at-jam` verb can follow if wanted.

## 10. Plans (tentative; the plan doc decides)

- **3a — session source and home channels.**
  - The `session` source, `HomeChannel`, the `session:` address and glob, and `Reconcile` (§8).
  - Nags and notices into the home channel, and keep/release by starter.
  - `list_targets`, `studiofacts`, and the docs.
  - This changes the default for personal and standing sessions on its own, and ships without call-in.
- **3b — membership verbs.** Call-in, join and leave in `Intercom` (with the §3 rules), the `/squawks` routes and the MCP tools, and `/me` Join, Leave and Call in.
- **3c — agent↔agent wakes.** Wake-on accepts session deliveries, plus the loop breaker and its notice.

## 11. Testing

- A table-driven source test covering resolve, `CanPost`, `CanSee`, audience, bindings, archival at end, and the personal versus standing visibility split.
- Call-in, join and leave matrices:
  - inviter not a member;
  - inviter not allowed to address the invitee;
  - invitee outside the project;
  - into a chat (refused);
  - self-join refused by the ceiling;
  - a session leaving its own channel (refused);
  - 403 before 404.
- Wake-on: a session's delivery wakes; the breaker trips at N and resets on a user post; a notice is posted once.
- Upgrade: `Reconcile` creates session channels for live sessions, calls in starters, and is idempotent. Old chats keep routing receipts.
- Relays: a session channel egresses to user members' inboxes, and a reply comes back by receipt.
- All tests are hermetic (`MemStore`, the in-memory log). The pg conformance suite covers membership rows as today.

## 12. Docs updated with the change

- `intercom.md`: home channels, call-in, join and leave, and wakes.
- `comms-addressing.md`: `session:` addresses and globs.
- `personal-sessions.md`: the starter, no owner chat, and keep/release.
- `standing-sessions.md`: a channel and people can reach it.
- `intercom-ui.md`: Join, Leave, Call in and New Message → session.
- `discord.md`: session channels on inboxes.
- `escalation.md`: unchanged until slice 4.
- `OVERVIEW.md`/`INDEX.md` rows.
