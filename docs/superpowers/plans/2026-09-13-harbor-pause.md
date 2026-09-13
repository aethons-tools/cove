# harbor comms B2 — container pause/unpause Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Freeze an idle managed cove (`docker pause`) after a warm timeout and unpause it to wake, so idle coves burn ~0 CPU — on top of B1's suspend/resume loop.

**Architecture:** Backend + Launcher gain `Pause`/`Unpause`; the supervisor gains a `PhaseIdled` + `Idle`/`Resume` and skips Idled in Reconcile; the B1 wake-on engine drives it (warm-timeout → `Idle`; reply on Idled → `Resume`, then retry `Wake` until the cove reconnects and resumes).

**Tech Stack:** Go 1.25; existing `internal/{backend/colima,harbor,harbor/launcher,wakeon}` + `cmd/at-harbor`.

## Global Constraints

- `GOPROXY=off` for go commands (no new deps).
- **Ownership:** the wake-on engine goes through the supervisor (`Idle`/`Resume`), never the Launcher/backend directly. The supervisor owns Phase + the Launcher.
- **Idled = intentionally idle, not dead:** Reconcile must skip Idled (a paused cove can't heartbeat → its lease expires → without the skip the reconciler would adopt/reap it). The wake-on engine bounds it (`wait-max` → Teardown; `docker rm -f` works on a paused container).
- **Retry-Wake:** after `Resume` (unpause), the engine re-sends `Wake` on later ticks until the cove reports Running (Wake is non-blocking + drops if no stream — early wakes are harmless).
- **agentrun unchanged** (transparent to freeze/thaw); its `max-wait` stays a backstop ≥ harbor's `wait-max`.
- **Interface-change atomicity:** adding `Pause`/`Unpause` to `harbor.Launcher` (Task 2) must update *all* implementers in the same task (real launcher + `placeholderLauncher` + every fake) or the build breaks.
- TDD; hermetic tests; gofmt-clean.
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

---

### Task 1: Backend `Pause`/`Unpause`

**Files:** Modify `internal/backend/backend.go` (`DispatchOps`), `internal/backend/colima/dispatch.go`. Test: `internal/backend/colima/dispatch_test.go`.

**Interfaces:** Produces `DispatchOps.Pause(name) error` + `Unpause(name) error`; Colima impls. Tasks 2 consume via the `launcher.Backend` composite (embeds `DispatchOps`).

- [ ] **Step 1: failing test** — add to `dispatch_test.go`, mirroring `TestRunEphemeralArgs` (fake runner records argv):
```go
func TestPauseUnpauseArgs(t *testing.T) {
	fr := &fakeRunner{} // the file's fake runner type
	c := New(fr).(*Colima)
	if err := c.Pause("cove-1"); err != nil { t.Fatal(err) }
	if err := c.Unpause("cove-1"); err != nil { t.Fatal(err) }
	// assert one `docker pause cove-1` and one `docker unpause cove-1` were run
}
```
(Use the file's real fake-runner recording API + `dargs` expectations — read the existing tests.)

- [ ] **Step 2:** run → fail (undefined).
- [ ] **Step 3: implement.** In `backend.go` `DispatchOps`, add:
```go
	Pause(name string) error   // docker pause; freeze an idle container (cgroup freezer)
	Unpause(name string) error // docker unpause; thaw it
```
In `colima/dispatch.go`, mirror `RemoveContainer`:
```go
func (c *Colima) Pause(name string) error {
	if err := c.preflight(); err != nil { return err }
	return c.r.Run("docker", dargs("pause", name)...)
}
func (c *Colima) Unpause(name string) error {
	if err := c.preflight(); err != nil { return err }
	return c.r.Run("docker", dargs("unpause", name)...)
}
```
- [ ] **Step 4:** `GOPROXY=off go test ./internal/backend/... && GOPROXY=off go build ./...`. (Adding to `DispatchOps` may break other implementers — there is only Colima; the `launcher.Backend` composite inherits the new methods, so `launcher`'s fake `DispatchOps` in `internal/harbor/launcher/launcher_test.go` needs the two no-op methods too — add them if the build flags it.)
- [ ] **Step 5:** gofmt + commit (`backend: Pause/Unpause (docker pause/unpause) for idle coves (COV-162)`).

---

### Task 2: `harbor.Launcher` seam — `Pause`/`Unpause` (all implementers)

**Files:** Modify `internal/harbor/supervisor.go` (Launcher interface), `internal/harbor/launcher/launcher.go` (real impl), `cmd/at-harbor/main.go` (placeholderLauncher), and every fake launcher in tests. Test: `internal/harbor/launcher/launcher_test.go`.

**Interfaces:** `Launcher` gains `Pause(ctx, inst Instance) error` + `Unpause(ctx, inst Instance) error`.

- [ ] **Step 1:** add to the `Launcher` interface (supervisor.go):
```go
	Pause(ctx context.Context, inst Instance) error
	Unpause(ctx context.Context, inst Instance) error
```
- [ ] **Step 2:** implement on the real launcher (`internal/harbor/launcher/launcher.go`) — `l.cfg.Ops` is the `Backend` composite (embeds `DispatchOps`, so it has `Pause`/`Unpause` from Task 1):
```go
func (l *Launcher) Pause(ctx context.Context, inst harbor.Instance) error   { return l.cfg.Ops.Pause(inst.Location) }
func (l *Launcher) Unpause(ctx context.Context, inst harbor.Instance) error { return l.cfg.Ops.Unpause(inst.Location) }
```
Add a test: `fakeOps` records Pause/Unpause; `l.Pause(ctx, Instance{Location:"cove-1"})` → `ops.paused == "cove-1"`.
- [ ] **Step 3:** update **every other implementer** with no-op methods. Find them: `grep -rln ') Raise(' --include=*.go` (implementers of the seam). Known set: `cmd/at-harbor/main.go` (`placeholderLauncher`), and fakes in `internal/harbor/supervisor_test.go`, `internal/harbor/attach/server_test.go`, `internal/harbor/adminclient/adminclient_test.go`, `internal/covemaster/client_test.go`, `cmd/at-harbor/main_test.go`. Each gets:
```go
func (X) Pause(context.Context, harbor.Instance) error   { return nil }
func (X) Unpause(context.Context, harbor.Instance) error { return nil }
```
(match the receiver name/type + whether it's `harbor.Instance` or `Instance` per the file's package).
- [ ] **Step 4:** `GOPROXY=off go build ./... && GOPROXY=off go test ./internal/harbor/... ./cmd/at-harbor/ ./internal/covemaster/ -v` → all pass (the fake-launcher additions unblock the build).
- [ ] **Step 5:** gofmt + commit (`harbor: Launcher Pause/Unpause seam + real impl + fake/placeholder updates (COV-162)`).

---

### Task 3: Supervisor — `PhaseIdled` + `Idle`/`Resume` + Reconcile skip

**Files:** Modify `internal/harbor/instance.go`, `internal/harbor/supervisor.go`. Test: `internal/harbor/supervisor_test.go`.

- [ ] **Step 1: failing tests** — add to `supervisor_test.go`: `Idle("w1")` → the fake launcher's Pause called + instance Phase==PhaseIdled; `Resume("w1")` → Unpause called + Phase==PhaseLive + WaitingSince advanced; `Reconcile` with an Idled instance whose lease is expired → the fake launcher's Probe/Teardown are NOT called and the instance stays Idled (skipped). (Extend the fake launcher to record Pause/Unpause — done in Task 2.)
- [ ] **Step 2:** run → fail.
- [ ] **Step 3: implement.**
`instance.go`: add `PhaseIdled Phase = "idled"` (doc: paused; intentionally idle — lease-reaping suspended).
`supervisor.go`: add `Idle`/`Resume` (per the spec):
```go
func (s *Supervisor) Idle(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone { return fmt.Errorf("idle: no live instance for %q", actorID) }
	if err := s.launcher.Pause(ctx, inst); err != nil { return err }
	inst.Phase = PhaseIdled
	return s.store.PutInstance(inst)
}
func (s *Supervisor) Resume(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone { return fmt.Errorf("resume: no live instance for %q", actorID) }
	if err := s.launcher.Unpause(ctx, inst); err != nil { return err }
	inst.Phase = PhaseLive
	inst.WaitingSince = s.now()
	return s.store.PutInstance(inst)
}
```
In `Reconcile`, after the `PhaseGone` continue, add:
```go
		if inst.Phase == PhaseIdled {
			continue // paused on purpose; the wake-on engine owns its lifecycle (resume/teardown)
		}
```
- [ ] **Step 4:** `GOPROXY=off go test ./internal/harbor/ -v && GOPROXY=off go build ./...` → pass.
- [ ] **Step 5:** gofmt + commit (`harbor: PhaseIdled + supervisor Idle/Resume + Reconcile skips Idled (COV-162)`).

---

### Task 4: Wake-on engine — pause/resume state machine

**Files:** Modify `internal/wakeon/wakeon.go`, `internal/wakeon/wakeon_test.go`.

**Interfaces:** adds `Idler` dep + `Config.WarmTimeout`; `New` gains the `idler` param.

- [ ] **Step 1: failing tests** — extend `wakeon_test.go` with a fake `Idler` (records Idle/Resume) and cases: Live+Waiting, no reply, `wait > WarmTimeout` → `Idle` called; `Phase==Idled` + reply → `Resume` called (no `Wake` that tick); Live + reply → `Wake` (no Idle); `Phase==Idled` + no reply + `wait <= MaxWait` → nothing; `wait > MaxWait` (Idled or Live) → `Teardown`. Use the injected `now` for the clock. Instances carry `Phase` (set `harbor.PhaseLive`/`harbor.PhaseIdled` in the fakes).
- [ ] **Step 2:** run → fail.
- [ ] **Step 3: implement.**
Add the interface + config + New param:
```go
type Idler interface {
	Idle(ctx context.Context, actorID string) error
	Resume(ctx context.Context, actorID string) error
}
type Config struct{ PollInterval, MaxWait, WarmTimeout time.Duration }
const defaultWarmTimeout = 60 * time.Second
// New(reg, cur, wake, reap, idler Idler, cmt, cfg, log): default WarmTimeout when <=0; store idler.
```
Rewrite `tick`'s per-instance body (keeping the `Activity != ActivityWaiting` skip + the max-wait teardown + the baseline):
```go
	for _, inst := range e.reg.ListInstances() {
		if inst.Activity != harbor.ActivityWaiting { continue }
		if !inst.WaitingSince.IsZero() && e.now().Sub(inst.WaitingSince) > e.cfg.MaxWait {
			if err := e.reap.Teardown(ctx, inst.ActorID); err != nil { e.log.Warn("wakeon: teardown (max-wait) failed", "actor", inst.ActorID, "error", err.Error()) }
			continue
		}
		issueID, err := e.cmt.IssueByIdentifier(ctx, inst.Unit)
		if err != nil { e.log.Warn("wakeon: resolve ticket failed", "actor", inst.ActorID, "error", err.Error()); continue }
		comments, err := e.cmt.Comments(ctx, issueID)
		if err != nil { e.log.Warn("wakeon: read comments failed", "actor", inst.ActorID, "error", err.Error()); continue }
		n := len(comments)
		if inst.WaitCursor == "" { // baseline (a fresh Live+Waiting cove; an Idled one was baselined pre-pause)
			if err := e.cur.SetWaitCursor(inst.ActorID, strconv.Itoa(n)); err != nil { e.log.Warn("wakeon: set cursor failed", "actor", inst.ActorID, "error", err.Error()) }
			continue
		}
		base, _ := strconv.Atoi(inst.WaitCursor)
		if n > base { // a reply arrived
			if inst.Phase == harbor.PhaseIdled {
				if err := e.idler.Resume(ctx, inst.ActorID); err != nil { e.log.Warn("wakeon: resume failed", "actor", inst.ActorID, "error", err.Error()) }
				// Wake is sent on a later tick, once it's Live+Waiting and the stream has reconnected.
				continue
			}
			e.log.Info("wakeon: reply detected, waking", "actor", inst.ActorID)
			e.wake.Wake(inst.ActorID)
			continue
		}
		// no reply
		if inst.Phase != harbor.PhaseIdled && e.now().Sub(inst.WaitingSince) > e.cfg.WarmTimeout {
			if err := e.idler.Idle(ctx, inst.ActorID); err != nil { e.log.Warn("wakeon: idle (pause) failed", "actor", inst.ActorID, "error", err.Error()) }
		}
	}
```
- [ ] **Step 4:** `GOPROXY=off go test ./internal/wakeon/ -v && GOPROXY=off go build ./...` → pass.
- [ ] **Step 5:** gofmt + commit (`wakeon: pause a warm-idle cove, resume+re-wake on reply (COV-162)`).

---

### Task 5: Wiring + config (`cmd/at-harbor`)

**Files:** Modify `cmd/at-harbor/config.go` (`dispatcherConfig`), `cmd/at-harbor/main.go` (the `wakeon.New` call). Test: `cmd/at-harbor/config_test.go`.

- [ ] **Step 1:** add `WarmTimeout string \`yaml:"warm-timeout"\`` to `dispatcherConfig`; extend the config parse test to assert it lands.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** in `cmdServe`, update the `wakeon.New(...)` call to pass `sup` as the new `Idler` arg and `WarmTimeout` in the Config:
```go
	warm, _ := time.ParseDuration(dc.WarmTimeout)
	eng := wakeon.New(st, sup, rsrv, sup, sup /*Idler*/, linearCommenter{tracker}, wakeon.Config{PollInterval: wpoll, MaxWait: wmax, WarmTimeout: warm}, log)
```
(adapt to the exact New arg order after Task 4). No new launcher wiring — `runtime.launcher`'s real launcher (COV-158) already flows `Pause`/`Unpause` through `sup.Idle`/`Resume`.
- [ ] **Step 4:** `GOPROXY=off go build ./... && GOPROXY=off go test ./cmd/at-harbor/ ./internal/wakeon/ -v`; boundary `go list -deps ./internal/harbor | grep -iE 'wakeon|internal/dispatch|grpc' || echo "harbor core clean"`.
- [ ] **Step 5:** gofmt + commit (`at-harbor: wire the Idler + warm-timeout into the wake-on engine (COV-162)`).

---

### Task 6: Docs

**Files:** Modify `docs/usage/harbor/messaging.md`, `docs/usage/harbor/coves.md`.

- [ ] **Step 1:** `messaging.md` — update the "Waiting for a reply (wake-on)" section: an idle cove is now **paused** (`docker pause`, ~0 CPU) after `warm-timeout` and **unpaused** to resume when a reply arrives; `wait-max` bounds total wait (paused or not); document `warm-timeout`. Drop the "the cove stays up idle … freezing … is the next slice" note (it's now this slice).
- [ ] **Step 2:** `coves.md` — add the `idled` phase to the lifecycle line (run → waiting → idled(paused) → wake/resume → done).
- [ ] **Step 3:** docs-audit delta (no new errors referencing the harbor usage docs).
- [ ] **Step 4:** commit (`docs: container-pause idle lifecycle (COV-162)`).

---

## Self-Review

- **Spec coverage:** backend Pause/Unpause (T1); Launcher seam + all impls (T2); PhaseIdled + Idle/Resume + Reconcile skip (T3); engine state machine (T4); wiring+config (T5); docs (T6). All map.
- **Type consistency:** `DispatchOps.Pause/Unpause` (T1) inherited by `launcher.Backend`; `harbor.Launcher.Pause/Unpause(ctx, Instance)` implemented by real+placeholder+fakes (T2); `Supervisor.Idle/Resume` satisfy the engine's `Idler` (T3/T4); `wakeon.New` gains `idler` consistent T4/T5.
- **Ordering:** T1 (backend) → T2 (launcher seam, needs backend Pause/Unpause) → T3 (supervisor, needs launcher.Pause/Unpause) → T4 (engine, needs Idle/Resume + PhaseIdled) → T5 (wiring) → T6.
- **Placeholder scan:** none — code steps carry full code; the fake-launcher set is enumerated + a grep given to catch any missed implementer.
- **State-machine invariants:** reply branch precedes pause (a woken cove is never re-paused); Resume resets WaitingSince (fresh reconnect window) but not WaitCursor (reply stays pending → retry-Wake until Running); Reconcile skips Idled (no reap/adopt of a paused cove); teardown-past-wait-max works on Idled via `docker rm -f`.
