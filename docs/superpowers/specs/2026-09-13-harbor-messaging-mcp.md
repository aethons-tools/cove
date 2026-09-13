# harbor: messaging MCP — brokered read/send on the cove's Linear ticket (COV-145, comms slice A)

**Status:** design approved, pre-plan
**Issue:** COV-145 (comms hub, slice A of 3: A messaging MCP → B wake-on suspend/resume → C escalation)
**Foundation:** COV-146 (Linear client + tickets), COV-158 (cove-master embedded in the image), COV-157 (:443 mux). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` (§universal runtime; messaging MCP as a harbor-brokered connector).

## Summary

Give a managed cove a **harbor-brokered messaging tool** so its agent can converse mid-turn on the ticket it's working. The agent gets `read`/`send` MCP tools; harbor holds the tracker token, does the platform I/O, and attributes the sender — the cove never holds a channel token, exactly like the Anthropic/git connectors.

**Decided in brainstorming:**
- **Channel:** the cove's own **Linear ticket** (`Instance.Unit`). `send` → a comment on that ticket; `read` → the ticket's comments as a tagged inbox. Reuses the Linear client + token harbor already holds.
- **Message shape:** `{id, from, body, at, source}` — `from` is **harbor-attributed** (never agent-supplied). `To` is implicit (the actor's own ticket); the symbolic `actor|role|channel` target space + access-graph are deferred to escalation (slice C).
- **Delivery:** a **stdio MCP server** as a `cove-master mcp` subcommand (no new binary), forwarding `read`/`send` to harbor's `/messages` over :443. Uses the official `github.com/modelcontextprotocol/go-sdk` v1.7.0 (fetch verified through the locked egress).
- **Scope:** `send` + `read`.

## Package layout & boundary

- **`internal/harbor` (or a small `internal/harbor/messages` file)** — the `/messages` HTTP handler. It may use the harbor `Store` (Lookup + GetInstance) but must NOT pull grpc/kit into harbor core. Since it needs the Linear client (which lives in `internal/dispatch/linear`, pulling `internal/kit`), the handler is **constructed in `cmd/at-harbor`** and injected the pieces it needs via a narrow interface — keeping the core kit-free. (Mirror the launcher/dispatcher pattern: the messaging *handler type* can live in `internal/harbor` taking a `Commenter` interface, with the concrete Linear client wired from cmd/at-harbor.)
- **`internal/dispatch/linear`** — add `IssueByIdentifier(ctx, identifier) (string, error)`.
- **`cmd/cove-master`** — a new `mcp` subcommand (arg dispatch in `main`).
- **`internal/assemble/hardening/image-files/etc/claude-code/managed-settings.json`** — add an `mcpServers` entry (payload for the built sandbox).
- **Boundary gates:** `internal/harbor` core stays grpc/kit/backend/connect-free (the messaging handler takes an interface; the Linear client is wired from cmd). `cmd/at-cove` stays oidc/grpc-free. Tokens never leave harbor.

## The message + the harbor `/messages` endpoint

Served on the cove-facing **:443 mux** (the non-gRPC HTTP side). Today `serveMux` routes non-gRPC to the broker; add a tiny path mux: `/messages` → the messaging handler, everything else → the broker.

**Auth + resolution (both verbs):**
1. `Authorization: Bearer <identity-token>` → `actor, ok := store.Lookup(HashToken(tok))` (401 if not ok) — the same resolution the broker uses.
2. `inst, ok := store.GetInstance(actor.ID)` → the actor's ticket `inst.Unit` (409 if the actor has no live instance/ticket). **Self-scoped:** the actor can only touch its own ticket — there is no target parameter, so no access-graph is needed.
3. `issueID, err := linear.IssueByIdentifier(ctx, inst.Unit)` (resolve "AET-42" → internal id).

**`POST /messages`** — send. Body `{"body":"..."}` → `linear.PostComment(ctx, issueID, body)`. Returns 204. (`from` is the cove; harbor need not stamp anything extra — the comment is authored by harbor's Linear token, i.e. the cove's brokered identity.)

**`GET /messages`** — read. → `linear.Comments(ctx, issueID)` → `{"messages":[{"id","author","body","at"}]}` (source is implicitly "tracker"). The tagged inbox.

Errors are structured (401/409/502) with clear messages; the token/comment bodies are never logged.

`IssueByIdentifier` (new, `internal/dispatch/linear/linear.go`) — a GraphQL query for one issue by its human identifier, returning the internal id (build on the existing `do(...)` helper + `issueNode{ID,Identifier}`).

## The `cove-master mcp` subcommand

`cmd/cove-master/main.go` dispatches on `os.Args[1]`: `mcp` → run the MCP server; otherwise the existing client run (unchanged).

The MCP server (official go-sdk) exposes two tools over **stdio**:
- **`send`** — input `{text: string}`. Calls `POST https://<harbor-host>/messages` with `{"body": text}` and the identity token as `Authorization: Bearer`. Returns a confirmation.
- **`read`** — no input. Calls `GET .../messages`, returns the inbox items as the tool result (formatted: `author (at): body` per line, or structured content).

It reads `AT_HARBOR_RUNTIME_ADDR` (`harbor.host:443` → `https://harbor.host/messages`) and `AT_HARBOR_IDENTITY_TOKEN` from env — both already injected into the cove by the launcher's connector (COV-158). The HTTP client uses TLS (system trust) and Go's default `ProxyFromEnvironment`, so the call rides the cove's squid `CONNECT` proxy to harbor:443, exactly like cove-master's Attach dial. No new env, no token in the cove beyond its identity.

## Cove image — giving `claude -p` the tools

Add to `managed-settings.json`:
```json
"mcpServers": {
  "messaging": { "command": "cove-master", "args": ["mcp"] }
}
```
When the agent wrapper runs `claude -p` in a raised cove, claude launches `cove-master mcp` as a stdio MCP server (inheriting the cove's env, so the subcommand has the identity token + harbor addr).

**Integration risk to verify in the plan (before wiring):** confirm headless `claude -p --dangerously-skip-permissions` actually loads `mcpServers` from `managed-settings.json` and exposes the tools. If it does not in `-p` mode, the fallback is for the agent wrapper (`internal/agentrun`) to pass `--mcp-config <path>` pointing at a baked config (and/or `--allowedTools`). The plan's first task verifies the loading mechanism and picks the config surface accordingly; everything else is independent of that choice.

## Wiring (`cmd/at-harbor serve`)

Build the Linear client once (it already is, for the dispatcher) and share it with both the dispatcher and the messaging handler. Enable `/messages` when a tracker is configured (Slice A pairs messaging with the Linear tracker the dispatcher uses — reuse `runtime.dispatcher.linear` + the resolved tracker token; if the dispatcher block is absent, `/messages` is simply not mounted). Wrap the broker in a path mux so `/messages` routes to the messaging handler.

## Tests (hermetic)

- **harbor messaging handler** (`internal/harbor` or `cmd/at-harbor`): a fake `Commenter` (records `PostComment`, returns canned `Comments`) + a fake store — assert: bad token → 401; actor with no instance → 409; `POST` → `PostComment(issueID, body)` on the actor's own ticket (identifier resolved); `GET` → the tagged inbox JSON; another actor cannot reach a different ticket (self-scope).
- **`linear.IssueByIdentifier`**: against the existing linear test harness (the package's httptest-style GraphQL fake) — identifier → id.
- **`cove-master mcp`** (`cmd/cove-master`): drive the MCP server via the go-sdk's in-memory transport (or a stdio pipe) — `initialize` → `tools/list` shows `read`/`send` → `tools/call send` POSTs to a fake `/messages`; `tools/call read` returns the inbox. Assert the identity token rides the `Authorization` header and the harbor URL is derived from `AT_HARBOR_RUNTIME_ADDR`.
- **assemble**: `managed-settings.json` parses and carries the `mcpServers.messaging` entry (extend the existing image-files/settings test if present).

New dep: `github.com/modelcontextprotocol/go-sdk` v1.7.0 (+ transitive: jsonschema-go, segmentio/encoding+asm, uritemplate, x/time) — fetch verified; commit go.mod/go.sum.

## Docs

- New `docs/usage/harbor/messaging.md` (leaf): the messaging MCP — what `read`/`send` do, that it's brokered (tokens stay in harbor) and self-scoped to the cove's ticket, the `cove-master mcp` delivery, and the deferred slices (wake-on, escalation, multi-channel). INDEX row.
- `docs/usage/harbor/coves.md`: one line — a raised cove's agent has `read`/`send` on its ticket.

## Deferred (later comms slices)

- **B — wake-on suspend/resume:** `exit { wake-on: messages | ticket-event | timer }`; harbor suspends a `Waiting` cove and `Wake`s it (Attach `Wake` + `WAITING` already built) on an inbound; `read` fetches the answer.
- **C — escalation:** Project on-call tiers (`category → ordered {actor|role|channel}` + per-tier timeout); the comms access-graph; roster addressing (Channel/roster types — greenfield).
- **Multi-channel** (Discord, generalizing at-switchboard + brokering its token); the symbolic `actor|role|channel` target space; the conductor loop moving into harbor.
