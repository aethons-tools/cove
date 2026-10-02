# Jam destination-declared client env — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Destinations declare the env a studio needs; every client path gets its env from the actor's connector instead of the hard-coded snippet.

**Architecture:** `snippet.Connector{Env, GitRoute}` (stdlib-only) is the client contract, with `Expand`/`Render`/`GitConfig` and a `Legacy(subscription)` constructor reproducing today's contract. `jam.Destination` gains `Env`/`Git`; `jam.ConnectorFor(store, actor)` unions the actor's in-scope destinations (legacy defaults for env-less `/anthropic/` and `/git/` routes). Delivery: supervisor → launcher → cove-master; admin enroll response; `GET /connector` for host-side clients with 404 → legacy fallback.

**Tech Stack:** Go stdlib; `internal/jam`, `internal/jam/snippet`, `internal/jam/launcher`, `internal/connect`, `internal/dispatchrun`, `cmd/at-jam`, `cmd/at-cove`.

**Spec:** `docs/superpowers/specs/2026-10-02-jam-destination-env.md`

## Global Constraints

- The identity token never appears in a stored template, a log, argv, or a rendered snippet's literal text (shell renders reference `$AT_JAM_IDENTITY_TOKEN`; in-memory `Expand` substitutes the value).
- Existing Jams with no destination `env` produce exactly today's studio env (legacy defaults).
- Fail closed: env-var conflicts or two git routes in one actor's connector → error (raise rolled back / 409 from `/connector`).
- Tests hermetic; TDD; docs in the same commit; commit + push after each task.

## Review Focus

1. Legacy Jam (anthropic `x-api-key`, git `/git/`, no `env`) → connector env equals today's `snippet.Env` keys/values — Task 2.
2. Pool Jam (anthropic identity-in `bearer`, no `env`) → `ANTHROPIC_AUTH_TOKEN`, no `ANTHROPIC_API_KEY` — Task 2.
3. Two in-scope destinations setting the same var differently → error; same value → fine — Task 2.
4. `/connector` with unknown/expired identity → 401; with a valid one → the actor's connector only (not other roles') — Task 3.
5. Host-side client against an older Jam (404) still gets the legacy contract — Task 6.

---

### Task 1: `snippet.Connector`

**Files:** Create `internal/jam/snippet/connector.go`, `internal/jam/snippet/connector_test.go`. Modify `snippet.go` so `Env`/`Render`/`RenderSubscription`/`GitConfig` are thin wrappers over `Legacy(...)` (existing tests keep passing).

**Produces:**
```go
type Connector struct {
	Env      map[string]string `json:"env,omitempty"`       // values may use {base}, {host}, {token}
	GitRoute string            `json:"git_route,omitempty"` // e.g. "/git/"; "" = no git routing
}
func Legacy(subscription bool) Connector
func (c Connector) Expand(baseURL, token string) map[string]string // + AT_JAM_IDENTITY_TOKEN, AT_HARBOR_IDENTITY_TOKEN
func (c Connector) Render(baseURL, token string) string          // export token once; {token} → $AT_JAM_IDENTITY_TOKEN; + GitConfig
func (c Connector) GitConfig(baseURL string) string               // "" when GitRoute == ""
var ErrNoConnectorEndpoint = errors.New("jam has no /connector endpoint")
func Fetch(hc *http.Client, baseURL, token string) (Connector, error) // GET {base}/connector, Authorization: Bearer; 404 → ErrNoConnectorEndpoint
```

- [ ] Tests: `Legacy(false).Expand` equals `Env(base, tok)` (map equality); `Legacy(true).Expand` has `ANTHROPIC_AUTH_TOKEN`, no `ANTHROPIC_API_KEY`; `Render` of a connector with `GH_ENTERPRISE_TOKEN={token}`, `GH_HOST={host}` contains `export GH_ENTERPRISE_TOKEN="$AT_JAM_IDENTITY_TOKEN"` and `GH_HOST=jam.example` and contains the raw token exactly once (the `AT_JAM_IDENTITY_TOKEN` export); keys render sorted; `GitConfig` uses `GitRoute` (`url."https://jam.example/git/".insteadOf`) and is empty when unset; `Fetch` against httptest: 200 → decoded connector and bearer header seen; 404 → `ErrNoConnectorEndpoint`; 500 → other error.
- [ ] RED → implement → GREEN; `go test ./internal/jam/snippet/...`.
- [ ] Commit `feat(snippet): Connector — the client connector contract` + push.

### Task 2: Destination `Env`/`Git` + `jam.ConnectorFor`

**Files:** Modify `internal/jam/policy.go` (fields, `ClientEnv`, `GitRouted`, `ValidateEnv`); create `internal/jam/connector.go` (`ConnectorFor`), `internal/jam/connector_test.go`; modify `admin.go` + `adminui/writes.go` destination add to call `ValidateEnv`; `cmd/at-jam/main.go` `destination add --env K=V` (repeatable) `--git`, list prints `env=K1,K2 git`.

**Produces:**
```go
// Destination fields
Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
Git bool              `json:"git,omitempty" yaml:"git,omitempty"`
func (d Destination) ClientEnv() map[string]string // {url} resolved to {base}+route (no trailing /); legacy defaults when Env == nil
func (d Destination) GitRouted() bool               // d.Git || (d.Env == nil && d.Route == "/git/")
func (d Destination) ValidateEnv() error            // keys ^[A-Z_][A-Z0-9_]*$, not AT_JAM_*/AT_HARBOR_*; placeholders only {url},{base},{host},{token}
func ConnectorFor(store Store, a Actor) (snippet.Connector, error)
```

- [ ] Tests (`connector_test.go`): legacy anthropic x-api-key + git → `Env{"ANTHROPIC_BASE_URL":"{base}/anthropic","ANTHROPIC_API_KEY":"{token}"}`, `GitRoute:"/git/"`; anthropic bearer → `ANTHROPIC_AUTH_TOKEN`; gh destination with explicit env → `GH_HOST={host}`; destination outside scope contributes nothing; conflicting values across two destinations → error; equal values → ok; two git routes → error; actor with two grants unions both scopes; unknown destination name in scope skipped. `ValidateEnv` rejects `lower`, `AT_JAM_X`, `{nope}`. Admin API destination add with bad env → 400. CLI `destination add --env GH_HOST={host} --git` round-trips.
- [ ] RED → implement → GREEN; `go test ./internal/jam/... ./cmd/at-jam/...`.
- [ ] Docs: new leaf `docs/usage/jam/connector.md` (destination env/git, templates, legacy defaults, conflicts; move the `gh` section here from `serve.md`, leave a link); row in `docs/usage/jam/INDEX.md`; `serve.md` destinations flags `--env`/`--git` → link.
- [ ] Commit `feat(jam): destination-declared client env + ConnectorFor` + push.

### Task 3: `GET /connector` on the broker listener

**Files:** Create `internal/jam/connector_handler.go` (+ test); modify `cmd/at-jam/mux.go` `coveHTTPHandler` to always route `/connector` to it ahead of the broker.

**Produces:** `func NewConnectorHandler(store Store, now func() time.Time, log *slog.Logger) http.Handler` — `GET` only; identity from `Authorization: Bearer|token`; unknown → 401; expired → 401; `ConnectorFor` error → 409 (`connector conflict`); 200 JSON `snippet.Connector`. Logs actor id only.

- [ ] Tests: 401 no header / unknown / expired; 200 body for a role with anthropic+git equals legacy connector; another actor's role's destinations absent; conflict → 409; `coveHTTPHandler` with nil intercom still serves `/connector` (mux test in `cmd/at-jam`).
- [ ] RED → GREEN; docs in `connector.md`; commit `feat(jam): GET /connector` + push.

### Task 4: Jam-raised studios use the connector

**Files:** `internal/jam/supervisor.go` (`RaiseSpec.Connector *snippet.Connector`; after `Enroll`, `Lookup(HashToken(tok))` → `ConnectorFor`; error → revoke + return), `internal/jam/launcher/launcher.go` (pass `spec.Connector`), `internal/connect/covemaster.go` (`CoveMasterOptions.Connector *snippet.Connector`; when non-nil `script.WriteString(o.Connector.Render(base, tok))`, else today's branch).

- [ ] Tests: supervisor raise passes a connector reflecting the role (fake launcher captures spec); conflict rolls back the actor; covemaster with a connector carrying `GH_HOST` writes `export GH_HOST=` into the staged env and no raw token beyond the single export.
- [ ] RED → GREEN; commit `feat(jam): raised studios get the role's connector env` + push.

### Task 5: `at-jam enroll` renders the connector

**Files:** `internal/jam/admin.go` (`EnrollResult.Connector *snippet.Connector`, filled via `ConnectorFor`), `internal/jam/adminclient` (result carries it), `cmd/at-jam/main.go` (render `res.Connector.Render(baseURL, token)`; nil → `jam.RenderEnrollSnippet`; `--json` adds `connector`), `internal/jam/enroll.go` doc.

- [ ] Tests: enroll API response includes connector; CLI snippet for a role with a gh destination includes `GH_HOST`; nil connector (old server stub) → legacy snippet.
- [ ] RED → GREEN; docs (`roster.md` enrollment note → link `connector.md`); commit + push.

### Task 6: Host-side clients fetch the connector

**Files:** `cmd/at-cove/main.go` (helper `jamConnector(host, token) snippet.Connector`: `snippet.Fetch` with a 10s client; `ErrNoConnectorEndpoint` → `Legacy(false)`; other error → returned), `internal/connect/connect.go` (`JamAuth.Connector`; env via `Connector.Expand`, git via `Connector.GitConfig` — skipped when empty), `internal/connect/teammate.go` + `internal/dispatchrun/dispatchrun.go` (options gain `JamConnector snippet.Connector`; dispatch uses `Expand` only).

- [ ] Tests: connect with a connector lacking git runs no git-config ssh; teammate env script contains connector vars; dispatch agent env contains connector vars; at-cove helper: httptest 404 → legacy, 200 → served connector.
- [ ] RED → GREEN; docs (`docs/usage/at-cove-config.md` jam section → link `connector.md`); commit + push.

### Task 7: Docs audit + whole-branch check

- [ ] docs-audit (`--index OVERVIEW.md`) diff vs main clean apart from superpowers convention; `just test && just lint`; push.
