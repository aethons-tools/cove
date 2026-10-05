---
summary: The intercom MCP — Jam-brokered read/commit/send/list_targets/escalate tools a managed studio's agent uses to converse on its own Linear ticket (and, via an addressed target, elsewhere). Tokens stay in Jam; the endpoint is broker-authorized; the tools reach claude via a `cove-master mcp` stdio server.
read_when: You want a raised studio's agent to be able to read and post comments on the ticket it's working (ask a question, leave a status), or you're wiring/operating Jam `/squawks` endpoint and its cove-side MCP delivery, tuning wake-on (`runtime.wake`), or running the intercom on a Jam with no Requisitioner.
owns: the operator-facing intercom-MCP story — the `/squawks` broker endpoint, the `cove-master mcp` stdio delivery, and how it's enabled. Does NOT own the target space or access-graph rules — see comms-addressing.md. Does NOT own escalation-category semantics for the `escalate` tool — see escalation.md.
prereqs: coves.md for the managed studio a squawk is scoped to; personal-sessions.md for a ticketless studio that talks to its owner; requisitioner.md for the tracker/Linear client this reuses; roster.md for the identity a squawk is attributed to; comms-addressing.md for addressing a target other than the studio's own ticket
tier: leaf
updated: 2026-10-05
---

# The intercom MCP

A managed studio's agent gets **Jam-brokered** tools — `read`, `send`,
`list_targets`, and `escalate` — centered on **its own Linear ticket**. Jam holds the tracker token, does the platform I/O, and attributes the sender; the studio never holds a channel token, exactly like the Anthropic and git connectors. This is the imperative foundation of the comms hub (slice A of A→B→C).

## What the tools do

- **`send(text, to?, content_type?)`** — appends the squawk to Jam's durable squawk Log and returns; a resident egress loop delivers it to Linear shortly after (see [Enabling it](#enabling-it) below for the async delivery contract). With no `to`, it goes to the studio's **default recipient**: its own ticket when it has one (the original, unchanged addressing); for a ticketless [personal session](personal-sessions.md), its owner (`human:<owner>`); with neither (e.g. a [standing session](standing-sessions.md#messaging)), the send answers `400 no default recipient: pass "to"`. With a `to`, it addresses a human or channel from the Project roster instead — see [comms-addressing.md](comms-addressing.md) for the target space, authorization, and delivery/reply rules (single source; not duplicated here). The author is Jam's brokered identity (the agent can't spoof it). The body is markdown unless `content_type` opts out; see [Content type](#content-type-markdown-or-plain-text).
- **`read(anchor?, id?, dir?, limit?)`** — reads the studio's inbox **as a queue**: by default the next unprocessed squawks after the studio's durable commit cursor, oldest-first. Seek with `anchor` (`cursor` default / `start` / `end` / `id`) × `dir` (`forward` default / `backward`) × `limit` (default 50); the response also carries `committed_cursor` / `page_first` / `page_last`. **Reading never advances the cursor.** **Always self-scoped to the studio's own ticket** — `read` takes no target. See [The inbox as a durable queue](#the-inbox-as-a-durable-queue) below.
- **`commit(up_to)`** — confirms the studio has processed its inbox up to a squawk id, advancing its durable commit cursor (monotonic, forward-only) so those squawks aren't handed to it again. Separate from `read` — reads don't commit. Self-scoped (the cursor is the caller's own; identity comes from the token, never the body).
- **`list_targets()`** — lists the humans/channels this studio is currently authorized to `send(to=…)`; see [comms-addressing.md](comms-addressing.md#discovering-targets-get-squawkstargets-list_targets).
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

- **Endpoint:** Jam serves `/squawks` on its cove-facing `:443` mux. A request carries the studio's identity token (`Authorization: Bearer`); Jam resolves the actor, derives **that actor's own ticket** (`Instance.Unit`), and calls Linear.
- **`read` is self-scoped by construction:** it carries no target, so it can only ever return the caller's own ticket. **`send` is self-scoped by default and explicitly authorized when addressed:** an omitted `to` behaves exactly as before; a present `to` is checked against the comms access-graph before delivery — see [comms-addressing.md](comms-addressing.md).
- **Tokens stay in Jam:** the Linear token lives in Jam's `SecretResolver`; the studio holds only its identity token. Nothing is logged that could leak either.

## Delivery to the agent

The studio's `claude` is pointed at a stdio MCP server named `messaging` in the per-run `--mcp-config` that cove-master's harness generates (alongside any kit [`mcp-servers`](kits.md#mcp-servers-cov-240), which can never replace it), which launches `cove-master mcp`. That subcommand exposes `read`/`send`/`list_targets` and forwards them to Jam `/squawks` (and `/squawks/targets`) over TLS through the studio's squid proxy, using the identity token + Jam address already in the studio's environment. No new binary, no new secret in the studio.

## Enabling it

`/squawks` (and `/escalate`) are always mounted: the intercom is always on, backed by the Postgres squawk Log (`store-postgres` is required — [serve.md](serve.md#the-serve-config)), with or without a [Requisitioner](requisitioner.md), so a [personal session](personal-sessions.md) can converse on a Jam with none. The Linear relay below still needs the Requisitioner's tracker; the Discord relay and wake-on do not.

**Outbound is Log→egress (asynchronous, at-least-once).** A `send` appends the squawk to Jam's durable squawk Log and returns `204`; a resident egress loop then delivers it to Linear (≈ the egress poll interval later). An append failure returns `502` (the append *is* the delivery). The Log entry — visible in the read-only [admin intercom view](ui.md#intercom) — appears as soon as it's appended, ahead of the Linear post landing.

**Read is Log-backed and tracker-independent.** `GET /squawks` returns the studio's **inbox** — the inbound squawks addressed to it (human replies), from Jam's durable squawk Log — not a live Linear query. It returns squawks sent **to** the studio (not the studio's own sent squawks), and reflects the Log from when ingestion began; pre-Log ticket history is not included. Because wake-on only wakes a studio once a reply is in the Log, the reply is always present by the time the studio reads.

When a Requisitioner (its tracker) is configured, Jam also runs a resident **relay linear engine**: on the inbound side it polls the team-scoped Linear comments feed and appends inbound human replies into that same Log, idempotently; on the outbound side it drains the Log's egressable squawks (the `send` path above) and posts them to Linear, at-least-once per squawk. Both directions are visible in the admin intercom view. Wake-on reads replies from this Log.

**Discord runs as a second, independent relay engine, egress AND ingress,** when `runtime.discord.bot-token-cred` is configured — with or without a Requisitioner — see [serve.md](serve.md#the-serve-config) for the config block. It shares the same Log and the same relay cursors/markers files (in [`state-dir`](serve.md#the-serve-config)) as the linear engine above, keyed separately (`EgressMark` is keyed by `Service()`, so `"linear"` and `"discord"` don't collide). On the outbound side it drains the Log's egressable squawks addressed to a discord-project's human DMs or discord roster channels — see [discord.md](discord.md) for delivery semantics; on first enable its egress mark is seeded to the Log's tail so turning it on never redelivers the Log's backlog to Discord. On the inbound side it polls the Discord inbox channels of **every project whose chat service is `discord`** (plus the Requisitioner's project, if any) and routes a human's **reply** (Discord's own reply-to-message feature) back to the studio whose squawk it replies to, appending it to the Log — see [discord.md](discord.md#egress-the-reply-loop) for the reply-loop mechanics, who a reply is attributed to,, the only-a-reply-routes constraint, and the unpruned-receipts caveat. Wake-on (below) picks up a routed Discord reply exactly like a Linear one.

> **Before relying on inbound (reply) delivery, confirm the Linear `comments` feed schema against your live Linear workspace** — specifically the `$since` scalar (`DateTimeOrDuration` vs `DateTime`) and the `issue → team → key` filter path. Jam targets the schema captured during development; if it differs, the ingress `Poll` errors and its cursor holds (no data loss, inbound stalls) while **egress is unaffected**. This can't be exercised in an egress-locked build environment.

## The inbox as a durable queue

A studio's inbox is a **durable, acked queue** over the squawk Log, not a snapshot
view — it's a conversation to process in order, not an email list.

> **Ordering is by a monotonic append sequence, not by squawk id.** Every Log
> squawk carries an internal append `seq`; "oldest-first", "forward",
> "tail", and the monotonic commit/wake cursors are all defined by that `seq`.
> Squawk **ids are identifiers, not ordering keys** — ingress ids
> (`in:linear:<uuid>`, `in:discord:<snowflake>`) are deterministic for
> idempotent dedup and are **not** lexically sortable against each other or the
> internal time-based ids, so cursors compare by sequence. The wire stays in
> squawk ids (`committed_cursor`/`page_first`/`page_last`/`up_to` are ids the
> studio echoes back); Jam resolves id↔seq at the boundary.

- **Commit cursor.** Each studio has a durable commit cursor (its last *processed*
  squawk id) stored on its instance in the Jam store. It is **initialized at
  raise to the Log's current tail**, so a freshly-raised studio consumes squawks
  addressed to it from that point forward, not the whole prior history. It is
  **separate from the wake-on `WaitSeq`** ([below](#waiting-for-a-reply-wake-on)):
  one is the consume offset, the other the reply-wake baseline.
- **Seekable reads that never commit.** `read` (default) returns the next
  squawks after the commit cursor, oldest-first. `anchor` (`cursor`/`start`/`end`/`id`)
  × `dir` (`forward`/`backward`) × `limit` let the studio page anywhere —
  re-read processed history, jump to the start/end, or walk from a given id.
  Reading is pure: it never moves the cursor.
- **Explicit commit.** When the studio has durably handled squawks, it calls
  `commit(up_to)` to advance the cursor past them (monotonic, forward-only,
  idempotent). Until it commits, uncommitted squawks remain in the queue — so a
  studio that restarts before committing re-consumes them (at-least-once).
- **Nothing is pruned** — the Log is a durable audit/research record; the cursor
  is only a position into an ever-growing log, so backward/`start` reads always
  work. (Date-anchored reads are a planned addition.)

## Waiting for a reply (wake-on)

A raised studio is no longer strictly one-shot. When its agent reports **`needs-input`**
(typically after asking a question via `send`), the studio **suspends** — it reports
Activity `waiting` and blocks instead of ending. Jam's resident **wake-on engine**
watches the studio's ticket and, when a **new comment** (a reply) arrives, **wakes** it
over the Attach stream; the studio runs its next turn — written into the live agent if one is running, else a new `claude --continue` episode — `read`s the
reply, and resumes. When its [session context](session-context.md#refresh) changed
meanwhile, the wake text says so. A **`wait-max`** bounds the wait — a studio with no reply within it is
torn down (no zombies), paused or not. **Resident sessions are exempt from `wait-max`:**
a [personal](personal-sessions.md) or [standing](standing-sessions.md) session waits
after every turn and is never torn down for `wait-max` (it is still paused at
`warm-timeout` and woken on a reply). A personal session instead climbs the
[idle ladder](personal-sessions.md#the-idle-ladder) — nags to its owner, and an
optional reclaim. The owner's `keep`/`release` reply to a nag is acted on by Jam
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

The wake trigger is **an external-origin squawk addressed to the studio landing in
the durable squawk Log** after a `WaitSeq` baseline: when a run starts (at raise, and
whenever the studio enters `running`) the supervisor stamps `WaitSeq` to the Log's
current tail sequence — the starting agent reads its inbox itself — and any later
external-origin squawk addressed to the studio (append `seq` > `WaitSeq`) counts as a
reply. Waking a `running` studio advances `WaitSeq` past the replies it was woken for;
entering `waiting` leaves `WaitSeq` alone, so a reply that landed during the run and was
not yet woken for wakes the studio as soon as it waits. It is an append-sequence compare,
not a wall-clock one (`WaitingSince` is unchanged, but now drives only
`wait-max` teardown and `warm-timeout` pausing below, not reply-detection). It's fed
by the relay ingress engine above, so a Waiting studio wakes on a reply only once a relay
has ingested it; with no relay configured, it's bounded only by `wait-max` teardown
(pausing at `warm-timeout` still happens).

A Project may also configure an **escalation policy** that actively pings ordered
human tiers on their own per-tier timers while a studio waits, instead of leaving it
to wait passively — a separate, independent clock from `wait-max` above; see
[escalation.md](escalation.md).

## Not yet (later comms slices)

- **Explicit `wake-on` triggers:** `exit { wake-on: squawks | ticket-event | timer(n) }` (timer + ticket-event beyond the implicit "a reply arrived").
- **Discord receipt pruning:** see [discord.md](discord.md#egress-the-reply-loop) — the discord-msg-id→studio receipt store the reply loop uses is currently unpruned.
- **Actor/role-to-actor addressing** (the `human`/`channel` target space shipped in C1; see [comms-addressing.md](comms-addressing.md)).
