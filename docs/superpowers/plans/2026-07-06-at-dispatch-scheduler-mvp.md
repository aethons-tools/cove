# at-dispatch Scheduler MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the poll-driven, single-writer scheduler inside `at-dispatch serve`: claim ready autonomous Linear issues, run each issue's configured dispatch command, and broker the result back to Linear.

**Architecture:** A hermetic `scheduler` engine driven by two interfaces — `Tracker` (Linear ops) and `Executor` (headless command run) — both faked in tests. Real adapters: `linear` (GraphQL client, live calls behind an `integration` tag) and `exec` (`exec.CommandContext` with injected env + timeout). `serve` wires them and runs the loop. at-dispatch stays at-cove-agnostic.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies** (GraphQL over `net/http`, no client library).

## Global Constraints

- Packages `internal/dispatch/{scheduler,linear,exec}`; module `github.com/aethons-tools/cove`.
- **No new third-party dependencies**; `go.mod` must still list only `gopkg.in/yaml.v3`.
- **at-cove-agnostic:** these packages import nothing from `internal/backend|connect|assemble|kit`. `scheduler` imports only `internal/dispatch/config` + stdlib; `linear` imports `scheduler` + `config` + stdlib; `scheduler` must **not** import `linear` (engine depends on the `Tracker` interface).
- Consume the config contract exactly: `config.Task{Issue,Class,Repo,Timeout,BriefPath,ResultPath}`, `config.BuildEnv`, `config.ResolveSecrets`, `config.ReadResult`, `config.Result` with `StatusOK`/`StatusNeedsInput`/`StatusError`.
- **Single writer:** only the engine writes tracker state.
- **Poll-only, autonomous-only, no retry, no durable queue** (spec §3).
- Linear GraphQL follows Linear's public API; exact field names are validated by the `integration`-tagged live test — the hermetic tests use matching recorded JSON.
- **TDD, hermetic tests** (no network/docker/ssh); the `integration` build tag gates the one live test.
- Spec: [`docs/superpowers/specs/2026-07-06-at-dispatch-scheduler-mvp-design.md`](../specs/2026-07-06-at-dispatch-scheduler-mvp-design.md).

---

## File Structure

- `internal/dispatch/scheduler/scheduler.go` — `Role`, `Issue`, `Comment`, `Tracker`, `Executor`.
- `internal/dispatch/scheduler/brief.go` — `assembleBrief`.
- `internal/dispatch/scheduler/engine.go` — `Engine`, `New`, `handle`, `broker`, `tick`, `Run`, semaphores.
- `internal/dispatch/scheduler/fakes_test.go` — `fakeTracker`, `fakeExecutor`, helpers.
- `internal/dispatch/scheduler/{brief_test.go,engine_test.go}` — tests.
- `internal/dispatch/exec/exec.go` (+ `exec_test.go`) — real `Executor`.
- `internal/dispatch/linear/linear.go` (+ `linear_test.go`, `linear_integration_test.go`) — real `Tracker`.
- `cmd/at-dispatch/main.go` (+ `main_test.go`) — `serve` wiring.

---

## Task 1: Scheduler types, interfaces, and brief assembly

**Files:**
- Create: `internal/dispatch/scheduler/scheduler.go`
- Create: `internal/dispatch/scheduler/brief.go`
- Test: `internal/dispatch/scheduler/brief_test.go`

**Interfaces:**
- Produces: `Role` (+ consts), `Issue`, `Comment`, `Tracker`, `Executor`; `assembleBrief(iss Issue, repo string, comments []Comment) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/scheduler/brief_test.go`:

```go
package scheduler

import (
	"strings"
	"testing"
)

func TestAssembleBrief(t *testing.T) {
	iss := Issue{Identifier: "AET-42", Title: "Do the thing", Description: "Make it work.", Class: "implement"}
	comments := []Comment{{Author: "brent", Body: "please prioritize"}, {Author: "agent", Body: "on it"}}
	got := assembleBrief(iss, "aethons-tools/cove", comments)

	for _, want := range []string{
		"# AET-42 — Do the thing",
		"**Class:** implement",
		"aethons-tools/cove",
		"Make it work.",
		"**brent:** please prioritize",
		"**agent:** on it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q\n---\n%s", want, got)
		}
	}
}

func TestAssembleBriefNoComments(t *testing.T) {
	got := assembleBrief(Issue{Identifier: "AET-1", Title: "T", Class: "plan"}, "o/r", nil)
	if strings.Contains(got, "## Thread") {
		t.Errorf("expected no Thread section when there are no comments:\n%s", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/scheduler/`
Expected: FAIL to build — `undefined: Issue`, `Comment`, `assembleBrief`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/scheduler/scheduler.go`:

```go
// Package scheduler is the at-dispatch engine: it polls a tracker for ready
// autonomous work, runs each issue's configured dispatch command, and brokers the
// result back as the single writer of tracker state. It is driven by the Tracker
// and Executor interfaces so it can be tested without a network or real commands.
package scheduler

import "context"

// Role is a lifecycle role the engine transitions issues through. The Tracker maps
// each role to this team's configured state name and then to a tracker state id.
type Role int

const (
	RoleReady Role = iota
	RoleInProgress
	RoleInReview
	RoleNeedsInput
	RoleBlocked
	RoleDone
)

// Issue is the subset of a tracker issue the engine needs.
type Issue struct {
	ID          string // tracker-internal id (for API calls)
	Identifier  string // human key, e.g. "AET-42"
	Title       string
	Description string
	Class       string // parsed from the class label; "" if none
}

// Comment is one entry in an issue's thread.
type Comment struct {
	Author string
	Body   string
}

// Tracker is every tracker operation the engine needs. The implementation owns
// team scoping and the Role→state-id mapping.
type Tracker interface {
	ListReady(ctx context.Context) ([]Issue, error)
	ListUnblockable(ctx context.Context) ([]Issue, error)
	Comments(ctx context.Context, issueID string) ([]Comment, error)
	Transition(ctx context.Context, issueID string, role Role) error
	PostComment(ctx context.Context, issueID, body string) error
}

// Executor runs a dispatch command headlessly with the given environment. ctx
// carries the per-task timeout; Run returns nil on exit 0, else an error.
type Executor interface {
	Run(ctx context.Context, argv []string, env []string) error
}
```

Create `internal/dispatch/scheduler/brief.go`:

```go
package scheduler

import "strings"

// assembleBrief renders the self-contained markdown brief a dispatch command reads
// from DISPATCH_BRIEF.
func assembleBrief(iss Issue, repo string, comments []Comment) string {
	var b strings.Builder
	b.WriteString("# " + iss.Identifier + " — " + iss.Title + "\n\n")
	b.WriteString("**Class:** " + iss.Class + "  **Repo:** " + repo + "\n\n")
	b.WriteString("## Description\n\n")
	b.WriteString(strings.TrimSpace(iss.Description) + "\n")
	if len(comments) > 0 {
		b.WriteString("\n## Thread\n\n")
		for _, c := range comments {
			b.WriteString("- **" + c.Author + ":** " + c.Body + "\n")
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/dispatch/scheduler/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/scheduler/scheduler.go internal/dispatch/scheduler/brief.go internal/dispatch/scheduler/brief_test.go
git commit -m "feat(scheduler): types, interfaces, brief assembly"
```

---

## Task 2: Engine `handle` — one issue, claim → run → broker

**Files:**
- Create: `internal/dispatch/scheduler/engine.go`
- Create: `internal/dispatch/scheduler/fakes_test.go`
- Test: `internal/dispatch/scheduler/engine_test.go`

**Interfaces:**
- Consumes: Task 1 types; `config.{Config,Task,BuildEnv,ResolveSecrets,ReadResult,Result,StatusOK,StatusNeedsInput,StatusError}`.
- Produces: `Engine`, `New(cfg, Tracker, Executor, resolve, *log.Logger) *Engine`, `(*Engine).handle(ctx, Issue)`, `(*Engine).broker(ctx, Issue, config.Result, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/scheduler/fakes_test.go`:

```go
package scheduler

import (
	"context"
	"os"
	"strings"
	"sync"
)

type transition struct {
	IssueID string
	Role    Role
}
type post struct {
	IssueID string
	Body    string
}

type fakeTracker struct {
	mu          sync.Mutex
	ready       []Issue
	unblockable []Issue
	comments    map[string][]Comment
	transitions []transition
	posts       []post
	failClaim   bool // Transition to RoleInProgress returns an error
}

func (f *fakeTracker) ListReady(context.Context) ([]Issue, error)       { return f.ready, nil }
func (f *fakeTracker) ListUnblockable(context.Context) ([]Issue, error) { return f.unblockable, nil }
func (f *fakeTracker) Comments(_ context.Context, id string) ([]Comment, error) {
	return f.comments[id], nil
}
func (f *fakeTracker) Transition(_ context.Context, id string, r Role) error {
	if f.failClaim && r == RoleInProgress {
		return errFake
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, transition{id, r})
	return nil
}
func (f *fakeTracker) PostComment(_ context.Context, id, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, post{id, body})
	return nil
}
func (f *fakeTracker) roles(id string) []Role {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rs []Role
	for _, t := range f.transitions {
		if t.IssueID == id {
			rs = append(rs, t.Role)
		}
	}
	return rs
}
func (f *fakeTracker) lastPost(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.posts) - 1; i >= 0; i-- {
		if f.posts[i].IssueID == id {
			return f.posts[i].Body
		}
	}
	return ""
}

var errFake = fakeErr("fake error")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// fakeExecutor mimics a dispatch command: it optionally writes canned JSON to the
// DISPATCH_RESULT path from env, then returns runErr.
type fakeExecutor struct {
	resultJSON string
	runErr     error
	panicMsg   string        // if non-empty, Run panics (to test recovery)
	started    chan struct{} // if non-nil, closed when Run starts
	release    chan struct{} // if non-nil, Run blocks until this is closed
}

func (f *fakeExecutor) Run(ctx context.Context, _ []string, env []string) error {
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	if f.started != nil {
		select {
		case <-f.started:
		default:
			close(f.started)
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.resultJSON != "" {
		if p := envVal(env, "DISPATCH_RESULT"); p != "" {
			_ = os.WriteFile(p, []byte(f.resultJSON), 0o600)
		}
	}
	return f.runErr
}

func envVal(env []string, key string) string {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return e[len(key)+1:]
		}
	}
	return ""
}
```

Create `internal/dispatch/scheduler/engine_test.go`:

```go
package scheduler

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/config"
)

func testConfig() config.Config {
	return config.Config{
		Repo: config.RepoConfig{Slug: "aethons-tools/cove"},
		Classes: map[string]config.Class{
			"implement": {Mode: "autonomous", Command: []string{"true"}, Timeout: "30m"},
			"spec":      {Mode: "interactive"},
		},
		Concurrency: 4,
	}
}

func newTestEngine(cfg config.Config, tr Tracker, ex Executor) *Engine {
	return New(cfg, tr, ex, func([]string) (string, error) { return "", nil }, log.New(io.Discard, "", 0))
}

func TestHandleOK(t *testing.T) {
	tr := &fakeTracker{}
	ex := &fakeExecutor{resultJSON: `{"status":"ok","artifacts":{"prUrl":"https://x/pr/9"},"summary":"done"}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i1", Identifier: "AET-9", Class: "implement"})

	if got := tr.roles("i1"); len(got) != 2 || got[0] != RoleInProgress || got[1] != RoleInReview {
		t.Fatalf("transitions = %v; want [InProgress InReview]", got)
	}
	if p := tr.lastPost("i1"); !strings.Contains(p, "https://x/pr/9") {
		t.Fatalf("comment = %q; want the prUrl", p)
	}
}

func TestHandleNeedsInput(t *testing.T) {
	tr := &fakeTracker{}
	ex := &fakeExecutor{resultJSON: `{"status":"needs_input","needsInput":{"blocker":"ambiguous","need":"pick A or B"}}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i2", Identifier: "AET-2", Class: "implement"})

	if got := tr.roles("i2"); len(got) != 2 || got[1] != RoleNeedsInput {
		t.Fatalf("transitions = %v; want ...NeedsInput", got)
	}
	if p := tr.lastPost("i2"); !strings.Contains(p, "pick A or B") || !strings.Contains(p, "NEEDS INPUT") {
		t.Fatalf("comment = %q; want the needs-input block", p)
	}
}

func TestHandleCommandErrorGoesToNeedsInput(t *testing.T) {
	tr := &fakeTracker{}
	ex := &fakeExecutor{runErr: errors.New("boom")} // no result file written → error
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i3", Identifier: "AET-3", Class: "implement"})

	if got := tr.roles("i3"); len(got) != 2 || got[1] != RoleNeedsInput {
		t.Fatalf("transitions = %v; want ...NeedsInput", got)
	}
}

func TestHandleFailedClaimStops(t *testing.T) {
	tr := &fakeTracker{failClaim: true}
	ex := &fakeExecutor{resultJSON: `{"status":"ok"}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i4", Identifier: "AET-4", Class: "implement"})

	if got := tr.roles("i4"); len(got) != 0 {
		t.Fatalf("transitions = %v; want none (claim failed)", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/scheduler/ -run TestHandle`
Expected: FAIL to build — `undefined: Engine`, `New`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/scheduler/engine.go`:

```go
package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/config"
)

// Engine polls a Tracker and dispatches ready autonomous work.
type Engine struct {
	cfg     config.Config
	tracker Tracker
	exec    Executor
	resolve func([]string) (string, error)
	log     *log.Logger

	gsem chan struct{}            // global concurrency
	csem map[string]chan struct{} // per-class concurrency (nil entry = no cap)
	wg   sync.WaitGroup
}

// New builds an Engine. resolve turns a secret's argv into its value (host side).
func New(cfg config.Config, t Tracker, e Executor, resolve func([]string) (string, error), logger *log.Logger) *Engine {
	gcap := cfg.Concurrency
	if gcap < 1 {
		gcap = 1
	}
	csem := map[string]chan struct{}{}
	for name, cl := range cfg.Classes {
		if cl.Concurrency > 0 {
			csem[name] = make(chan struct{}, cl.Concurrency)
		}
	}
	return &Engine{
		cfg: cfg, tracker: t, exec: e, resolve: resolve, log: logger,
		gsem: make(chan struct{}, gcap), csem: csem,
	}
}

// handle runs one issue synchronously: claim → brief → command → broker.
func (e *Engine) handle(ctx context.Context, iss Issue) {
	if err := e.tracker.Transition(ctx, iss.ID, RoleInProgress); err != nil {
		e.log.Printf("claim %s: %v", iss.Identifier, err)
		return
	}
	cl := e.cfg.Classes[iss.Class]

	comments, err := e.tracker.Comments(ctx, iss.ID)
	if err != nil {
		e.log.Printf("comments %s: %v (continuing with none)", iss.Identifier, err)
	}
	brief := assembleBrief(iss, e.cfg.Repo.Slug, comments)

	dir, err := os.MkdirTemp("", "at-dispatch-")
	if err != nil {
		e.broker(ctx, iss, config.Result{}, fmt.Errorf("tempdir: %w", err))
		return
	}
	defer os.RemoveAll(dir)
	briefPath := filepath.Join(dir, "brief.md")
	resultPath := filepath.Join(dir, "result.json")
	if err := os.WriteFile(briefPath, []byte(brief), 0o600); err != nil {
		e.broker(ctx, iss, config.Result{}, fmt.Errorf("write brief: %w", err))
		return
	}

	secrets, err := config.ResolveSecrets(e.cfg.Secrets, e.resolve)
	if err != nil {
		e.broker(ctx, iss, config.Result{}, fmt.Errorf("resolve secrets: %w", err))
		return
	}
	env := config.BuildEnv(config.Task{
		Issue: iss.Identifier, Class: iss.Class, Repo: e.cfg.Repo.Slug,
		Timeout: cl.Timeout, BriefPath: briefPath, ResultPath: resultPath,
	}, secrets)

	d, _ := time.ParseDuration(cl.Timeout) // validated by config
	rctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	runErr := e.exec.Run(rctx, cl.Command, env)

	e.broker(ctx, iss, config.ReadResult(resultPath), runErr)
}

// broker performs the tracker writes for one result. Single writer.
func (e *Engine) broker(ctx context.Context, iss Issue, res config.Result, runErr error) {
	switch {
	case runErr == nil && res.Status == config.StatusOK:
		e.post(ctx, iss, okComment(res))
		e.transition(ctx, iss, RoleInReview)
	case res.Status == config.StatusNeedsInput:
		e.post(ctx, iss, needsInputComment(res))
		e.transition(ctx, iss, RoleNeedsInput)
	default:
		e.post(ctx, iss, errorComment(res, runErr))
		e.transition(ctx, iss, RoleNeedsInput)
	}
}

func (e *Engine) transition(ctx context.Context, iss Issue, r Role) {
	if err := e.tracker.Transition(ctx, iss.ID, r); err != nil {
		e.log.Printf("transition %s -> %d: %v", iss.Identifier, r, err)
	}
}
func (e *Engine) post(ctx context.Context, iss Issue, body string) {
	if err := e.tracker.PostComment(ctx, iss.ID, body); err != nil {
		e.log.Printf("comment %s: %v", iss.Identifier, err)
	}
}

func okComment(res config.Result) string {
	var b strings.Builder
	b.WriteString("✅ Done.\n\n")
	if res.Artifacts.PRURL != "" {
		b.WriteString("PR: " + res.Artifacts.PRURL + "\n")
	}
	if res.Artifacts.Branch != "" {
		b.WriteString("Branch: " + res.Artifacts.Branch + "\n")
	}
	if res.Artifacts.DocPath != "" {
		b.WriteString("Doc: " + res.Artifacts.DocPath + "\n")
	}
	if res.Summary != "" {
		b.WriteString("\n" + res.Summary + "\n")
	}
	return b.String()
}

func needsInputComment(res config.Result) string {
	n := res.NeedsInput
	if n == nil {
		return "❓ NEEDS INPUT\n\n" + res.Summary
	}
	return "❓ NEEDS INPUT\n\n" +
		"**Doing:** " + n.Doing + "\n" +
		"**Blocker:** " + n.Blocker + "\n" +
		"**Need:** " + n.Need + "\n" +
		"**Tried:** " + n.Tried + "\n" +
		"**Safe state:** " + n.SafeState + "\n"
}

func errorComment(res config.Result, runErr error) string {
	msg := res.Summary
	if runErr != nil {
		msg = "command failed: " + runErr.Error()
		if res.Summary != "" {
			msg += " (" + res.Summary + ")"
		}
	}
	return "⚠️ Dispatch error — routed to NEEDS INPUT.\n\n" + msg + "\n"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/scheduler/`
Expected: PASS (brief tests + the four handle tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/scheduler/engine.go internal/dispatch/scheduler/fakes_test.go internal/dispatch/scheduler/engine_test.go
git commit -m "feat(scheduler): engine handle + broker (single-issue pipeline)"
```

---

## Task 3: Engine `tick` + `Run` — poll loop, concurrency, BLOCKED→READY

**Files:**
- Modify: `internal/dispatch/scheduler/engine.go` (add `tick`, `Run`, `acquire`, `release`, `wait`)
- Test: `internal/dispatch/scheduler/engine_test.go` (add tick/Run tests)

**Interfaces:**
- Consumes: Task 2 `handle`; the semaphore fields on `Engine`.
- Produces: `(*Engine).tick(ctx)`, `(*Engine).Run(ctx) error`.

- [ ] **Step 1: Write the failing test**

Append to `internal/dispatch/scheduler/engine_test.go`:

```go
func TestTickReconcilesAndDispatches(t *testing.T) {
	tr := &fakeTracker{
		unblockable: []Issue{{ID: "b1", Identifier: "AET-B1"}},
		ready: []Issue{
			{ID: "i1", Identifier: "AET-1", Class: "implement"},
			{ID: "s1", Identifier: "AET-S1", Class: "spec"}, // interactive → skipped
			{ID: "x1", Identifier: "AET-X1", Class: "unknown"}, // unknown → skipped
		},
	}
	ex := &fakeExecutor{resultJSON: `{"status":"ok"}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.tick(context.Background())
	e.wait()

	// unblockable moved to READY
	if got := tr.roles("b1"); len(got) != 1 || got[0] != RoleReady {
		t.Fatalf("unblock transitions = %v; want [Ready]", got)
	}
	// implement issue dispatched (claimed + brokered)
	if got := tr.roles("i1"); len(got) != 2 || got[0] != RoleInProgress || got[1] != RoleInReview {
		t.Fatalf("i1 transitions = %v; want [InProgress InReview]", got)
	}
	// interactive + unknown skipped (no transitions)
	if len(tr.roles("s1")) != 0 || len(tr.roles("x1")) != 0 {
		t.Fatalf("interactive/unknown should not be touched")
	}
}

func TestTickRespectsGlobalConcurrency(t *testing.T) {
	cfg := testConfig()
	cfg.Concurrency = 1
	tr := &fakeTracker{ready: []Issue{
		{ID: "i1", Identifier: "AET-1", Class: "implement"},
		{ID: "i2", Identifier: "AET-2", Class: "implement"},
	}}
	release := make(chan struct{})
	started := make(chan struct{})
	ex := &fakeExecutor{resultJSON: `{"status":"ok"}`, release: release, started: started}
	e := newTestEngine(cfg, tr, ex)

	e.tick(context.Background()) // one slot: only one issue claimed, the other skipped this tick
	<-started                    // the single claimed issue's command has begun (its claim is recorded)
	claimed := 0
	for _, id := range []string{"i1", "i2"} {
		if len(tr.roles(id)) >= 1 {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed = %d; want exactly 1 (global cap = 1)", claimed)
	}
	close(release)
	e.wait()
}

func TestTickRecoversPanic(t *testing.T) {
	tr := &fakeTracker{ready: []Issue{{ID: "p1", Identifier: "AET-P1", Class: "implement"}}}
	e := newTestEngine(testConfig(), tr, &fakeExecutor{panicMsg: "kaboom"})

	e.tick(context.Background())
	e.wait() // must not crash; the panic is recovered

	if got := tr.roles("p1"); len(got) != 2 || got[0] != RoleInProgress || got[1] != RoleNeedsInput {
		t.Fatalf("transitions = %v; want [InProgress NeedsInput] after a recovered panic", got)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	cfg := testConfig()
	cfg.Tracker.PollInterval = "1h" // long, so only the immediate first tick runs
	tr := &fakeTracker{}
	e := newTestEngine(cfg, tr, &fakeExecutor{resultJSON: `{"status":"ok"}`})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if err := e.Run(ctx); err == nil {
		t.Fatal("Run should return ctx.Err() when cancelled")
	}
}
```

Note: `cfg.Tracker.PollInterval` must parse; `testConfig` doesn't set it, so add `Tracker: config.TrackerConfig{PollInterval: "1m"}` to `testConfig` now (used by `Run`). Update `testConfig`:

```go
func testConfig() config.Config {
	return config.Config{
		Tracker: config.TrackerConfig{PollInterval: "1m"},
		Repo:    config.RepoConfig{Slug: "aethons-tools/cove"},
		Classes: map[string]config.Class{
			"implement": {Mode: "autonomous", Command: []string{"true"}, Timeout: "30m"},
			"spec":      {Mode: "interactive"},
		},
		Concurrency: 4,
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/scheduler/ -run 'TestTick|TestRun'`
Expected: FAIL to build — `undefined: (*Engine).tick`, `wait`, `Run`.

- [ ] **Step 3: Write the implementation**

Append to `internal/dispatch/scheduler/engine.go`:

```go
// Run polls every poll-interval until ctx is done, draining in-flight work on exit.
func (e *Engine) Run(ctx context.Context) error {
	d, _ := time.ParseDuration(e.cfg.Tracker.PollInterval) // validated by config
	t := time.NewTicker(d)
	defer t.Stop()
	e.tick(ctx) // immediate first pass
	for {
		select {
		case <-ctx.Done():
			e.wait()
			return ctx.Err()
		case <-t.C:
			e.tick(ctx)
		}
	}
}

// tick is one poll pass: reconcile BLOCKED→READY, then claim+dispatch ready
// autonomous issues up to the concurrency caps.
func (e *Engine) tick(ctx context.Context) {
	if unb, err := e.tracker.ListUnblockable(ctx); err != nil {
		e.log.Printf("list unblockable: %v", err)
	} else {
		for _, iss := range unb {
			if err := e.tracker.Transition(ctx, iss.ID, RoleReady); err != nil {
				e.log.Printf("unblock %s: %v", iss.Identifier, err)
			}
		}
	}

	ready, err := e.tracker.ListReady(ctx)
	if err != nil {
		e.log.Printf("list ready: %v", err)
		return
	}
	for _, iss := range ready {
		cl, ok := e.cfg.Classes[iss.Class]
		if !ok || cl.Mode != "autonomous" {
			continue // skip interactive / unknown classes
		}
		if !e.acquire(iss.Class) {
			continue // caps full this tick
		}
		iss := iss
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			defer e.release(iss.Class)
			defer func() {
				if r := recover(); r != nil {
					e.log.Printf("panic handling %s: %v", iss.Identifier, r)
					// best-effort: park the issue for a human rather than crash the loop
					e.transition(context.Background(), iss, RoleNeedsInput)
				}
			}()
			e.handle(ctx, iss)
		}()
	}
}

// acquire takes a global slot and (if the class caps concurrency) a class slot,
// without blocking. It returns false and holds nothing if either is full.
func (e *Engine) acquire(class string) bool {
	select {
	case e.gsem <- struct{}{}:
	default:
		return false
	}
	cs := e.csem[class]
	if cs == nil {
		return true
	}
	select {
	case cs <- struct{}{}:
		return true
	default:
		<-e.gsem
		return false
	}
}

func (e *Engine) release(class string) {
	if cs := e.csem[class]; cs != nil {
		<-cs
	}
	<-e.gsem
}

// wait blocks until all in-flight dispatches finish (shutdown drain / tests).
func (e *Engine) wait() { e.wg.Wait() }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/scheduler/`
Expected: PASS (all scheduler tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/scheduler/engine.go internal/dispatch/scheduler/engine_test.go
git commit -m "feat(scheduler): tick + Run poll loop with bounded concurrency"
```

---

## Task 4: Real `Executor` (`internal/dispatch/exec`)

**Files:**
- Create: `internal/dispatch/exec/exec.go`
- Test: `internal/dispatch/exec/exec_test.go`

**Interfaces:**
- Produces: `type Executor struct{ … }`, `New() *Executor`, `(*Executor).Run(ctx, argv []string, env []string) error` (satisfies `scheduler.Executor`).

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/exec/exec_test.go`:

```go
package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunInjectsEnvAndRuns(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	env := []string{"DISPATCH_ISSUE=AET-7", "DISPATCH_RESULT=" + out}
	// a command that writes $DISPATCH_ISSUE to $DISPATCH_RESULT
	err := New().Run(context.Background(), []string{"sh", "-c", `printf '%s' "$DISPATCH_ISSUE" > "$DISPATCH_RESULT"`}, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "AET-7" {
		t.Fatalf("output = %q; want AET-7 (env not injected?)", got)
	}
}

func TestRunNonZeroExitIsError(t *testing.T) {
	if err := New().Run(context.Background(), []string{"sh", "-c", "exit 3"}, nil); err == nil {
		t.Fatal("expected an error for non-zero exit")
	}
}

func TestRunTimeoutIsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := New().Run(ctx, []string{"sh", "-c", "sleep 5"}, nil); err == nil {
		t.Fatal("expected an error when the command exceeds the deadline")
	}
}

func TestRunEmptyArgv(t *testing.T) {
	if err := New().Run(context.Background(), nil, nil); err == nil {
		t.Fatal("expected an error for empty argv")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/exec/`
Expected: FAIL to build — `undefined: New`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/exec/exec.go`:

```go
// Package exec runs dispatch commands headlessly with an injected environment and a
// context timeout. It is at-dispatch's real scheduler.Executor.
package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
)

// Executor runs commands via os/exec. Command stdout/stderr stream to Log (default
// os.Stderr) for observability; the command's structured result goes to its
// DISPATCH_RESULT file, not stdout.
type Executor struct {
	Log io.Writer
}

func New() *Executor { return &Executor{Log: os.Stderr} }

// Run executes argv with env (appended to the parent environment so the command
// still has PATH etc.), bounded by ctx. Returns nil on exit 0, else an error.
func (e *Executor) Run(ctx context.Context, argv []string, env []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("exec: empty command")
	}
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	w := e.Log
	if w == nil {
		w = os.Stderr
	}
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/exec/`
Expected: PASS (env injection, non-zero exit, timeout, empty argv).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/exec/
git commit -m "feat(dispatch/exec): headless Executor with env injection + timeout"
```

---

## Task 5: Linear client core — construction, `Transition`, `PostComment`

**Files:**
- Create: `internal/dispatch/linear/linear.go`
- Test: `internal/dispatch/linear/linear_test.go`

**Interfaces:**
- Consumes: `scheduler.{Role,Issue,Comment,Tracker}`, `config.Config`.
- Produces: `type Client`, `New(cfg config.Config, token string, httpc *http.Client) (*Client, error)`, `(*Client).Transition`, `(*Client).PostComment`, internal `do`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/linear/linear_test.go`:

```go
package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/config"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
)

// rtFunc is a fake http.RoundTripper returning canned responses per request.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func testCfg() config.Config {
	return config.Config{
		Tracker: config.TrackerConfig{
			Team:             "AET",
			ClassLabelPrefix: "class:",
			States: config.StateMap{
				Ready: "Todo", InProgress: "In Progress", InReview: "In Review",
				Done: "Done", NeedsInput: "Needs Input", Blocked: "Backlog",
			},
		},
	}
}

// statesResponse is the workflowStates payload New fetches at construction.
const statesResponse = `{"data":{"workflowStates":{"nodes":[
 {"id":"s-todo","name":"Todo","type":"unstarted"},
 {"id":"s-prog","name":"In Progress","type":"started"},
 {"id":"s-rev","name":"In Review","type":"started"},
 {"id":"s-done","name":"Done","type":"completed"},
 {"id":"s-ni","name":"Needs Input","type":"unstarted"},
 {"id":"s-block","name":"Backlog","type":"backlog"}]}}}`

func newTestClient(t *testing.T, rt rtFunc) *Client {
	t.Helper()
	c, err := New(testCfg(), "tok", &http.Client{Transport: rt})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewFetchesStateMapAndAuthHeader(t *testing.T) {
	var sawAuth string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		return jsonResp(statesResponse), nil
	})
	if sawAuth != "tok" {
		t.Fatalf("Authorization = %q; want tok", sawAuth)
	}
	if c.stateID[scheduler.RoleInReview] != "s-rev" {
		t.Fatalf("RoleInReview id = %q; want s-rev", c.stateID[scheduler.RoleInReview])
	}
}

func TestTransitionSendsIssueUpdate(t *testing.T) {
	var body map[string]any
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil // New's fetch
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		return jsonResp(`{"data":{"issueUpdate":{"success":true}}}`), nil
	})
	if err := c.Transition(context.Background(), "i1", scheduler.RoleInReview); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	q, _ := body["query"].(string)
	vars, _ := body["variables"].(map[string]any)
	if !strings.Contains(q, "issueUpdate") {
		t.Fatalf("query missing issueUpdate: %s", q)
	}
	if vars["stateId"] != "s-rev" || vars["id"] != "i1" {
		t.Fatalf("variables = %v; want id=i1 stateId=s-rev", vars)
	}
}

func TestPostCommentSendsCommentCreate(t *testing.T) {
	var body map[string]any
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		return jsonResp(`{"data":{"commentCreate":{"success":true}}}`), nil
	})
	if err := c.PostComment(context.Background(), "i9", "hello"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	vars, _ := body["variables"].(map[string]any)
	if vars["issueId"] != "i9" || vars["body"] != "hello" {
		t.Fatalf("variables = %v; want issueId=i9 body=hello", vars)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/linear/`
Expected: FAIL to build — `undefined: New`, `Client`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/linear/linear.go`:

```go
// Package linear is at-dispatch's real scheduler.Tracker: a small GraphQL client
// over the Linear API. Live calls are exercised by the integration-tagged test.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aethons-tools/cove/internal/dispatch/config"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
)

const endpoint = "https://api.linear.app/graphql"

// Client talks to one Linear team. It satisfies scheduler.Tracker.
type Client struct {
	http     *http.Client
	token    string
	team     string
	prefix   string                 // class label prefix
	states   config.StateMap        // role → configured state name
	stateID  map[scheduler.Role]string
}

// New constructs a Client and resolves the team's state names to ids up front.
func New(cfg config.Config, token string, httpc *http.Client) (*Client, error) {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	c := &Client{
		http: httpc, token: token,
		team:   cfg.Tracker.Team,
		prefix: cfg.Tracker.ClassLabelPrefix,
		states: cfg.Tracker.States,
	}
	if err := c.loadStates(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) loadStates(ctx context.Context) error {
	const q = `query($key:String!){workflowStates(filter:{team:{key:{eq:$key}}}){nodes{id name type}}}`
	var out struct {
		WorkflowStates struct {
			Nodes []struct{ ID, Name, Type string } `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := c.do(ctx, q, map[string]any{"key": c.team}, &out); err != nil {
		return err
	}
	byName := map[string]string{}
	for _, n := range out.WorkflowStates.Nodes {
		byName[n.Name] = n.ID
	}
	c.stateID = map[scheduler.Role]string{}
	roles := map[scheduler.Role]string{
		scheduler.RoleReady: c.states.Ready, scheduler.RoleInProgress: c.states.InProgress,
		scheduler.RoleInReview: c.states.InReview, scheduler.RoleDone: c.states.Done,
		scheduler.RoleNeedsInput: c.states.NeedsInput, scheduler.RoleBlocked: c.states.Blocked,
	}
	for role, name := range roles {
		id, ok := byName[name]
		if !ok {
			return fmt.Errorf("linear: team %s has no workflow state named %q (for role %d)", c.team, name, role)
		}
		c.stateID[role] = id
	}
	return nil
}

// do posts a GraphQL query and unmarshals data into out.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: http %d", resp.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear: %s", envelope.Errors[0].Message)
	}
	if out != nil {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

// Transition moves an issue to the state configured for role.
func (c *Client) Transition(ctx context.Context, issueID string, role scheduler.Role) error {
	id, ok := c.stateID[role]
	if !ok {
		return fmt.Errorf("linear: no state id for role %d", role)
	}
	const m = `mutation($id:String!,$stateId:String!){issueUpdate(id:$id,input:{stateId:$stateId}){success}}`
	var out struct{ IssueUpdate struct{ Success bool } }
	if err := c.do(ctx, m, map[string]any{"id": issueID, "stateId": id}, &out); err != nil {
		return err
	}
	if !out.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate reported failure for %s", issueID)
	}
	return nil
}

// PostComment adds a comment to an issue.
func (c *Client) PostComment(ctx context.Context, issueID, body string) error {
	const m = `mutation($issueId:String!,$body:String!){commentCreate(input:{issueId:$issueId,body:$body}){success}}`
	var out struct{ CommentCreate struct{ Success bool } }
	if err := c.do(ctx, m, map[string]any{"issueId": issueID, "body": body}, &out); err != nil {
		return err
	}
	if !out.CommentCreate.Success {
		return fmt.Errorf("linear: commentCreate reported failure for %s", issueID)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/linear/`
Expected: PASS (state-map fetch + auth header, issueUpdate, commentCreate).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/linear/linear.go internal/dispatch/linear/linear_test.go
git commit -m "feat(dispatch/linear): GraphQL client core (New, Transition, PostComment)"
```

---

## Task 6: Linear client reads — `ListReady`, `ListUnblockable`, `Comments` (+ integration test)

**Files:**
- Modify: `internal/dispatch/linear/linear.go` (add the three read methods + a label→class helper)
- Test: `internal/dispatch/linear/linear_test.go` (add read tests)
- Create: `internal/dispatch/linear/linear_integration_test.go` (build-tagged live smoke test)

**Interfaces:**
- Consumes: Task 5 `Client`, `do`, `stateID`, `prefix`, `states`.
- Produces: `(*Client).ListReady`, `(*Client).ListUnblockable`, `(*Client).Comments` — completing `scheduler.Tracker`.

- [ ] **Step 1: Write the failing test**

Append to `internal/dispatch/linear/linear_test.go`:

```go
func TestListReadyParsesIssuesAndClass(t *testing.T) {
	const resp = `{"data":{"issues":{"nodes":[
	 {"id":"i1","identifier":"AET-1","title":"T1","description":"D1","labels":{"nodes":[{"name":"class:implement"},{"name":"p1"}]}}
	]}}}`
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		return jsonResp(resp), nil
	})
	got, err := c.ListReady(context.Background())
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if len(got) != 1 || got[0].Identifier != "AET-1" || got[0].Class != "implement" {
		t.Fatalf("ListReady = %+v; want one AET-1 with class implement", got)
	}
}

func TestListUnblockableFiltersByBlockerState(t *testing.T) {
	// b1: sole blocker is completed → unblockable. b2: a blocker still started → not.
	const resp = `{"data":{"issues":{"nodes":[
	 {"id":"b1","identifier":"AET-B1","title":"","description":"","labels":{"nodes":[]},
	  "inverseRelations":{"nodes":[{"type":"blocks","issue":{"state":{"type":"completed"}}}]}},
	 {"id":"b2","identifier":"AET-B2","title":"","description":"","labels":{"nodes":[]},
	  "inverseRelations":{"nodes":[{"type":"blocks","issue":{"state":{"type":"started"}}}]}}
	]}}}`
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		return jsonResp(resp), nil
	})
	got, err := c.ListUnblockable(context.Background())
	if err != nil {
		t.Fatalf("ListUnblockable: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b1" {
		t.Fatalf("ListUnblockable = %+v; want only b1", got)
	}
}

func TestCommentsParsesThread(t *testing.T) {
	const resp = `{"data":{"issue":{"comments":{"nodes":[
	 {"body":"hi","user":{"displayName":"brent"}},
	 {"body":"yo","user":{"displayName":"agent"}}]}}}}`
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResp(statesResponse), nil
		}
		return jsonResp(resp), nil
	})
	got, err := c.Comments(context.Background(), "i1")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != 2 || got[0].Author != "brent" || got[1].Body != "yo" {
		t.Fatalf("Comments = %+v", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/linear/ -run 'TestList|TestComments'`
Expected: FAIL to build — `undefined: (*Client).ListReady` etc.

- [ ] **Step 3: Write the implementation**

Append to `internal/dispatch/linear/linear.go` (add `strings` to the import block):

```go
// issueNode is the shared GraphQL projection for an issue.
type issueNode struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Labels      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	InverseRelations struct {
		Nodes []struct {
			Type  string `json:"type"`
			Issue struct {
				State struct {
					Type string `json:"type"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
}

func (c *Client) toIssue(n issueNode) scheduler.Issue {
	class := ""
	for _, l := range n.Labels.Nodes {
		if strings.HasPrefix(l.Name, c.prefix) {
			class = strings.TrimPrefix(l.Name, c.prefix)
			break
		}
	}
	return scheduler.Issue{
		ID: n.ID, Identifier: n.Identifier, Title: n.Title,
		Description: n.Description, Class: class,
	}
}

func (c *Client) ListReady(ctx context.Context) ([]scheduler.Issue, error) {
	const q = `query($key:String!,$state:String!){issues(filter:{team:{key:{eq:$key}},state:{name:{eq:$state}}}){nodes{id identifier title description labels{nodes{name}}}}}`
	var out struct {
		Issues struct {
			Nodes []issueNode `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, q, map[string]any{"key": c.team, "state": c.states.Ready}, &out); err != nil {
		return nil, err
	}
	issues := make([]scheduler.Issue, 0, len(out.Issues.Nodes))
	for _, n := range out.Issues.Nodes {
		issues = append(issues, c.toIssue(n))
	}
	return issues, nil
}

// ListUnblockable returns BLOCKED issues all of whose "blocks" blockers are complete.
func (c *Client) ListUnblockable(ctx context.Context) ([]scheduler.Issue, error) {
	const q = `query($key:String!,$state:String!){issues(filter:{team:{key:{eq:$key}},state:{name:{eq:$state}}}){nodes{id identifier title description labels{nodes{name}} inverseRelations{nodes{type issue{state{type}}}}}}}`
	var out struct {
		Issues struct {
			Nodes []issueNode `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, q, map[string]any{"key": c.team, "state": c.states.Blocked}, &out); err != nil {
		return nil, err
	}
	var issues []scheduler.Issue
	for _, n := range out.Issues.Nodes {
		allDone := true
		for _, rel := range n.InverseRelations.Nodes {
			if rel.Type == "blocks" && rel.Issue.State.Type != "completed" {
				allDone = false
				break
			}
		}
		if allDone {
			issues = append(issues, c.toIssue(n))
		}
	}
	return issues, nil
}

func (c *Client) Comments(ctx context.Context, issueID string) ([]scheduler.Comment, error) {
	const q = `query($id:String!){issue(id:$id){comments{nodes{body user{displayName}}}}}`
	var out struct {
		Issue struct {
			Comments struct {
				Nodes []struct {
					Body string `json:"body"`
					User struct {
						DisplayName string `json:"displayName"`
					} `json:"user"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := c.do(ctx, q, map[string]any{"id": issueID}, &out); err != nil {
		return nil, err
	}
	cs := make([]scheduler.Comment, 0, len(out.Issue.Comments.Nodes))
	for _, n := range out.Issue.Comments.Nodes {
		cs = append(cs, scheduler.Comment{Author: n.User.DisplayName, Body: n.Body})
	}
	return cs, nil
}
```

- [ ] **Step 4: Add a compile-time interface check and the integration smoke test**

At the end of `internal/dispatch/linear/linear.go`, assert the client satisfies the interface:

```go
var _ scheduler.Tracker = (*Client)(nil)
```

Create `internal/dispatch/linear/linear_integration_test.go`:

```go
//go:build integration

package linear

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/config"
)

// TestLive hits real Linear. Run with: LINEAR_TOKEN=… LINEAR_TEAM=AET \
//   go test -tags integration ./internal/dispatch/linear/ -run TestLive -v
func TestLive(t *testing.T) {
	token, team := os.Getenv("LINEAR_TOKEN"), os.Getenv("LINEAR_TEAM")
	if token == "" || team == "" {
		t.Skip("set LINEAR_TOKEN and LINEAR_TEAM to run the live smoke test")
	}
	cfg := config.Config{Tracker: config.TrackerConfig{
		Team: team, ClassLabelPrefix: "class:",
		States: config.StateMap{Ready: "Todo", InProgress: "In Progress", InReview: "In Review", Done: "Done", NeedsInput: "Needs Input", Blocked: "Backlog"},
	}}
	c, err := New(cfg, token, http.DefaultClient)
	if err != nil {
		t.Fatalf("New (state map): %v", err)
	}
	if _, err := c.ListReady(context.Background()); err != nil {
		t.Fatalf("ListReady: %v", err)
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/linear/`
Expected: PASS (reads parse correctly; the `var _` line proves `*Client` satisfies `scheduler.Tracker`). The integration test is excluded (no `integration` tag).

- [ ] **Step 6: Commit**

```bash
git add internal/dispatch/linear/
git commit -m "feat(dispatch/linear): ListReady/ListUnblockable/Comments + integration smoke test"
```

---

## Task 7: Wire the engine into `at-dispatch serve`

**Files:**
- Modify: `cmd/at-dispatch/main.go` (`doServe` runs the engine)
- Modify: `cmd/at-dispatch/main_test.go` (update serve tests)

**Interfaces:**
- Consumes: `config.LoadConfig`, `linear.New`, `exec.New`, `scheduler.New`, `runner.OS`.
- Produces: `serve --config` that loads+validates, then runs the poll loop until signalled.

- [ ] **Step 1: Write the failing test**

In `cmd/at-dispatch/main_test.go`, **remove `TestServeLoadsValidConfig`** (serve no longer exits 0 on a valid config — it proceeds to run the loop) and add a token-resolve-failure test that exercises the wiring up to the network boundary without dialing Linear. Keep `TestServeRequiresConfig` and `TestServeRejectsBadConfig` unchanged.

```go
func TestServeTokenResolveFailure(t *testing.T) {
	// Valid config, but the tracker token resolver command fails → serve exits 1
	// before constructing the Linear client (no network needed).
	cfg := strings.Replace(goodConfig, `token:          { command: ["true"] }`, `token:          { command: ["false"] }`, 1)
	p := writeConfig(t, cfg)
	var out, errOut bytes.Buffer
	code := run([]string{"serve", "--config", p}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Fatalf("stderr = %q; want a token-resolution error", errOut.String())
	}
}
```

(`goodConfig` and `writeConfig` already exist from the config-layer serve tests; `goodConfig` uses `token: { command: ["true"] }`.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/at-dispatch/ -run TestServe`
Expected: FAIL — `TestServeTokenResolveFailure` gets exit 0 (current `doServe` prints the summary and returns 0 without resolving the token) and `TestServeLoadsValidConfig` (if still present) also conflicts.

- [ ] **Step 3: Rewrite `doServe` in `cmd/at-dispatch/main.go`**

Replace the `doServe` function with the version below, and update the import block to add `context`, `os/signal`, `syscall`, `log`, and the new packages. The final import block:

```go
import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/aethons-tools/cove/internal/dispatch/config"
	dexec "github.com/aethons-tools/cove/internal/dispatch/exec"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/runner"
)
```

```go
func doServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the at-dispatch config file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(stderr, "at-dispatch serve: --config <path> is required")
		return 2
	}
	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-dispatch serve: %v\n", err)
		return 1
	}

	classes := make([]string, 0, len(cfg.Classes))
	for name := range cfg.Classes {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	fmt.Fprintf(stdout, "at-dispatch: config OK for %s — %d class(es): %s\n",
		cfg.Repo.Slug, len(classes), strings.Join(classes, ", "))

	// resolver: run a secret's argv on the host, return trimmed stdout (in memory).
	resolve := func(argv []string) (string, error) {
		out, err := runner.OS{}.Output(argv[0], argv[1:]...)
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(out, "\n"), nil
	}

	token, err := resolve(cfg.Tracker.Token.Command)
	if err != nil {
		fmt.Fprintf(stderr, "at-dispatch serve: resolve tracker token: %v\n", err)
		return 1
	}

	tracker, err := linear.New(cfg, token, nil)
	if err != nil {
		fmt.Fprintf(stderr, "at-dispatch serve: connect to Linear: %v\n", err)
		return 1
	}

	logger := log.New(stderr, "at-dispatch ", log.LstdFlags)
	engine := scheduler.New(cfg, tracker, dexec.New(), resolve, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("scheduler started (poll %s); Ctrl-C to stop", cfg.Tracker.PollInterval)
	_ = engine.Run(ctx) // returns ctx.Err() on signal — a clean shutdown
	logger.Printf("scheduler stopped")
	return 0
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/at-dispatch/`
Expected: PASS — `TestServeRequiresConfig` (2), `TestServeRejectsBadConfig` (1), `TestServeTokenResolveFailure` (1), plus `TestVersionPrintsStampedValue`/`TestUnknownCommandPrintsUsage`/`TestNoArgsPrintsUsage`.

Run: `go build ./cmd/... && go vet ./... && gofmt -l cmd/ internal/dispatch/`
Expected: builds; no vet errors; `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-dispatch/main.go cmd/at-dispatch/main_test.go
git commit -m "feat(at-dispatch): serve runs the scheduler engine"
```

---

## Task 8: Docs — record the scheduler in the architecture map

**Files:**
- Modify: `docs/OVERVIEW.md` (architecture map + the at-dispatch entry line)

**Interfaces:** none (documentation only).

- [ ] **Step 1: Update the architecture map**

In `docs/OVERVIEW.md`, update the `cmd/at-dispatch/` line to reflect that `serve` now runs the scheduler:

Replace:
```
cmd/at-dispatch/              at-dispatch entry: version + serve --config (loads/validates config)
```
with:
```
cmd/at-dispatch/              at-dispatch entry: version + serve --config (runs the scheduler)
```

Add these rows after the existing `internal/dispatch/config/` line:
```
internal/dispatch/scheduler/  scheduler engine (poll → claim → run command → broker) + Tracker/Executor interfaces
internal/dispatch/linear/     real Tracker: Linear GraphQL client (live calls behind the integration tag)
internal/dispatch/exec/       real Executor: headless command run with injected env + timeout
```

- [ ] **Step 2: Verify docs + full suite**

Run: `grep -n "runs the scheduler" docs/OVERVIEW.md`
Expected: the updated at-dispatch line is present.

Run: `go test ./... && go vet ./... && gofmt -l cmd/ internal/dispatch/`
Expected: all pass; gofmt clean.

- [ ] **Step 3: Commit**

```bash
git add docs/OVERVIEW.md
git commit -m "docs: record the scheduler engine + adapters in the architecture map"
```

---

## Final verification

- [ ] `go test ./...` — all packages pass (scheduler, exec, linear hermetic tests, at-dispatch, at-cove).
- [ ] `just build` — both binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3` (no new deps).
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/dispatch/` prints nothing.
- [ ] Optional live check: `LINEAR_TOKEN=… LINEAR_TEAM=AET go test -tags integration ./internal/dispatch/linear/ -run TestLive -v`.

## Notes

- **`result.json` single source** — `scheduler` consumes `config.Result`/`ReadResult`; it does not redefine the schema.
- **Graceful drain is best-effort:** on SIGINT the in-flight commands' context is cancelled and `wait()` drains them; brokering a cancelled dispatch may fail its tracker writes (the issue stays IN PROGRESS until the reaper — AET-27). Acceptable for the MVP.
- **GraphQL field names** follow Linear's public API; the `integration`-tagged `TestLive` validates them against the real endpoint. If a field name is wrong, the hermetic tests (which use matching recorded JSON) won't catch it — run `TestLive` once against a scratch team before relying on the service.
