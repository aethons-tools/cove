# Turn-end lifecycle — Slice 4: alarm gates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An alarm can carry a gate — a shell command cove-master runs inside the cove when the alarm comes due — and the agent is woken only if the gate passes (with its stdout), or if the gate could not give an answer (with what went wrong); otherwise the alarm stays asleep.

**Architecture:** The Attach stream gains `ControlDown.RunGate{run_id, alarm, command, timeout_s}` and `StatusUp.GateResult{run_id, exit, timed_out, output, truncated}`. When a gated alarm is due for a `holding`/`waiting` session, wake-on resumes a paused cove, then has the supervisor record a `GateRun` on the alarm and sends `RunGate` through the attach server. cove-master's `agentrun` runs `sh -c <gate>` in the workspace with the agent's last spawn env, killed at the timeout, and reports `GateResult` up; the attach server hands it to `Supervisor.ResolveGate`, which fires the alarm (`alarm` with the stdout, or `gate-failed` with the cause) or lets it sleep (cron advances; a one-shot is dropped). A run with no result within timeout + 30s grace resolves as `gate-failed`. Fired alarms then flow exactly as in slice 3 (pending until the session runs).

**Tech Stack:** Go, protobuf (`just buf-gen`), `os/exec` (`sh -c`).

**Spec:** `docs/superpowers/specs/2026-10-05-turn-end-lifecycle-design.md` — §8 slice 4: §3 "Due alarms → Gate / GateResult", "the gate couldn't answer" rule, §1 gate limits, §3 Wire (`RunGate`/`GateResult`), §5 version skew and restart. Also the user's rule: a gate that exits 0 wakes **with its stdout**; a gate that times out (or can't run) wakes **with a message saying so**.

## Global Constraints

- **No changes** under `internal/dispatch/**`, `internal/dispatchrun`, `cmd/at-task`. No ticket transitions (slice 5).
- Gate outcomes (exact): exit 0 → wake, reason `alarm` with `Detail` = output; exit 126 or 127, `timed_out`, or no result within `timeout + 30s` → wake, reason `gate-failed` with `Detail` describing the cause and the output; any other exit → no wake (one-shot: removed; cron: next match).
- Gate limits: command ≤ 4096 bytes; timeout 60s; output (stdout+stderr) capped at 4096 bytes with a truncation marker.
- Gate runs: `sh -c <command>`, working directory = the agent's `WorkDir`, env = the agent's last spawn env (`nil` = cove-master's env), never starts a claude turn.
- Proto additions only (field numbers from the spec): `ControlDown.RunGate gate = 6`, `StatusUp.GateResult gate = 5`.
- A gate never runs while the session is `running` (alarms are held, slice 3); one run per alarm at a time.
- Tests hermetic (gate-runner tests may exec `/bin/sh`, present everywhere this repo builds); TDD; `export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local`.

## Design rulings

1. **The gate runner lives in `agentrun`** (it knows the workspace and the agent's spawn env); the covemaster client only carries messages. `covemaster.Handle` gains `GateResult(GateResult)`.
2. **Gate results are not buffered across reconnects.** A result lost to a dropped stream is covered by Jam's grace timeout (→ `gate-failed` wake), so no durable queue is needed.
3. **No explicit re-pause after a "not yet" gate.** A cove resumed for a gate is `waiting` with a fresh `WaitingSince`; the existing warm-timeout pause re-idles it (default 60s).
4. **"Session event" for a one-shot whose gate said not-yet → a log line** (`wakeon: gate said not yet; one-shot alarm dropped`) plus `LastGate` on the alarm until it is removed; session-events are agent stream lines, not a Jam-authored log.
5. **Jam restart:** an in-flight `GateRun` survives in the store; if its grace has passed it resolves `gate-failed` (the spec said "cleared and re-run" — resolving is simpler, and still never silent). Ledger this as a deviation.

## Review Focus

1. **The cove never answers** (old cove-master, disconnect mid-gate, Jam restart) → a `gate-failed` wake after timeout + 30s, never a silent never-fire. (Task 4, `TestTick_GateNoResultFailsAfterGrace`.)
2. **A late `GateResult` for a run already resolved by the grace timeout** (or for a re-set alarm) → ignored, no double fire. (Task 3, `TestResolveGateIgnoresStaleRun`.)
3. **Gate output that is huge or binary** → capped at 4096 bytes with a marker, valid UTF-8 in the prompt. (Task 2, `TestRunGateCapsOutput`.)
4. **The alarm is cleared or replaced while its gate runs** → the result is dropped. (Task 3, `TestResolveGateIgnoresStaleRun`.)
5. **A paused session with a due gated alarm** → resumed, gate run on a later tick, not skipped. (Task 4, `TestTick_GateResumesPausedFirst`.)

---

## File Structure

| File | Change |
|------|--------|
| `internal/jam/attach/proto/attach.proto` (+ regen) | `RunGate`, `GateResult`. |
| `internal/covemaster/covemaster.go`, `client.go` | `ControlKind RunGate`, `Control.Gate`, `GateResult`, `Handle.GateResult`, send loop. |
| `internal/agentrun/gate.go` (new), `workload.go` | `runGate`; `Control(RunGate)`; remember spawn env + handle. |
| `internal/jam/alarm.go` | `Alarm.Gate`, `GateRun`, `GateOutcome`, `FireKind`, `FireDetail`; limits. |
| `internal/jam/supervisor.go` | `SetAlarm(…, gate)`; `FireAlarms` skips gated; `StartGate`, `ResolveGate`. |
| `internal/jam/attach/server.go` | `RunGate` sender; `GateResult` → `ResolveGate`. |
| `internal/wakeon/wakeon.go` | gated-alarm flow; `alarmReasons` uses `FireKind`/`FireDetail`. |
| `internal/jam/alarm_handler.go`, `cmd/cove-master/mcp.go` | `gate` param; list shows gate + last result. |
| Docs | `turn-end.md` §Alarms → gates. |

---

### Task 1: Wire + covemaster plumbing

**Files:** `internal/jam/attach/proto/attach.proto`, regenerated `attachpb`, `internal/covemaster/covemaster.go`, `internal/covemaster/client.go`, `internal/agentrun/workload_test.go` (fake handle), tests in `internal/covemaster/client_test.go`.

**Interfaces — Produces:**
```proto
message ControlDown { oneof msg { … RunGate gate = 6; } }
message StatusUp    { oneof msg { … GateResult gate = 5; } }
message RunGate    { string run_id = 1; string alarm = 2; string command = 3; uint32 timeout_s = 4; }
message GateResult { string run_id = 1; int32 exit = 2; bool timed_out = 3; bytes output = 4; bool truncated = 5; }
```
```go
// covemaster
const RunGate ControlKind // after Teardown
type GateRequest struct{ RunID, Alarm, Command string; Timeout time.Duration }
type GateResult struct{ RunID string; Exit int; TimedOut bool; Output []byte; Truncated bool }
// Control gains Gate *GateRequest (RunGate only)
// Handle gains GateResult(GateResult) — never blocks; dropped if the stream is down
```

- [ ] **Step 1:** Edit the proto as above (comments: `RunGate` asks cove-master to run an alarm's gate; `GateResult` answers it; `exit` is the shell's status, `timed_out` set when killed at `timeout_s`). Run `just buf-gen`; `go build ./...` passes.
- [ ] **Step 2: Failing tests** (`internal/covemaster`, pure helpers plus one client check; the end-to-end attach round-trip is tested in Task 3 once the server can send `RunGate`):
  - `TestControlFromPBRunGate`: a decode helper `controlFromPB(*attachpb.ControlDown) (Control, bool)` maps `ControlDown_Gate{RunId: "r1", Alarm: "ci", Command: "true", TimeoutS: 60}` to `Control{Kind: RunGate, Gate: &GateRequest{RunID: "r1", Alarm: "ci", Command: "true", Timeout: 60 * time.Second}}`, and `ControlDown_Wake` with reasons exactly as today (refactor the inline switch in `client.go` into this helper).
  - `TestGateResultMsg`: `gateResultMsg(GateResult{RunID: "r1", Exit: 3, TimedOut: false, Output: []byte("x"), Truncated: true})` builds the matching `StatusUp_Gate`.
  - `TestClientGateResultNeverBlocks`: on a `New(...)` client that is not running, 100 `GateResult` calls return promptly (the buffer drops beyond capacity).
- [ ] **Step 3:** Run `go test ./internal/covemaster/` — compile errors.
- [ ] **Step 4: Implement.** `covemaster.go`: types above; `gateResultMsg(GateResult) *attachpb.StatusUp`; `controlFromPB`. `client.go`: `gates chan GateResult` (buffer 16) created in `New`; `GateResult` does a non-blocking send (drop + log when full); the send loop gains `case g := <-c.gates: stream.Send(gateResultMsg(g))`; the recv dispatch uses `controlFromPB` (Teardown keeps its return path). Add `GateResult(covemaster.GateResult)` to `agentrun`'s `recordHandle` (record them; Task 2 asserts).
- [ ] **Step 5:** `go test ./internal/covemaster/ ./internal/agentrun/ ./cmd/cove-master/` — PASS.
- [ ] **Step 6:** Commit `feat(covemaster): RunGate and GateResult on the attach stream`.

---

### Task 2: The gate runner in `agentrun`

**Files:** Create `internal/agentrun/gate.go`, `internal/agentrun/gate_test.go`; modify `internal/agentrun/workload.go` (`Workload` fields `h covemaster.Handle`, `gateEnv []string`, `gateEnvSet bool`; set them in `Run`; `Control` handles `RunGate`).

**Interfaces — Produces:**
```go
const gateOutputCap = 4096
func runGate(ctx context.Context, dir string, env []string, command string, timeout time.Duration) (exit int, timedOut bool, out []byte, truncated bool)
```
- `exec.CommandContext(ctx2, "/bin/sh", "-c", command)` with `ctx2` = `context.WithTimeout(ctx, timeout)`; `Dir = dir`; `Env = env` (nil inherits); stdout+stderr into a capped buffer (keep the first 4096 bytes, set `truncated`, discard the rest without blocking the child); `cmd.WaitDelay = 2 * time.Second` so a child holding the pipe can't hang us.
- Exit: `cmd.ProcessState.ExitCode()`; timed out when `ctx2.Err() == context.DeadlineExceeded` (`exit = -1`); a start failure (no `/bin/sh`) → `exit = 127`, output = the error text.
- Output is made valid UTF-8 (`strings.ToValidUTF8(string(out), "�")`) and, when truncated, suffixed with `"\n[output truncated]"`.

`Workload.Control(RunGate)`: if `w.h == nil` (Run not started) log and drop; else `go func(){ exit, to, out, tr := runGate(w.runCtx, w.cfg.WorkDir, env, req.Command, req.Timeout); w.h.GateResult(covemaster.GateResult{RunID: req.RunID, Exit: exit, TimedOut: to, Output: out, Truncated: tr}) }()`, where `env` is the last spawn env captured in `Run` right after `overlayEnv` (`w.gateEnv`, guarded by a mutex). `w.runCtx` is `Run`'s ctx (store it with the handle).

- [ ] **Step 1: Failing tests** (`gate_test.go`):

```go
func TestRunGateExitAndOutput(t *testing.T) {
	exit, to, out, tr := runGate(context.Background(), t.TempDir(), nil, `echo green; exit 0`, 5*time.Second)
	if exit != 0 || to || tr || string(out) != "green\n" {
		t.Fatalf("exit=%d to=%v tr=%v out=%q", exit, to, tr, out)
	}
	if exit, _, _, _ := runGate(context.Background(), t.TempDir(), nil, `exit 3`, 5*time.Second); exit != 3 {
		t.Fatalf("exit = %d, want 3", exit)
	}
	if exit, _, _, _ := runGate(context.Background(), t.TempDir(), nil, `definitely-not-a-command-xyz`, 5*time.Second); exit != 127 {
		t.Fatalf("missing command exit = %d, want 127", exit)
	}
}

func TestRunGateTimesOut(t *testing.T) {
	start := time.Now()
	exit, to, _, _ := runGate(context.Background(), t.TempDir(), nil, `sleep 30`, 200*time.Millisecond)
	if !to || exit != -1 || time.Since(start) > 5*time.Second {
		t.Fatalf("exit=%d timedOut=%v after %v", exit, to, time.Since(start))
	}
}

func TestRunGateCapsOutput(t *testing.T) {
	_, _, out, tr := runGate(context.Background(), t.TempDir(), nil, `head -c 20000 /dev/urandom`, 5*time.Second)
	if !tr || len(out) > gateOutputCap+64 || !utf8.Valid(out) || !strings.HasSuffix(string(out), "[output truncated]") {
		t.Fatalf("truncated=%v len=%d valid=%v", tr, len(out), utf8.Valid(out))
	}
}

func TestRunGateUsesDirAndEnv(t *testing.T) {
	dir := t.TempDir()
	_, _, out, _ := runGate(context.Background(), dir, []string{"GATE_X=42", "PATH=" + os.Getenv("PATH")}, `pwd; echo $GATE_X`, 5*time.Second)
	if got := strings.Fields(string(out)); len(got) != 2 || got[0] != dir || got[1] != "42" {
		t.Fatalf("out=%q", out)
	}
}
```

And in `episode_test.go`:

```go
// A RunGate control runs the gate in the workspace and reports its result,
// without starting an agent turn.
func TestControlRunGateReportsResult(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnResult)
	p.in.waitClosed(t)
	p.exit <- nil // episode over: waiting
	w.Control(covemaster.Control{Kind: covemaster.RunGate, Gate: &covemaster.GateRequest{RunID: "r1", Alarm: "ci", Command: "echo ok", Timeout: 5 * time.Second}})
	if !eventually(func() bool { return len(h.gateResults()) == 1 }) {
		t.Fatal("no gate result")
	}
	if g := h.gateResults()[0]; g.RunID != "r1" || g.Exit != 0 || string(g.Output) != "ok\n" {
		t.Fatalf("result = %+v", g)
	}
	select {
	case p2 := <-s.procs:
		t.Fatalf("a gate started an agent turn: %+v", p2)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	<-done
}
```

(`recordHandle.gateResults()` returns a copy under `h.mu`.)

- [ ] **Step 2:** `go test ./internal/agentrun/ -run 'Gate'` — compile errors.
- [ ] **Step 3:** Implement `gate.go` and the `workload.go` wiring as described.
- [ ] **Step 4:** `go test ./internal/agentrun/` — PASS.
- [ ] **Step 5:** Commit `feat(agentrun): run alarm gates in the workspace and report the result`.

---

### Task 3: Jam — gated alarms, StartGate/ResolveGate, the attach server

**Files:** `internal/jam/alarm.go`, `internal/jam/supervisor.go`, `internal/jam/attach/server.go`, tests in `internal/jam/supervisor_test.go`, `internal/jam/attach/server_test.go`.

**Interfaces — Produces:**
```go
// alarm.go
const (maxGateBytes = 4096; GateTimeout = 60 * time.Second; GateGrace = 30 * time.Second)
// Alarm gains:
//   Gate       string      `json:"gate,omitempty"`
//   GateRun    *GateRun    `json:"gate_run,omitempty"`    // in flight
//   LastGate   *GateOutcome `json:"last_gate,omitempty"`
//   FireKind   string      `json:"fire_kind,omitempty"`   // WakeAlarm | WakeGateFailed, set with FiredAt
//   FireDetail string      `json:"fire_detail,omitempty"` // gate output / failure cause
type GateRun struct{ RunID string `json:"run_id"`; StartedAt time.Time `json:"started_at"` }
type GateOutcome struct {
    At time.Time `json:"at"`; Exit int `json:"exit"`; TimedOut bool `json:"timed_out,omitempty"`
    NoResult bool `json:"no_result,omitempty"`; Output string `json:"output,omitempty"`
}
func (o GateOutcome) Verdict() string // "pass" (exit 0) | "failed" (timed out, no result, 126, 127) | "not-yet"
// supervisor.go
func (s *Supervisor) SetAlarm(actorID, name, schedule, note, gate string) (Alarm, error) // gate ≤ maxGateBytes
func (s *Supervisor) StartGate(actorID, name, runID string, now time.Time) (Alarm, bool, error)
    // under instMu: the alarm exists, is gated, due, not in flight, and the cove's turn is over → record GateRun, return it
func (s *Supervisor) ResolveGate(actorID, runID string, o GateOutcome) error
    // under instMu: find the alarm whose GateRun.RunID == runID (none → no-op: stale/cleared/re-set);
    // clear GateRun; LastGate = o; then by Verdict:
    //   pass   → FiredAt (if zero) = o.At, FireKind = WakeAlarm,      FireDetail = o.Output; NextAt advances (cron) / zero (one-shot)
    //   failed → FiredAt (if zero) = o.At, FireKind = WakeGateFailed, FireDetail = gateFailure(o); NextAt as above
    //   not-yet → cron: NextAt = NextAfter(now); one-shot: removed
// FireAlarms: skips alarms with a Gate (also stamps FireKind = WakeAlarm on the ones it fires)
// attach.Server:
func (s *Server) RunGate(actorID, runID, alarm, command string, timeout time.Duration) // enqueue ControlDown_Gate
// recv loop: StatusUp_Gate → sup.ResolveGate(actorID, runID, GateOutcome{At: now, Exit, TimedOut, Output: string(out) (+ marker if truncated)})
```
`gateFailure(o)`: `timed out after 60s`; `no result from the cove within 90s`; `exit 127 (command not found)` / `exit 126 (not executable)`; each followed by `":\n" + output` when output is non-empty.

- [ ] **Step 1: Failing tests** (supervisor):

```go
func gatedSession(t *testing.T) (*Supervisor, Store, *time.Time) {
	sup, store, now := raiseWithTurnEnd(t, TurnEndPolicy{})
	*now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err := sup.SetAlarm("w1", "ci", "*/5 * * * *", "CI changed", "gh run view --exit-status"); err != nil {
		t.Fatal(err)
	}
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	*now = time.Date(2026, 10, 5, 12, 5, 0, 0, time.UTC)
	return sup, store, now
}

func TestFireAlarmsSkipsGated(t *testing.T) {
	sup, _, now := gatedSession(t)
	got, _ := sup.FireAlarms("w1", *now)
	if !got[0].FiredAt.IsZero() {
		t.Fatalf("a gated alarm fired without its gate: %+v", got[0])
	}
}

func TestStartGateOncePerRun(t *testing.T) {
	sup, _, now := gatedSession(t)
	a, ok, err := sup.StartGate("w1", "ci", "r1", *now)
	if err != nil || !ok || a.GateRun == nil || a.GateRun.RunID != "r1" {
		t.Fatalf("start = %+v %v %v", a, ok, err)
	}
	if _, ok, _ := sup.StartGate("w1", "ci", "r2", *now); ok {
		t.Fatal("a second run started while one is in flight")
	}
}

func TestResolveGatePassFiresWithOutput(t *testing.T) {
	sup, store, now := gatedSession(t)
	_, _, _ = sup.StartGate("w1", "ci", "r1", *now)
	if err := sup.ResolveGate("w1", "r1", GateOutcome{At: *now, Exit: 0, Output: "all green"}); err != nil {
		t.Fatal(err)
	}
	a := mustAlarm(t, store, "ci")
	if a.FiredAt.IsZero() || a.FireKind != WakeAlarm || a.FireDetail != "all green" || a.GateRun != nil || !a.NextAt.Equal(time.Date(2026, 10, 5, 12, 10, 0, 0, time.UTC)) {
		t.Fatalf("alarm = %+v", a)
	}
}

func TestResolveGateFailureWakesWithCause(t *testing.T) {
	for _, o := range []GateOutcome{{Exit: -1, TimedOut: true}, {Exit: 127, Output: "sh: gh: not found"}, {Exit: 126}, {NoResult: true}} {
		sup, store, now := gatedSession(t)
		_, _, _ = sup.StartGate("w1", "ci", "r1", *now)
		o.At = *now
		_ = sup.ResolveGate("w1", "r1", o)
		a := mustAlarm(t, store, "ci")
		if a.FiredAt.IsZero() || a.FireKind != WakeGateFailed || a.FireDetail == "" {
			t.Fatalf("%+v → alarm %+v; want a gate-failed fire with a cause", o, a)
		}
	}
}

func TestResolveGateNotYet(t *testing.T) {
	sup, store, now := gatedSession(t)
	_, _ = sup.SetAlarm("w1", "once", "2026-10-05T12:01:00Z", "", "false") // set before the clock moved: due now
	_, _, _ = sup.StartGate("w1", "ci", "r1", *now)
	_, _, _ = sup.StartGate("w1", "once", "r2", *now)
	_ = sup.ResolveGate("w1", "r1", GateOutcome{At: *now, Exit: 1})
	_ = sup.ResolveGate("w1", "r2", GateOutcome{At: *now, Exit: 1})
	inst, _ := store.GetInstance("w1")
	if len(inst.Alarms) != 1 || inst.Alarms[0].Name != "ci" || !inst.Alarms[0].FiredAt.IsZero() || inst.Alarms[0].LastGate == nil {
		t.Fatalf("alarms = %+v; want ci un-fired with LastGate, the one-shot removed", inst.Alarms)
	}
}

func TestResolveGateIgnoresStaleRun(t *testing.T) {
	sup, store, now := gatedSession(t)
	_, _, _ = sup.StartGate("w1", "ci", "r1", *now)
	_, _ = sup.SetAlarm("w1", "ci", "*/5 * * * *", "CI changed", "true") // re-set while r1 runs
	if err := sup.ResolveGate("w1", "r1", GateOutcome{At: *now, Exit: 0}); err != nil {
		t.Fatal(err)
	}
	if a := mustAlarm(t, store, "ci"); !a.FiredAt.IsZero() {
		t.Fatalf("a stale run's result fired the re-set alarm: %+v", a)
	}
}
```

(`mustAlarm(t, store, name)` fetches `w1`'s alarm by name or fails. The `once` alarm in `TestResolveGateNotYet` must be set while the clock is still 12:00 — restructure `gatedSession` to accept extra setup if needed.) Also update the slice-3 `SetAlarm` call sites to the new 5-argument form (`""` gate). `SetAlarm` rejects a gate longer than 4096 bytes (add to `TestSetAlarmLimits`).

Attach server tests (`server_test.go`, following `TestWakeDelivery`): `TestRunGateDelivery` — `srv.RunGate("w1", "r1", "ci", "true", time.Minute)` arrives as `ControlDown_Gate` with `TimeoutS == 60`; `TestGateResultResolves` — with a raised instance that has a gated alarm with `GateRun{RunID: "r1"}` (set through the supervisor), the client sends `StatusUp_Gate{RunId: "r1", Exit: 0, Output: []byte("ok")}`; `eventually` the store's alarm has `FireKind == jam.WakeAlarm` and `FireDetail == "ok"`.

- [ ] **Step 2:** `go test ./internal/jam/... -run 'Gate|FireAlarms|SetAlarm'` — compile errors.
- [ ] **Step 3:** Implement (all supervisor methods under `instMu`, like slice 3). The truncation marker is appended by the attach server when `truncated` is set (`"\n[output truncated]"` — cove-master already adds it; append only if absent).
- [ ] **Step 4:** `go test ./internal/jam/... ./cmd/at-jam/` — PASS.
- [ ] **Step 5:** Commit `feat(jam): gated alarms — start, resolve, and the attach gate messages`.

---

### Task 4: Wake-on runs gates

**Files:** `internal/wakeon/wakeon.go`, `internal/wakeon/wakeon_test.go`, `cmd/at-jam/main.go`.

**Interfaces — Produces:**
```go
type GateRunner interface { // *attach.Server
    RunGate(actorID, runID, alarm, command string, timeout time.Duration)
}
type GateState interface { // *jam.Supervisor
    StartGate(actorID, name, runID string, now time.Time) (jam.Alarm, bool, error)
    ResolveGate(actorID, runID string, o jam.GateOutcome) error
}
func (e *Engine) SetGates(state GateState, runner GateRunner) // call before Run; nil = gated alarms never run
```

Behavior, for a `holding`/`waiting` session not asking to end (per tick, before computing reasons):
- For each alarm with a `Gate` and a due `NextAt`:
  - in flight (`GateRun != nil`): if `now ≥ StartedAt + GateTimeout + GateGrace` → `ResolveGate(runID, GateOutcome{At: now, NoResult: true})`; else nothing.
  - else if the cove is `idled` → `Resume` (once per tick) and run the gate on a later tick.
  - else → `runID := newRunID()` (16 random bytes, hex); `StartGate`; if started → `RunGate(actorID, runID, name, gate, jam.GateTimeout)`.
- `alarmReasons` emits `WakeReason{Kind: a.FireKind (default WakeAlarm), Alarm: a.Name, Note: a.Note, Detail: a.FireDetail}` for every fired alarm.
- A gate's "not yet" is invisible to wake-on (no fired alarm → no wake); the warm-timeout pause re-idles a resumed cove.

- [ ] **Step 1: Failing tests** — extend the test fakes: `fakeGates` implementing both interfaces against the registry (StartGate records a `GateRun`; ResolveGate applies the Verdict rules for pass/failed with `FireKind`/`FireDetail`; record `ran []string`). Tests:
  - `TestTick_GateRunsWhenDue` — waiting, live, gated alarm due → one `RunGate` with the alarm's command, no Wake yet.
  - `TestTick_GatePassWakesWithOutput` — after the fake resolves `pass` with output `"green"`, the next tick's Wake has `{Kind: alarm, Alarm: "ci", Detail: "green"}`.
  - `TestTick_GateFailureWakesGateFailed` — resolve `TimedOut` → next tick Wake `{Kind: gate-failed, Detail contains "timed out"}`.
  - `TestTick_GateNoResultFailsAfterGrace` — `GateRun.StartedAt = now - 91s` → `ResolveGate(NoResult)` called and, next tick, a `gate-failed` Wake.
  - `TestTick_GateInFlightNotRerun` — `GateRun.StartedAt = now - 10s` → no second `RunGate`, no resolve.
  - `TestTick_GateResumesPausedFirst` — idled → `Resume`, no `RunGate`; after `Phase = Live`, next tick → `RunGate`.
  - `TestTick_GateHeldWhileRunning` — running → no `RunGate`.
- [ ] **Step 2:** `go test ./internal/wakeon/` — compile errors.
- [ ] **Step 3:** Implement; wire `eng.SetGates(sup, rsrv)` in `cmd/at-jam/main.go`.
- [ ] **Step 4:** `go test ./internal/wakeon/ ./cmd/at-jam/` — PASS.
- [ ] **Step 5:** Commit `feat(wakeon): run alarm gates; wake on pass or failure`.

---

### Task 5: Endpoint + tool `gate`, list shows gate state, docs

**Files:** `internal/jam/alarm_handler.go` (+ test), `cmd/cove-master/mcp.go` (+ test), docs.

- [ ] **Step 1: Failing tests:** `PUT /alarms/ci` with `{"schedule":"*/5 * * * *","gate":"gh run view --exit-status"}` reaches `SetAlarm` with the gate (extend `fakeAlarmSetter` to record it); `GET /alarms` includes `"gate":…` and, when present, `"last_gate":{"at","verdict","exit","output"}`; `c.setAlarm(ctx, name, schedule, note, gate)` sends `"gate"`; `alarmItem` decodes `gate` and `last_gate`.
- [ ] **Step 2:** run — fail. **Step 3:** implement (`alarmView` gains `Gate string` and `LastGate *gateView{At, Verdict, Exit, Output}`; `alarm_set` gains `gate` with jsonschema: `"optional shell command run in your workspace when the alarm comes due (60s limit): exit 0 wakes you with its output; any other exit keeps sleeping; a timeout or a missing command wakes you with the error"`). **Step 4:** PASS.
- [ ] **Step 5: Docs** — `turn-end.md` §Alarms: a **Gates** subsection (the command, where/how it runs — workspace, agent env, `sh -c`, 60s, 4 KiB output; the three outcomes; the 30s grace and old cove-masters → `gate-failed`; one run at a time; a paused session is resumed to run it and re-paused by warm-timeout; a one-shot whose gate says not-yet is dropped; `alarm_list` shows the last gate result). Update the opening paragraph (gates live; `report` later), `summary`/`owns`, INDEX row if `read_when` changes. docs-audit clean for touched docs.
- [ ] **Step 6:** `go build ./... && go test -count=1 ./... && just lint` — PASS.
- [ ] **Step 7:** Commit `feat(jam): alarm gate parameter and status; docs for gates`. PR: `feat(jam): turn-end slice 4 — alarm gates`.
