---
summary: The messaging MCP — a harbor-brokered read/send tool a managed cove's agent uses to converse on its own Linear ticket. Tokens stay in harbor; the endpoint is self-scoped; the tools reach claude via a `cove-master mcp` stdio server.
read_when: You want a raised cove's agent to be able to read and post comments on the ticket it's working (ask a question, leave a status), or you're wiring/operating the harbor `/messages` endpoint and its cove-side MCP delivery.
owns: the operator-facing messaging-MCP story — the `/messages` broker endpoint (self-scoped read/send on the cove's ticket), the `cove-master mcp` stdio delivery, and how it's enabled
prereqs: coves.md for the managed cove a message is scoped to; dispatcher.md for the tracker/Linear client this reuses; roster.md for the identity a message is attributed to
tier: leaf
updated: 2026-09-13
---

# The messaging MCP

A managed cove's agent gets two **harbor-brokered** tools — `read` and `send` — over **its own Linear ticket**. Harbor holds the tracker token, does the platform I/O, and attributes the sender; the cove never holds a channel token, exactly like the Anthropic and git connectors. This is the imperative foundation of the comms hub (slice A of A→B→C).

## What the tools do

- **`send(text)`** — posts a comment on the cove's own ticket. The author is harbor's brokered identity (the agent can't spoof it).
- **`read()`** — returns the ticket's comment thread as a tagged inbox (`{author, body, …}` per comment).

The agent blends these with its work inside a turn — e.g. leave a status, read a human's prior comment, adjust. (Suspending until a *reply* arrives — the `wake-on` exit — is the next comms slice; today `read` reflects the thread as of the call.)

## How it's brokered and scoped

- **Endpoint:** harbor serves `/messages` on its cove-facing `:443` mux. A request carries the cove's identity token (`Authorization: Bearer`); harbor resolves the actor, derives **that actor's own ticket** (`Instance.Unit`), and calls Linear.
- **Self-scoped by construction:** there is **no ticket/target parameter** — the ticket is derived only from the authenticated identity, so a cove can only ever touch its own ticket. (Addressing *other* actors/roles/channels + a comms access-graph is the escalation slice, not this one.)
- **Tokens stay in harbor:** the Linear token lives in harbor's `SecretResolver`; the cove holds only its identity token. Nothing is logged that could leak either.

## Delivery to the agent

The cove's `claude` is pointed at a stdio MCP server via `--mcp-config /etc/claude-code/mcp.json` (baked into the image), which launches `cove-master mcp`. That subcommand exposes `read`/`send` and forwards them to harbor `/messages` over TLS through the cove's squid proxy, using the identity token + harbor address already in the cove's environment. No new binary, no new secret in the cove.

## Enabling it

`/messages` is mounted when harbor has a tracker (Linear) configured — it reuses the same Linear client the [dispatcher](dispatcher.md) uses. With no tracker configured, the endpoint is not mounted (and a cove's `read`/`send` calls simply error).

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

## Not yet (later comms slices)

- **Explicit `wake-on` triggers:** `exit { wake-on: messages | ticket-event | timer(n) }` (timer + ticket-event beyond the implicit "a reply arrived").
- **C — escalation:** Project on-call tiers (`category → ordered {actor|role|channel}` + per-tier timeout) and the comms access-graph that governs who an actor may message.
- **Multi-channel** (Discord, generalizing the switchboard) and the symbolic `actor|role|channel` target space.
