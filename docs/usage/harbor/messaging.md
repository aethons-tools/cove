---
summary: The messaging MCP — harbor-brokered read/send/list_targets/escalate tools a managed cove's agent uses to converse on its own Linear ticket (and, via an addressed target, elsewhere). Tokens stay in harbor; the endpoint is broker-authorized; the tools reach claude via a `cove-master mcp` stdio server.
read_when: You want a raised cove's agent to be able to read and post comments on the ticket it's working (ask a question, leave a status), or you're wiring/operating the harbor `/messages` endpoint and its cove-side MCP delivery.
owns: the operator-facing messaging-MCP story — the `/messages` broker endpoint, the `cove-master mcp` stdio delivery, and how it's enabled. Does NOT own the target space or access-graph rules — see comms-addressing.md. Does NOT own escalation-category semantics for the `escalate` tool — see escalation.md.
prereqs: coves.md for the managed cove a message is scoped to; dispatcher.md for the tracker/Linear client this reuses; roster.md for the identity a message is attributed to; comms-addressing.md for addressing a target other than the cove's own ticket
tier: leaf
updated: 2026-09-15
---

# The messaging MCP

A managed cove's agent gets **harbor-brokered** tools — `read`, `send`,
`list_targets`, and `escalate` — centered on **its own Linear ticket**. Harbor holds the tracker token, does the platform I/O, and attributes the sender; the cove never holds a channel token, exactly like the Anthropic and git connectors. This is the imperative foundation of the comms hub (slice A of A→B→C).

## What the tools do

- **`send(text, to?)`** — appends the message to harbor's durable message-log and returns; a resident egress loop delivers it to Linear shortly after (see [Enabling it](#enabling-it) below for the async delivery contract). With no `to`, it lands on the cove's own ticket (the original, unchanged addressing). With a `to`, it addresses a human or channel from the Project roster instead — see [comms-addressing.md](comms-addressing.md) for the target space, authorization, and delivery/reply rules (single source; not duplicated here). The author is harbor's brokered identity (the agent can't spoof it).
- **`read()`** — returns the cove's inbox as a tagged list (`{author, body, …}` per message); see [Enabling it](#enabling-it) below for what backs it. **Always self-scoped to the cove's own ticket** — `read` takes no target, addressed or otherwise.
- **`list_targets()`** — lists the humans/channels this cove is currently authorized to `send(to=…)`; see [comms-addressing.md](comms-addressing.md#discovering-targets-get-messagestargets-list_targets).
- **`escalate(category)`** — declares the cove's current block category, routing the (auto-on-Waiting) escalation ping to that category's tier chain; see [escalation.md](escalation.md#categories-routing-by-block-kind) for the semantics — it's a separate brokered endpoint (`/escalate`), documented there rather than duplicated here.

The agent blends these with its work inside a turn — e.g. leave a status, read a human's prior comment, adjust. (Today `read` reflects the inbox as of the call; see [Waiting for a reply](#waiting-for-a-reply-wake-on) below for suspending until a reply arrives.)

## How it's brokered and scoped

- **Endpoint:** harbor serves `/messages` on its cove-facing `:443` mux. A request carries the cove's identity token (`Authorization: Bearer`); harbor resolves the actor, derives **that actor's own ticket** (`Instance.Unit`), and calls Linear.
- **`read` is self-scoped by construction:** it carries no target, so it can only ever return the caller's own ticket. **`send` is self-scoped by default and explicitly authorized when addressed:** an omitted `to` behaves exactly as before; a present `to` is checked against the comms access-graph before delivery — see [comms-addressing.md](comms-addressing.md).
- **Tokens stay in harbor:** the Linear token lives in harbor's `SecretResolver`; the cove holds only its identity token. Nothing is logged that could leak either.

## Delivery to the agent

The cove's `claude` is pointed at a stdio MCP server via `--mcp-config /etc/claude-code/mcp.json` (baked into the image), which launches `cove-master mcp`. That subcommand exposes `read`/`send`/`list_targets` and forwards them to harbor `/messages` (and `/messages/targets`) over TLS through the cove's squid proxy, using the identity token + harbor address already in the cove's environment. No new binary, no new secret in the cove.

## Enabling it

`/messages` is mounted when harbor has a tracker (Linear) configured — it reuses the same Linear client the [dispatcher](dispatcher.md) uses. With no tracker configured, the endpoint is not mounted (and a cove's `read`/`send` calls simply error).

**Outbound is Log→egress (asynchronous, at-least-once).** A `send` appends the message to harbor's durable message-log and returns `204`; a resident egress loop then delivers it to Linear (≈ the egress poll interval later). A **message-log is required** for sends — without one, `POST /messages` returns `503`. An append failure returns `502` (the append *is* the delivery). The Log entry — visible in the read-only [admin message view](ui.md#messages) — appears as soon as it's appended, ahead of the Linear post landing.

**Read is Log-backed and tracker-independent.** `GET /messages` returns the cove's **inbox** — the inbound messages addressed to it (human replies), from harbor's durable message-log — not a live Linear query. It returns messages sent **to** the cove (not the cove's own sent messages), and reflects the Log from when ingestion began; pre-Log ticket history is not included. Because wake-on only wakes a cove once a reply is in the Log, the reply is always present by the time the cove reads. A read requires a configured message-log (none → `503`).

When `message-log:` and a tracker are both configured, harbor also runs a resident **msgport linear engine**: on the inbound side it polls the team-scoped Linear comments feed and appends inbound human replies into that same Log, idempotently; on the outbound side it drains the Log's egressable messages (the `send` path above) and posts them to Linear, at-least-once per message. Both directions are visible in the admin message view. **Wake-on (below) now reads replies from this Log**, so `message-log:` is required for a Waiting cove to wake on a reply.

**Discord runs as a second, independent msgport engine, egress AND ingress,** when `message-log:` and `runtime.discord.bot-token` are both configured — see [serve.md](serve.md#the-serve-config) for the config block. It shares the same Log, directory, and cursors/markers file as the linear engine above, keyed separately (`EgressMark` is keyed by `Service()`, so `"linear"` and `"discord"` don't collide). On the outbound side it drains the Log's egressable messages addressed to a discord-project's human DMs or discord roster channels — see [comms-addressing.md](comms-addressing.md#delivery-profiles-per-project-chat-service) for delivery/addressing semantics; on first enable its egress mark is seeded to the Log's tail so turning it on never redelivers the Log's backlog to Discord. On the inbound side it polls each project's Discord inbox channels and routes a human's **reply** (Discord's own reply-to-message feature) back to the cove whose message it replies to, appending it to the Log — see [comms-addressing.md](comms-addressing.md#delivery-profiles-per-project-chat-service) for the reply-loop mechanics, the only-a-reply-routes constraint, and the unpruned-receipts caveat. Wake-on (below) picks up a routed Discord reply exactly like a Linear one.

> **Before relying on inbound (reply) delivery, confirm the Linear `comments` feed schema against your live Linear workspace** — specifically the `$since` scalar (`DateTimeOrDuration` vs `DateTime`) and the `issue → team → key` filter path. harbor targets the schema captured during development; if it differs, the ingress `Poll` errors and its cursor holds (no data loss, inbound stalls) while **egress is unaffected**. This can't be exercised in an egress-locked build environment.

## Waiting for a reply (wake-on)

A raised cove is no longer strictly one-shot. When its agent reports **`needs-input`**
(typically after asking a question via `send`), the cove **suspends** — it reports
Activity `waiting` and blocks instead of ending. Harbor's resident **wake-on engine**
watches the cove's ticket and, when a **new comment** (a reply) arrives, **wakes** it
over the Attach stream; the cove runs its next turn (`claude --continue`), `read`s the
reply, and resumes. A **`wait-max`** bounds the wait — a cove with no reply within it is
torn down (no zombies), paused or not.

While waiting, a cove doesn't stay live-and-idle indefinitely: once it's been waiting
past a **`warm-timeout`** with no reply, the engine **pauses** it (`docker pause`, ≈0
CPU) and moves it to the `idled` [phase](coves.md#the-model) — a paused, intentionally
idle cove whose lease-reaping is suspended (see [coves.md](coves.md) for the phase
chain). When a reply then lands, the engine **unpauses** it (`Resume`) and sends `Wake`
on a subsequent tick once it's reconnected and reporting Live + `waiting` again.

Configure it under `runtime.dispatcher` (it reuses the tracker + Linear client):

```yaml
runtime:
  dispatcher:
    # …role / max-concurrent / linear as before…
    wake-poll-interval: 15s   # how often harbor checks a waiting cove's ticket (default 15s)
    wait-max: 24h             # max a cove may wait for a reply before teardown, paused or not (default 30m)
    warm-timeout: 5m          # how long a waiting cove stays live before it's paused (empty → default 60s)
```

The wake trigger is **an external-origin message addressed to the cove landing in
the durable message Log** after a `WaitCursor` baseline: on entering Waiting the
supervisor stamps `WaitCursor` to the Log's current tail position, and any later
external-origin message addressed to the cove counts as a reply — a log-position
compare, not a wall-clock one (`WaitingSince` is unchanged, but now drives only
`wait-max` teardown and `warm-timeout` pausing below, not reply-detection). It's fed
by the msgport ingress engine above, so **without a configured `message-log:`, a
Waiting cove never wakes on a reply** — it's bounded only by `wait-max` teardown
(pausing at `warm-timeout` still happens).

A Project may also configure an **escalation policy** that actively pings ordered
human tiers on their own per-tier timers while a cove waits, instead of leaving it
to wait passively — a separate, independent clock from `wait-max` above; see
[escalation.md](escalation.md).

## Not yet (later comms slices)

- **Explicit `wake-on` triggers:** `exit { wake-on: messages | ticket-event | timer(n) }` (timer + ticket-event beyond the implicit "a reply arrived").
- **Discord receipt pruning:** see [comms-addressing.md](comms-addressing.md#delivery-profiles-per-project-chat-service) — the discord-msg-id→cove receipt store the reply loop uses is currently unpruned.
- **Actor/role-to-actor addressing** (the `human`/`channel` target space shipped in C1; see [comms-addressing.md](comms-addressing.md)).
