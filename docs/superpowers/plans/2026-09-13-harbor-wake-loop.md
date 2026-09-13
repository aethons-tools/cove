# harbor comms B1 — turn loop + wake-on-reply Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A managed cove suspends on `needs-input` (reports `Waiting`, blocks) and resumes (`claude --continue`) when harbor `Wake`s it — over the live Attach stream — on a reply to its ticket, bounded by a max-wait.

**Architecture:** `internal/agentrun`'s `Run` becomes a turn loop that blocks on a wake channel between turns. A new resident `internal/wakeon` engine (wired from `cmd/at-harbor`, like the dispatcher) watches `Waiting` instances' tickets and calls the supervisor's `ControlSink.Wake` on a new comment (or tears down past max-wait). The supervisor stamps `WaitingSince` and persists a `WaitCursor` baseline.

**Tech Stack:** Go 1.25; existing `internal/{agentrun,covemaster,harbor,dispatch/linear}` + the COV-145 `linearCommenter` adapter.

## Global Constraints

- `GOPROXY=off` for go commands (deps cached; no new deps this slice).
- **Boundaries:** `internal/harbor` core stays kit/dispatch/grpc-free (the new `Instance` fields + `SetWaitCursor` are plain). `internal/wakeon` is wired from `cmd/at-harbor` (may import `internal/dispatch/*`/`internal/kit`), never imported by harbor core. `internal/agentrun` unchanged deps (covemaster + worker). `cmd/at-cove` stays oidc/grpc-free.
- **needs-input ⇒ suspend-until-reply** (no new agent-facing syntax); the wait is bounded by `MaxWait` → then Done (teardown). Spurious wakes are harmless (the agent re-reads and re-waits).
- **No container pause** (that's B2). A Waiting cove stays Live and keeps its lease via cove-master's heartbeats.
- TDD; hermetic tests (fakes; small real timers for the max-wait path, no clock injection); gofmt-clean.
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

---

### Task 1: Supervisor — `Instance` wait fields + `Report` transition + `SetWaitCursor`

**Files:** Modify `internal/harbor/instance.go`, `internal/harbor/supervisor.go`. Test: `internal/harbor/supervisor_test.go`.

**Interfaces:** Produces `Instance.WaitingSince time.Time` + `Instance.WaitCursor string`; `Supervisor.SetWaitCursor(actorID, cursor string) error`. Tasks 3/4 consume them.

- [ ] **Step 1: failing test** — add to `supervisor_test.go`:
```go
func TestReportWaitingStampsAndClearsCursor(t *testing.T) {
	// harness like the file's other Report tests (fake launcher, seeded instance)
	sup, st := newSupTest(t) // adapt to the file's existing helper
	// raise/seed an instance for actor "w1" that is Live/Running
	// ...
	must(sup.SetWaitCursor("w1", "3"))
	must(sup.Report(ctx, "w1", ActivityWaiting)) // transition Running→Waiting
	inst, _ := st.GetInstance("w1")
	if inst.WaitingSince.IsZero() { t.Fatal("WaitingSince not set on transition into Waiting") }
	if inst.WaitCursor != "" { t.Fatalf("WaitCursor should clear on transition into Waiting, got %q", inst.WaitCursor) }
	// a second Report(Waiting) while already Waiting must NOT reset WaitingSince
	first := inst.WaitingSince
	must(sup.SetWaitCursor("w1", "4"))
	must(sup.Report(ctx, "w1", ActivityWaiting))
	inst, _ = st.GetInstance("w1")
	if !inst.WaitingSince.Equal(first) { t.Fatal("WaitingSince reset while already Waiting") }
	if inst.WaitCursor != "4" { t.Fatalf("WaitCursor cleared while already Waiting, got %q", inst.WaitCursor) }
}
```
(Adapt harness/helper names to the file's existing Report tests.)

- [ ] **Step 2:** run → fail (`SetWaitCursor`/fields undefined).

- [ ] **Step 3: implement.**
`internal/harbor/instance.go` — add to `Instance` (after `LastSeen`):
```go
	WaitingSince time.Time `json:"waiting_since,omitempty"` // set when Activity enters Waiting (B1)
	WaitCursor   string    `json:"wait_cursor,omitempty"`   // opaque wake-on baseline set by the wake-on engine
```
`internal/harbor/supervisor.go` — in `Report`, before `inst.Activity = a`, capture the transition and set/clear:
```go
	enteringWaiting := a == ActivityWaiting && inst.Activity != ActivityWaiting
	// ...existing: inst.Activity = a; inst.LastSeen = now; inst.Lease = ...
	if enteringWaiting {
		inst.WaitingSince = now
		inst.WaitCursor = ""
	}
```
Add the method:
```go
// SetWaitCursor persists an opaque wake-on baseline on the instance (used by the
// wake-on engine to detect a new ticket comment). No-op semantics if the actor
// is gone.
func (s *Supervisor) SetWaitCursor(actorID, cursor string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.WaitCursor = cursor
	return s.store.PutInstance(inst)
}
```

- [ ] **Step 4:** `GOPROXY=off go test ./internal/harbor/ -v` → pass.
- [ ] **Step 5:** gofmt + commit (`harbor: Instance wait fields + Report Waiting-transition stamp + SetWaitCursor (COV-160)`).

---

### Task 2: `internal/agentrun` — the turn loop

**Files:** Modify `internal/agentrun/workload.go`. Test: `internal/agentrun/workload_test.go`.

**Interfaces:** `Config` gains `MaxWait time.Duration`; `Run` loops; `Control(Wake)` signals a wake channel.

- [ ] **Step 1: failing tests** — add to `workload_test.go` (the file already has a fake `Spawner` + a recording `Handle`; extend the fake spawner to script per-call results):
```go
// resume: needs-input turn, then a Wake, then an ok turn → two spawns, 2nd has --continue.
func TestRunResumesOnWake(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{
		results: []string{`{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`,
			`{"status":{"ok":{}}}`}, // written to worker-result.json per call
		dir: dir,
	}
	w := New(Config{WorkDir: dir, Prompt: "do it", MaxWait: time.Minute, Spawner: f}, nil)
	h := &recordHandle{}
	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background(), h) }()
	waitFor(t, func() bool { return h.count(Waiting) == 1 }) // first turn reported Waiting
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	if err := <-done; err != nil { t.Fatalf("Run: %v", err) }
	if len(f.calls) != 2 { t.Fatalf("want 2 spawns, got %d", len(f.calls)) }
	if !hasArg(f.calls[1].args, "--continue") { t.Fatalf("2nd turn missing --continue: %v", f.calls[1].args) }
}

// max-wait: needs-input, no wake → after MaxWait, Run returns nil (Done), one spawn.
func TestRunMaxWaitEndsUnit(t *testing.T) {
	dir := t.TempDir()
	f := &scriptedSpawner{results: []string{`{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`}, dir: dir}
	w := New(Config{WorkDir: dir, Prompt: "p", MaxWait: 40 * time.Millisecond, Spawner: f}, nil)
	if err := w.Run(context.Background(), &recordHandle{}); err != nil { t.Fatalf("Run: %v", err) }
	if len(f.calls) != 1 { t.Fatalf("want 1 spawn (no resume), got %d", len(f.calls)) }
}

// ctx cancel while waiting → Run returns ctx err.
func TestRunCtxCancelWhileWaiting(t *testing.T) { /* needs-input, cancel ctx, expect ctx.Err() */ }
```
Provide `scriptedSpawner` (returns a proc whose Wait writes `results[call]` into `dir/.at-task/worker-result.json` then returns nil, recording `calls[i].args`), plus small `waitFor`/`hasArg`/`recordHandle.count` helpers. Keep the existing one-shot tests (`ok`→one turn; `error`; missing-result) passing.

- [ ] **Step 2:** run → fail (`MaxWait`/loop/wake undefined).

- [ ] **Step 3: implement** — in `workload.go`:
  - `Config` add `MaxWait time.Duration`. `New`: default `MaxWait` when `<= 0` (e.g. `const defaultMaxWait = 30 * time.Minute`).
  - `Workload` add `wake chan struct{}`; `New` sets `wake: make(chan struct{}, 1)`.
  - Add the resume prompt + arg builder:
    ```go
    const resumePrompt = "New input may have arrived on your ticket — use the messaging `read` tool to fetch it, then continue the task. When finished, write .at-task/worker-result.json as before."
    func (w *Workload) claudeArgs(prompt string, continued bool) []string {
        args := []string{"-p"}
        if continued { args = append(args, "--continue") }
        return append(args, "--dangerously-skip-permissions", "--mcp-config", mcpConfigPath, "--strict-mcp-config", prompt)
    }
    ```
  - Rewrite `Run` as a loop (turn → report Running → Wait → ctx check → read result → switch): `ok`→return nil; `error`→return err; missing→return err; `needs-input`→`h.Report(Waiting)` then
    ```go
    select {
    case <-w.wake:
        prompt, continued = resumePrompt, true
        continue
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(w.cfg.MaxWait):
        w.log.Info("agentrun: max-wait elapsed; ending unit")
        return nil
    }
    ```
    (Loop variables `prompt string = w.cfg.Prompt`, `continued bool = false`, updated on wake.)
  - `Control`: `case covemaster.Wake:` → non-blocking send `select { case w.wake <- struct{}{}: default: }` (+ keep the log). Teardown case unchanged (ctx cancel from the client unwinds Run).

- [ ] **Step 4:** `GOPROXY=off go test ./internal/agentrun/ -v` → pass (new + existing).
- [ ] **Step 5:** gofmt + commit (`agentrun: turn loop — suspend on needs-input, resume on Wake, bounded by max-wait (COV-160)`).

---

### Task 3: `internal/wakeon` — the wake-on engine

**Files:** Create `internal/wakeon/wakeon.go`, `internal/wakeon/wakeon_test.go`.

**Interfaces:** `Registry`/`Cursors`/`Waker`/`Reaper`/`Commenter` (below); `New`, `(*Engine).Run`, `(*Engine).tick`.

- [ ] **Step 1: failing test** — `internal/wakeon/wakeon_test.go`: fakes for each dep. Assert per `tick`:
  - a `Waiting` instance with empty `WaitCursor` → `SetWaitCursor(actorID, "<count>")` called, **no Wake**.
  - `WaitCursor="2"`, comments count 2 → no Wake; count 3 → `Wake(actorID)`.
  - `WaitingSince` older than `MaxWait` → `Teardown(actorID)`, no Wake, no cursor.
  - a non-Waiting instance → ignored entirely.
```go
type fakeReg struct{ insts []harbor.Instance }
func (f *fakeReg) ListInstances() []harbor.Instance { return f.insts }
type fakeCur struct{ set map[string]string }
func (f *fakeCur) SetWaitCursor(a, c string) error { f.set[a] = c; return nil }
type fakeWaker struct{ woke []string }
func (f *fakeWaker) Wake(a string) { f.woke = append(f.woke, a) }
type fakeReaper struct{ down []string }
func (f *fakeReaper) Teardown(_ context.Context, a string) error { f.down = append(f.down, a); return nil }
type fakeCmt struct{ n int } // Comments returns n items
func (f *fakeCmt) IssueByIdentifier(_ context.Context, id string) (string, error) { return "iss-" + id, nil }
func (f *fakeCmt) Comments(_ context.Context, _ string) ([]harbor.Comment, error) { return make([]harbor.Comment, f.n), nil }
```

- [ ] **Step 2:** run → fail.

- [ ] **Step 3: implement** — `internal/wakeon/wakeon.go`:
```go
// Package wakeon is harbor's resident wake-on engine: it watches Waiting managed
// coves and wakes them (over the Attach ControlSink) when a reply lands on their
// ticket, or tears them down past a max-wait. Wired from cmd/at-harbor; not
// imported by internal/harbor core.
package wakeon

import (
	"context"
	"strconv"
	"time"
	"log/slog"

	"github.com/aethons-tools/cove/internal/harbor"
)

type Registry interface{ ListInstances() []harbor.Instance }
type Cursors interface{ SetWaitCursor(actorID, cursor string) error }
type Waker interface{ Wake(actorID string) }
type Reaper interface{ Teardown(ctx context.Context, actorID string) error }
type Commenter interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	Comments(ctx context.Context, issueID string) ([]harbor.Comment, error)
}

type Config struct{ PollInterval, MaxWait time.Duration }

const (
	defaultPollInterval = 15 * time.Second
	defaultMaxWait      = 30 * time.Minute
)

type Engine struct {
	reg  Registry
	cur  Cursors
	wake Waker
	reap Reaper
	cmt  Commenter
	cfg  Config
	now  func() time.Time
	log  *slog.Logger
}

func New(reg Registry, cur Cursors, wake Waker, reap Reaper, cmt Commenter, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 { cfg.PollInterval = defaultPollInterval }
	if cfg.MaxWait <= 0 { cfg.MaxWait = defaultMaxWait }
	if log == nil { log = slog.New(slog.NewTextHandler(discard{}, nil)) }
	return &Engine{reg, cur, wake, reap, cmt, cfg, time.Now, log}
}

func (e *Engine) Run(ctx context.Context) {
	e.tick(ctx)
	tk := time.NewTicker(e.cfg.PollInterval); defer tk.Stop()
	for {
		select {
		case <-ctx.Done(): return
		case <-tk.C: e.tick(ctx)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	for _, inst := range e.reg.ListInstances() {
		if inst.Activity != harbor.ActivityWaiting { continue }
		if !inst.WaitingSince.IsZero() && e.now().Sub(inst.WaitingSince) > e.cfg.MaxWait {
			if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
				e.log.Warn("wakeon: teardown (max-wait) failed", "actor", inst.ActorID, "error", err.Error())
			}
			continue
		}
		issueID, err := e.cmt.IssueByIdentifier(ctx, inst.Unit)
		if err != nil { e.log.Warn("wakeon: resolve ticket failed", "actor", inst.ActorID, "error", err.Error()); continue }
		comments, err := e.cmt.Comments(ctx, issueID)
		if err != nil { e.log.Warn("wakeon: read comments failed", "actor", inst.ActorID, "error", err.Error()); continue }
		n := len(comments)
		if inst.WaitCursor == "" { // baseline
			if err := e.cur.SetWaitCursor(inst.ActorID, strconv.Itoa(n)); err != nil {
				e.log.Warn("wakeon: set cursor failed", "actor", inst.ActorID, "error", err.Error())
			}
			continue
		}
		base, _ := strconv.Atoi(inst.WaitCursor)
		if n > base {
			e.log.Info("wakeon: reply detected, waking", "actor", inst.ActorID)
			e.wake.Wake(inst.ActorID)
		}
	}
}

type discard struct{}
func (discard) Write(p []byte) (int, error) { return len(p), nil }
```

- [ ] **Step 4:** `GOPROXY=off go test ./internal/wakeon/ -v` → pass; `GOPROXY=off go build ./...`.
- [ ] **Step 5:** gofmt + commit (`wakeon: resident wake-on engine — reply→Wake, max-wait→teardown (COV-160)`).

---

### Task 4: Wire the wake-on engine + config (`cmd/at-harbor`)

**Files:** Modify `cmd/at-harbor/config.go` (dispatcherConfig fields), `cmd/at-harbor/main.go` (cmdServe). Test: `cmd/at-harbor/config_test.go`.

- [ ] **Step 1:** add to `dispatcherConfig` (config.go): `WakePollInterval string \`yaml:"wake-poll-interval"\`` + `WaitMax string \`yaml:"wait-max"\`` (both optional; empty → engine defaults). Add a parse test asserting they land.
- [ ] **Step 2:** run config test → fail.
- [ ] **Step 3:** the wake-on engine needs both `tracker` (built inside the dispatcher block) and `rsrv` (the attach `Server` = the `ControlSink`/Waker) — but `rsrv` is currently constructed at ~main.go:855, *after* the dispatcher block (~825). **Hoist the attach-server construction above the dispatcher block:** move these four lines
  ```go
  rsrv := attach.NewServer(st, sup, log)
  sup.SetControlSink(rsrv)
  gs := grpc.NewServer()
  attachpb.RegisterRuntimeServer(gs, rsrv)
  ```
  from ~855 to just before `var httpHandler http.Handler = broker` (~824). The later `runtime.listen` dev-listener block and the :443-mux serve (which use `gs`/`rsrv`) stay where they are and still compile (built earlier now). Then, inside the `if dc := cfg.Runtime.Dispatcher; dc != nil` block, after `disp`/`msgH` (reusing `tracker`), add:
```go
	wpoll, _ := time.ParseDuration(dc.WakePollInterval) // "" → 0 → engine default
	wmax, _ := time.ParseDuration(dc.WaitMax)
	eng := wakeon.New(st, sup, rsrv /*ControlSink Waker*/, sup, linearCommenter{tracker}, wakeon.Config{PollInterval: wpoll, MaxWait: wmax}, log)
	go eng.Run(context.Background())
	log.Info("harbor wake-on engine: resident", "wait-max", wmax)
```
`st`=Registry; `sup`=Cursors+Reaper; `rsrv`=Waker. Add the `internal/wakeon` import. Verify the hoist didn't break the mux/dev-listener wiring (`go build` + existing `cmd/at-harbor` tests).
- [ ] **Step 4:** `GOPROXY=off go build ./... && GOPROXY=off go test ./cmd/at-harbor/ ./internal/wakeon/ -v`; boundary `go list -deps ./internal/harbor | grep -iE 'internal/wakeon|internal/dispatch|internal/kit|grpc' || echo "harbor core clean"`; `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo "at-cove clean"`.
- [ ] **Step 5:** gofmt + commit (`at-harbor: run the wake-on engine when a tracker is configured (COV-160)`).

---

### Task 5: Docs

**Files:** Modify `docs/usage/harbor/messaging.md`, `docs/usage/harbor/coves.md`.

- [ ] **Step 1:** `messaging.md` — add a "Waiting for a reply" note: a cove that reports `needs-input` now **suspends** (Activity `waiting`) instead of ending; harbor's wake-on engine watches the ticket and **wakes** it (`claude --continue`) when a new comment arrives; a `wait-max` bounds the wait (then the cove is torn down). No container pause yet (B2). Mention `wake-poll-interval`/`wait-max` config.
- [ ] **Step 2:** `coves.md` — one line: a managed cove's lifecycle is now run → (needs-input) wait → wake/resume → done, not strictly one-shot.
- [ ] **Step 3:** docs-audit delta (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md` — no new errors referencing the harbor usage docs).
- [ ] **Step 4:** commit (`docs: wake-on wait/resume lifecycle (COV-160)`).

---

## Self-Review

- **Spec coverage:** Instance fields + Report transition + SetWaitCursor (T1); agentrun turn loop + wake/max-wait (T2); wakeon engine (T3); wiring + config (T4); docs (T5). All map.
- **Type consistency:** `Supervisor.SetWaitCursor`/`Teardown` (Cursors/Reaper); `attach.Server.Wake` (Waker); `harbor.Store.ListInstances` (Registry); `linearCommenter` satisfies `wakeon.Commenter` (IssueByIdentifier + Comments already used by messaging); `agentrun.Config.MaxWait` + wake channel consistent T2.
- **Placeholder scan:** none — code steps carry full code; test helpers (`scriptedSpawner`, `waitFor`) are described with their contract and mirror the existing agentrun test fakes.
- **Restart-safety:** the wake baseline is persisted in `Instance.WaitCursor` (not in-memory); max-wait is measured from persisted `WaitingSince` — both survive a harbor restart.
- **Boundary:** `internal/wakeon` is new + wired from cmd; harbor core gains only plain fields + one method (no wakeon/dispatch/kit import).
