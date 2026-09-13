# harbor Messaging MCP (brokered read/send on the cove's ticket) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a managed cove's agent harbor-brokered `read`/`send` messaging tools over its own Linear ticket — tokens stay in harbor, the endpoint is self-scoped, and the tools reach claude via a `cove-master mcp` stdio server.

**Architecture:** Harbor gains a `/messages` HTTP handler on the :443 mux (identity-token auth → the actor's own ticket → Linear comment/read). A `cove-master mcp` subcommand runs a stdio MCP server (official go-sdk) whose `read`/`send` tools forward to `/messages` over TLS-through-proxy. The cove's `claude -p` is pointed at it via `--mcp-config`.

**Tech Stack:** Go 1.25; `github.com/modelcontextprotocol/go-sdk` v1.7.0 (new); existing `internal/harbor`, `internal/dispatch/linear`, `internal/agentrun`, `internal/assemble`.

## Global Constraints

- **Module commands offline:** `GOPROXY=off` for go commands; the go-sdk + its transitive deps are in the cache (verified). Task 4 adds it to go.mod from cache (add the require line first, then `GOPROXY=off GOFLAGS=-mod=mod go mod tidy`).
- **Security (this is a trust boundary — the cove is semi-trusted):**
  - **Authz by construction / no IDOR:** the ticket identifier is derived **only** from the authenticated actor's own `Instance.Unit`, **never** from any request body/query/header. There is no ticket/target parameter. A cove cannot name another cove's ticket.
  - **Auth:** reuse the broker's exact pattern — `actor, ok := store.Lookup(HashToken(bearerToken))`. Fail **closed**: unknown/missing token → 401; actor has no live `Instance` → 403. No custom token comparison (the hash-map lookup is the correct constant-work primitive; do NOT byte-compare tokens).
  - **Never log secrets:** never log the bearer token, the harbor-held Linear token, or full comment bodies. Client errors are generic (401/403/502 + a short message); server-side logs carry actor id + ticket + byte lengths, never token or body content (mirrors the COV-156 "don't log the prompt" rule).
  - **Input limits:** allow only `GET`/`POST` on `/messages` (else 405); cap the `POST` body (e.g. reject bodies over a fixed max, ~16 KiB) to bound abuse.
  - **No injection:** `linear.PostComment`/`IssueByIdentifier` must pass the body/identifier as GraphQL **variables** (via the existing `do(ctx, query, vars, out)` helper), never string-concatenated into the query.
  - **Cove→harbor is TLS:** the `mcp` subcommand dials `https://<harbor-host>/messages` (system trust, `ProxyFromEnvironment` for the squid CONNECT), never plaintext; the identity token comes from env only and is never placed on argv or in logs.
- **Boundaries:** `internal/harbor` core stays grpc/kit/backend/connect-free — the `/messages` handler takes a narrow `Commenter` interface (the concrete `*linear.Client` is wired from `cmd/at-harbor`). `cmd/at-cove` stays oidc/grpc-free.
- TDD; hermetic tests (fakes; no network/claude); gofmt-clean.
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

---

### Task 1: `linear.IssueByIdentifier`

**Files:** Modify `internal/dispatch/linear/linear.go`; test in the package's existing test file.

**Interfaces:** Produces `func (c *Client) IssueByIdentifier(ctx context.Context, identifier string) (string, error)` — resolves "AET-42" → the internal issue id. Tasks 2/3 consume it.

- [ ] **Step 1: failing test** — using the package's existing GraphQL test harness (httptest server returning canned JSON — mirror an existing `linear` test), assert `IssueByIdentifier(ctx, "AET-42")` issues a query with the identifier **as a variable** and returns the node id; and that a not-found response returns a clear error.
- [ ] **Step 2:** run it, confirm fail.
- [ ] **Step 3:** implement using the existing `do(ctx, query, vars, out)` helper — a GraphQL `issue`/`issueSearch` by identifier passing `{"id": identifier}` (or the team-scoped search the client already uses) as **vars**; decode into an `issueNode{ID}`. No string concatenation of the identifier into the query.
- [ ] **Step 4:** run tests green.
- [ ] **Step 5:** gofmt + commit (`linear: IssueByIdentifier (identifier → internal id) for the messaging endpoint (COV-145)`).

---

### Task 2: Harbor `/messages` handler

**Files:** Create `internal/harbor/messages.go`; test `internal/harbor/messages_test.go`.

**Interfaces:**
- Produces: `Commenter` interface (`IssueByIdentifier`, `PostComment`, `Comments`); `MessagesHandler` (an `http.Handler`) built via `NewMessagesHandler(store messagesStore, cmt Commenter, log *slog.Logger)`.
- `messagesStore` (narrow): `Lookup(tokenHash string) (Actor, bool)` + `GetInstance(actorID string) (Instance, bool)` — satisfied by `harbor.Store`.

- [ ] **Step 1: failing test** — `internal/harbor/messages_test.go`. A `fakeCommenter` (records `PostComment`, returns canned `Comments`, maps identifier→id) + a `fakeStore` (canned actor-by-hash + instance-by-actor). Assert:
  - missing/unknown Bearer → **401**, no Commenter calls.
  - known token, actor has no Instance → **403**.
  - `POST {"body":"hi"}` with a valid token → `PostComment(<id of the actor's Unit>, "hi")` called; **204**. The ticket id comes from the actor's own `Instance.Unit` (the test's fakeStore returns Unit="AET-7"; assert PostComment got the id `IssueByIdentifier("AET-7")` returns) — there is no ticket field in the request.
  - `GET` → 200 with `{"messages":[{"id","author","body","at"}]}` from `Comments`.
  - method `PUT` → **405**.
  - `POST` body over the max → **413** (or 400), no PostComment.
  - the token string never appears in any logged output (capture the test logger; assert absent) and error bodies are generic.

```go
// sketch of the security-critical assertion
func TestMessagesPostIsSelfScoped(t *testing.T) {
    store := &fakeStore{
        actors:    map[string]Actor{HashToken("tok-A"): {ID: "cove-AET-7"}},
        instances: map[string]Instance{"cove-AET-7": {ActorID: "cove-AET-7", Unit: "AET-7"}},
    }
    cmt := &fakeCommenter{ids: map[string]string{"AET-7": "iss_7"}}
    h := NewMessagesHandler(store, cmt, testLogger)
    // POST with tok-A → comments on AET-7 (=iss_7); the request carries NO ticket field.
    // ...assert cmt.posted == {issueID:"iss_7", body:"hi"}...
}
```

- [ ] **Step 2:** run, confirm fail.
- [ ] **Step 3: implement** `internal/harbor/messages.go`:
  - `Commenter` + `messagesStore` interfaces; `MessagesHandler` with `ServeHTTP`.
  - Parse `Authorization: Bearer <tok>` (400/401 if absent/malformed). `actor, ok := store.Lookup(HashToken(tok))` → 401 if `!ok`. `inst, ok := store.GetInstance(actor.ID)` → 403 if `!ok`.
  - Resolve `issueID, err := cmt.IssueByIdentifier(ctx, inst.Unit)` → 502 (generic) on error, log detail server-side (no token/body).
  - `POST`: read body with `http.MaxBytesReader` (cap ~16 KiB → 413 on exceed); decode `{"body":string}` (400 if empty); `cmt.PostComment(ctx, issueID, body)` → 502 on error; else 204.
  - `GET`: `cmt.Comments(ctx, issueID)` → JSON `{"messages":[...]}` (map `scheduler.Comment` → `{author, body}`; include id/at if available). 200.
  - other methods → 405.
  - Logging: `log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "send", "bytes", n)` — never the token or body text.
- [ ] **Step 4:** run tests green.
- [ ] **Step 5:** gofmt + commit (`harbor: /messages handler — self-scoped brokered comment read/send (COV-145)`).

---

### Task 3: Wire `/messages` into `cmd/at-harbor serve`

**Files:** Modify `cmd/at-harbor/main.go` (cmdServe).

- [ ] **Step 1:** where the dispatcher's Linear client is built (Task COV-146 wiring), share that `*linear.Client` (it satisfies `harbor.Commenter`). If a dispatcher/tracker is configured, build `msgH := harbor.NewMessagesHandler(st, tracker, log)` and mount it: wrap the broker in a path mux so the HTTP handler passed to `serveMux` routes `/messages` → `msgH`, everything else → `broker`. Gate on the tracker being configured (no tracker ⇒ `/messages` not mounted). Reuse the single linear client for both dispatcher + messaging (build it once).
- [ ] **Step 2:** `GOPROXY=off go build ./... && GOPROXY=off go test ./cmd/at-harbor/ ./internal/harbor/ -v`; boundary `go list -deps ./internal/harbor | grep -iE 'internal/dispatch|internal/kit|grpc' || echo "harbor core clean"` (the handler type is in core but takes an interface; the linear client is only referenced from cmd — confirm core stays clean).
- [ ] **Step 3:** gofmt + commit (`at-harbor: mount /messages on the :443 mux (shared linear client) (COV-145)`).

---

### Task 4: `cove-master mcp` stdio MCP subcommand

**Files:** Modify `cmd/cove-master/main.go` (+ a new file e.g. `cmd/cove-master/mcp.go`); `go.mod`/`go.sum`. Test `cmd/cove-master/mcp_test.go`.

- [ ] **Step 1:** add the dep from cache — put `github.com/modelcontextprotocol/go-sdk v1.7.0` in go.mod's require, then `GOPROXY=off GOFLAGS=-mod=mod go mod tidy` (pulls the 5 cached transitives). Confirm `GOPROXY=off go build ./...` still works.
- [ ] **Step 2: failing test** — `cmd/cove-master/mcp_test.go`, using the go-sdk's in-memory transports so no real stdio/claude is needed:
  ```go
  // server + client over paired in-memory transports (go-sdk v1.7.0 API)
  cli, srv := mcp.NewInMemoryTransports()
  // build our server against an httptest fake /messages (sets AT_HARBOR_RUNTIME_ADDR/TOKEN via getenv)
  go newMessagingServer(getenv).Run(ctx, srv)
  c := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
  sess, _ := c.Connect(ctx, cli, nil)
  // sess.ListTools → contains read+send; sess.CallTool("send", {"text":"hi"}) → fake /messages saw POST {"body":"hi"} + Bearer <tok>; CallTool("read") → inbox
  ```
  Assert: `read` + `send` are listed; `send {text:"hi"}` issues `POST <base>/messages` with `Authorization: Bearer <tok>` and `{"body":"hi"}` (verified at the httptest fake); `read` GETs and returns the inbox; the base URL derives from `AT_HARBOR_RUNTIME_ADDR`, the token from `AT_HARBOR_IDENTITY_TOKEN`; a non-2xx from harbor → a tool error that does **not** contain the token. (Adjust `Connect`/`ListTools`/`CallTool` to the exact v1.7.0 session API — the shape above is the SDK's; confirm method names against the cached package.)
- [ ] **Step 3:** run, confirm fail.
- [ ] **Step 4: implement** — `main` dispatches `os.Args[1] == "mcp"` → `runMCP(getenv, stdin, stdout, stderr)`; otherwise the existing client run (unchanged). `runMCP`:
  - build base URL `https://<host>` from `AT_HARBOR_RUNTIME_ADDR` (`harbor.host:443` → strip `:443`/derive scheme+host); read `AT_HARBOR_IDENTITY_TOKEN`; error clearly if either missing.
  - `http.Client` with default transport (TLS system trust + `ProxyFromEnvironment`); a small `postMessage(text)` / `readMessages()` that set `Authorization: Bearer <tok>`, `Content-Type: application/json`, cap/stream responses, and map non-2xx → a generic error (never echo the token).
  - register two tools with the go-sdk (v1.7.0 API): `s := mcp.NewServer(&mcp.Implementation{Name:"messaging", Version:"…"}, nil)`; `mcp.AddTool(s, &mcp.Tool{Name:"send", Description:"…"}, sendHandler)` and `mcp.AddTool(s, &mcp.Tool{Name:"read", …}, readHandler)` — the generic `AddTool[In,Out]` derives the JSON schema from the handler's typed `In`/`Out` structs (`send`: `In{Text string}`; `read`: `In struct{}`, `Out` the inbox). Serve with `s.Run(ctx, &mcp.StdioTransport{})`. `read`'s `Out`/result carries the inbox items.
  - never log the token; the subcommand writes only diagnostics (no secrets) to stderr.
- [ ] **Step 5:** run tests green; `GOPROXY=off go build ./...`.
- [ ] **Step 6:** `go tool govulncheck ./cmd/cove-master/...` if available (else note); gofmt + commit (`cove-master: mcp subcommand — stdio read/send forwarding to harbor /messages (COV-145)`).

---

### Task 5: Give `claude -p` the tools (image config + agent wrapper)

**Files:** Create an MCP config in the image (`internal/assemble/hardening/image-files/etc/claude-code/mcp.json`); modify `internal/agentrun/workload.go` (spawn args); modify assemble/its test if it enumerates image files.

**Decision (removes the "does claude -p read managed-settings mcpServers" risk):** deliver the MCP **deterministically via `--mcp-config`** rather than relying on claude auto-loading `managed-settings.json` in headless mode.

- [ ] **Step 1:** bake `etc/claude-code/mcp.json` into the hardening image-files:
  ```json
  { "mcpServers": { "messaging": { "command": "cove-master", "args": ["mcp"] } } }
  ```
- [ ] **Step 2: failing test** — in `internal/agentrun` (fake Spawner, from COV-156), assert the spawned argv now includes `--mcp-config /etc/claude-code/mcp.json` (and `--strict-mcp-config`) in addition to `-p --dangerously-skip-permissions <prompt>`.
- [ ] **Step 3:** in `agentrun`'s command build (COV-156 `workload.go`), add `--mcp-config /etc/claude-code/mcp.json` + `--strict-mcp-config` to the fixed args. (bypassPermissions defaultMode already permits MCP tools; no allowedTools change needed.)
- [ ] **Step 4:** `GOPROXY=off go test ./internal/agentrun/ ./internal/assemble/ -v && GOPROXY=off go build ./...`.
- [ ] **Step 5:** gofmt + commit (`cove: bake messaging MCP config + agentrun --mcp-config so a raised cove has read/send (COV-145)`).

Note: `managed-settings.json` mcpServers is the alternative if the team later confirms headless `claude -p` loads it — but `--mcp-config` is deterministic and ours, so this slice uses it.

---

### Task 6: Docs

**Files:** Create `docs/usage/harbor/messaging.md`; modify `docs/usage/harbor/INDEX.md`, `docs/usage/harbor/coves.md`.

- [ ] **Step 1:** `messaging.md` (leaf, frontmatter matching siblings): what the messaging MCP is (brokered `read`/`send` on the cove's own ticket; tokens stay in harbor; self-scoped — an actor only touches its own ticket); the `cove-master mcp` delivery; that it's enabled when a tracker is configured; the deferred slices (wake-on, escalation, multi-channel).
- [ ] **Step 2:** INDEX row; a one-line note in `coves.md` (a raised cove's agent has `read`/`send` on its ticket).
- [ ] **Step 3:** docs-audit delta (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md` — no new errors referencing the harbor usage docs).
- [ ] **Step 4:** commit (`docs: messaging MCP usage (messaging.md) + INDEX/coves (COV-145)`).

---

## Self-Review

- **Spec coverage:** IssueByIdentifier (T1); /messages handler with auth+self-scope+limits (T2); serve wiring/mount (T3); cove-master mcp stdio server + dep (T4); image MCP config + agentrun --mcp-config (T5); docs (T6). All map.
- **Security (skill-applied):** authz-by-construction (ticket server-derived, no client param — tested in T2); reuse `store.Lookup(HashToken)` fail-closed (T2); no token/body logging + generic errors (T2 asserts token absent from logs); body-size cap + method allow-list (T2); GraphQL vars not concatenation (T1/linear); TLS+proxy + token-from-env-only on the cove side (T4). No IDOR, no injection, no secret leakage.
- **Type consistency:** `Commenter{IssueByIdentifier,PostComment,Comments}` satisfied by `*linear.Client` (T1 adds the missing method; PostComment/Comments already exist); `messagesStore{Lookup,GetInstance}` satisfied by `harbor.Store`; the go-sdk tool signatures per its v1.7.0 API (implementer reads the SDK's `mcp` package).
- **Placeholder scan:** the go-sdk API is now concrete in T4 (`NewServer`/`AddTool[In,Out]`/`Server.Run(&StdioTransport{})`; tests via `NewInMemoryTransports`/`NewClient`, verified against the cached v1.7.0 package). The implementer confirms only the exact client-session method names (`Connect`/`ListTools`/`CallTool`) against the cache — behavior contract fixed by the tests.
- **Risk removed:** MCP delivery is `--mcp-config` (deterministic), not dependent on claude's managed-settings behavior.
