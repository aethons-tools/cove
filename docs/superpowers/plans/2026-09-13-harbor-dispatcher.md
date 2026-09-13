# harbor Resident Dispatcher (poll → raise) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An always-on poll loop inside `at-harbor serve` that turns ready tracker tickets into managed-cove raises, bounded by a registry-derived concurrency cap.

**Architecture:** New `internal/dispatcher` package (sibling of `internal/harbor/launcher`, wired from `cmd/at-harbor`, not harbor core). It reuses `scheduler.Tracker`/`linear.Client`/`AssembleBrief`, dedups + caps against the durable Instance registry, claims via a tracker transition, and calls `Supervisor.Raise` with the issue brief as the prompt. Config gated behind `runtime.dispatcher`.

**Tech Stack:** Go 1.25; existing `internal/dispatch/{scheduler,linear}`, `internal/harbor`, `internal/kit`, `internal/secret`, `internal/runner`.

## Global Constraints

- **Module commands offline:** run go commands with **`GOPROXY=off`** (live proxy blocked, deps cached). No new external deps.
- **Boundaries:** `internal/dispatcher` is wired from `cmd/at-harbor`, never imported by `internal/harbor` core. `internal/harbor` core stays kit/backend/connect/grpc-free. `cmd/at-cove` stays oidc/grpc-free (untouched — verify `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` empty).
- **Elastic-raise-under-a-cap:** in-flight is counted from the **durable Instance registry** each pass (never an in-memory counter), so the cap survives restart. `max-concurrent` is **required, > 0**.
- **Claim-then-act:** `Transition(IN PROGRESS)` precedes `Raise`; a crash between them leaves the issue IN PROGRESS (never double-raised).
- **Secrets:** the tracker token is harbor's own secret (calls the tracker API); resolve it once at serve startup via `secret.Resolve`, never inject it into a cove, never log it.
- TDD; hermetic tests (fakes; drive `tick`, not the live ticker); gofmt-clean (CI lint gate).
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

---

### Task 1: Export `scheduler.AssembleBrief`

**Files:**
- Modify: `internal/dispatch/scheduler/brief.go` (rename `assembleBrief` → `AssembleBrief`), and its caller in `internal/dispatch/scheduler/engine.go`.

**Interfaces:**
- Produces: `scheduler.AssembleBrief(iss Issue, comments []Comment) string`. Task 2 consumes it.

- [ ] **Step 1: Rename the function**

In `internal/dispatch/scheduler/brief.go`, rename `func assembleBrief(` → `func AssembleBrief(` and update its doc comment (it's now exported — "AssembleBrief renders …"). Update the call site in `engine.go` (grep `assembleBrief` — there should be exactly one caller) to `AssembleBrief`.

- [ ] **Step 2: Build + tests**

Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./internal/dispatch/... -v`
Expected: builds; all scheduler tests pass (a `brief` test, if present, may reference the old name — update it to `AssembleBrief` too).

- [ ] **Step 3: gofmt + commit**

```bash
gofmt -w internal/dispatch/scheduler/
git add internal/dispatch/scheduler/
git commit -m "scheduler: export AssembleBrief for reuse by the resident dispatcher (COV-146)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 2: `internal/dispatcher` — the poll loop

**Files:**
- Create: `internal/dispatcher/dispatcher.go`
- Test: `internal/dispatcher/dispatcher_test.go`

**Interfaces:**
- Consumes: `scheduler.{Issue,Comment,Role,RoleInProgress,RoleNeedsInput,AssembleBrief}`; `harbor.{RaiseSpec,Instance,Phase,PhaseGone}`.
- Produces: `Raiser`, `Registry`, `Tracker` interfaces; `Config`; `New`; `(*Dispatcher).Run`; `(*Dispatcher).tick`.

- [ ] **Step 1: Write the failing test** — `internal/dispatcher/dispatcher_test.go`

```go
package dispatcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/harbor"
)

type fakeTracker struct {
	ready       []scheduler.Issue
	comments    []scheduler.Comment
	transitions []struct {
		id   string
		role scheduler.Role
	}
	transitionErr error
}

func (f *fakeTracker) ListReady(ctx context.Context) ([]scheduler.Issue, error) {
	return f.ready, nil
}
func (f *fakeTracker) Comments(ctx context.Context, id string) ([]scheduler.Comment, error) {
	return f.comments, nil
}
func (f *fakeTracker) Transition(ctx context.Context, id string, role scheduler.Role) error {
	f.transitions = append(f.transitions, struct {
		id   string
		role scheduler.Role
	}{id, role})
	return f.transitionErr
}

type fakeRaiser struct {
	specs []harbor.RaiseSpec
	err   error
}

func (f *fakeRaiser) Raise(ctx context.Context, spec harbor.RaiseSpec) (harbor.Instance, string, string, error) {
	f.specs = append(f.specs, spec)
	if f.err != nil {
		return harbor.Instance{}, "", "", f.err
	}
	return harbor.Instance{ActorID: spec.ActorID}, "tok", "sec", nil
}

type fakeRegistry struct{ insts []harbor.Instance }

func (f *fakeRegistry) GetInstance(actorID string) (harbor.Instance, bool) {
	for _, i := range f.insts {
		if i.ActorID == actorID {
			return i, true
		}
	}
	return harbor.Instance{}, false
}
func (f *fakeRegistry) ListInstances() []harbor.Instance { return f.insts }

func newTestDispatcher(t *fakeTracker, r *fakeRaiser, reg *fakeRegistry, max int) *Dispatcher {
	return New(t, r, reg, Config{Role: "worker", Project: "acme", MaxConcurrent: max}, nil)
}

func TestTickClaimsAndRaises(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1", Title: "do a thing", Description: "the desc"}}}
	r := &fakeRaiser{}
	d := newTestDispatcher(tr, r, &fakeRegistry{}, 5)
	d.tick(context.Background())

	if len(tr.transitions) != 1 || tr.transitions[0].id != "id1" || tr.transitions[0].role != scheduler.RoleInProgress {
		t.Fatalf("want one InProgress transition on id1, got %+v", tr.transitions)
	}
	if len(r.specs) != 1 {
		t.Fatalf("want 1 raise, got %d", len(r.specs))
	}
	s := r.specs[0]
	if s.ActorID != "cove-AET-1" || s.Role != "worker" || s.Project != "acme" || s.Unit != "AET-1" {
		t.Fatalf("raise spec = %+v", s)
	}
	if !strings.Contains(s.Prompt, "do a thing") || !strings.Contains(s.Prompt, "worker-result.json") {
		t.Fatalf("prompt missing brief or result-protocol:\n%s", s.Prompt)
	}
}

func TestTickDedupsExistingInstance(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1"}}}
	r := &fakeRaiser{}
	reg := &fakeRegistry{insts: []harbor.Instance{{ActorID: "cove-AET-1", Phase: harbor.PhaseLive}}}
	newTestDispatcher(tr, r, reg, 5).tick(context.Background())
	if len(tr.transitions) != 0 || len(r.specs) != 0 {
		t.Fatalf("existing instance must be skipped: transitions=%v raises=%v", tr.transitions, r.specs)
	}
}

func TestTickRespectsCap(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{
		{ID: "id1", Identifier: "AET-1"}, {ID: "id2", Identifier: "AET-2"}, {ID: "id3", Identifier: "AET-3"},
	}}
	r := &fakeRaiser{}
	// 1 already live + cap 2 ⇒ exactly 1 new raise allowed this tick.
	reg := &fakeRegistry{insts: []harbor.Instance{{ActorID: "cove-OTHER", Phase: harbor.PhaseLive}}}
	newTestDispatcher(tr, r, reg, 2).tick(context.Background())
	if len(r.specs) != 1 {
		t.Fatalf("cap: want 1 raise, got %d", len(r.specs))
	}
}

func TestTickCapCountExcludesGone(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1"}}}
	r := &fakeRaiser{}
	// A gone instance must NOT count toward the cap.
	reg := &fakeRegistry{insts: []harbor.Instance{{ActorID: "cove-OLD", Phase: harbor.PhaseGone}}}
	newTestDispatcher(tr, r, reg, 1).tick(context.Background())
	if len(r.specs) != 1 {
		t.Fatalf("gone instance should not consume a slot; want 1 raise, got %d", len(r.specs))
	}
}

func TestTickRaiseFailureMovesToNeedsInput(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1"}}}
	r := &fakeRaiser{err: errors.New("launch boom")}
	newTestDispatcher(tr, r, &fakeRegistry{}, 5).tick(context.Background())
	// InProgress (claim) then NeedsInput (failure).
	if len(tr.transitions) != 2 ||
		tr.transitions[0].role != scheduler.RoleInProgress ||
		tr.transitions[1].role != scheduler.RoleNeedsInput {
		t.Fatalf("want [InProgress, NeedsInput], got %+v", tr.transitions)
	}
}

func TestTickClaimFailureSkipsRaise(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1"}}, transitionErr: errors.New("claim boom")}
	r := &fakeRaiser{}
	newTestDispatcher(tr, r, &fakeRegistry{}, 5).tick(context.Background())
	if len(r.specs) != 0 {
		t.Fatalf("claim failure must skip raise, got %d raises", len(r.specs))
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `GOPROXY=off go test ./internal/dispatcher/ -v`
Expected: FAIL — package/`New`/`Dispatcher` undefined.

- [ ] **Step 3: Write the implementation** — `internal/dispatcher/dispatcher.go`

```go
// Package dispatcher is harbor's resident intake: an always-on poll loop that
// turns ready tracker tickets into managed-cove raises, bounded by a
// registry-derived concurrency cap. It lives outside internal/harbor core (it
// imports the tracker + kit + supervisor) and is wired from cmd/at-harbor.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/harbor"
)

// Raiser is the supervisor's raise entrypoint (satisfied by *harbor.Supervisor).
type Raiser interface {
	Raise(ctx context.Context, spec harbor.RaiseSpec) (harbor.Instance, string, string, error)
}

// Registry reads the durable Instance registry (satisfied by harbor.Store).
type Registry interface {
	GetInstance(actorID string) (harbor.Instance, bool)
	ListInstances() []harbor.Instance
}

// Tracker is the scheduler.Tracker subset the dispatcher needs (satisfied by *linear.Client).
type Tracker interface {
	ListReady(ctx context.Context) ([]scheduler.Issue, error)
	Comments(ctx context.Context, issueID string) ([]scheduler.Comment, error)
	Transition(ctx context.Context, issueID string, role scheduler.Role) error
}

// Config is the dispatcher's behavior configuration.
type Config struct {
	Role          string        // role raised coves get (must grant anthropic + git)
	Project       string        // optional
	MaxConcurrent int           // required, > 0 — max live Instances maintained
	PollInterval  time.Duration // default 30s if <= 0
}

const defaultPollInterval = 30 * time.Second

// resultProtocol instructs the agent to record its outcome. The task is inline
// (the brief precedes this), so unlike the dispatch-worker protocol there is no
// ".at-task/task.json" to read; output-handling (PR/push) is deferred. The
// worker-result.json schema matches internal/dispatch/worker.WorkerResult, which
// the cove's agent wrapper reads to map ok/needs-input/error onto its lifecycle.
const resultProtocol = `---
Your task is described above. Do the work in this repository: make the changes and run the project's tests.

When finished, write your result to .at-task/worker-result.json as EXACTLY ONE of:
  {"status":{"ok":{}}}
  {"status":{"needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}}
  {"status":{"error":{"message":"<what went wrong>"}}}
Use ok only if the change is complete and tests pass.`

type Dispatcher struct {
	tracker  Tracker
	raiser   Raiser
	registry Registry
	cfg      Config
	log      *slog.Logger
}

func New(t Tracker, r Raiser, reg Registry, cfg Config, log *slog.Logger) *Dispatcher {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Dispatcher{tracker: t, raiser: r, registry: reg, cfg: cfg, log: log}
}

// Run polls until ctx is cancelled: an immediate tick, then every PollInterval
// (mirrors Supervisor.Run).
func (d *Dispatcher) Run(ctx context.Context) {
	d.tick(ctx)
	tk := time.NewTicker(d.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			d.tick(ctx)
		}
	}
}

// tick runs one poll pass: list ready → dedup → cap → claim → raise.
func (d *Dispatcher) tick(ctx context.Context) {
	issues, err := d.tracker.ListReady(ctx)
	if err != nil {
		d.log.Warn("dispatcher: list ready failed", "error", err.Error())
		return
	}
	live := d.countLive()
	for _, iss := range issues {
		actorID := "cove-" + iss.Identifier
		if _, ok := d.registry.GetInstance(actorID); ok {
			continue // already raised (dedup)
		}
		if live >= d.cfg.MaxConcurrent {
			d.log.Info("dispatcher: at capacity, deferring", "max", d.cfg.MaxConcurrent)
			break // backpressure — wait for a slot next tick
		}
		if err := d.tracker.Transition(ctx, iss.ID, scheduler.RoleInProgress); err != nil {
			d.log.Warn("dispatcher: claim failed", "issue", iss.Identifier, "error", err.Error())
			continue
		}
		prompt, err := d.buildPrompt(ctx, iss)
		if err != nil {
			d.log.Warn("dispatcher: build prompt failed", "issue", iss.Identifier, "error", err.Error())
			d.needsInput(ctx, iss)
			continue
		}
		if _, _, _, err := d.raiser.Raise(ctx, harbor.RaiseSpec{
			ActorID: actorID, Role: d.cfg.Role, Project: d.cfg.Project, Unit: iss.Identifier, Prompt: prompt,
		}); err != nil {
			d.log.Warn("dispatcher: raise failed", "issue", iss.Identifier, "error", err.Error())
			d.needsInput(ctx, iss)
			continue
		}
		d.log.Info("dispatcher: raised cove", "issue", iss.Identifier, "actor", actorID)
		live++
	}
}

// countLive counts Instances that occupy a concurrency slot (everything not gone;
// gone Instances are already deregistered, but filter defensively).
func (d *Dispatcher) countLive() int {
	n := 0
	for _, i := range d.registry.ListInstances() {
		if i.Phase != harbor.PhaseGone {
			n++
		}
	}
	return n
}

func (d *Dispatcher) buildPrompt(ctx context.Context, iss scheduler.Issue) (string, error) {
	comments, err := d.tracker.Comments(ctx, iss.ID)
	if err != nil {
		return "", fmt.Errorf("comments: %w", err)
	}
	return scheduler.AssembleBrief(iss, comments) + "\n\n" + resultProtocol, nil
}

func (d *Dispatcher) needsInput(ctx context.Context, iss scheduler.Issue) {
	if err := d.tracker.Transition(ctx, iss.ID, scheduler.RoleNeedsInput); err != nil {
		d.log.Warn("dispatcher: move to needs-input failed", "issue", iss.Identifier, "error", err.Error())
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `GOPROXY=off go test ./internal/dispatcher/ -v`
Expected: PASS (all six).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/dispatcher/
git add internal/dispatcher/
git commit -m "dispatcher: resident poll loop — dedup/cap/claim/raise (COV-146)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 3: serve-config `runtime.dispatcher`

**Files:**
- Modify: `cmd/at-harbor/config.go` (a `dispatcherConfig` struct + `Runtime.Dispatcher *dispatcherConfig` + validation)
- Test: `cmd/at-harbor/config_test.go`

**Interfaces:**
- Produces: `dispatcherConfig` + `serveConfig.validateDispatcher()`. Task 4 consumes them.

- [ ] **Step 1: Write the failing test** — add to `cmd/at-harbor/config_test.go`

A test that parses a serve-config with a `runtime.dispatcher` block (asserting `role`, `max-concurrent`, `poll-interval`, `linear.team`, and `tracker-token` land) and that `validateDispatcher()` errors when the block is present but `role` is empty, `max-concurrent` <= 0, or `linear` is nil. Match the file's existing `launcher` config-test style (see `TestRuntimeLauncherParsed` / `TestValidateLauncherRequiredFields`).

- [ ] **Step 2: Run, verify fail**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run Dispatcher`
Expected: FAIL — `dispatcherConfig` undefined.

- [ ] **Step 3: Add the config** — `cmd/at-harbor/config.go`

Add to the anonymous `Runtime` struct: `Dispatcher *dispatcherConfig \`yaml:"dispatcher"\``. Add:

```go
// dispatcherConfig enables the resident dispatcher: harbor polls the tracker and
// raises a managed cove per ready ticket, bounded by max-concurrent.
type dispatcherConfig struct {
	Role          string             `yaml:"role"`
	Project       string             `yaml:"project"`
	MaxConcurrent int                `yaml:"max-concurrent"`
	PollInterval  string             `yaml:"poll-interval"` // optional; falls back to linear.poll-interval
	TrackerToken  credSpec           `yaml:"tracker-token"`
	Linear        *kit.LinearTracker `yaml:"linear"`
}
```
(Add the `internal/kit` import to config.go if not already present.)

Add a `credSpec`→`secret.Spec` converter with a fixed name (mirrors the inline logic in `credSpecs()`, which doesn't set `Name`), so Task 4 can resolve the tracker token:
```go
// toSpec converts this credential to a named secret.Spec (literal or command).
func (cs credSpec) toSpec(name string) secret.Spec {
	if cs.Value != "" {
		return secret.Spec{Name: name, Value: cs.Value, Literal: true}
	}
	return secret.Spec{Name: name, Command: cs.Command}
}
```

Add validation, called from wherever serve-config validation runs (next to `validateLauncher()`):
```go
func (c serveConfig) validateDispatcher() error {
	d := c.Runtime.Dispatcher
	if d == nil {
		return nil
	}
	if d.Role == "" {
		return fmt.Errorf("runtime.dispatcher.role is required")
	}
	if d.MaxConcurrent <= 0 {
		return fmt.Errorf("runtime.dispatcher.max-concurrent must be > 0")
	}
	if d.Linear == nil {
		return fmt.Errorf("runtime.dispatcher.linear is required")
	}
	return nil
}
```

- [ ] **Step 4: Run tests + build**

Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./cmd/at-harbor/ -v`
Expected: PASS.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w cmd/at-harbor/config.go cmd/at-harbor/config_test.go
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go
git commit -m "at-harbor: runtime.dispatcher serve-config + validation (COV-146)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 4: `cmd/at-harbor serve` wiring

**Files:**
- Modify: `cmd/at-harbor/main.go` (`cmdServe`)

- [ ] **Step 1: Wire the dispatcher**

Call `validateDispatcher()` in the serve validation sequence (next to `validateLauncher()`). Then, after the supervisor + launcher are built and `go sup.Run(...)` is started, add:

```go
if dc := cfg.Runtime.Dispatcher; dc != nil {
	// Resolve harbor's own tracker token (never injected into a cove, never logged).
	tokEnv, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{dc.TrackerToken.toSpec("AT_DISPATCH_TRACKER_TOKEN")})
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor: dispatcher tracker-token:", err)
		return 1
	}
	token := tokEnv["AT_DISPATCH_TRACKER_TOKEN"]
	// linear.New wants a full kit.Config; wrap the configured LinearTracker.
	kitShell := kit.Config{Tracker: &kit.Tracker{Linear: dc.Linear}}
	tracker, err := linear.New(kitShell, token, nil)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor: dispatcher tracker:", err)
		return 1
	}
	poll, _ := time.ParseDuration(dc.PollInterval) // "" or invalid → 0 → dispatcher default
	disp := dispatcher.New(tracker, sup, st, dispatcher.Config{
		Role: dc.Role, Project: dc.Project, MaxConcurrent: dc.MaxConcurrent, PollInterval: poll,
	}, log)
	go disp.Run(context.Background())
	log.Info("harbor dispatcher: resident", "role", dc.Role, "max-concurrent", dc.MaxConcurrent)
}
```

**Notes for the implementer:**
- The store variable is `st` and the supervisor is `sup` in `cmdServe` (confirmed: `st, err := harbor.NewFileStore(...)`, `sup := harbor.NewSupervisor(st, lch, ...)`). `st` satisfies `dispatcher.Registry` (harbor.Store has `GetInstance`/`ListInstances`); `sup` satisfies `dispatcher.Raiser`.
- `credSpec.toSpec("AT_DISPATCH_TRACKER_TOKEN")` is the helper added in Task 3; `secret.Resolve` returns the value keyed by that `Name`.
- Verify `linear.New` reads only `cfg.Tracker.Linear` (+ token) from the shell — if it needs other `kit.Config` fields, populate them minimally; the existing `newTracker` in `cmd/at-cove/main.go` is the reference call.
- Add imports: `internal/dispatcher`, `internal/dispatch/linear`, `internal/kit`, `internal/secret`, `internal/runner`, `time` (check which already exist).

- [ ] **Step 2: Build + tests + boundary**

Run:
```bash
GOPROXY=off go build ./...
GOPROXY=off go test ./cmd/at-harbor/ ./internal/dispatcher/ -v
go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo "at-cove clean"
go list -deps ./internal/harbor | grep -iE 'internal/dispatch|internal/kit' || echo "harbor core clean"
```
Expected: builds; tests pass; "at-cove clean"; "harbor core clean" (the dispatcher is imported by cmd/at-harbor, not harbor core).

- [ ] **Step 3: gofmt + commit**

```bash
gofmt -w cmd/at-harbor/main.go
git add cmd/at-harbor/main.go
git commit -m "at-harbor: run the resident dispatcher when runtime.dispatcher is set (COV-146)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 5: Docs

**Files:**
- Create: `docs/usage/harbor/dispatcher.md`
- Modify: `docs/usage/harbor/INDEX.md`, `docs/usage/harbor/serve.md`, `docs/usage/harbor/coves.md`

- [ ] **Step 1: New leaf `docs/usage/harbor/dispatcher.md`**

Frontmatter (match the section's other leaves — `summary`/`read_when`/`owns`/`prereqs`/`tier: leaf`/`updated: 2026-09-13`). Body: what the resident dispatcher is (an always-on poll loop in `at-harbor serve`), the flow (ListReady → dedup via the Instance registry → cap check → claim READY→IN PROGRESS → raise a managed cove with the issue brief as prompt), the **elastic-raise-under-a-cap** model (tracker is the durable queue; `max-concurrent` is registry-derived so it survives restart), the `runtime.dispatcher` config block, and the deferred pieces (outcome→tracker writeback, webhook, multi-instance). Link `serve.md` (config) + `coves.md` (what a raised cove does).

- [ ] **Step 2: INDEX row** — add a `dispatcher.md` row to `docs/usage/harbor/INDEX.md` (link + a `read_when` one-liner mirroring the frontmatter).

- [ ] **Step 3: `serve.md`** — add a `runtime.dispatcher` row to the config table + a short pointer to `dispatcher.md`.

- [ ] **Step 4: `coves.md`** — one line noting a managed cove can be raised by the resident dispatcher (not only `cove raise`), linking `dispatcher.md`.

- [ ] **Step 5: Docs audit (delta)**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no *new* errors referencing the harbor usage docs (the new leaf is reachable via its INDEX row; links resolve). Compare the delta against the known baseline.

- [ ] **Step 6: Commit**

```bash
git add docs/usage/harbor/
git commit -m "docs: resident dispatcher usage (dispatcher.md) + INDEX/serve/coves (COV-146)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

## Self-Review

- **Spec coverage:** export AssembleBrief (T1); dispatcher core — seams/tick/dedup/cap/claim/raise/prompt (T2); serve-config + validation (T3); serve wiring (T4); docs (T5). Every spec section maps to a task.
- **Type consistency:** `Raiser.Raise(ctx, harbor.RaiseSpec) (harbor.Instance, string, string, error)` matches `Supervisor.Raise`; `Registry` = `GetInstance`/`ListInstances` (harbor.Store); `Tracker` subset matches `*linear.Client`/`scheduler.Tracker`; `scheduler.RoleInProgress`/`RoleNeedsInput`, `harbor.PhaseGone`, `scheduler.AssembleBrief(Issue,[]Comment)` are real; `dispatcher.Config`/`New` consistent across T2/T4.
- **Placeholder scan:** none — code steps carry full code; the two "verify the real converter/reader" notes in T4 (`credSpec.toSpec`, `linear.New` needs) point the implementer at the exact reference (`credSpecs()`, `cmd/at-cove` `newTracker`) rather than guessing.
- **Cap correctness:** counted from the registry each pass, incremented per raise within the pass, excludes `PhaseGone` — tested (cap, gone-excluded).
