---
summary: The messaging MCP — harbor-brokered read/send/list_targets/escalate tools a managed cove's agent uses to converse on its own Linear ticket (and, via an addressed target, elsewhere). Tokens stay in harbor; the endpoint is broker-authorized; the tools reach claude via a `cove-master mcp` stdio server.
read_when: You want a raised cove's agent to be able to read and post comments on the ticket it's working (ask a question, leave a status), or you're wiring/operating the harbor `/messages` endpoint and its cove-side MCP delivery.
owns: the operator-facing messaging-MCP story — the `/messages` broker endpoint, the `cove-master mcp` stdio delivery, and how it's enabled. Does NOT own the target space or access-graph rules — see comms-addressing.md. Does NOT own escalation-category semantics for the `escalate` tool — see escalation.md.
prereqs: coves.md for the managed cove a message is scoped to; dispatcher.md for the tracker/Linear client this reuses; roster.md for the identity a message is attributed to; comms-addressing.md for addressing a target other than the cove's own ticket
tier: leaf
updated: 2026-09-14
---

# The messaging MCP

A managed cove's agent gets **harbor-brokered** tools — `read`, `send`,
`list_targets`, and `escalate` — centered on **its own Linear ticket**. Harbor holds the tracker token, does the platform I/O, and attributes the sender; the cove never holds a channel token, exactly like the Anthropic and git connectors. This is the imperative foundation of the comms hub (slice A of A→B→C).

## What the tools do

- **`send(text, to?)`** — posts a comment. With no `to`, it lands on the cove's own ticket (the original, unchanged behavior). With a `to`, it addresses a human or channel from the Project roster instead — see [comms-addressing.md](comms-addressing.md) for the target space, authorization, and delivery/reply rules (single source; not duplicated here). The author is harbor's brokered identity (the agent can't spoof it).
- **`read()`** — returns the ticket's comment thread as a tagged inbox (`{author, body, …}` per comment). **Always self-scoped to the cove's own ticket** — `read` takes no target, addressed or otherwise.
- **`list_targets()`** — lists the humans/channels this cove is currently authorized to `send(to=…)`; see [comms-addressing.md](comms-addressing.md#discovering-targets-get-messagestargets-list_targets).
- **`escalate(category)`** — declares the cove's current block category, routing the (auto-on-Waiting) escalation ping to that category's tier chain; see [escalation.md](escalation.md#categories-routing-by-block-kind) for the semantics — it's a separate brokered endpoint (`/escalate`), documented there rather than duplicated here.

The agent blends these with its work inside a turn — e.g. leave a status, read a human's prior comment, adjust. (Today `read` reflects the thread as of the call; see [Waiting for a reply](#waiting-for-a-reply-wake-on) below for suspending until a reply arrives.)

## How it's brokered and scoped

- **Endpoint:** harbor serves `/messages` on its cove-facing `:443` mux. A request carries the cove's identity token (`Authorization: Bearer`); harbor resolves the actor, derives **that actor's own ticket** (`Instance.Unit`), and calls Linear.
- **`read` is self-scoped by construction:** it carries no target, so it can only ever return the caller's own ticket. **`send` is self-scoped by default and explicitly authorized when addressed:** an omitted `to` behaves exactly as before; a present `to` is checked against the comms access-graph before delivery — see [comms-addressing.md](comms-addressing.md).
- **Tokens stay in harbor:** the Linear token lives in harbor's `SecretResolver`; the cove holds only its identity token. Nothing is logged that could leak either.

## Delivery to the agent

The cove's `claude` is pointed at a stdio MCP server via `--mcp-config /etc/claude-code/mcp.json` (baked into the image), which launches `cove-master mcp`. That subcommand exposes `read`/`send`/`list_targets` and forwards them to harbor `/messages` (and `/messages/targets`) over TLS through the cove's squid proxy, using the identity token + harbor address already in the cove's environment. No new binary, no new secret in the cove.

## Enabling it

`/messages` is mounted when harbor has a tracker (Linear) configured — it reuses the same Linear client the [dispatcher](dispatcher.md) uses. With no tracker configured, the endpoint is not mounted (and a cove's `read`/`send` calls simply error).

When `message-log:` is also configured, every `send` additionally shadow-writes the logical message (raw body, not the rendered `@handle` form) into the durable Log — visible in the read-only [admin message view](ui.md#messages) — with zero change to live delivery; this is the first writer in the msgport arc (see [ui.md](ui.md) for the Log itself).

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

The wake trigger this slice is **a new ticket comment** (detected as a comment-count
increase past a baseline captured when the cove suspended — restart-safe).

A Project may also configure an **escalation policy** that actively pings ordered
human tiers on their own per-tier timers while a cove waits, instead of leaving it
to wait passively — a separate, independent clock from `wait-max` above; see
[escalation.md](escalation.md).

## Not yet (later comms slices)

- **Explicit `wake-on` triggers:** `exit { wake-on: messages | ticket-event | timer(n) }` (timer + ticket-event beyond the implicit "a reply arrived").
- **C3 — multi-channel** (Discord, generalizing the switchboard) and **actor/role-to-actor addressing** (the `human`/`channel` target space shipped in C1; see [comms-addressing.md](comms-addressing.md)).
