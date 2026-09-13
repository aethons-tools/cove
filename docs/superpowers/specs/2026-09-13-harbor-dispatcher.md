# harbor: resident dispatcher — poll tracker, raise a cove per ready ticket (COV-146)

**Status:** design approved, pre-plan
**Issue:** COV-146 (slice 7 — the capstone of the cove-management arc)
**Foundation:** COV-158 (real Launcher — raise→run→teardown fully wired), COV-149 (supervisor + Instance registry). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md`.

## Summary

An always-on intake inside `at-harbor serve` that turns ready tracker tickets into managed-cove raises. The supervisor + Launcher already own everything downstream (raise → run → report → teardown), so this slice is purely the **producer**: poll the tracker → dedup → claim → build a prompt → `Supervisor.Raise`. The cove does the work and the supervisor tears it down.

**Model (decided in brainstorming):**
- **Poll, not webhook** — reuse `internal/dispatch/scheduler.Tracker` + the `linear.Client`. Webhook stays future work (`AT_DISPATCH_WEBHOOK_SECRET` is reserved-unused).
- **Elastic raise under a cap** — coves are one-shot ephemeral, so there is no idle-actor pool: each ready ticket → a fresh raise, bounded by a required `max-concurrent`. The tracker's READY column is the durable queue; the dispatcher is the bounded consumer.
- **Single-instance** — the tracker-transition claim + the Instance-registry dedup. Multi-instance ticket-leasing deferred (like the FileStore single-writer / COV-150).

## Package & boundary

- **`internal/dispatcher`** (new; harbor-resident) — the poll loop. Imports `internal/dispatch/scheduler` (Tracker/Issue/Role/AssembleBrief), the supervisor + store via **narrow interfaces** (`Raiser`, `Registry`) that reference `harbor` types, and stdlib. Wired from `cmd/at-harbor`. **Not** imported by `internal/harbor` core (mirrors `internal/harbor/launcher`).
- Export **`scheduler.AssembleBrief`** (currently unexported `assembleBrief(iss Issue, comments []Comment) string`; rename + update its in-package caller `engine.go`).
- **Boundary gates:** `internal/harbor` core stays kit/backend/connect/grpc-free (the dispatcher, like the launcher, is a sibling wired from `cmd/at-harbor`, which already imports `internal/kit`). `cmd/at-cove` stays oidc/grpc-free (untouched).

## The seams

```go
package dispatcher

// Raiser is the supervisor's raise entrypoint (satisfied by *harbor.Supervisor).
type Raiser interface {
    Raise(ctx context.Context, spec harbor.RaiseSpec) (harbor.Instance, string, string, error)
}

// Registry reads the durable Instance registry (satisfied by harbor.Store / *harbor.FileStore).
type Registry interface {
    GetInstance(actorID string) (harbor.Instance, bool)
    ListInstances() []harbor.Instance
}

// Tracker is the scheduler.Tracker subset the dispatcher needs.
type Tracker interface {
    ListReady(ctx context.Context) ([]scheduler.Issue, error)
    Comments(ctx context.Context, issueID string) ([]scheduler.Comment, error)
    Transition(ctx context.Context, issueID string, role scheduler.Role) error
}

type Config struct {
    Role         string        // the role raised coves get (must grant anthropic + git)
    Project      string        // optional
    MaxConcurrent int          // required, > 0 — max live Instances the dispatcher maintains
    PollInterval time.Duration // default from the linear config / a floor
}

type Dispatcher struct { /* tracker, raiser, registry, cfg, log */ }
func New(t Tracker, r Raiser, reg Registry, cfg Config, log *slog.Logger) *Dispatcher
func (d *Dispatcher) Run(ctx context.Context)     // immediate tick + ticker, mirrors Supervisor.Run
func (d *Dispatcher) tick(ctx context.Context)    // one pass — the unit tests drive this
```

`*harbor.Supervisor` satisfies `Raiser`; `harbor.Store` (the `*FileStore`) satisfies `Registry`; `*linear.Client` satisfies `Tracker` (a subset of `scheduler.Tracker`).

## The loop (`tick`)

```
issues, err := tracker.ListReady(ctx)            // the READY queue
live := countLive(registry.ListInstances())      // Instances not gone (raising/live/terminating/lost)
for _, iss := range issues:
    if !dispatchable(iss) { continue }            // (this slice: dispatch all ready issues; class-filter is a later refinement)
    actorID := "cove-" + iss.Identifier
    if _, ok := registry.GetInstance(actorID); ok { continue }   // dedup — already raised
    if live >= cfg.MaxConcurrent { break }        // backpressure — cap reached, wait for a slot next tick
    if err := tracker.Transition(ctx, iss.ID, scheduler.RoleInProgress); err != nil {
        log.Warn("claim failed", ...); continue   // couldn't claim → leave READY, retry next tick
    }
    prompt, err := buildPrompt(ctx, tracker, iss) // AssembleBrief(iss, Comments) + resultProtocol
    if err != nil { log + Transition(NeedsInput); continue }
    if _, _, _, err := raiser.Raise(ctx, harbor.RaiseSpec{
        ActorID: actorID, Role: cfg.Role, Project: cfg.Project, Unit: iss.Identifier, Prompt: prompt,
    }); err != nil {
        log.Warn("raise failed", ...); _ = tracker.Transition(ctx, iss.ID, scheduler.RoleNeedsInput); continue
    }
    live++                                         // count the just-raised cove toward the cap
```

- **Dedup is doubly covered:** the READY→IN PROGRESS transition removes the issue from the next `ListReady`; the `GetInstance` check guards the window and is the horizontal-safety hook.
- **The cap is registry-derived**, so it holds across a harbor restart (in-flight is re-counted from the durable Instance registry, never an in-memory counter) — serving the "in-progress survives restart" driver.
- **Claim-then-act:** the transition precedes the raise, so a crash between claim and raise leaves the issue IN PROGRESS (recoverable by the tracker reaper / a later outcome slice), never double-raised.
- **`countLive`:** counts Instances whose Phase is not `gone` (gone instances are already removed from the store, so in practice `len(ListInstances())` — but filter defensively on Phase != PhaseGone).

## Prompt building

The managed cove receives the brief **inline** as its prompt (no `.at-task/task.json` is dropped — that's the dispatch-worker flow). So the dispatcher builds:

```
buildPrompt = AssembleBrief(iss, comments) + "\n\n" + resultProtocol
```

where `resultProtocol` is a **dispatcher-local** const adapted from `dispatchrun`'s (no "read task.json" line — the task is inline; no post-exit PR-handling line — output-handling is deferred). It still instructs the agent to write `.at-task/worker-result.json` as exactly one of `ok` / `needs-input` / `error` (the schema `internal/dispatch/worker.WorkerResult`, which the cove's agent wrapper reads via `worker.ReadWorkerResult`), so the cove reports its lifecycle correctly (needs-input → a brief Waiting; ok/error → Done). Example:

```
---
Your task is described above. Do the work in this repository: make the changes and run the project's tests.

When finished, write your result to .at-task/worker-result.json as EXACTLY ONE of:
  {"status":{"ok":{}}}
  {"status":{"needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}}
  {"status":{"error":{"message":"<what went wrong>"}}}
Use ok only if the change is complete and tests pass.
```

## Config (`runtime.dispatcher` serve-config, gated like `runtime.launcher`)

```yaml
runtime:
  dispatcher:
    role: worker              # required — raised coves' role (must grant anthropic + git)
    project: acme             # optional
    max-concurrent: 5         # required, > 0 — the backpressure cap
    poll-interval: 30s        # optional; defaults to the linear block's poll-interval or a floor
    tracker-token:            # credSpec resolved at serve startup (harbor's own secret to call the tracker)
      command: ["op", "read", "op://harbor/linear/token"]
    linear:                   # a kit.LinearTracker (team, states, class-label-prefix, poll-interval)
      team: AET
      class-label-prefix: "class:"
      states: { ready: "Ready", in-progress: "In Progress", in-review: "In Review", done: "Done", needs-input: "Needs Input", blocked: "Blocked" }
```

- Unset `runtime.dispatcher` ⇒ no intake (unchanged serve). Present ⇒ `role`, `max-concurrent` (>0), and `linear` are required (clear validation errors).
- **`linear.New` takes a full `kit.Config`**, so the wiring wraps the config's `LinearTracker` in a `kit.Config{Tracker: &kit.Tracker{Linear: &lt}}` shell to construct the client. (The dispatcher package itself only touches the `Tracker` interface.)
- **Tracker token:** `tracker-token` is a `credSpec` (the same `{command}`/`{value}` shape harbor already resolves for broker `credentials`), resolved once at serve startup and passed to `linear.New`. It is harbor's own secret (calls the tracker API); it is never injected into a cove and never logged.

## Wiring (`cmd/at-harbor serve`)

When `cfg.Runtime.Dispatcher != nil`: resolve the tracker token, build `linear.New(kitConfigShell, token, nil)`, build `dispatcher.New(tracker, sup, store, dispatcher.Config{...})`, and `go disp.Run(ctx)` alongside `go sup.Run(...)`. The dispatcher uses the same `sup`/`store` the rest of serve uses, so raised coves flow through the configured launcher (real when `runtime.launcher` is set, placeholder otherwise — a dispatcher against a placeholder launcher is a useful intake-only test).

## Tests (hermetic)

`internal/dispatcher`, driving `tick` (not the live ticker) with fakes:
- **fakeTracker** (canned `ListReady`, records `Transition` calls, canned `Comments`), **fakeRaiser** (records `RaiseSpec`s, optional error), **fakeRegistry** (canned Instances).
- ready issue → `Transition(InProgress)` **then** `Raise` with `ActorID="cove-<id>"`, `Role`, `Unit=<id>`, `Prompt` containing the brief + result-protocol.
- **dedup:** an issue whose `actorID` already has an Instance → no Transition, no Raise.
- **cap:** with `MaxConcurrent=2` and 2 live Instances → no Raise this tick; with 1 live and 3 ready → exactly 1 Raise (cap reached mid-pass).
- **claim failure:** `Transition` errors → no Raise, continue.
- **raise failure:** `Raise` errors → `Transition(NeedsInput)` called.
- **prompt:** asserts `AssembleBrief` output + the result-protocol are both present.
- **config** (`cmd/at-harbor`): parse `runtime.dispatcher`; validation errors when role / max-concurrent(>0) / linear missing.

No live-ticker or real-network test is required (the Linear client is exercised by the existing dispatch tests; an `integration`-tagged real-tracker round-trip is out of scope this slice).

## Docs

- New `docs/usage/harbor/dispatcher.md` (owned leaf): the resident dispatcher — what it does (poll → claim → raise), the `runtime.dispatcher` config, the elastic-cap model, and the deferred pieces. Add its INDEX row.
- `docs/usage/harbor/serve.md`: a `runtime.dispatcher` row + pointer to `dispatcher.md`.
- `docs/usage/harbor/coves.md`: a one-line note that raises can come from the dispatcher (not only `cove raise`).

## Deferred (follow-ups)

- **Outcome → tracker writeback** (issue → Done/NeedsInput/InReview + a result comment on cove completion) — needs outcome propagation across cove-master/agent-wrapper → Attach → supervisor (COV-156/158 deferred it). **The next slice.**
- **Webhook intake** (poll-only here).
- **Multi-instance ticket-leasing / leader election** (single-instance now).
- Per-role/per-class caps (global cap this slice); GitHub-issues tracker (works via the same `Tracker` interface, not the focus); the class-based dispatchability filter (`ResolvedWorker`) — this slice dispatches all ready issues.
