# Turn-end lifecycle — Slice 1: wake reasons + `holding` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Wake carries *why* the session is being woken, and a session whose turn ended with background tasks still running reports a new `holding` activity instead of `running`.

**Architecture:** `attach.proto` gains `Activity.HOLDING` and `Wake.reasons`. Jam's `ControlSink.Wake` takes variadic `jam.WakeReason`s; the wake-on engine passes `squawk`. cove-master decodes the reasons into `covemaster.Control.Reasons`; `agentrun` merges pending reasons (instead of dropping duplicates on a cap-1 channel) and renders them into the resume prompt; it reports `Holding` while an episode is held open for background tasks. Jam treats `holding` like `running` for pause/teardown and like a live wake target for squawks — and, crucially, `holding → running` does **not** re-baseline `WaitSeq`.

**Tech Stack:** Go, protobuf/gRPC (`buf generate` via `just buf-gen`), the existing hermetic test fakes.

**Spec:** `docs/superpowers/specs/2026-10-05-turn-end-lifecycle-design.md` — this plan implements §8 slice 1 (the `holding` activity and "Wake carries its reasons" parts of §3, and their wire in §3 "Wire").

## Global Constraints

- **No changes** under `internal/dispatch/**`, `internal/dispatchrun`, `cmd/at-task` (the non-Jam path stays exactly as is).
- All proto changes are **additive** (new enum value, new field, new message) — an old cove-master must keep working against a new Jam and vice versa.
- Wake reason kinds (exact strings): `squawk`, `alarm`, `gate-failed`, `idle`, `context-changed`. Slice 1 only *emits* `squawk`; renderers must handle any kind (unknown kinds get a generic line).
- Activity string for the new state: `holding` (Go: `jam.ActivityHolding`, `covemaster.Holding`, proto `HOLDING = 5`).
- Tests stay hermetic (no Docker/network). TDD: failing test first.
- Build env in this sandbox: `export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local`.
- Every PR updates the docs that describe the changed behavior in the same change (AGENTS.md rule).

## Review Focus

1. **A squawk lands while `holding`, then a background task completes and starts a self-turn (`holding → running`) before the next wake-on tick** — the reply must still wake the agent; `holding → running` must not re-baseline `WaitSeq` past it. (Task 4, `TestReportHoldingToRunningKeepsWaitSeq`.)
2. **Several Wakes arrive mid-turn with different reasons** — one resume prompt carrying every distinct reason, not just the first or last. (Task 3, `TestEpisodeMergesWakeReasons`.)
3. **An old Jam sends a bare `Wake{}` (no reasons)** — the agent gets today's per-kind resume prompt unchanged. (Task 3, `TestRenderWakeNoReasonsIsLegacyPrompt`.)
4. **A `holding` cove sits past `warm-timeout` / `wait-max`** — never paused, never torn down. (Task 4, `TestTick_HoldingCoveNeverPausedOrReaped`.)
5. **The turn after a hold resumes (a delivered Wake or a background self-turn)** — activity goes back to `running`, so the UI and wake-on don't keep treating a busy agent as holding. (Task 3, `TestEpisodeReportsHoldingThenRunning`.)

---

## File Structure

| File | Change |
|------|--------|
| `internal/jam/attach/proto/attach.proto` | `HOLDING = 5`; `Wake { repeated WakeReason reasons = 1; }`; `message WakeReason`. |
| `internal/jam/attach/attachpb/*.pb.go` | Regenerated (`just buf-gen`), committed. |
| `internal/jam/instance.go` | `ActivityHolding`. |
| `internal/jam/wake.go` (new) | `WakeReason` type + kind constants. |
| `internal/jam/supervisor.go` | `ControlSink.Wake(actorID, ...WakeReason)`; `Report`: `holding → running` keeps `WaitSeq`. |
| `internal/jam/admin.go` | `parseActivity` accepts `holding`. |
| `internal/jam/attach/server.go` | `Wake` encodes reasons; `fromPBActivity` maps `HOLDING`. |
| `internal/covemaster/covemaster.go`, `client.go` | `Holding` activity; `Control.Reasons`; decode `Wake.reasons`. |
| `internal/agentrun/wake.go` (new) | `wakeBox` (merge pending reasons) + `renderWake`. |
| `internal/agentrun/workload.go` | use `wakeBox`; report `Holding` on `actHold`, `Running` when a turn resumes. |
| `internal/wakeon/wakeon.go` | `Waker.Wake(actorID, ...jam.WakeReason)`; `holding` uses the running branch; pass `squawk`. |
| `internal/jam/meui/presence.go` | `holding` → "is working in the background". |
| `docs/usage/jam/turn-end.md` (new), `docs/usage/jam/INDEX.md`, `coves.md`, `intercom.md` | Docs. |

---

### Task 1: Wire + Jam types (proto, `ActivityHolding`, `WakeReason`, attach server)

**Files:**
- Modify: `internal/jam/attach/proto/attach.proto`
- Regenerate: `internal/jam/attach/attachpb/`
- Modify: `internal/jam/instance.go:24-29`, `internal/jam/admin.go:251-257`, `internal/jam/supervisor.go:94-100`, `internal/jam/attach/server.go:225-249`
- Create: `internal/jam/wake.go`
- Test: `internal/jam/attach/server_test.go`, `internal/jam/admin_test.go` (or wherever `parseActivity` is exercised — add a table test if none), `internal/jam/supervisor_test.go:870` (fake sink signature)

**Interfaces:**
- Produces:
  ```go
  // internal/jam
  const ActivityHolding Activity = "holding"
  type WakeReason struct{ Kind, Alarm, Note, Detail string }
  const (
      WakeSquawk = "squawk"; WakeAlarm = "alarm"; WakeGateFailed = "gate-failed"
      WakeIdle = "idle"; WakeContextChanged = "context-changed"
  )
  type ControlSink interface {
      RequestTeardown(actorID string)
      Wake(actorID string, reasons ...WakeReason)
  }
  // internal/jam/attach
  func (s *Server) Wake(actorID string, reasons ...jam.WakeReason)
  // proto: attachpb.Activity_HOLDING, attachpb.Wake{Reasons []*attachpb.WakeReason}, attachpb.WakeReason{Kind, Alarm, Note, Detail}
  ```

- [ ] **Step 1: Edit the proto**

In `internal/jam/attach/proto/attach.proto`:

```proto
enum Activity {
  ACTIVITY_UNSPECIFIED = 0;
  RUNNING = 1;
  WAITING = 2;
  BLOCKED = 3;
  DONE    = 4;
  HOLDING = 5; // turn ended, background tasks outstanding: wakeable, never paused
}
```

```proto
// Wake asks the cove to start (or resume) a turn. reasons says why; empty from
// an older Jam (the cove then uses its generic resume prompt).
message Wake { repeated WakeReason reasons = 1; }

// WakeReason is one cause of a Wake. kind: squawk | alarm | gate-failed | idle |
// context-changed (unknown kinds are rendered generically).
message WakeReason {
  string kind   = 1;
  string alarm  = 2; // alarm name (alarm, gate-failed)
  string note   = 3; // the alarm's note
  string detail = 4; // free text: gate stdout, failure cause, …
}
```

- [ ] **Step 2: Regenerate**

Run: `just buf-gen`
Expected: `internal/jam/attach/attachpb/attach.pb.go` changes; `go build ./...` passes.

- [ ] **Step 3: Write the failing tests**

In `internal/jam/attach/server_test.go`, extend `TestWakeDelivery` (after the existing assertion) — replace `srv.Wake("w1")` with a call carrying a reason and assert it arrives:

```go
	srv.Wake("w1", jam.WakeReason{Kind: jam.WakeSquawk})

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	wk, ok := msg.GetMsg().(*attachpb.ControlDown_Wake)
	if !ok {
		t.Fatalf("expected ControlDown_Wake, got %T", msg.GetMsg())
	}
	if rs := wk.Wake.GetReasons(); len(rs) != 1 || rs[0].GetKind() != "squawk" {
		t.Fatalf("wake reasons = %v, want [squawk]", rs)
	}
```

Add:

```go
func TestFromPBActivityHolding(t *testing.T) {
	got, ok := fromPBActivity(attachpb.Activity_HOLDING)
	if !ok || got != jam.ActivityHolding {
		t.Fatalf("fromPBActivity(HOLDING) = %q, %v; want holding, true", got, ok)
	}
}
```

In the `internal/jam` package tests (new file `internal/jam/activity_test.go` if no existing table):

```go
func TestParseActivityAcceptsHolding(t *testing.T) {
	for _, s := range []string{"running", "waiting", "holding", "blocked", "done"} {
		if a, ok := parseActivity(s); !ok || string(a) != s {
			t.Errorf("parseActivity(%q) = %q, %v", s, a, ok)
		}
	}
	if _, ok := parseActivity("sleeping"); ok {
		t.Error("parseActivity accepted an unknown activity")
	}
}
```

- [ ] **Step 4: Run to verify they fail**

Run: `go test ./internal/jam/ ./internal/jam/attach/ -run 'Wake|Holding|ParseActivity'`
Expected: compile errors (`jam.WakeReason`, `jam.ActivityHolding` undefined).

- [ ] **Step 5: Implement**

`internal/jam/instance.go` — add to the Activity const block:

```go
	ActivityHolding Activity = "holding" // turn ended, background tasks outstanding: wakeable, never paused or reaped
```

`internal/jam/wake.go`:

```go
package jam

// WakeReason is one cause of a Wake, carried down the Attach stream so the
// cove's resume prompt can say why the agent was woken.
type WakeReason struct {
	Kind   string `json:"kind"`             // one of the Wake* kinds
	Alarm  string `json:"alarm,omitempty"`  // alarm name (WakeAlarm, WakeGateFailed)
	Note   string `json:"note,omitempty"`   // the alarm's note
	Detail string `json:"detail,omitempty"` // gate stdout, failure cause, …
}

// Wake reason kinds.
const (
	WakeSquawk         = "squawk"
	WakeAlarm          = "alarm"
	WakeGateFailed     = "gate-failed"
	WakeIdle           = "idle"
	WakeContextChanged = "context-changed"
)
```

`internal/jam/supervisor.go` — `ControlSink.Wake(actorID string, reasons ...WakeReason)`.

`internal/jam/admin.go` — add `ActivityHolding` to the `parseActivity` case list.

`internal/jam/attach/server.go`:

```go
func (s *Server) Wake(actorID string, reasons ...jam.WakeReason) {
	pb := make([]*attachpb.WakeReason, 0, len(reasons))
	for _, r := range reasons {
		pb = append(pb, &attachpb.WakeReason{Kind: r.Kind, Alarm: r.Alarm, Note: r.Note, Detail: r.Detail})
	}
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Wake{Wake: &attachpb.Wake{Reasons: pb}}})
}
```

and in `fromPBActivity`:

```go
	case attachpb.Activity_HOLDING:
		return jam.ActivityHolding, true
```

Update `fakeSink.Wake` in `internal/jam/supervisor_test.go:870` to `func (f *fakeSink) Wake(id string, _ ...WakeReason)`.

- [ ] **Step 6: Run to verify they pass**

Run: `go build ./... && go test ./internal/jam/...`
Expected: PASS. (`internal/wakeon` still compiles: its `Waker` interface is satisfied by the variadic method — Task 4 changes it.)

- [ ] **Step 7: Commit**

```bash
git add internal/jam/attach internal/jam/instance.go internal/jam/wake.go internal/jam/supervisor.go internal/jam/admin.go internal/jam/*_test.go
git commit -m "feat(jam): attach wire for wake reasons and the holding activity"
```

---

### Task 2: cove-master decodes reasons and reports `Holding`

**Files:**
- Modify: `internal/covemaster/covemaster.go:20-40,82-95`, `internal/covemaster/client.go:325-330`
- Test: `internal/covemaster/client_test.go`

**Interfaces:**
- Consumes: `attachpb.Activity_HOLDING`, `attachpb.Wake.GetReasons()` (Task 1).
- Produces:
  ```go
  // internal/covemaster (no internal/jam import — keep it that way)
  const Holding Activity // appended after Done
  type WakeReason struct{ Kind, Alarm, Note, Detail string }
  type Control struct {
      Kind    ControlKind
      Reasons []WakeReason // Wake only; nil from an older Jam
  }
  ```

- [ ] **Step 1: Write the failing tests**

In `internal/covemaster/client_test.go`, following `TestClientTeardownFromServer`'s harness (real attach server over bufconn, `blockWorkload` recording controls):

```go
func TestClientDecodesWakeReasons(t *testing.T) {
	_, srv, dial, tok, secret := serverHarness(t)
	w := &blockWorkload{first: Running, controls: make(chan Control, 4)}
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 50 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, w)
	time.Sleep(100 * time.Millisecond) // attached (as TestClientTeardownFromServer)
	srv.Wake("w1", jam.WakeReason{Kind: jam.WakeSquawk}, jam.WakeReason{Kind: jam.WakeAlarm, Alarm: "pr-watch", Note: "check the PR"})
	select {
	case got := <-w.controls:
		want := Control{Kind: Wake, Reasons: []WakeReason{{Kind: "squawk"}, {Kind: "alarm", Alarm: "pr-watch", Note: "check the PR"}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("control = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Wake control delivered")
	}
}

// End to end: a Holding report lands on the instance as jam.ActivityHolding.
func TestClientReportsHolding(t *testing.T) {
	store, _, dial, tok, secret := serverHarness(t)
	c := New(Config{Addr: "bufnet", Token: tok, LaunchSecret: secret, Heartbeat: 20 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), dial}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, &blockWorkload{first: Holding, controls: make(chan Control, 4)})
	if !eventually(func() bool { inst, ok := store.GetInstance("w1"); return ok && inst.Activity == jam.ActivityHolding }) {
		inst, _ := store.GetInstance("w1")
		t.Fatalf("activity never reached holding: %+v", inst)
	}
}
```

(`client_test.go` is `package covemaster` and already imports `internal/jam`, `attachpb`, `grpc`, `insecure`; add `reflect`.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/covemaster/ -run 'WakeReasons|Holding'`
Expected: compile errors (`Holding`, `Control.Reasons` undefined).

- [ ] **Step 3: Implement**

`covemaster.go`:

```go
const (
	Running Activity = iota
	Waiting
	Blocked
	Done
	Holding // turn ended, background tasks still running
)

// WakeReason mirrors attachpb.WakeReason (covemaster never imports internal/jam).
type WakeReason struct{ Kind, Alarm, Note, Detail string }

type Control struct {
	Kind    ControlKind
	Reasons []WakeReason // Wake only; nil from an older Jam
}
```

`toPBActivity`: `case Holding: return attachpb.Activity_HOLDING`.

`client.go` (the `ControlDown_Wake` case):

```go
			case *attachpb.ControlDown_Wake:
				var rs []WakeReason
				for _, r := range m.Wake.GetReasons() {
					rs = append(rs, WakeReason{Kind: r.GetKind(), Alarm: r.GetAlarm(), Note: r.GetNote(), Detail: r.GetDetail()})
				}
				w.Control(Control{Kind: Wake, Reasons: rs})
```

(Change `switch cd.GetMsg().(type)` to `switch m := cd.GetMsg().(type)`.)

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/covemaster/ ./cmd/cove-master/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/covemaster
git commit -m "feat(covemaster): decode wake reasons; report the holding activity"
```

---

### Task 3: `agentrun` merges reasons, renders them, reports `Holding`

**Files:**
- Create: `internal/agentrun/wake.go`, `internal/agentrun/wake_test.go`
- Modify: `internal/agentrun/workload.go` (the `wake chan struct{}` field at ~110, `New` at ~150, `Control` at ~524-540, `Run`'s post-exit waits at ~270-340, `episode` at ~420-500)
- Test: `internal/agentrun/episode_test.go`, `internal/agentrun/workload_test.go`

**Interfaces:**
- Consumes: `covemaster.Control{Kind, Reasons}`, `covemaster.Holding` (Task 2).
- Produces (package-private):
  ```go
  type wakeBox struct{ /* mu, pending []covemaster.WakeReason, sig chan struct{} (cap 1) */ }
  func newWakeBox() *wakeBox
  func (b *wakeBox) post(rs []covemaster.WakeReason) // append (dedup identical), signal non-blocking
  func (b *wakeBox) signal() <-chan struct{}
  func (b *wakeBox) take() []covemaster.WakeReason   // drain
  func (b *wakeBox) repost()                          // re-signal without new reasons (WakeOwed hand-off)
  func renderWake(base string, rs []covemaster.WakeReason) string
  ```

Rendering rule: no reasons, or only `squawk` → `base` (today's per-kind `resumeText()`, unchanged). Otherwise `base` is used only if a `squawk` is among them, preceded by one line per non-squawk reason:
- `alarm`: `Alarm "<name>" fired: <note>` (+ `\nGate output:\n<detail>` when detail is non-empty)
- `gate-failed`: `Alarm "<name>" gate could not run: <detail>` (+ `\nNote: <note>` when non-empty)
- `idle`: `Idle timeout: no other wake arrived.`
- `context-changed`: skipped (the context notice is already appended by `contextNotice`)
- anything else: `Woken (<kind>): <detail>`
When no `squawk` reason is present, the lines are followed by `"\nContinue."`.

- [ ] **Step 1: Write the failing unit tests** (`internal/agentrun/wake_test.go`)

```go
package agentrun

import (
	"testing"

	"github.com/aethons-tools/cove/internal/covemaster"
)

func TestRenderWakeNoReasonsIsLegacyPrompt(t *testing.T) {
	if got := renderWake(standingResumePrompt, nil); got != standingResumePrompt {
		t.Fatalf("got %q", got)
	}
	if got := renderWake(residentResumePrompt, []covemaster.WakeReason{{Kind: "squawk"}}); got != residentResumePrompt {
		t.Fatalf("squawk-only got %q", got)
	}
}

func TestRenderWakeAlarmAndSquawk(t *testing.T) {
	got := renderWake(standingResumePrompt, []covemaster.WakeReason{
		{Kind: "alarm", Alarm: "pr-watch", Note: "check the PR", Detail: "CI red"},
		{Kind: "squawk"},
	})
	want := "Alarm \"pr-watch\" fired: check the PR\nGate output:\nCI red\n" + standingResumePrompt
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestRenderWakeWithoutSquawk(t *testing.T) {
	got := renderWake(standingResumePrompt, []covemaster.WakeReason{{Kind: "idle"}, {Kind: "mystery", Detail: "x"}})
	want := "Idle timeout: no other wake arrived.\nWoken (mystery): x\nContinue."
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestWakeBoxMergesAndDedups(t *testing.T) {
	b := newWakeBox()
	b.post([]covemaster.WakeReason{{Kind: "squawk"}})
	b.post([]covemaster.WakeReason{{Kind: "squawk"}, {Kind: "alarm", Alarm: "a"}})
	b.post(nil) // a bare Wake from an older Jam still signals
	select {
	case <-b.signal():
	default:
		t.Fatal("no signal")
	}
	got := b.take()
	if len(got) != 2 || got[0].Kind != "squawk" || got[1].Alarm != "a" {
		t.Fatalf("take = %+v", got)
	}
	if len(b.take()) != 0 {
		t.Fatal("take must drain")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/agentrun/ -run 'RenderWake|WakeBox'`
Expected: compile errors (`renderWake`, `newWakeBox` undefined).

- [ ] **Step 3: Implement `internal/agentrun/wake.go`**

```go
package agentrun

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// wakeBox holds the reasons of Wakes not yet delivered to the agent. Wakes
// coalesce (one resume prompt answers all of them), so reasons are merged —
// never dropped — and drained by the delivery that answers them.
type wakeBox struct {
	mu      sync.Mutex
	pending []covemaster.WakeReason
	sig     chan struct{} // cap 1
}

func newWakeBox() *wakeBox { return &wakeBox{sig: make(chan struct{}, 1)} }

func (b *wakeBox) post(rs []covemaster.WakeReason) {
	b.mu.Lock()
	for _, r := range rs {
		if !slices.Contains(b.pending, r) {
			b.pending = append(b.pending, r)
		}
	}
	b.mu.Unlock()
	b.repost()
}

// repost signals without adding reasons (a coalesced wake handed to the
// post-exit wait).
func (b *wakeBox) repost() {
	select {
	case b.sig <- struct{}{}:
	default:
	}
}

func (b *wakeBox) signal() <-chan struct{} { return b.sig }

func (b *wakeBox) take() []covemaster.WakeReason {
	b.mu.Lock()
	defer b.mu.Unlock()
	rs := b.pending
	b.pending = nil
	return rs
}

// renderWake is the resume prompt for a delivery answering rs. base is the
// session kind's squawk prompt (resumeText); see the plan's rendering rule.
func renderWake(base string, rs []covemaster.WakeReason) string {
	var lines []string
	squawk := len(rs) == 0
	for _, r := range rs {
		switch r.Kind {
		case "squawk":
			squawk = true
		case "context-changed":
		case "alarm":
			l := fmt.Sprintf("Alarm %q fired: %s", r.Alarm, r.Note)
			if r.Detail != "" {
				l += "\nGate output:\n" + r.Detail
			}
			lines = append(lines, l)
		case "gate-failed":
			l := fmt.Sprintf("Alarm %q gate could not run: %s", r.Alarm, r.Detail)
			if r.Note != "" {
				l += "\nNote: " + r.Note
			}
			lines = append(lines, l)
		case "idle":
			lines = append(lines, "Idle timeout: no other wake arrived.")
		default:
			lines = append(lines, fmt.Sprintf("Woken (%s): %s", r.Kind, r.Detail))
		}
	}
	if squawk {
		lines = append(lines, base)
	} else {
		lines = append(lines, "Continue.")
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Run unit tests to verify they pass**

Run: `go test ./internal/agentrun/ -run 'RenderWake|WakeBox'`
Expected: PASS.

- [ ] **Step 5: Write the failing episode tests** (`internal/agentrun/episode_test.go`)

```go
func TestEpisodeMergesWakeReasons(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident, c.SessionKind = true, "standing" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, &recordHandle{})
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "alarm", Alarm: "nightly", Note: "run the backup check"}}})
	p.in.noMessage(t, 50*time.Millisecond) // mid-turn: held
	p.emit(lnResult)
	want := "Alarm \"nightly\" fired: run the backup check\n" + standingResumePrompt
	if got := p.in.next(t); got != want {
		t.Fatalf("delivered %q, want %q", got, want)
	}
	cancel()
	<-done
}

func TestEpisodeReportsHoldingThenRunning(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult) // turn ends with a background task outstanding
	if !eventually(func() bool { return h.count(covemaster.Holding) == 1 }) {
		t.Fatalf("Holding not reported on hold; got %v", h.got)
	}
	runningBefore := h.count(covemaster.Running)
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
	p.in.next(t) // the resume prompt starts a turn
	if !eventually(func() bool { return h.count(covemaster.Running) == runningBefore+1 }) {
		t.Fatalf("Running not reported when the held episode resumed; got %v", h.got)
	}
	cancel()
	<-done
}
```

(`internal/agentrun` has no `eventually`; add this to `episode_test.go`:

```go
func eventually(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
```

and read `h.got` in failure messages under `h.mu`, or print `h.count(...)` values instead.)

- [ ] **Step 6: Run to verify they fail**

Run: `go test ./internal/agentrun/ -run 'MergesWakeReasons|HoldingThenRunning'`
Expected: FAIL (wrong prompt delivered; no `Holding` reported).

- [ ] **Step 7: Wire `wakeBox` and `Holding` into `workload.go`**

- Field `wake chan struct{}` → `wake *wakeBox`; `New`: `wake: newWakeBox()`.
- `Control`, `covemaster.Wake` case: `w.wake.post(c.Reasons)`.
- Every `case <-w.wake:` → `case <-w.wake.signal():`, and every place that builds the resume text uses `renderWake(w.resumeText(), w.wake.take())`:
  - the live-episode `resume` closure: `text := renderWake(w.resumeText(), w.wake.take())` (the `contextNotice` append is unchanged);
  - the resident post-exit wait: `prompt, continued = renderWake(w.resumeText(), w.wake.take()), true`;
  - the needs-input post-exit wait: `prompt, continued = renderWake(resumePrompt, w.wake.take()), true`.
- The `tr.WakeOwed()` hand-off: replace the non-blocking send with `w.wake.repost()` (reasons not yet taken stay pending).
- `episode` gains the handle: `func (w *Workload) episode(ctx context.Context, h covemaster.Handle, proc Process, tr *idleTracker, prompt string) error` (update the one caller in `Run`). Track `held bool`:
  - `case actHold:` — when `hold == nil` (first entry) also `h.Report(covemaster.Holding); held = true`.
  - `case actDeliverWake:` and the live `case <-wake:` branch after `write(...)`, and `case actWait:` — if `held` then `h.Report(covemaster.Running); held = false`.
  - `case actClose:` — leave `held` as is (the post-exit path reports `Waiting`).

- [ ] **Step 8: Run the package**

Run: `go test ./internal/agentrun/`
Expected: PASS, including the pre-existing `TestEpisodeCoalescesWakesDuringTurn`, `TestEpisodeWakeDuringHoldDeliveredNow`, `TestEpisodePendingWakeSurvivesExit`, `TestResumeTextPerKind` (bare wakes still render the legacy prompt).

- [ ] **Step 9: Commit**

```bash
git add internal/agentrun
git commit -m "feat(agentrun): merge wake reasons into the resume prompt; report holding"
```

---

### Task 4: Jam treats `holding` correctly (supervisor + wake-on)

**Files:**
- Modify: `internal/jam/supervisor.go:418-440` (`Report`), `internal/wakeon/wakeon.go:22,147-215`
- Test: `internal/jam/supervisor_test.go`, `internal/wakeon/wakeon_test.go:77`

**Interfaces:**
- Consumes: `jam.ActivityHolding`, `jam.WakeReason`, `jam.WakeSquawk` (Task 1).
- Produces: `wakeon.Waker` is `interface{ Wake(actorID string, reasons ...jam.WakeReason) }`.

- [ ] **Step 1: Write the failing tests**

`internal/jam/supervisor_test.go`:

```go
// holding → running is the same run resuming (a delivered Wake or a background
// task's self-started turn), not a new run: WaitSeq must not jump past a reply
// that landed while holding and has not been woken for yet.
func TestReportHoldingToRunningKeepsWaitSeq(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	tail := &fakeTailReader{seq: 2, ok: true}
	sup.SetTailReader(tail)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityHolding); err != nil {
		t.Fatal(err)
	}
	tail.seq = 5 // a reply landed while holding
	if err := sup.Report(context.Background(), "w1", ActivityRunning); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitSeq != 2 {
		t.Fatalf("WaitSeq = %d, want 2 (holding→running keeps the baseline)", inst.WaitSeq)
	}
	if !inst.WaitingSince.IsZero() {
		t.Fatal("holding must not stamp WaitingSince")
	}
}
```

`internal/wakeon/wakeon_test.go` — change the fake to record reasons:

```go
type fakeWaker struct {
	woke    []string
	reasons [][]jam.WakeReason
}

func (f *fakeWaker) Wake(a string, rs ...jam.WakeReason) {
	f.woke = append(f.woke, a)
	f.reasons = append(f.reasons, rs)
}
```

and add:

```go
func TestTick_ReplyToHoldingCoveWakesWithSquawkReason(t *testing.T) {
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityHolding, SessionKind: jam.SessionKindStanding, WaitSeq: 5},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{"a1": {extInbound("a1", 6, "id-6")}}}
	wake := &fakeWaker{}
	cur := &fakeCursor{reg: reg}
	e := New(reg, wake, &fakeReaper{}, &fakeIdler{}, inbox, Config{MaxWait: time.Minute, WarmTimeout: time.Second}, nil)
	e.SetRunningWake(cur)
	e.now = func() time.Time { return time.Unix(2000, 0) }
	e.tick(context.Background())
	if len(wake.woke) != 1 || len(wake.reasons[0]) != 1 || wake.reasons[0][0].Kind != jam.WakeSquawk {
		t.Fatalf("want one squawk wake, got woke=%v reasons=%v", wake.woke, wake.reasons)
	}
	if cur.set["a1"] != 6 {
		t.Fatalf("baseline = %d, want 6", cur.set["a1"])
	}
}

func TestTick_HoldingCoveNeverPausedOrReaped(t *testing.T) {
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityHolding, SessionKind: "", // a ticket (non-resident) session: wait-max would reap it if it were waiting
			WaitingSince: time.Unix(0, 0)}, // stale stamp from an earlier wait must not matter
	}}
	reap, idler := &fakeReaper{}, &fakeIdler{}
	e := New(reg, &fakeWaker{}, reap, idler, &fakeInbox{}, Config{MaxWait: time.Minute, WarmTimeout: time.Second}, nil)
	e.SetRunningWake(&fakeCursor{reg: reg})
	e.now = func() time.Time { return time.Unix(100000, 0) }
	e.tick(context.Background())
	if len(reap.down) != 0 || len(idler.idled) != 0 {
		t.Fatalf("holding cove paused/reaped: teardown=%v idle=%v", reap.down, idler.idled)
	}
}
```

Also assert the squawk reason in the existing Waiting-path test that checks a reply wakes (e.g. extend `TestTick_ReplyToRunningCoveWakesOnceAndAdvancesBaseline` and one waiting-reply test with `wake.reasons[0][0].Kind == jam.WakeSquawk`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/jam/ ./internal/wakeon/ -run 'Holding'`
Expected: FAIL (`WaitSeq` re-baselined to 5; holding cove not woken).

- [ ] **Step 3: Implement**

`supervisor.go` `Report`:

```go
	// holding → running is the same run resuming, not a new one: keep WaitSeq so
	// a reply that landed while holding (not yet woken for) still wakes it.
	enteringRunning := a == ActivityRunning && inst.Activity != ActivityRunning && inst.Activity != ActivityHolding
```

`wakeon.go`:

```go
type Waker interface {
	Wake(actorID string, reasons ...jam.WakeReason)
}
```

In `tick`:

```go
		if inst.Activity == jam.ActivityRunning || inst.Activity == jam.ActivityHolding {
			// holding (turn over, background tasks running) is woken like running
			// and, like running, never paused or reaped here.
			e.wakeRunning(inst)
			continue
		}
```

Both `e.wake.Wake(inst.ActorID)` calls become `e.wake.Wake(inst.ActorID, jam.WakeReason{Kind: jam.WakeSquawk})`. Update the `wakeRunning` doc comment to say "Running or Holding".

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/jam/... ./internal/wakeon/ ./cmd/at-jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/supervisor.go internal/jam/supervisor_test.go internal/wakeon
git commit -m "feat(wakeon): wake holding sessions on squawks with a reason; never pause them"
```

---

### Task 5: Presence + docs

**Files:**
- Modify: `internal/jam/meui/presence.go:77-84`
- Test: `internal/jam/meui/presence_test.go` (fixture in `presenceFixture`)
- Create: `docs/usage/jam/turn-end.md`
- Modify: `docs/usage/jam/INDEX.md`, `docs/usage/jam/coves.md` (activity list), `docs/usage/jam/intercom.md` (wake-on section: running-wake paragraph)

- [ ] **Step 1: Write the failing presence test**

Add a Live, `ActivityHolding` session named `background` to `presenceFixture` (same shape as the existing `waiting` entry), and to `TestPresenceRows`' want list:

```go
		`<div class="sess busy"><span class="sname">background</span> is working in the background</div>`,
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/jam/meui/ -run TestPresenceRows`
Expected: FAIL (row rendered as "working" from the tracker fallback, or missing).

- [ ] **Step 3: Implement**

In `presence.go`'s activity switch:

```go
	case jam.ActivityHolding:
		return row("working in the background", "busy")
```

(Escalation and `/me` "waiting on you" grouping need no change: both test `== ActivityWaiting`, and a holding session is not soliciting a human.)

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/jam/meui/`
Expected: PASS. (If the busy row renders the animated dots span like "is working", match the exact markup the test produces for `row(..., "busy")` and adjust the want string.)

- [ ] **Step 5: Docs** (use the docs-author skill)

- Create `docs/usage/jam/turn-end.md` (frontmatter: summary / read_when / owns / prereqs / tier: leaf / updated: 2026-10-05). For this slice it owns: the turn-end model in one paragraph (end-or-wait; slices 2–5 fill in `end`, idle, alarms, gates, `report`), the **`holding`** activity (definition; wakeable like waiting, never paused or reaped; `holding → running` keeps the wake baseline), and **wake reasons** (the kinds; the resume prompt names them; a bare Wake from an older Jam gets the legacy prompt). Link the spec.
- `docs/usage/jam/INDEX.md`: add the row for `turn-end.md`.
- `coves.md`: add `holding` to the Activity list, linking `turn-end.md#holding`.
- `intercom.md` §"Waiting for a reply": in the "A reply that lands while the studio is still `running`" paragraph, say the agent now reports `holding` in that state and link `turn-end.md#holding` instead of re-describing it.
- Run the docs-audit checker: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep -v superpowers/` — no new errors for the touched docs.

- [ ] **Step 6: Full verification**

Run: `go build ./... && go test ./... && just lint`
Expected: all PASS.

- [ ] **Step 7: Commit and open the PR**

```bash
git add internal/jam/meui docs
git commit -m "feat(meui): show holding sessions; docs for wake reasons and holding"
```

PR title: `feat(jam): turn-end slice 1 — wake reasons + holding`. Body links the spec and lists the Review Focus items with their tests.
