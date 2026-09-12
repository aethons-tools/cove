---
kind: design-spec
subject: COV-142 — inject the harbor connector into teammate + dispatch-worker sessions (chat path shipped in COV-138)
status: draft
date: 2026-09-12
prereq: 2026-09-11-harbor-cove-networking-design.md (COV-138 — the chat-path connector) and 2026-09-12-harbor-cove-autoenroll-design.md (COV-141 — harborPlan mint/revoke)
read-when: implementing or reviewing harbor connector injection for at-cove teammate + dispatch-worker sessions
---

# Harbor connector for teammate + worker sessions (COV-142)

COV-138 routes the **interactive/managed chat** cove's Anthropic + git through harbor
(`connect.Options.Harbor`). Teammate and dispatch-worker sessions use distinct launch
paths that never got the connector, so their agents still use OAuth/Vertex. This wires
the same connector (env + git config, superseding OAuth) into both — reusing
`internal/harbor/snippet` (`Env` + token-free `GitConfig`) so at-cove stays go-oidc-free.

Routability is already handled by COV-138 (`--add-host` at create + the ephemeral
`RunEphemeral` path), so both containers can already *reach* harbor — only the
per-session connector **injection** is missing.

## Scope

**In:**
- **Worker (`internal/dispatchrun`, ephemeral `--rm`) — Anthropic-only via harbor.**
  - `dispatchrun.Options` gains `HarborHost, HarborToken string` (both empty ⇒ off).
    When set, `dispatchrun` injects `snippet.Env(https://Host, Token)` into the **agent
    step's** env and **skips** seeding the OAuth credentials file. Imports only
    `snippet` (stdlib).
  - **Git is deliberately NOT routed through harbor for a worker.** at-task
    `prepare`/`complete` own the worker's git with a per-step minted code-host token;
    a global harbor `insteadOf` would misroute those to harbor (which expects the
    identity token, not the real PAT) and break clone/push. So the worker takes
    `snippet.Env` only — **no `snippet.GitConfig`**. (The agent's own in-session git is
    rare, and push/PR is at-task's job.)
  - `cmd/at-cove` `doWork` resolves harbor via `harborPlan` with the **worker
    container name** as the coveID, sets those fields, and **defers the revoke** around
    `dispatchrun.Run` — so an auto-enrolled worker mints per unit and revokes when the
    `--rm` unit ends (pre-supplied path: nil revoke).
- **Teammate (`connect.LaunchTeammate`, detached) — pre-supplied only.**
  - `TeammateOptions` gains `HarborHost, HarborToken string`. When set, `LaunchTeammate`
    **skips `ensureAuthenticated`**, appends the `snippet.Env` exports to the launch
    script, and runs `snippet.GitConfig` in the VM.
  - `doTeammate` resolves harbor via `harborPlan`, but **requires `harbor.identity`**:
    it errors early when a harbor teammate omits `identity`, because auto-enroll's
    revoke can't attach to a detached conductor. (Teammate auto-enroll is deferred.)

**Out (deferred):**
- **Teammate auto-enroll** (mint/revoke) — needs a teammate teardown/stop hook that
  doesn't exist; pre-supplied `identity` only for teammates this cut.
- Configurable enroll scope (still the COV-141 derived defaults).

## Interfaces

```go
// internal/dispatchrun
type Options struct {
	// … existing …
	HarborHost  string // when non-empty, route the agent step's Anthropic+git through
	HarborToken string // the harbor broker at HarborHost with this identity token
}

// internal/connect (teammate.go)
type TeammateOptions struct {
	// … existing …
	HarborHost  string
	HarborToken string
}
```

Both consume `snippet.Env(baseURL, token) map[string]string` and
`snippet.GitConfig(baseURL) string` (COV-138). `baseURL = "https://" + HarborHost`.

## Behavior details

- **Supersede:** when harbor is set, the path takes the harbor branch **instead of**
  OAuth (worker: skip the creds-file seed; teammate: skip `ensureAuthenticated`). The
  agent then authenticates to Anthropic via `ANTHROPIC_BASE_URL`/x-api-key and git via
  the `insteadOf`/credential-helper — same as chat.
- **Token secrecy:** the token is delivered env-only (the existing tmpfs env-script per
  path); the git config is token-free (helper reads `$AT_HARBOR_IDENTITY_TOKEN` at run
  time); never on argv/logs.
- **Worker id / revoke:** `doWork` passes the worker's unique container name as the
  coveID (auto-enroll `--id`), so concurrent workers don't collide; the deferred revoke
  targets it. Best-effort revoke; the 24h TTL is the crash backstop.

## Error handling

| Situation | Behavior |
|-----------|----------|
| harbor teammate omits `identity` | `doTeammate` errors early ("teammate harbor requires harbor.identity; auto-enroll is unsupported for a detached teammate") |
| harbor worker, `identity` set | pre-supplied path (nil revoke) |
| harbor worker, `identity` absent | auto-enroll: mint at start, revoke after the unit |
| enroll/resolve fails | fail closed before the session/worker launches |

## Testing

Hermetic (`runner.Fake`):
- **dispatchrun:** with `HarborHost`/`HarborToken` set, the agent-step env contains
  `ANTHROPIC_BASE_URL=https://<host>/anthropic` + `ANTHROPIC_API_KEY`/token, the OAuth
  creds-file seed is **skipped**, and **no** harbor git config is applied (at-task's
  git plumbing is untouched); unset → unchanged (creds seeded, no harbor env). Token
  not on argv.
- **doWork:** a harbor kit sets `Options.HarborHost/Token` (via `harborPlan`) and
  defers a revoke; a kit without harbor is unchanged.
- **LaunchTeammate:** with harbor set, `ensureAuthenticated` is skipped and the launch
  script carries the connector env + git config; unset → unchanged.
- **doTeammate:** a harbor teammate without `identity` errors; with `identity`, it
  resolves and passes Host/Token.

Real teammate + worker round-trips through harbor behind `//go:build integration`.

## Manual verification (definition of done)

1. A dispatched worker of a `harbor:` kit runs its agent step with Anthropic + git
   through harbor (auto-enrolled + revoked per unit; OAuth superseded).
2. A teammate of a `harbor:` kit with `identity` set routes Anthropic + git through
   harbor (OAuth superseded).
3. Kits without `harbor:` — worker + teammate unchanged.
