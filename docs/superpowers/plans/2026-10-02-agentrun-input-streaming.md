# agentrun Input Streaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the cove agent as a live `claude -p --input-format stream-json` process that receives Attach Wakes on stdin and is closed only when its turn has ended and all background tasks are done.

**Architecture:** The spawner exposes the child's stdin. A pure, mutex-guarded `idleTracker` reads claude's stream-json stdout and decides when a turn is over, whether background tasks are outstanding, and whether a coalesced Wake is pending. `Workload.Run` runs one *episode* (one claude process) per loop iteration through a select loop over tracker changes, Wakes, a background-wait timer, process exit and ctx; after exit the existing worker-result / resident / needs-input logic runs unchanged. The admin session UI's totals are fixed for multi-result episodes.

**Tech Stack:** Go stdlib (`os/exec`, `encoding/json`, `sync`, `time`), `html/template` for the session totals fragment.

**Spec:** `docs/superpowers/specs/2026-10-02-agentrun-input-streaming-design.md`

## Global Constraints

- No change to `attach.proto`, `internal/covemaster`, or the Jam supervisor; `covemaster.Workload`/`Handle` are unchanged.
- `Wake` stays payload-free. Resume prompts are the existing `resumePrompt` / `residentResumePrompt` constants.
- Argv: `-p [--continue] --input-format stream-json --output-format stream-json --verbose --dangerously-skip-permissions --mcp-config <path> --strict-mcp-config` — the prompt is **not** in argv.
- `Config.BackgroundWait` default **30m**.
- Idle tracker line cap **64 MiB** (`trackerMaxLine = 64 << 20`); the session-event splitter keeps its 1 MiB cap.
- `SessionEvent.turn` stays the per-spawn counter (= episode). No code change to it.
- Tests are hermetic (fake spawners; the only real processes are `sh` in `spawner_test.go`). TDD: failing test first.
- Never log prompt text, stdin contents, or env values.
- Docs updated in the same branch (AGENTS.md rule).
- Run Go with the repo's toolchain settings (`docs/DEVELOPMENT.md`); `go test ./internal/agentrun/` takes ~30s.

---

### Task 1: Spawner exposes stdin

**Files:**
- Modify: `internal/agentrun/spawner.go`
- Modify: `internal/agentrun/workload_test.go` (fake `scriptedProc` gains `Input`)
- Test: `internal/agentrun/spawner_test.go`

**Interfaces:**
- Produces: `Process` interface now `{ Wait() error; Input() io.WriteCloser }`. Test helper `discardInput` (in `workload_test.go`) for fakes that ignore stdin.

- [ ] **Step 1: Write the failing tests** — append to `internal/agentrun/spawner_test.go`:

```go
func TestExecSpawnerStdinRoundTrip(t *testing.T) {
	needSh(t)
	var buf bytes.Buffer
	p, err := execSpawner{grace: time.Second}.Spawn(context.Background(), "sh", []string{"-c", "cat"}, "", nil, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(p.Input(), "hello\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := p.Input().Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if buf.String() != "hello\n" {
		t.Fatalf("stdout got %q", buf.String())
	}
}

// A child that exits on its own while we still hold stdin open must not make
// Wait hang (StdinPipe is closed by Wait, unlike a copied io.Reader).
func TestExecSpawnerExitWithStdinOpen(t *testing.T) {
	needSh(t)
	p, err := execSpawner{grace: 5 * time.Second}.Spawn(context.Background(), "sh", []string{"-c", "exit 0"}, "", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait hung with stdin still open")
	}
	if _, err := io.WriteString(p.Input(), "late\n"); err == nil {
		t.Fatal("write after exit: want error, got nil")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/agentrun/ -run 'TestExecSpawner(StdinRoundTrip|ExitWithStdinOpen)'`
Expected: build failure — `p.Input undefined (type Process has no field or method Input)`.

- [ ] **Step 3: Implement** — in `internal/agentrun/spawner.go` replace the `Process` interface, `execProcess`, and the start of `Spawn`:

```go
// Process is a started agent process. Wait blocks until it exits, returning the
// process's exit error (nil on exit 0). If ctx is cancelled, the runtime sends
// SIGTERM then SIGKILL (after grace) and Wait returns a non-nil error. Input is
// the process's stdin: closing it signals EOF; a write after the process has
// exited fails.
type Process interface {
	Wait() error
	Input() io.WriteCloser
}

type execProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}
```

In `Spawn`, after `cmd.Stderr = os.Stderr` and before `cmd.Start()`:

```go
	// StdinPipe (not cmd.Stdin = reader): Wait closes it once the child exits,
	// so a child that exits on its own never leaves Wait blocked on a copy.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProcess{cmd: cmd, stdin: stdin}, nil
}

func (p execProcess) Wait() error            { return p.cmd.Wait() }
func (p execProcess) Input() io.WriteCloser { return p.stdin }
```

(Delete the old `if err := cmd.Start()…return execProcess{cmd: cmd}, nil` and the old one-line `Wait`.)

In `internal/agentrun/workload_test.go`, below `func (p scriptedProc) Wait() error { return p.wait() }`, add:

```go
func (p scriptedProc) Input() io.WriteCloser {
	if p.in != nil {
		return p.in
	}
	return discardInput{}
}

// discardInput is stdin for fakes that ignore it.
type discardInput struct{}

func (discardInput) Write(b []byte) (int, error) { return len(b), nil }
func (discardInput) Close() error                { return nil }
```

and change `type scriptedProc struct{ wait func() error }` to:

```go
// scriptedProc runs a closure as its Wait; in (optional) records stdin.
type scriptedProc struct {
	wait func() error
	in   io.WriteCloser
}
```

- [ ] **Step 4: Run all agentrun tests**

Run: `go test ./internal/agentrun/`
Expected: `ok` (existing workload tests still pass — Run doesn't use Input yet).

- [ ] **Step 5: Commit**

```bash
git add internal/agentrun/spawner.go internal/agentrun/spawner_test.go internal/agentrun/workload_test.go
git commit -m "feat(agentrun): spawner exposes the agent's stdin"
```

---

### Task 2: Idle tracker

**Files:**
- Create: `internal/agentrun/idle.go`
- Test: `internal/agentrun/idle_test.go`

**Interfaces:**
- Produces (all unexported, package `agentrun`):
  - `type idleAction int` with `actWait`, `actDeliverWake`, `actHold`, `actClose`
  - `func newIdleTracker(warn func(msg string, args ...any)) *idleTracker` — starts **busy** (the prompt is about to be written)
  - `(*idleTracker).Observe(line []byte)` — one stream-json stdout line
  - `(*idleTracker).Wrote()` — we wrote a user message to stdin
  - `(*idleTracker).Wake() (deliverNow bool)` — false → coalesced into `pendingWake`
  - `(*idleTracker).Next() (idleAction, []string)` — `actDeliverWake` clears `pendingWake` and marks busy; `[]string` = sorted descriptions of outstanding + awaiting-notification tasks for `actHold`
  - A task that leaves the `background_tasks_changed` list stays **awaiting** until its `task_notification` (matched by `task_id`) — claude empties the list *before* notifying
  - `(*idleTracker).PendingWake() bool`
  - field `changed chan struct{}` (cap 1) signalled on every state change from `Observe`
  - `const trackerMaxLine = 64 << 20`

- [ ] **Step 1: Write the failing tests** — create `internal/agentrun/idle_test.go`:

```go
package agentrun

import (
	"slices"
	"testing"
)

// Lines below mirror real Claude Code 2.1.284 stream-json output (trimmed to
// the fields the tracker reads).
const (
	lnInit      = `{"type":"system","subtype":"init","session_id":"s"}`
	lnAssistant = `{"type":"assistant","message":{"content":[]}}`
	lnToolRes   = `{"type":"user","message":{"content":[]}}`
	lnResult    = `{"type":"result","subtype":"success","queued_turn_count":0,"terminal_reason":"completed"}`
	lnResultQ1  = `{"type":"result","subtype":"success","queued_turn_count":1}`
	lnTasks1    = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b1","task_type":"local_bash","description":"Sleep 25 seconds"}]}`
	lnTasks0    = `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`
	lnStarted   = `{"type":"system","subtype":"task_started","task_id":"b1","is_backgrounded":true}`
	lnUpdated   = `{"type":"system","subtype":"task_updated","task_id":"b1","patch":{"status":"completed"}}`
	lnNotify    = `{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed"}`
	lnRate      = `{"type":"rate_limit_event"}`
)

func feed(tr *idleTracker, lines ...string) {
	for _, l := range lines {
		tr.Observe([]byte(l))
	}
}

func wantAct(t *testing.T, tr *idleTracker, want idleAction) []string {
	t.Helper()
	got, tasks := tr.Next()
	if got != want {
		t.Fatalf("Next() = %v, want %v", got, want)
	}
	return tasks
}

func TestIdleTrackerStartsBusy(t *testing.T) {
	tr := newIdleTracker(nil)
	wantAct(t, tr, actWait)
}

func TestIdleTrackerPlainTurnThenIdle(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit, lnAssistant, lnToolRes, lnAssistant, lnRate)
	wantAct(t, tr, actWait)
	feed(tr, lnResult)
	wantAct(t, tr, actClose)
}

func TestIdleTrackerQueuedTurnStaysBusy(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit, lnResultQ1)
	wantAct(t, tr, actWait)
	feed(tr, lnInit, lnResult)
	wantAct(t, tr, actClose)
}

// The verified background sequence: result while a task runs → hold; task
// completes and claude self-starts a turn → not idle until that turn's result.
func TestIdleTrackerBackgroundTaskSequence(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit, lnAssistant, lnTasks1, lnStarted, lnToolRes, lnAssistant, lnResult)
	if tasks := wantAct(t, tr, actHold); !slices.Equal(tasks, []string{"Sleep 25 seconds"}) {
		t.Fatalf("hold tasks = %v", tasks)
	}
	// claude empties the task list BEFORE the completion notification; the
	// task is then awaiting its notification, so we must still hold.
	feed(tr, lnTasks0, lnUpdated)
	if tasks := wantAct(t, tr, actHold); !slices.Equal(tasks, []string{"Sleep 25 seconds"}) {
		t.Fatalf("awaiting-notification tasks = %v", tasks)
	}
	feed(tr, lnNotify) // starts the self-started turn
	wantAct(t, tr, actWait)
	feed(tr, lnInit, lnAssistant, lnResult)
	wantAct(t, tr, actClose)
}

// A task that leaves the list but never gets a notification keeps the episode
// on hold (the BackgroundWait cap is the backstop), never closes early.
func TestIdleTrackerMissingNotificationHolds(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit, lnTasks1, lnResult, lnTasks0)
	wantAct(t, tr, actHold)
}

// A notification for a task we never saw listed must not wedge anything.
func TestIdleTrackerUnknownNotification(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit, lnNotify, lnResult)
	wantAct(t, tr, actClose)
}

func TestIdleTrackerWakeWhileBusyIsCoalesced(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit)
	for i := 0; i < 3; i++ {
		if tr.Wake() {
			t.Fatal("Wake while busy must not deliver now")
		}
	}
	if !tr.PendingWake() {
		t.Fatal("PendingWake = false after Wake while busy")
	}
	feed(tr, lnResult)
	wantAct(t, tr, actDeliverWake)
	if tr.PendingWake() {
		t.Fatal("pending wake not cleared by actDeliverWake")
	}
	wantAct(t, tr, actWait) // delivering marked us busy
	feed(tr, lnInit, lnResult)
	wantAct(t, tr, actClose) // exactly one delivery for three wakes
}

func TestIdleTrackerPendingWakeBeatsHold(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit, lnTasks1)
	tr.Wake()
	feed(tr, lnResult)
	wantAct(t, tr, actDeliverWake)
}

func TestIdleTrackerWakeDuringHoldDeliversNow(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnInit, lnTasks1, lnResult)
	wantAct(t, tr, actHold)
	if !tr.Wake() {
		t.Fatal("Wake between turns must deliver now")
	}
	tr.Wrote()
	wantAct(t, tr, actWait)
}

func TestIdleTrackerUnparseableLineIgnored(t *testing.T) {
	var warned int
	tr := newIdleTracker(func(string, ...any) { warned++ })
	feed(tr, lnInit, lnResult)
	tr.Observe([]byte("not json"))
	tr.Observe(nil)
	wantAct(t, tr, actClose)
	if warned != 1 {
		t.Fatalf("warned %d times, want 1 (empty line is silent)", warned)
	}
}

func TestIdleTrackerSignalsChange(t *testing.T) {
	tr := newIdleTracker(nil)
	feed(tr, lnResult)
	select {
	case <-tr.changed:
	default:
		t.Fatal("no change signal after a result")
	}
	feed(tr, lnRate) // irrelevant line: no signal
	select {
	case <-tr.changed:
		t.Fatal("signalled on an irrelevant line")
	default:
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/agentrun/ -run TestIdleTracker`
Expected: build failure — `undefined: newIdleTracker`.

- [ ] **Step 3: Implement** — create `internal/agentrun/idle.go`:

```go
package agentrun

import (
	"encoding/json"
	"slices"
	"sync"
)

// trackerMaxLine caps one stdout line the idle tracker parses. Far above the
// session-event cap so a large result line is never truncated out of
// recognition.
const trackerMaxLine = 64 << 20

// idleAction is what the episode loop should do after the tracker changes.
type idleAction int

const (
	actWait        idleAction = iota // a turn is in progress
	actDeliverWake                   // turn over, a coalesced Wake is pending: write one resume prompt
	actHold                          // turn over, background tasks still outstanding
	actClose                         // idle: close stdin
)

// idleTracker follows claude's stream-json stdout to decide when the agent
// is truly idle: its turn has ended (a result with nothing queued) AND no
// background task is outstanding. Wakes arriving mid-turn are coalesced into
// one pending resume. Observe runs on the stdout copy goroutine; everything
// else on Run's goroutine — hence the mutex. It never blocks the stdout path.
type idleTracker struct {
	mu          sync.Mutex
	busy bool
	// tasks are the outstanding background tasks (id → description), per the
	// latest background_tasks_changed snapshot. awaiting holds tasks that left
	// that list but whose task_notification has not arrived yet: claude empties
	// the list BEFORE notifying, and the notification starts a turn.
	tasks, awaiting map[string]string
	pendingWake     bool
	changed     chan struct{} // cap 1; signalled on every state change from Observe
	warn        func(msg string, args ...any)
}

func newIdleTracker(warn func(msg string, args ...any)) *idleTracker {
	if warn == nil {
		warn = func(string, ...any) {}
	}
	return &idleTracker{busy: true, tasks: map[string]string{}, awaiting: map[string]string{},
		changed: make(chan struct{}, 1), warn: warn}
}

type trackedEvent struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	TaskID          string `json:"task_id"`
	QueuedTurnCount int    `json:"queued_turn_count"`
	Tasks           []struct {
		TaskID      string `json:"task_id"`
		Description string `json:"description"`
	} `json:"tasks"`
}

// Observe updates state from one stdout line.
func (t *idleTracker) Observe(line []byte) {
	if len(line) == 0 {
		return
	}
	var ev trackedEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		t.warn("agentrun: idle tracker ignored an unparseable stdout line", "err", err.Error())
		return
	}
	t.mu.Lock()
	switch {
	case ev.Type == "result":
		if ev.QueuedTurnCount == 0 {
			t.busy = false
		}
	case ev.Type == "system" && ev.Subtype == "background_tasks_changed":
		next := make(map[string]string, len(ev.Tasks))
		for _, k := range ev.Tasks {
			next[k.TaskID] = k.Description
		}
		for id, d := range t.tasks {
			if _, still := next[id]; !still {
				t.awaiting[id] = d
			}
		}
		t.tasks = next
	case ev.Type == "system" && ev.Subtype == "task_notification":
		delete(t.awaiting, ev.TaskID)
		t.busy = true
	case ev.Type == "system" && ev.Subtype == "init",
		ev.Type == "assistant", ev.Type == "user":
		t.busy = true
	default:
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// Wrote records that a user message was written to stdin.
func (t *idleTracker) Wrote() {
	t.mu.Lock()
	t.busy = true
	t.mu.Unlock()
}

// Wake reports whether a Wake can be delivered now (claude is between turns);
// otherwise it is coalesced into the pending wake.
func (t *idleTracker) Wake() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy {
		t.pendingWake = true
		return false
	}
	return true
}

// Next returns the action for the current state. actDeliverWake consumes the
// pending wake and marks the tracker busy (the caller writes the prompt).
func (t *idleTracker) Next() (idleAction, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.busy:
		return actWait, nil
	case t.pendingWake:
		t.pendingWake, t.busy = false, true
		return actDeliverWake, nil
	case len(t.tasks)+len(t.awaiting) > 0:
		var descs []string
		for _, d := range t.tasks {
			descs = append(descs, d)
		}
		for _, d := range t.awaiting {
			descs = append(descs, d)
		}
		slices.Sort(descs)
		return actHold, descs
	default:
		return actClose, nil
	}
}

// PendingWake reports whether a coalesced wake is still undelivered.
func (t *idleTracker) PendingWake() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingWake
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agentrun/ -run TestIdleTracker -v`
Expected: all `TestIdleTracker*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentrun/idle.go internal/agentrun/idle_test.go
git commit -m "feat(agentrun): idle tracker over claude's stream-json output"
```

---

### Task 3: Episode loop — prompt on stdin, coalesced wakes, close when idle

**Files:**
- Modify: `internal/agentrun/workload.go` (argv, `Config.BackgroundWait`, episode loop, package-level doc of `Run`)
- Modify: `internal/agentrun/spawner.go:1-5` (package comment)
- Modify: `internal/agentrun/workload_test.go` (argv/prompt assertions; scripted stdin recording)
- Create: `internal/agentrun/episode_test.go`

**Interfaces:**
- Consumes: `Process.Input()` (Task 1); `newIdleTracker`, `Observe`, `Wrote`, `Wake`, `Next`, `PendingWake`, `changed`, `trackerMaxLine`, `act*` (Task 2).
- Produces: `Config.BackgroundWait time.Duration`; `const defaultBackgroundWait = 30 * time.Minute`; `func userMessage(text string) []byte`; `func (w *Workload) episode(ctx context.Context, proc Process, tr *idleTracker, prompt string) error`.

- [ ] **Step 1: Write the failing episode tests** — create `internal/agentrun/episode_test.go`:

```go
package agentrun

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// pipeInput records stream-json user messages written to the agent's stdin.
type pipeInput struct {
	msgs   chan string
	closed chan struct{}
	once   sync.Once
}

func newPipeInput() *pipeInput {
	return &pipeInput{msgs: make(chan string, 16), closed: make(chan struct{})}
}

func (p *pipeInput) Write(b []byte) (int, error) {
	select {
	case <-p.closed:
		return 0, os.ErrClosed
	default:
	}
	var m struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Type != "user" || m.Message.Role != "user" || b[len(b)-1] != '\n' {
		return 0, io.ErrShortWrite // malformed: surface as a test failure via next()
	}
	p.msgs <- m.Message.Content
	return len(b), nil
}

func (p *pipeInput) Close() error { p.once.Do(func() { close(p.closed) }); return nil }

func (p *pipeInput) next(t *testing.T) string {
	t.Helper()
	select {
	case m := <-p.msgs:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no stdin message")
		return ""
	}
}

func (p *pipeInput) noMessage(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-p.msgs:
		t.Fatalf("unexpected stdin message %q", m)
	case <-time.After(d):
	}
}

func (p *pipeInput) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-p.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("stdin not closed")
	}
}

func (p *pipeInput) staysOpen(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-p.closed:
		t.Fatal("stdin closed too early")
	case <-time.After(d):
	}
}

// streamProc is an interactive fake claude: the test emits stdout lines and
// decides when it exits.
type streamProc struct {
	args   []string
	in     *pipeInput
	stdout io.Writer
	exit   chan error
}

func (p *streamProc) Wait() error            { return <-p.exit }
func (p *streamProc) Input() io.WriteCloser { return p.in }

func (p *streamProc) emit(lines ...string) {
	for _, l := range lines {
		io.WriteString(p.stdout, l+"\n")
	}
}

type streamSpawner struct{ procs chan *streamProc }

func newStreamSpawner() *streamSpawner { return &streamSpawner{procs: make(chan *streamProc, 4)} }

func (s *streamSpawner) Spawn(ctx context.Context, _ string, args []string, _ string, _ []string, stdout io.Writer) (Process, error) {
	p := &streamProc{args: append([]string(nil), args...), in: newPipeInput(), stdout: stdout, exit: make(chan error, 2)}
	go func() { <-ctx.Done(); p.exit <- ctx.Err() }()
	s.procs <- p
	return p, nil
}

func (s *streamSpawner) next(t *testing.T) *streamProc {
	t.Helper()
	select {
	case p := <-s.procs:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("no spawn")
		return nil
	}
}

func streamWL(t *testing.T, dir string, s *streamSpawner, mut func(*Config)) *Workload {
	t.Helper()
	cfg := Config{WorkDir: dir, Prompt: "do it", MaxWait: time.Minute, MCPConfigPath: mcpConfigFile(t, dir), Spawner: s}
	if mut != nil {
		mut(&cfg)
	}
	return New(cfg, nil)
}

func TestEpisodePromptOnStdinAndCloseWhenIdle(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	done := runAsync(context.Background(), w, &recordHandle{})
	p := s.next(t)
	if hasArg(p.args, "do it") {
		t.Fatalf("prompt must not be in argv: %v", p.args)
	}
	if got := p.in.next(t); got != "do it" {
		t.Fatalf("first stdin message = %q", got)
	}
	p.emit(lnInit, lnAssistant)
	p.in.staysOpen(t, 50*time.Millisecond)
	p.emit(lnResult)
	p.in.waitClosed(t)
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestEpisodeHoldsStdinUntilBackgroundTasksDone(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	h := &recordHandle{}
	done := runAsync(context.Background(), w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnStarted, lnResult)
	p.in.staysOpen(t, 50*time.Millisecond)
	p.emit(lnTasks0, lnUpdated, lnNotify)
	p.in.staysOpen(t, 50*time.Millisecond)
	p.emit(lnInit, lnAssistant, lnResult)
	p.in.waitClosed(t)
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range h.eventList() {
		if ev.turn != 1 {
			t.Fatalf("every event of one episode is turn 1; got %+v", ev)
		}
	}
}

func TestEpisodeCoalescesWakesDuringTurn(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	done := runAsync(context.Background(), w, &recordHandle{})
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	for i := 0; i < 3; i++ {
		w.Control(covemaster.Control{Kind: covemaster.Wake})
		time.Sleep(5 * time.Millisecond)
	}
	p.in.noMessage(t, 50*time.Millisecond) // mid-turn: held
	p.emit(lnResult)
	if got := p.in.next(t); got != resumePrompt {
		t.Fatalf("delivered %q, want resumePrompt", got)
	}
	p.in.noMessage(t, 50*time.Millisecond) // three wakes → one prompt
	p.emit(lnInit, lnResult)
	p.in.waitClosed(t)
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestEpisodeWakeDuringHoldDeliveredNow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resident bool
		want     string
	}{{"dispatched", false, resumePrompt}, {"resident", true, residentResumePrompt}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := newStreamSpawner()
			w := streamWL(t, dir, s, func(c *Config) { c.Resident = tc.resident })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := runAsync(ctx, w, &recordHandle{})
			p := s.next(t)
			p.in.next(t)
			p.emit(lnInit, lnTasks1, lnResult)
			p.in.staysOpen(t, 30*time.Millisecond)
			w.Control(covemaster.Control{Kind: covemaster.Wake})
			if got := p.in.next(t); got != tc.want {
				t.Fatalf("delivered %q, want %q", got, tc.want)
			}
			cancel()
			<-done
		})
	}
}

func TestEpisodeBackgroundWaitCapClosesStdin(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.BackgroundWait = 50 * time.Millisecond })
	done := runAsync(context.Background(), w, &recordHandle{})
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult)
	p.in.waitClosed(t)
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A wake coalesced mid-turn must not be lost when the process exits before
// delivering it: a needs-input outcome resumes at once.
func TestEpisodePendingWakeSurvivesExit(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	h := &recordHandle{}
	done := runAsync(context.Background(), w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	p.in.noMessage(t, 30*time.Millisecond)
	writeResult(t, dir, `{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`)
	p.exit <- nil // crashed mid-turn, wake undelivered
	p2 := s.next(t)
	if !hasArg(p2.args, "--continue") {
		t.Fatalf("resume spawn missing --continue: %v", p2.args)
	}
	if got := p2.in.next(t); got != resumePrompt {
		t.Fatalf("resume prompt = %q", got)
	}
	p2.emit(lnInit, lnResult)
	p2.in.waitClosed(t)
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	p2.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestUserMessageIsOneStreamJSONLine(t *testing.T) {
	b := userMessage("line1\nline2 \"q\"")
	if b[len(b)-1] != '\n' || bytesCount(b, '\n') != 1 {
		t.Fatalf("not a single line: %q", b)
	}
	var m struct {
		Type    string `json:"type"`
		Message struct{ Role, Content string } `json:"message"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Type != "user" || m.Message.Role != "user" || m.Message.Content != "line1\nline2 \"q\"" {
		t.Fatalf("decoded %+v (%v)", m, err)
	}
}

func bytesCount(b []byte, c byte) int {
	n := 0
	for _, x := range b {
		if x == c {
			n++
		}
	}
	return n
}
```

- [ ] **Step 2: Update existing assertions that encode the old argv** — in `internal/agentrun/workload_test.go`:

`TestRunSpawnArgs`: replace the `want` line with

```go
	want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--mcp-config", mcp, "--strict-mcp-config"}
```

and give the fake a recording stdin, asserting the prompt arrives there. Replace the `f := &fakeSpawner{…}` line in that test with:

```go
	in := newPipeInput()
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }, in: in}}
```

and after the `f.dir` check add:

```go
	if got := in.next(t); got != "do the thing" {
		t.Errorf("stdin prompt = %q", got)
	}
```

`TestRunResumesOnWake`: replace the argv-prefix check with

```go
	if got := strings.Join(f.calls[1].args[:6], " "); got != "-p --continue --input-format stream-json --output-format stream-json" {
		t.Fatalf("2nd turn argv prefix = %q", got)
	}
```

`scriptedSpawner`: record stdin per call. Replace `scriptedCall` and the first lines of `Spawn`:

```go
// scriptedCall records one Spawn call's arguments and its stdin.
type scriptedCall struct {
	bin, dir string
	args     []string
	env      []string
	in       *pipeInput
}

func (f *scriptedSpawner) Spawn(_ context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error) {
	in := newPipeInput()
	f.mu.Lock()
	i := len(f.calls)
	f.calls = append(f.calls, scriptedCall{bin: bin, args: append([]string(nil), args...), dir: dir, env: append([]string(nil), env...), in: in})
	f.mu.Unlock()
	return scriptedProc{in: in, wait: func() error {
```

(the rest of the `wait` closure is unchanged).

`TestResidentResumesOnWake`: replace the two prompt assertions (which read `args[len(args)-1]`) with stdin checks:

```go
	if hasArg(f.calls[0].args, "--continue") {
		t.Fatalf("1st turn: want no --continue, got %v", f.calls[0].args)
	}
	if got := f.calls[0].in.next(t); got != "p" {
		t.Fatalf("1st turn prompt = %q; want the original prompt", got)
	}
	for _, c := range f.calls[1:] {
		if !hasArg(c.args, "--continue") {
			t.Fatalf("resumed turn missing --continue: %v", c.args)
		}
		if got := c.in.next(t); got != residentResumePrompt {
			t.Fatalf("resumed turn prompt = %q; want residentResumePrompt", got)
		}
	}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/agentrun/`
Expected: build failure — `undefined: userMessage` / `unknown field BackgroundWait`.

- [ ] **Step 4: Implement** — in `internal/agentrun/workload.go`:

Add `"encoding/json"` to imports. Below `defaultMaxWait`:

```go
// defaultBackgroundWait bounds how long an episode holds stdin open after the
// agent's turn ended while background tasks are still running.
const defaultBackgroundWait = 30 * time.Minute
```

In `Config`, after `MaxWait`:

```go
	// BackgroundWait bounds how long stdin stays open after a turn ends with
	// background tasks outstanding; then stdin is closed and claude stops them.
	// Default 30m.
	BackgroundWait time.Duration
```

In `New`, after the `MaxWait` default:

```go
	if cfg.BackgroundWait <= 0 {
		cfg.BackgroundWait = defaultBackgroundWait
	}
```

Replace `claudeArgs`:

```go
// claudeArgs builds claude's argv for one episode. continued prepends
// --continue, used for every episode after a resume-on-wake. The prompt is
// not in argv: it is the first stream-json message on stdin.
func (w *Workload) claudeArgs(continued bool) []string {
	args := []string{"-p"}
	if continued {
		args = append(args, "--continue")
	}
	// stream-json stdout is the session event source (see docs/usage/jam/session-events.md).
	args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	return append(args, "--dangerously-skip-permissions", "--mcp-config", w.cfg.MCPConfigPath, "--strict-mcp-config")
}

// userMessage encodes text as one stream-json stdin line.
func userMessage(text string) []byte {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Message msg    `json:"message"`
	}{"user", msg{"user", text}})
	return append(b, '\n')
}

// resumeText is the prompt a Wake delivers.
func (w *Workload) resumeText() string {
	if w.cfg.Resident {
		return residentResumePrompt
	}
	return resumePrompt
}
```

Replace the `Run` doc comment's first paragraph with:

```go
// Run runs the agent as a sequence of episodes. An episode is one claude
// process fed stream-json on stdin: the prompt first, then one coalesced
// resume prompt per batch of Wakes. Stdin is closed only when the agent's turn
// has ended and no background task is outstanding (or BackgroundWait elapsed),
// so claude's backgrounding works. After the process exits, its worker-result
// maps to Activity as before: a needs-input episode reports Waiting and blocks
// until a Wake resumes it (a new episode with --continue) or MaxWait elapses
// (Run then returns nil, ending the unit). Returning nil or an error both lead
// the client to report Done. In resident mode (personal sessions) every
// episode ends in Waiting and only a Wake or ctx cancel moves the loop on.
```

Inside `Run`'s `for` loop, replace from `args := w.claudeArgs(prompt, continued)` through `waitErr := proc.Wait()` / `split.Flush()` with:

```go
		args := w.claudeArgs(continued)
		turn++
		t := turn
		split := &lineSplitter{max: maxEventLine, emit: func(line []byte, dropped uint64) { h.Event(t, line, dropped) }}
		tr := newIdleTracker(func(msg string, a ...any) { w.log.Warn(msg, a...) })
		trSplit := &lineSplitter{max: trackerMaxLine, emit: func(line []byte, dropped uint64) {
			if dropped > 0 {
				w.log.Warn("agentrun: stdout line over the idle tracker cap ignored", "dropped", dropped)
				return
			}
			tr.Observe(line)
		}}
		sinks := []io.Writer{split, trSplit}
		if out != nil {
			sinks = append([]io.Writer{out}, sinks...)
		}
		sink := io.MultiWriter(sinks...)
		var env []string
		if w.conn != nil {
			var fp string
			var changed bool
			env, fp, changed = w.conn.prepare(ctx)
			if changed {
				h.ConnectorApplied(fp)
			}
		}
		proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir, env, sink)
		if err != nil {
			return fmt.Errorf("agentrun: start claude: %w", err)
		}
		h.Report(covemaster.Running)
		w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir, "continued", continued)

		waitErr := w.episode(ctx, proc, tr, prompt)
		split.Flush()
		if tr.PendingWake() {
			// Coalesced mid-turn but never delivered (the process exited first):
			// hand it to the post-exit wait so it resumes at once.
			select {
			case w.wake <- struct{}{}:
			default:
			}
		}
```

(The `var sink io.Writer = split / if out != nil {…MultiWriter}` block it replaces is removed.)

Add the episode method below `Run`:

```go
// episode drives one claude process: writes prompt, then reacts to tracker
// changes, Wakes, the background-wait timer and exit until the process exits.
// It returns the process's exit error.
func (w *Workload) episode(ctx context.Context, proc Process, tr *idleTracker, prompt string) error {
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	in := proc.Input()
	open := true
	closeInput := func(why string) {
		if !open {
			return
		}
		open = false
		w.log.Info("agentrun: closing agent stdin", "why", why)
		_ = in.Close()
	}
	write := func(text string) {
		if _, err := in.Write(userMessage(text)); err != nil {
			// The process is gone (EPIPE); its exit reports the real outcome.
			w.log.Warn("agentrun: write to agent stdin failed; awaiting exit", "err", err.Error())
			closeInput("stdin write failed")
		}
	}
	var hold *time.Timer
	var holdC <-chan time.Time
	stopHold := func() {
		if hold != nil {
			hold.Stop()
			hold, holdC = nil, nil
		}
	}
	defer stopHold()

	write(prompt) // the tracker starts busy
	resume := w.resumeText()
	for {
		var wake <-chan struct{}
		if open {
			wake = w.wake // once stdin is closed, Wakes stay buffered for the post-exit wait
		}
		select {
		case err := <-exited:
			return err
		case <-ctx.Done():
			return <-exited // CommandContext SIGTERM/SIGKILLs the process
		case <-wake:
			if tr.Wake() {
				write(resume)
				tr.Wrote()
			}
		case <-tr.changed:
		case <-holdC:
			_, tasks := tr.Next()
			w.log.Warn("agentrun: background-wait elapsed with tasks outstanding; closing stdin (claude stops them)",
				"wait", w.cfg.BackgroundWait.String(), "tasks", tasks)
			holdC = nil
			closeInput("background-wait elapsed")
		}
		if !open {
			continue
		}
		switch act, tasks := tr.Next(); act {
		case actDeliverWake:
			stopHold()
			write(resume)
		case actHold:
			if hold == nil {
				w.log.Info("agentrun: turn ended with background tasks outstanding; holding stdin open", "tasks", tasks)
				hold = time.NewTimer(w.cfg.BackgroundWait)
				holdC = hold.C
			}
		case actClose:
			stopHold()
			closeInput("idle")
		case actWait:
			stopHold()
		}
	}
}
```

Update the package comment at the top of `internal/agentrun/spawner.go`:

```go
// Package agentrun is cove-master's agent wrapper: a covemaster.Workload that
// runs the claude agent headless (claude -p, stream-json in and out) as a
// sequence of episodes and maps its lifecycle onto the Attach Activity stream.
// An episode stays open while the agent works or has background tasks, and
// takes Wakes as stdin messages. It depends on internal/covemaster (the
// Workload seam) and internal/dispatch/worker (the worker-result.json
// contract); it never imports internal/jam.
```

Update `Control`'s doc comment: replace its last sentence with

```go
// Wake delivers a non-blocking signal on the wake channel. While an episode
// is live, the episode loop consumes it (delivered now between turns, or
// coalesced into one resume prompt at turn end); after the process exited, a
// Run blocked on a needs-input wait consumes it and resumes. A wake arriving
// with one already buffered is dropped — a resumed turn re-reads its inbox.
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/agentrun/ && go vet ./internal/agentrun/`
Expected: `ok`. If a pre-existing test relying on per-call `scriptedProc` without stdin hangs, confirm its fake returns from `Wait` on its own — the episode then falls through on `exited`.

- [ ] **Step 6: Race check**

Run: `go test -race ./internal/agentrun/`
Expected: `ok`, no races (tracker is mutex-guarded; `changed` is a channel).

- [ ] **Step 7: Commit**

```bash
git add internal/agentrun/
git commit -m "feat(agentrun): stream input into a live claude; coalesce wakes; close when idle"
```

---

### Task 4: Session UI totals correct for multi-result episodes

**Files:**
- Modify: `internal/jam/adminui/session.go:56-75` (totals) and `:170-173` (result summary)
- Modify: `internal/jam/adminui/templates/session.html:65` (totals fragment)
- Test: `internal/jam/adminui/session_internal_test.go`

**Interfaces:**
- Produces: `totals{Turns, Episodes uint32; ToolCalls int; InputTokens, OutputTokens int64; CostUSD float64; episodeCost map[uint32]float64}`.

- [ ] **Step 1: Write the failing test** — append to `internal/jam/adminui/session_internal_test.go` (add `"math"` and `"github.com/aethons-tools/cove/internal/jam/sessionevents"` to imports):

```go
// total_cost_usd is cumulative per claude process (episode = turn); usage is
// per result. Real numbers from one process with two results (0.1364 → 0.1453).
func TestTotalsCostIsLastPerEpisode(t *testing.T) {
	res := func(turn uint32, cost float64, in, out int64) sessionevents.Event {
		return sessionevents.Event{Kind: sessionevents.KindEvent, Turn: turn,
			Index: sessionevents.Index{Type: "result", CostUSD: cost, InputTokens: in, OutputTokens: out}}
	}
	var tot totals
	tot.add(sessionevents.Event{Kind: sessionevents.KindEvent, Turn: 1, Index: sessionevents.Index{Type: "assistant", ToolName: "Bash"}})
	tot.add(res(1, 0.1364288, 10, 116))
	tot.add(res(1, 0.145289, 20, 21))
	tot.add(res(2, 0.05, 5, 7))
	if math.Abs(tot.CostUSD-0.195289) > 1e-9 {
		t.Fatalf("CostUSD = %v, want 0.195289 (last per episode, summed)", tot.CostUSD)
	}
	if tot.Turns != 3 || tot.Episodes != 2 {
		t.Fatalf("Turns=%d Episodes=%d, want 3 and 2", tot.Turns, tot.Episodes)
	}
	if tot.InputTokens != 35 || tot.OutputTokens != 144 || tot.ToolCalls != 1 {
		t.Fatalf("tokens %d/%d tools %d", tot.InputTokens, tot.OutputTokens, tot.ToolCalls)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/adminui/ -run TestTotalsCostIsLastPerEpisode`
Expected: build failure — `tot.Episodes undefined`.

- [ ] **Step 3: Implement** — in `internal/jam/adminui/session.go` replace `totals` and `add`:

```go
type totals struct {
	Turns                     uint32 // results seen: prompts answered
	Episodes                  uint32 // claude processes: max turn
	ToolCalls                 int
	InputTokens, OutputTokens int64
	CostUSD                   float64
	// episodeCost is the latest total_cost_usd per episode (turn). claude
	// reports it cumulatively per process, so only the last one counts.
	episodeCost map[uint32]float64
}

func (t *totals) add(ev sessionevents.Event) {
	if ev.Turn > t.Episodes {
		t.Episodes = ev.Turn
	}
	if ev.Index.Type == "assistant" && ev.Index.ToolName != "" {
		t.ToolCalls++
	}
	if ev.Index.Type == "result" {
		t.Turns++
		t.InputTokens += ev.Index.InputTokens
		t.OutputTokens += ev.Index.OutputTokens
		if t.episodeCost == nil {
			t.episodeCost = map[uint32]float64{}
		}
		t.CostUSD += ev.Index.CostUSD - t.episodeCost[ev.Turn]
		t.episodeCost[ev.Turn] = ev.Index.CostUSD
	}
}
```

In `viewOf`'s `case "result":` change the summary format to label the cumulative figure:

```go
		v.Summary = fmt.Sprintf("$%.4f episode total · in %d / out %d tokens · %s", ev.Index.CostUSD, ev.Index.InputTokens,
			ev.Index.OutputTokens, time.Duration(ev.Index.DurationMS)*time.Millisecond)
```

In `internal/jam/adminui/templates/session.html` line 65, change `<span>turns <b>{{.Turns}}</b></span>` to:

```html
<span>turns <b>{{.Turns}}</b></span><span>episodes <b>{{.Episodes}}</b></span>
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/jam/adminui/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/adminui/session.go internal/jam/adminui/session_internal_test.go internal/jam/adminui/templates/session.html
git commit -m "fix(adminui): session cost = last total per episode; count turns by result"
```

---

### Task 5: Docs

**Files:**
- Modify: `docs/usage/jam/coves.md` (the "cove-master runs the agent as a **headless one-shot**" section, the held-wake sentence, **Connector refresh**; bump `updated:`)
- Modify: `docs/usage/jam/session-events.md` (Cove side `turn` bullet; add cost note; bump `updated:`)
- Modify: `docs/usage/jam/ui.md` (session timeline totals sentence near line 189; bump `updated:`)
- Modify: `docs/usage/jam/intercom.md:117-125` (a wake may land in a live agent)
- Modify: `cmd/cove-master/main.go:3` (doc comment)

Use the **docs-author** skill; then the **docs-audit** skill.

- [ ] **Step 1: coves.md** — replace the paragraph beginning `cove-master runs the agent as a **headless one-shot**` through `tools. On a present config it proceeds:` with:

```markdown
cove-master runs the agent headless in **episodes** (`internal/agentrun`). An
episode is one `claude -p --input-format stream-json --output-format stream-json
--verbose --dangerously-skip-permissions` process in `AT_COVE_WORKDIR`; the prompt
is its first stdin message. cove-master reports `running` and watches the
stream-json output: it closes stdin only when the agent's turn has ended **and**
no background task (`run_in_background` Bash, background subagents, Monitors) is
outstanding, so backgrounding works. A turn that ends with tasks still running
holds stdin open for at most `BackgroundWait` (30m), then closes it and claude
stops the stragglers (logged at WARN). When the process exits cove-master reads
`.at-task/worker-result.json` (the same contract as the dispatch worker). Before
spawning, it **fails loud if the `--mcp-config` file is missing** (a stale image
without `/etc/claude-code/mcp.json`) rather than launch a silently toolless agent
(COV-190). On a present config it proceeds:
```

Replace the sentence `A \`wake\` that arrives while the agent is still running is held (at most one), so the next \`needs-input\` wait resumes at once; further wakes are dropped.` with:

```markdown
A `wake` that arrives while an episode is live goes **into** it: between turns it
is written to stdin as the resume prompt at once; mid-turn, any number of wakes are
coalesced into **one** resume prompt written when the turn ends. A wake coalesced
but undelivered when the process exits is kept, so the next `needs-input` wait
resumes at once.
```

In **Connector refresh**, change `Before every agent spawn — the first turn, a resume, a wake —` to `Before every episode (agent spawn) — the first, and each resume after the process exited —` and `at its next turn (never mid-turn)` to `at its next episode (never within one: a wake delivered into a live episode runs under the env that episode started with)`.

- [ ] **Step 2: session-events.md** — change `**turn** (the claude invocation number, from 1).` to `**turn** (the episode — claude process — number, from 1; one episode can answer several prompts, see [coves.md](coves.md)).` and append to the same bullet list:

```markdown
- **Cost is cumulative per episode.** A `result` line's `total_cost_usd` is the
  process's running total, while `usage` is per result. Sum the *last*
  `total_cost_usd` of each `turn`, not every result.
```

- [ ] **Step 3: ui.md** — in the session timeline sentence listing `totals (turns, tool calls, tokens in/out, cost)`, change it to `totals (turns = results answered, episodes, tool calls, tokens in/out, cost = last total per episode)`.

- [ ] **Step 4: intercom.md** — in the paragraph at line ~117–125, change `the studio runs its next turn (\`claude --continue\`), \`read\`s the` to `the studio runs its next turn — written into the live agent if one is running, else a new \`claude --continue\` episode — \`read\`s the`.

- [ ] **Step 5: cove-master doc comment** — `cmd/cove-master/main.go:3`: change `running claude \`-p\` as a headless one-shot.` to `running claude \`-p\` headless in stream-json episodes.`

- [ ] **Step 6: Audit + build**

Run the docs-audit skill's checker; then `go build ./... && go test ./internal/agentrun/ ./internal/jam/adminui/`
Expected: audit clean; `ok`.

- [ ] **Step 7: Commit**

```bash
git add docs/usage/jam/coves.md docs/usage/jam/session-events.md docs/usage/jam/ui.md docs/usage/jam/intercom.md cmd/cove-master/main.go
git commit -m "docs(jam): agent episodes with streamed input; per-episode cost"
```

---

### Task 6: Live verification (attended)

Not hermetic — run once by hand in this sandbox before opening the PR.

- [ ] **Step 1:** `just lint && just test` — expected clean.
- [ ] **Step 2:** Exercise a real claude through a throwaway harness: in the scratchpad, write a tiny `main` that builds `agentrun.New(Config{WorkDir: <tmp>, Prompt: "Run \`sleep 20; echo done > bg.txt\` with run_in_background, then end your turn with: started. When it finishes, write .at-task/worker-result.json with {\"status\":{\"ok\":{}}}.", MCPConfigPath: <empty-servers json>, StreamLogPath: <tmp>/s.jsonl}, slog stderr)` and a stub `covemaster.Handle`; run it.
  Expected: log shows `holding stdin open` with the sleep task, then `closing agent stdin why=idle` only after the self-started turn's result; `bg.txt` contains `done`; Run returns nil.
- [ ] **Step 3:** Open the PR (finishing-a-development-branch skill).
