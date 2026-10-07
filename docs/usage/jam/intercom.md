---
summary: The intercom MCP — Jam-brokered read/commit/send/list_targets/escalate tools a managed studio's agent uses to converse in channels (its ticket's, a chat, a room) on the channel log. Tokens stay in Jam; the endpoint is broker-authorized; the tools reach claude via a `cove-master mcp` stdio server.
read_when: You want a raised studio's agent to be able to read and post comments on the ticket it's working (ask a question, leave a status), or you're wiring/operating Jam `/squawks` endpoint and its cove-side MCP delivery, tuning wake-on (`runtime.wake`), or running the intercom on a Jam with no Requisitioner.
owns: the operator-facing intercom-MCP story — the `/squawks` broker endpoint and its wire shapes, the channel log and its cutover from the legacy log, channel membership (call-in, join, leave), the inbox as a queue, the relays, wake-on, the `cove-master mcp` stdio delivery, and how it's enabled. Does NOT own the target space or access-graph rules — see comms-addressing.md. Does NOT own escalation-category semantics for the `escalate` tool — see escalation.md.
prereqs: coves.md for the managed studio a squawk is scoped to; personal-sessions.md for a ticketless studio that talks to the person who started it; requisitioner.md for the tracker/Linear client this reuses; roster.md for the identity a squawk is attributed to; comms-addressing.md for addressing a target other than the studio's own ticket
tier: leaf
updated: 2026-10-07
---

# The intercom MCP

A managed studio's agent gets **Jam-brokered** tools — `read`, `send`,
`list_targets`, and `escalate` — over Jam's **channel log**: every squawk is in
one channel (a ticket's conversation, a session's own channel, a chat with people, a room) and reaches
that channel's members. Jam holds the tracker token, does the platform I/O, and attributes the sender; the studio never holds a channel token, exactly like the Anthropic and git connectors. This is the imperative foundation of the comms hub (slice A of A→B→C).

## What the tools do

- **`send(text, to?, content_type?)`** — posts the squawk to a channel of the channel log and returns its id and channel (`200 {"id", "channel": {id, kind, label}}`; a studio on an older image just sees success); the relays render it onto Linear/Discord shortly after (see [Enabling it](#enabling-it)). With no `to`, it goes to the studio's **home channel**, the channel it is always in: its ticket's conversation when it has one, else its own **session channel** (for a [personal session](personal-sessions.md) the person who started it is in it; people can join a [standing session](standing-sessions.md#messaging)'s). With a `to` (`user:`, `chat:`, `channel:`, `ticket:`, `session:`) it goes to that channel instead — see [comms-addressing.md](comms-addressing.md) for the target space, authorization, and delivery/reply rules (single source; not duplicated here). The author is Jam's brokered identity (the agent can't spoof it). The body is markdown unless `content_type` opts out; see [Content type](#content-type-markdown-or-plain-text).
- **`read(anchor?, id?, dir?, limit?)`** — reads the studio's inbox **as a queue**: the squawks delivered to it (others' posts in its channels), by default the next unprocessed ones after its durable commit cursor, oldest-first. Seek with `anchor` (`cursor` default / `start` / `end` / `id`) × `dir` (`forward` default / `backward`) × `limit` (default 50); the response also carries `committed_cursor` / `page_first` / `page_last`. Each entry has `channel` (`{id, kind, label}`; absent on a squawk from before the cutover) and `from` (`{id, kind: session|user|account, label}`), and keeps `author` (the sender's label) for older clients. **Reading never advances the cursor.** Self-scoped: `read` takes no target. See [The inbox as a durable queue](#the-inbox-as-a-durable-queue) below.
- **`commit(up_to)`** — confirms the studio has processed its inbox up to a squawk id, advancing its durable commit cursor (monotonic, forward-only) so those squawks aren't handed to it again. Separate from `read` — reads don't commit. Self-scoped (the cursor is the caller's own; identity comes from the token, never the body).
- **`list_targets()`** — lists the people, rooms and sessions this studio is currently authorized to `send(to=…)`; see [comms-addressing.md](comms-addressing.md#discovering-targets-get-squawkstargets-list_targets).
- **`call_in(who, channel?)`** and **`leave(channel)`** — membership (`POST /squawks/call-in` → `200 {channel, member}`, `POST /squawks/leave` → `204`); see [Channel membership](#channel-membership).
- **`report(state, summary, pr?)`**, **`end(reason)`**, **`idle_timeout(duration, scope)`** and **`alarm_set` / `alarm_clear` / `alarm_list`** — report a ticket's state, end the session at turn end, tune how long it may sit idle after one, or set named alarms that wake it; see [turn-end.md](turn-end.md).
- **`escalate(category)`** — declares the studio's current block category, routing the (auto-on-Waiting) escalation ping to that category's tier chain; see [escalation.md](escalation.md#categories-routing-by-block-kind) for the semantics — it's a separate brokered endpoint (`/escalate`), documented there rather than duplicated here.

The agent blends these with its work inside a turn — e.g. leave a status, read the next unprocessed replies, handle them, `commit` up to the last one it handled. See [Waiting for a reply](#waiting-for-a-reply-wake-on) below for suspending until a reply arrives.

## Content type: markdown or plain text

Every squawk carries a `content_type` (a MIME type) saying how its body is meant
to be read:

- **`text/markdown`**: the **default**. A send that omits `content_type`, a
  Linear comment, and a Discord message are all markdown. Squawks logged before
  the field existed read back as markdown too.
- **`text/plain`**: the **opt-out**, for text that would render badly as
  markdown (logs, ASCII art, stray `*` and `_`). Every surface shows it
  literally, never interpreted as markdown.

Each side can opt out:
- an agent passes `content_type: "text/plain"` to `send` (`POST /squawks` takes
  the same optional field);
- a human ticks **Plain text** in the [`/me` composer](intercom-ui.md#sending-unread-and-refresh)
  (`POST /me/send` takes the same field).

Anything else is a `400`, and nothing is appended. `read` (`GET /squawks`)
returns each squawk's `content_type`, so an agent knows when a reply is meant
literally.

**Rendering:**
- The [`/me` inbox](intercom-ui.md) and the [admin intercom view](ui.md#intercom)
  render markdown as sanitized HTML: raw HTML is omitted, `javascript:`-style
  link targets are dropped, and links open in a new tab. Plain text is shown
  as-is.
- On the relays, markdown is posted byte-for-byte. Plain text is
  backslash-escaped for the surface's markdown flavor, so Linear and Discord
  display it literally. For Linear, newlines also become hard breaks and
  leading indentation is kept, so neither collapses nor turns into a code block.

## How it's brokered and scoped

- **Endpoint:** Jam serves `/squawks` on its cove-facing `:443` mux. A request carries the studio's identity token (`Authorization: Bearer`); Jam resolves the session from it — never from the body.
- **`read` is self-scoped by construction:** it carries no target, so it can only ever return the caller's own inbox. **`send` is authorized:** the home channel is always allowed; a `to` is checked against the role's addressing before anything else — see [comms-addressing.md](comms-addressing.md).
- **Tokens stay in Jam:** the Linear token lives in Jam's `SecretResolver`; the studio holds only its identity token. Nothing is logged that could leak either.

## Delivery to the agent

The studio's `claude` is pointed at a stdio MCP server named `messaging` in the per-run `--mcp-config` that cove-master's harness generates (alongside any kit [`mcp-servers`](kits.md#mcp-servers-cov-240), which can never replace it), which launches `cove-master mcp`. That subcommand exposes `read`/`send`/`list_targets` and forwards them to Jam `/squawks` (and `/squawks/targets`) over TLS through the studio's squid proxy, using the identity token + Jam address already in the studio's environment. No new binary, no new secret in the studio.

## Channel membership

Who hears a channel is its members. A member **calls someone in** (`call_in`, or **Call in…** in [`/me`](intercom-ui.md)): `who` is `user:<name|id>` or `session:<label|id>` of the channel's project, and a session inviter's addressing must allow it, as for a send (`403` before `404`); a person may call in only a session they could message themselves (never someone else's personal session). The invitee joins from now on and hears the inviter's "called in" notice, which reaches people's inboxes but is never posted onto a ticket's issue or a room's surface. A **chat** never takes new members (`409`), a **room** takes people but not sessions, and calling in a member does nothing. People also **join** a channel they can see (a ticket, a room, a standing session's channel — never a personal session's, which is invite-only) by posting or with **Join**. Anyone **leaves** (`leave`, **Leave**) except a session from its own channel or ticket, and nobody leaves a chat; what was delivered stays.

## Enabling it

`/squawks` (and `/escalate`) are always mounted: the intercom is always on, backed by the Postgres channel log (`store-postgres` is required — [serve.md](serve.md#the-serve-config)), with or without a [Requisitioner](requisitioner.md), so a [personal session](personal-sessions.md) can converse on a Jam with none. The Linear relay needs the Requisitioner's tracker; the Discord relay and wake-on do not.

**The channel log.** Each squawk is in one channel, from one participant (a session, a user, or an *account* — an unlinked sender on Linear or Discord), and its audience — the channel's members but the author, at that moment — is recorded with it. A **ticket channel** is created when a session is set up on a ticket (or at serve startup for one already running), bound to the issue; the session joins it, and a re-dispatched ticket's new session joins the same channel (with an empty inbox). A session with no ticket gets its own **session channel** when it is set up (or at serve startup for one already running): the session is in it for good, a personal session's starter is called in once (and may leave), and it is archived when the session ends — after Jam's notice about the end, which its members still get. A **chat** is a fixed set of people and sessions; a **room** is a project's named channel ([comms-addressing.md](comms-addressing.md#project-members-and-rooms)).

**The cutover.** Upgrading to the channel log freezes the earlier log as **legacy history**, read-only: the admin view's Legacy tab and `/me`'s History show it, and seqs carry on from its tail. A session already running at the upgrade still reads (and commits past) the legacy replies to it it hadn't processed, ahead of new ones. Squawks the relays hadn't yet delivered at the upgrade are not delivered. Stop every older Jam before starting this one, and don't roll back past it.

**Outbound is log→egress (asynchronous, at-least-once).** A `send` appends to the log and returns; a resident egress loop per relay then renders it onto its channel's surfaces (≈ the egress poll interval later). An append failure returns `502`. Every author's squawks are rendered — a person's `/me` post too — but never back onto the surface it came from.

**Relays.** When a Requisitioner is configured, the **Linear relay** posts a ticket's conversation as comments on its issue (a person's prefixed `<name>: `, a session's as written), renders a room bound to an issue the same way, and @-mentions a chat's people who have no Discord inbox on the ticket of a session in the chat. Inbound, it polls the team's comments feed and posts each comment on an issue a channel is bound to into that channel (idempotently); a comment on any other issue is dropped. The **Discord relay** (when `runtime.discord` names its connection, with or without a Requisitioner) renders chats and session channels onto their people's inbox channels and rooms onto their bound channels, and routes replies back into the conversation of the post they answer — see [discord.md](discord.md). Both relays share the cursors/markers files in [`state-dir`](serve.md#the-serve-config), keyed by Service; a first-enabled relay's mark is seeded to the log's tail, so it never redelivers the backlog. Wake-on reads what they post.

> **Before relying on inbound (reply) delivery, confirm the Linear `comments` feed schema against your live Linear workspace** — specifically the `$since` scalar (`DateTimeOrDuration` vs `DateTime`) and the `issue → team → key` filter path. If it differs, the ingress `Poll` errors and its cursor holds (no data loss, inbound stalls) while **egress is unaffected**.
>
> An inbound author is recorded as an **account** on its connection by their
> service user id only (labelled with their display name — never matched by it).
> Once an operator links that account to a user (`at-jam account list
> --connection linear`, then `account link`), a post in a project the user is a
> member of is theirs; until then it's the account's.

## The inbox as a durable queue

A studio's inbox is a **durable, acked queue** over the log — the squawks
delivered to it, a conversation to process in order, not an email list.

> **Ordering is by a monotonic append sequence, not by squawk id.** Every squawk
> carries an internal append `seq` (continuing across the cutover);
> "oldest-first", "forward", "tail", and the commit/wake cursors are all defined
> by it. Squawk **ids are identifiers, not ordering keys** — ingress ids
> (`in:linear:<uuid>`, `in:discord:<snowflake>`) are deterministic for idempotent
> dedup and don't sort against generated ids. The wire stays in ids
> (`committed_cursor`/`page_first`/`page_last`/`up_to`); Jam resolves id↔seq.

- **Commit cursor.** Each studio has a durable commit cursor (its last
  *processed* squawk id), **initialized at raise to the log's tail**, so a
  freshly-raised studio consumes what reaches it from then on. It is **separate
  from the wake-on `WaitSeq`** ([below](#waiting-for-a-reply-wake-on)).
- **Seekable reads that never commit.** `anchor` × `dir` × `limit` page anywhere
  — re-read processed history, jump to the start/end, walk from an id.
- **Explicit commit.** `commit(up_to)` advances the cursor (monotonic,
  forward-only, idempotent); a studio that restarts before committing
  re-consumes (at-least-once).
- **Nothing is pruned** — backward/`start` reads always work.

## Waiting for a reply (wake-on)

A raised studio is not one-shot. When its agent's turn ends (typically after asking
a question via `send`), the studio **suspends** — it reports Activity `waiting` and
blocks instead of ending ([turn-end.md](turn-end.md)). Jam's resident **wake-on engine**
watches the studio's inbox and, when a reply arrives, **wakes** it
over the Attach stream; the studio runs its next turn — written into the live agent if one is running, else a new `claude --continue` episode — `read`s the
reply, and resumes. When its [session context](session-context.md#refresh) changed
meanwhile, the wake text says so. A **`wait-max`** bounds the wait — a studio with no reply within it is
torn down (no zombies), paused or not — unless the role's
[idle timeout](turn-end.md#idle-timeout) is armed, which then decides instead. **Resident sessions are exempt from `wait-max`:**
a [personal](personal-sessions.md) or [standing](standing-sessions.md) session waits
after every turn and is never torn down for `wait-max` (it is still paused at
`warm-timeout` and woken on a reply). A personal session instead climbs the
[idle ladder](personal-sessions.md#the-idle-ladder) — nags into its channel, and an
optional reclaim. Its starter's `keep`/`release` reply to a nag is acted on by Jam
and doesn't wake the agent ([personal-sessions.md](personal-sessions.md#the-idle-ladder)).
A standing session has no owner, so it gets no nags.

A reply that lands while the studio is still **`running`** or
**[`holding`](turn-end.md#holding)** also wakes it, at once. The Wake is written
straight into the live agent between turns (mid-turn, it is coalesced into one
resume prompt at turn end), and it says why the agent was woken
([wake reasons](turn-end.md#wake-reasons)). A running or holding studio is never
paused or torn down for this.

While waiting, a studio doesn't stay live-and-idle indefinitely: once it's been waiting
past a **`warm-timeout`** with no reply, the engine **pauses** it (`docker pause`, ≈0
CPU) and moves it to the `idled` [phase](coves.md#the-model) — a paused, intentionally
idle studio whose lease-reaping is suspended (see [coves.md](coves.md) for the phase
chain). When a reply then lands, the engine **unpauses** it (`Resume`) and sends `Wake`
on a subsequent tick once it's reconnected and reporting Live + `waiting` again.

The engine always runs and is configured under `runtime.wake`:

```yaml
runtime:
  wake:
    poll-interval: 15s   # how often Jam checks waiting studios for a reply (default 15s)
    wait-max: 24h        # max a (non-personal) cove may wait before teardown, paused or not (default 30m)
    warm-timeout: 5m     # how long a waiting cove stays live before it's paused (default 60s)
```

Each field resolves independently: **`runtime.wake` > the matching `runtime.requisitioner`
field** (`wake-poll-interval` / `wait-max` / `warm-timeout`, kept as a fallback for
existing configs) **> the default**. An invalid `runtime.wake` duration fails `serve`
at startup; an invalid Requisitioner value still falls back to the default.

The wake trigger is **a squawk delivered to the studio** — from a person, an
account, or another session — after a `WaitSeq`
baseline: when a run starts (at raise, and
whenever the studio enters `running`) the supervisor stamps `WaitSeq` to the Log's
current tail sequence — the starting agent reads its inbox itself — and any later
delivery (append `seq` > `WaitSeq`) counts as a reply. Waking a `running` studio advances `WaitSeq` past the replies it was woken for;
entering `waiting` leaves `WaitSeq` alone, so a reply that landed during the run and was
not yet woken for wakes the studio as soon as it waits. It is an append-sequence compare,
not a wall-clock one (`WaitingSince` is unchanged, but now drives only
`wait-max` teardown and `warm-timeout` pausing below, not reply-detection). It's fed
by the relay ingress engine above, so a Waiting studio wakes on a reply only once a relay
has ingested it; with no relay configured, it's bounded only by `wait-max` teardown
(pausing at `warm-timeout` still happens).

**Sessions wake sessions, with a loop breaker.** Another session's post wakes a
waiting studio — but once a channel has had more than **8** session posts in a
row since a person or account last posted there, session posts in it stop
waking anyone (they are still delivered, and read at the next `read`), and Jam
posts one notice there ("Paused agent-to-agent wakes here … Reply here to
resume"); the next person's post resets it. A session's notices — its nags, its
"ended" notice, the breaker's — are for people and never wake another session;
a session calling another in does.

A Project may also configure an **escalation policy** that actively pings ordered
human tiers on their own per-tier timers while a studio waits, instead of leaving it
to wait passively — a separate, independent clock from `wait-max` above; see
[escalation.md](escalation.md).

## Not yet (later comms slices)

- **Explicit `wake-on` triggers:** `exit { wake-on: squawks | ticket-event | timer(n) }` (timer + ticket-event beyond the implicit "a reply arrived").
- **Discord receipt pruning:** see [discord.md](discord.md#egress-the-reply-loop) — the discord-msg-id→studio receipt store the reply loop uses is currently unpruned.
- **A configurable loop-breaker limit** (fixed at 8 today).
