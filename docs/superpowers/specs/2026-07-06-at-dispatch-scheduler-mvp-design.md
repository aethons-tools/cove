# at-dispatch — scheduler MVP — Design

**Date:** 2026-07-06
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binary `at-dispatch`)
**Tracks:** [AET-25](https://linear.app/aethons-tools/issue/AET-25) (child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic)
**Builds on:** [at-dispatch configuration](2026-07-06-at-dispatch-config-design.md) (the config surface + `DISPATCH_*`/`result.json` contract this consumes).
**Related:** [orchestration design](../../orchestration/INDEX.md).

## 1. Purpose

Design the **scheduler MVP**: the loop inside `at-dispatch serve` that watches one
Linear team, claims ready autonomous work, runs each issue's configured **dispatch
command**, and brokers the result back to Linear as the single writer of tracker
state. This turns the config layer into a running, deployable service.

## 2. Dependency reframing

Because the config's **command-seam** model makes at-dispatch at-cove-agnostic (a
class maps to a command; the command is the only seam), AET-25's originally-listed
blockers no longer bind:

- **AET-21** (at-cove `run`/lifecycle) — *not required*. The scheduler runs a
  configured command; whether it wraps at-cove is the command's business. Tests and
  early use can point a class at any script.
- **AET-23** (webhook receiver) — *not required* for a **poll-only** MVP.
- **New real dependency:** a direct **Linear GraphQL client** (a 24/7 service holds
  the token and cannot use interactive MCP), plus network egress to `api.linear.app`
  where the service runs.

(The AET-25 `blockedBy` edges on AET-21/AET-23 should be dropped in Linear.)

## 3. Governing decisions (from brainstorming)

- **Deployable service**, not just a library: engine + a real Linear client + an exec
  executor, wired into `serve`. Engine logic is hermetic; the Linear client's live
  calls sit behind an `integration` build tag (same pattern as at-cove's real-ssh
  tests).
- **Poll-only** triggering (interval from `tracker.poll-interval`).
- **Autonomous classes only.** Interactive classes are skipped (assignment deferred).
- **Include auto BLOCKED→READY** (the dependency graph runs itself).
- **In-process, semaphore-bounded concurrency** (global `concurrency` + per-class
  caps) — **no durable queue**.
- **No retry:** any command failure / `error` result / timeout routes straight to
  NEEDS INPUT with a comment.
- **Plan/execution split:** the engine is driven by two interfaces (`Tracker`,
  `Executor`), both faked in hermetic tests; real adapters are thin.

**Non-goals (deferred):** durable queue; webhook (AET-23); retry policy + stale-claim
reaper (AET-27); interactive-class assignment; multi-repo; any at-cove-specific
knowledge.

## 4. Architecture & package layout

```
internal/dispatch/scheduler/   engine (Run/tick/handle), Tracker & Executor interfaces, Issue/Comment/Role types, fakes
internal/dispatch/linear/      real Tracker: Linear GraphQL client (live calls behind `integration` tag)
internal/dispatch/exec/        real Executor: exec.CommandContext with injected env + timeout
cmd/at-dispatch/               serve builds the real adapters from config and runs the engine
```

The existing `internal/runner.Runner` is **not** reused — it streams a TTY and takes
no environment; the scheduler needs headless execution with an injected env and a
timeout.

## 5. Interfaces and types

```go
type Role int
const ( RoleReady Role = iota; RoleInProgress; RoleInReview; RoleNeedsInput; RoleBlocked; RoleDone )

type Issue struct {
    ID          string // Linear internal id (for API calls)
    Identifier  string // e.g. "AET-42" (DISPATCH_ISSUE, brief heading)
    Title       string
    Description string
    Class       string // parsed from the class-label-prefix label; "" if none
}
type Comment struct { Author, Body string }

// Tracker is every Linear operation the engine needs. The real impl owns the
// Role→configured-state-name→Linear-state-id mapping and the team scoping.
type Tracker interface {
    ListReady(ctx context.Context) ([]Issue, error)        // issues in the READY-role state
    ListUnblockable(ctx context.Context) ([]Issue, error)  // BLOCKED issues whose blockers are all DONE
    Comments(ctx context.Context, issueID string) ([]Comment, error)
    Transition(ctx context.Context, issueID string, role Role) error
    PostComment(ctx context.Context, issueID, body string) error
}

// Executor runs a dispatch command headlessly with a given environment; ctx carries
// the per-task timeout. Returns nil on exit 0, else an error (including on timeout).
type Executor interface {
    Run(ctx context.Context, argv []string, env []string) error
}
```

## 6. The engine

```go
type Engine struct { /* cfg config.Config; tracker Tracker; exec Executor; resolve func([]string)(string,error); log *log.Logger */ }
func New(cfg config.Config, t Tracker, e Executor, resolve func([]string) (string, error), log *log.Logger) *Engine
func (e *Engine) Run(ctx context.Context) error   // poll loop until ctx is done; drains in-flight on shutdown
func (e *Engine) tick(ctx context.Context)        // one poll iteration (unit-tested)
func (e *Engine) handle(ctx context.Context, iss Issue) // one issue: claim → dispatch → broker (unit-tested, synchronous)
```

**`tick`** (every `poll-interval`):
1. **Reconcile:** `ListUnblockable` → `Transition(id, RoleReady)` for each.
2. **Claim + dispatch:** `ListReady` → for each issue whose `Class` is a config
   `autonomous` class **and** a slot is free (global + per-class semaphore,
   non-blocking acquire; skip if full), launch `handle` in a goroutine (slot released
   on completion). Interactive/unknown-class issues are skipped.

**`handle`** (synchronous, the unit under test):
1. Claim: `Transition(id, RoleInProgress)`.
2. Assemble the **markdown brief** (title, description, class, repo slug, `Comments`)
   → temp file; pick a temp `result.json` path.
3. Resolve per-command secrets fresh (`config.ResolveSecrets(cfg.Secrets, e.resolve)`).
4. `env := config.BuildEnv(config.Task{Issue: iss.Identifier, Class: iss.Class,
   Repo: cfg.Repo.Slug, Timeout: <class.Timeout>, BriefPath, ResultPath}, secrets)`.
5. `ctx2, cancel := context.WithTimeout(ctx, <class.Timeout parsed>)`; run
   `e.exec.Run(ctx2, class.Command, env)`.
6. `res := config.ReadResult(resultPath)`; broker (§7); clean up temp files.

Concurrency is enforced by buffered-channel semaphores sized from `cfg.Concurrency`
and each class's `Concurrency` (0 = only the global cap applies). `handle` is
synchronous and directly unit-testable; a fake `Executor` that returns promptly makes
`tick`'s launched goroutines deterministic (`Run`/tests drain via an internal
`sync.WaitGroup`).

## 7. Result brokering (single writer)

| Outcome | Tracker writes |
|---|---|
| `res.Status == ok` | `PostComment` artifacts (`prUrl`, `docPath`, branch), then `Transition(RoleInReview)` |
| `res.Status == needs_input` | `PostComment` the `❓ NEEDS INPUT` block formatted from `res.NeedsInput` (Doing/Blocker/Need/Tried/Safe state), then `Transition(RoleNeedsInput)` |
| `res.Status == error`, `Executor` error, timeout, or missing/invalid result | `PostComment` the diagnostic, then `Transition(RoleNeedsInput)` |

The engine performs **all** tracker writes; the command does none. (`ReadResult`
already collapses absent/unparseable/unknown-status into `error`.)

## 8. The real Linear client (`internal/dispatch/linear`)

A small GraphQL client over `net/http`, constructed from config (team key, resolved
token, the `states` role→name map). At construction it queries the team's workflow
states once to build a **name→state-id** map, so `Transition(role)` resolves
role→configured-name→id. Operations:

- `ListReady` / `ListUnblockable` — `issues(filter:…)`; `ListUnblockable` filters
  BLOCKED issues and inspects each blocker relation's state *type* (`completed`).
- `Comments` — the issue's comment connection.
- `Transition` — `issueUpdate(input:{stateId})`.
- `PostComment` — `commentCreate(input:{issueId, body})`.

**Testing:** hermetic unit tests inject an `http.Client` with a fake `RoundTripper`
returning recorded JSON, asserting request shape and response decoding. An
`integration`-tagged smoke test hits real `api.linear.app` (token + scratch team via
env), never run by default `go test ./...`.

## 9. `serve` wiring

`at-dispatch serve --config <path>` gains a runtime path: after `LoadConfig` +
validate (still fail-closed — a bad config prints a `config:` error and exits 1), it
resolves `tracker.token` (fail-closed too), constructs `linear.New(cfg, token)` and
`exec.New()`, builds a host secret resolver (run argv → stdout, in memory; reusing
`internal/secret`'s mechanism), prints the startup banner (the current
repo/classes summary), and then calls `engine.Run(ctx)` with a `ctx` cancelled on
SIGINT/SIGTERM for graceful drain. `serve` now **blocks** running the loop instead of
exiting after validation; no separate flag is added.

## 10. Error posture

- Transient Linear/network errors in `tick` are logged and the loop continues — one
  bad issue or a dropped poll never crashes the service (the next poll retries).
- A failed claim (`Transition→IN PROGRESS` errors) logs and skips the issue.
- Each `handle` runs in isolation; a panic in one dispatch is recovered and logged as
  an `error` broker (the issue goes to NEEDS INPUT).

## 11. Testing

- **Engine (hermetic, the bulk):** a fake `Tracker` (in-memory issues; records
  transitions + comments) and a fake `Executor` (writes a canned `result.json`, or
  returns an error/timeout) drive `handle`/`tick` tests asserting the exact
  transitions and comment bodies for **ok / needs_input / error / timeout / unblock /
  interactive-skip / concurrency-cap** paths.
- **Linear client:** recorded-JSON unit tests for query/response; `integration`-tagged
  live smoke test.
- **`exec` executor:** a unit test running a trivial real command (e.g. a shell that
  writes a file from `$DISPATCH_RESULT`) to prove env injection + timeout, kept
  hermetic (no network).

## 12. Open questions

- **Where the service runs** (host process vs. inside a sandbox) — a deployment
  choice affecting how `api.linear.app` egress is granted; out of scope here.
- **DONE vs IN REVIEW for review-less issues.** The MVP always routes `ok` to IN
  REVIEW. Auto-closing issues that have no `review` stage is a follow-up once the
  subissue/stage model is wired.
