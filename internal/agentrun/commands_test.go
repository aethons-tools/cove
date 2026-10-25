package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

func TestCmdBoxTakesOnceAndCollapses(t *testing.T) {
	b := newCmdBox()
	if _, ok := b.takeInterrupt(); ok {
		t.Fatal("empty box gave an interrupt")
	}
	b.postInterrupt("Ann")
	b.postCompact("Ann", "keep a")
	b.postCompact("Bob", "keep b") // two compacts collapse; the last wins
	b.postClear("Cy")
	select {
	case <-b.signal():
	default:
		t.Fatal("post did not signal")
	}
	if by, ok := b.takeInterrupt(); !ok || by != "Ann" {
		t.Fatalf("takeInterrupt = %q, %v", by, ok)
	}
	if by, in, ok := b.takeCompact(); !ok || by != "Bob" || in != "keep b" {
		t.Fatalf("takeCompact = %q, %q, %v", by, in, ok)
	}
	if by, ok := b.takeClear(); !ok || by != "Cy" {
		t.Fatalf("takeClear = %q, %v", by, ok)
	}
	if _, ok := b.takeInterrupt(); ok {
		t.Fatal("interrupt taken twice")
	}
	if _, _, ok := b.takeCompact(); ok {
		t.Fatal("compact taken twice")
	}
	if _, ok := b.takeClear(); ok {
		t.Fatal("clear taken twice")
	}
}

func TestInterruptPrompt(t *testing.T) {
	ack := "Ann pressed **Interrupt** in the session header, which stopped your turn. Don't resume what you were doing. " +
		"In your channel, acknowledge the interrupt with `send`, ask what they want instead, then end your turn."
	if got := interruptPrompt("Ann", nil); got != ack {
		t.Fatalf("got %q\nwant %q", got, ack)
	}
	got := interruptPrompt("Ann", []covemaster.WakeReason{{Kind: "squawk"}, {Kind: "alarm", Alarm: "a", Note: "n"}})
	want := ack + "\nNew messages may be waiting. Read them, but don't act on any request until the person who interrupted you answers." +
		"\nAlarm \"a\" fired: n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// interruptRun starts a resident Run and returns the first episode with its
// prompt read.
func interruptRun(t *testing.T, h *recordHandle) (*Workload, *streamProc) {
	t.Helper()
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, h)
	t.Cleanup(func() { cancel(); <-done })
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	return w, p
}

func interrupt(w *Workload, by string) {
	w.Control(covemaster.Control{Kind: covemaster.Interrupt, By: by})
}

func TestInterruptMidTurnAcks(t *testing.T) {
	w, p := interruptRun(t, &recordHandle{})
	interrupt(w, "Ann")
	if got := p.in.nextCtrl(t); got != "interrupt" {
		t.Fatalf("control request = %q", got)
	}
	p.in.noMessage(t, 30*time.Millisecond) // nothing until the turn ends
	p.emit(lnToolRes, lnResult)
	got := p.in.next(t)
	if !strings.Contains(got, "Ann pressed **Interrupt**") || strings.Contains(got, "New messages may be waiting") {
		t.Fatalf("acknowledge prompt = %q", got)
	}
}

func TestInterruptFoldsCoalescedWakes(t *testing.T) {
	w, p := interruptRun(t, &recordHandle{})
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "alarm", Alarm: "a", Note: "n"}}})
	interrupt(w, "Ann")
	p.in.nextCtrl(t)
	p.emit(lnToolRes, lnResult)
	got := p.in.next(t)
	for _, want := range []string{"Ann pressed **Interrupt**", "New messages may be waiting", `Alarm "a" fired`} {
		if !strings.Contains(got, want) {
			t.Fatalf("acknowledge prompt %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, residentResumePrompt) || strings.Contains(got, "Continue.") {
		t.Fatalf("acknowledge prompt carries a resume prompt: %q", got)
	}
	p.in.noMessage(t, 100*time.Millisecond) // the wakes were answered by the ack
}

func TestInterruptTwiceOneAck(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	interrupt(w, "Ann")
	p.in.nextCtrl(t)
	interrupt(w, "Bob") // taken on its own: the turn is already being interrupted
	p.in.noCtrl(t, 100*time.Millisecond)
	if !hasIgnored(h, "interrupt", "Bob", "already interrupting") {
		t.Fatalf("no ignored record in %+v", h.eventList())
	}
	p.emit(lnToolRes, lnResult)
	if got := p.in.next(t); !strings.Contains(got, "Ann pressed **Interrupt**") {
		t.Fatalf("acknowledge prompt = %q", got)
	}
	p.in.noMessage(t, 100*time.Millisecond)
	if !hasCommand(h, "interrupt", "Ann") || commandCount(h) != 2 {
		t.Fatalf("want one record per press in %+v", h.eventList())
	}
}

func TestInterruptBetweenTurnsDropped(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	p.emit(lnTasks1, lnResult) // the turn ended; a background task keeps stdin open
	if !eventually(func() bool { return h.count(covemaster.Holding) == 1 }) {
		t.Fatal("no Holding")
	}
	interrupt(w, "Ann")
	p.in.noCtrl(t, 100*time.Millisecond)
	p.in.noMessage(t, 100*time.Millisecond)
	if !hasIgnored(h, "interrupt", "Ann", "not in a turn") || commandCount(h) != 1 {
		t.Fatalf("want one ignored record in %+v", h.eventList())
	}
}

func TestInterruptRecordsEvent(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	interrupt(w, "Ann")
	p.in.nextCtrl(t)
	want := `{"type":"cove","subtype":"command","command":"interrupt","by":"Ann"}`
	if !eventually(func() bool {
		for _, ev := range h.eventList() {
			if ev.raw == want && ev.turn == 1 {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no command record %s in %+v", want, h.eventList())
	}
}

// With no live episode (the process exited, Run waiting), an Interrupt is
// dropped: no turn starts.
func TestInterruptWhileWaitingDropped(t *testing.T) {
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
	p.exit <- nil
	if !eventually(func() bool { return h.count(covemaster.Waiting) == 1 }) {
		t.Fatal("not waiting")
	}
	interrupt(w, "Ann")
	select {
	case p2 := <-s.procs:
		t.Fatalf("an interrupt started an episode: %+v", p2)
	case <-time.After(100 * time.Millisecond):
	}
	if !hasIgnored(h, "interrupt", "Ann", "no live episode") {
		t.Fatalf("no ignored record in %+v", h.eventList())
	}
	// The box is drained: a later Wake starts a normal episode, not an interrupted one.
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
	p2 := s.next(t)
	if got := p2.in.next(t); got != residentResumePrompt {
		t.Fatalf("resume prompt = %q", got)
	}
	p2.in.noCtrl(t, 50*time.Millisecond)
	cancel()
	<-done
}

// An Interrupt pressed while stdin is closed (here after the background-wait
// cap) never reaches the next episode, even when a Wake starts that episode.
func TestInterruptBeforeEpisodeDropped(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true; c.BackgroundWait = 30 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &recordHandle{}
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult)
	p.in.waitClosed(t) // the cap closed stdin; the agent stops its tasks
	interrupt(w, "Ann")
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
	p.exit <- nil
	p2 := s.next(t)
	if got := p2.in.next(t); got != residentResumePrompt {
		t.Fatalf("resume prompt = %q", got)
	}
	p2.in.noCtrl(t, 100*time.Millisecond)
	// Taken while waiting or at the episode's start, whichever sees it first.
	if !(hasIgnored(h, "interrupt", "Ann", "no live episode") || hasIgnored(h, "interrupt", "Ann", "before this episode")) ||
		commandCount(h) != 1 {
		t.Fatalf("want one ignored record in %+v", h.eventList())
	}
	cancel()
	<-done
}

// An Interrupt already pending when an episode starts is recorded as ignored.
func TestInterruptPendingAtStartRecorded(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	w.cmds.postInterrupt("Ann") // before Run: no episode has started
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, h)
	t.Cleanup(func() { cancel(); <-done })
	p := s.next(t)
	p.in.next(t)
	p.in.noCtrl(t, 50*time.Millisecond)
	if !eventually(func() bool { return hasIgnored(h, "interrupt", "Ann", "before this episode") }) {
		t.Fatalf("no ignored record in %+v", h.eventList())
	}
}

// pressOnRunning presses Interrupt the moment Running is reported, as a member
// who just saw the session go running.
type pressOnRunning struct {
	*recordHandle
	w *Workload
}

func (h pressOnRunning) Report(a covemaster.Activity) {
	h.recordHandle.Report(a)
	if a == covemaster.Running {
		interrupt(h.w, "Ann")
	}
}

// An Interrupt pressed once Running is reported reaches the episode's turn.
func TestInterruptAfterRunningReported(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, pressOnRunning{h, w})
	t.Cleanup(func() { cancel(); <-done })
	p := s.next(t)
	p.in.next(t)
	if got := p.in.nextCtrl(t); got != "interrupt" {
		t.Fatalf("control request = %q", got)
	}
	if !eventually(func() bool { return hasCommand(h, "interrupt", "Ann") }) {
		t.Fatalf("no interrupt record in %+v", h.eventList())
	}
}

func TestCompactText(t *testing.T) {
	if got := compactText("keep X"); got != "/compact keep X" {
		t.Fatalf("compactText = %q", got)
	}
	if got := compactText(""); got != "/compact" {
		t.Fatalf("compactText(\"\") = %q", got)
	}
}

func compact(w *Workload, by, instructions string) {
	w.Control(covemaster.Control{Kind: covemaster.Compact, By: by, Instructions: instructions})
}

func squawk(w *Workload) {
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
}

func TestCompactMidTurnWaitsForResult(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	compact(w, "Ann", "keep X")
	p.in.noMessage(t, 100*time.Millisecond)
	p.emit(lnResult)
	if got := p.in.next(t); got != "/compact keep X" {
		t.Fatalf("message after the result = %q", got)
	}
	want := `{"type":"cove","subtype":"command","command":"compact","by":"Ann"}`
	if !eventually(func() bool {
		for _, ev := range h.eventList() {
			if ev.raw == want && ev.turn == 1 {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no command record %s", want)
	}
	p.in.staysOpen(t, 30*time.Millisecond) // the compaction runs
	p.emit(lnInit, lnResult)
	p.in.waitClosed(t) // then the normal turn-end logic: idle, close
}

func TestCompactThenCoalescedWake(t *testing.T) {
	w, p := interruptRun(t, &recordHandle{})
	squawk(w)
	compact(w, "Ann", "keep X")
	p.in.noMessage(t, 50*time.Millisecond)
	p.emit(lnResult)
	if got := p.in.next(t); got != "/compact keep X" {
		t.Fatalf("first message after the result = %q", got)
	}
	p.in.noMessage(t, 50*time.Millisecond) // the wake waits for the compaction
	p.emit(lnInit, lnResult)
	if got := p.in.next(t); got != residentResumePrompt {
		t.Fatalf("message after the compaction = %q", got)
	}
}

func TestCompactHoldingWritesNow(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	p.emit(lnTasks1, lnResult)
	if !eventually(func() bool { return h.count(covemaster.Holding) == 1 }) {
		t.Fatal("no Holding")
	}
	compact(w, "Ann", "keep X")
	if got := p.in.next(t); got != "/compact keep X" {
		t.Fatalf("message = %q", got)
	}
	running := h.count(covemaster.Running)
	p.emit(lnInit)
	time.Sleep(30 * time.Millisecond) // the loop sees the compaction's turn in flight
	p.emit(lnResult)                  // compacted; the task is still outstanding
	p.in.staysOpen(t, 50*time.Millisecond)
	if n := h.count(covemaster.Holding); n != 1 {
		t.Fatalf("Holding reported %d times; the session never left the hold", n)
	}
	if n := h.count(covemaster.Running); n != running {
		t.Fatalf("Running reported during the compaction (%d, was %d)", n, running)
	}
}

func TestInterruptDuringCompactIgnored(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	compact(w, "Ann", "keep X")
	p.in.noMessage(t, 30*time.Millisecond) // taken mid-turn
	p.emit(lnResult)
	p.in.next(t) // the boundary /compact
	p.emit(lnInit)
	interrupt(w, "Bob")
	p.in.noCtrl(t, 100*time.Millisecond)
	if !hasIgnored(h, "interrupt", "Bob", "compacting") {
		t.Fatalf("no ignored record in %+v", h.eventList())
	}
	p.emit(lnResult)
	p.in.waitClosed(t) // no acknowledge turn either
}

// answerOn makes p's agent finish its turn before the stdin write that starts
// it returns, for messages with prefix.
func answerOn(p *streamProc, prefix string) {
	f := func(msg string) {
		if strings.HasPrefix(msg, prefix) {
			p.emit(lnInit, lnResult)
		}
	}
	p.in.onWrite.Store(&f)
}

// A compaction whose result arrives before the /compact write returns still
// ends: the tracker is marked busy before the write, not after.
func TestCompactFastResultNotWedged(t *testing.T) {
	w, p := interruptRun(t, &recordHandle{})
	answerOn(p, "/compact")
	compact(w, "Ann", "keep X")
	p.in.noMessage(t, 30*time.Millisecond)
	p.emit(lnResult)
	if got := p.in.next(t); got != "/compact keep X" {
		t.Fatalf("message = %q", got)
	}
	p.in.waitClosed(t) // the compaction's turn ended: idle, close
}

// Likewise an acknowledge turn that ends before its write returns.
func TestInterruptFastAckNotWedged(t *testing.T) {
	w, p := interruptRun(t, &recordHandle{})
	answerOn(p, "Ann pressed")
	interrupt(w, "Ann")
	p.in.nextCtrl(t)
	p.emit(lnToolRes, lnResult)
	if got := p.in.next(t); !strings.Contains(got, "Ann pressed **Interrupt**") {
		t.Fatalf("acknowledge prompt = %q", got)
	}
	p.in.waitClosed(t)
}

// Likewise a resumed turn (a Wake delivered while holding) that ends before
// its write returns: the hold carries on and its cap closes stdin.
func TestWakeFastResultNotWedged(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true; c.BackgroundWait = 300 * time.Millisecond })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, h)
	t.Cleanup(func() { cancel(); <-done })
	p := s.next(t)
	p.in.next(t)
	answerOn(p, residentResumePrompt)
	p.emit(lnInit, lnTasks1, lnResult)
	if !eventually(func() bool { return h.count(covemaster.Holding) == 1 }) {
		t.Fatal("no Holding")
	}
	squawk(w)
	if got := p.in.next(t); got != residentResumePrompt {
		t.Fatalf("resume prompt = %q", got)
	}
	p.in.waitClosed(t)
}

// A Compact pressed while a compaction's turn is in flight is dropped.
func TestCompactDuringCompactIgnored(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	compact(w, "Ann", "keep X")
	p.in.noMessage(t, 30*time.Millisecond)
	p.emit(lnResult)
	p.in.next(t) // the boundary /compact
	p.emit(lnInit)
	compact(w, "Bob", "keep Y")
	if !eventually(func() bool { return hasIgnored(h, "compact", "Bob", "already compacting") }) {
		t.Fatalf("no ignored record in %+v", h.eventList())
	}
	p.emit(lnResult)
	p.in.waitClosed(t) // no second /compact
	if !hasCommand(h, "compact", "Ann") || commandCount(h) != 2 {
		t.Fatalf("want one record per press in %+v", h.eventList())
	}
}

// Both owed at one turn boundary: the interrupt's acknowledge turn first,
// then the compact.
func TestInterruptThenCompactAckFirst(t *testing.T) {
	w, p := interruptRun(t, &recordHandle{})
	interrupt(w, "Ann")
	p.in.nextCtrl(t)
	compact(w, "Bob", "keep X")
	p.in.noMessage(t, 30*time.Millisecond)
	p.emit(lnToolRes, lnResult)
	if got := p.in.next(t); !strings.Contains(got, "Ann pressed **Interrupt**") {
		t.Fatalf("first message = %q, want the acknowledge prompt", got)
	}
	p.in.noMessage(t, 30*time.Millisecond)
	p.emit(lnInit, lnResult)
	if got := p.in.next(t); got != "/compact keep X" {
		t.Fatalf("second message = %q", got)
	}
}

// idleRun starts a resident Run whose first episode has ended and Waiting is
// reported.
func idleRun(t *testing.T, h *recordHandle) (*Workload, *streamSpawner, context.CancelFunc, chan error) {
	t.Helper()
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, h)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return")
		}
	})
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnResult)
	p.in.waitClosed(t)
	p.exit <- nil
	if !eventually(func() bool { return h.count(covemaster.Waiting) == 1 }) {
		t.Fatal("not waiting")
	}
	return w, s, cancel, done
}

func TestCompactIdleSpawnsMaintenance(t *testing.T) {
	h := &recordHandle{}
	w, s, _, _ := idleRun(t, h)
	compact(w, "Ann", "keep X")
	m := s.next(t)
	if !hasArg(m.args, "--continue") {
		t.Fatalf("maintenance spawn missing --continue: %v", m.args)
	}
	if got := m.in.next(t); got != "/compact keep X" {
		t.Fatalf("maintenance message = %q", got)
	}
	m.emit(lnInit, lnResult)
	m.in.waitClosed(t)
	m.exit <- nil
	time.Sleep(50 * time.Millisecond)
	if n := h.count(covemaster.Running); n != 1 {
		t.Fatalf("Running reported %d times; the maintenance episode must not report it", n)
	}
	if n := h.count(covemaster.Waiting); n != 1 {
		t.Fatalf("Waiting reported %d times; the session never left waiting", n)
	}
	want := `{"type":"cove","subtype":"command","command":"compact","by":"Ann"}`
	found := false
	for _, ev := range h.eventList() {
		if ev.raw == want && ev.turn == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no command record %s on turn 2 in %+v", want, h.eventList())
	}
}

func TestWakeDuringIdleCompactWaits(t *testing.T) {
	h := &recordHandle{}
	w, s, _, _ := idleRun(t, h)
	compact(w, "Ann", "keep X")
	m := s.next(t)
	m.in.next(t)
	squawk(w)
	m.in.noMessage(t, 100*time.Millisecond)
	m.emit(lnInit, lnResult)
	m.in.waitClosed(t)
	m.exit <- nil
	p := s.next(t)
	if got := p.in.next(t); got != residentResumePrompt {
		t.Fatalf("resume prompt = %q", got)
	}
	if !eventually(func() bool { return h.count(covemaster.Running) == 2 }) {
		t.Fatalf("Running not reported for the resumed episode (running=%d)", h.count(covemaster.Running))
	}
}

// A maintenance episode that never finishes is stopped after compactTimeout;
// Run goes back to waiting.
func TestCompactIdleTimeout(t *testing.T) {
	old := compactTimeout
	compactTimeout = 50 * time.Millisecond
	t.Cleanup(func() { compactTimeout = old })
	h := &recordHandle{}
	w, s, _, done := idleRun(t, h)
	compact(w, "Ann", "keep X")
	m := s.next(t)
	m.in.next(t) // never answered: the timeout kills it
	assertBlocked(t, done, 150*time.Millisecond)
	squawk(w)
	p := s.next(t)
	if got := p.in.next(t); got != residentResumePrompt {
		t.Fatalf("resume prompt = %q", got)
	}
}

// Teardown during a maintenance episode still ends Run with the ctx error.
func TestCompactIdleCancelReturns(t *testing.T) {
	w, s, cancel, done := idleRun(t, &recordHandle{})
	compact(w, "Ann", "keep X")
	m := s.next(t)
	m.in.next(t)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	done <- nil // for the cleanup
}

// A Clear posted during a maintenance episode is left for the wait after it.
func TestClearDuringIdleCompactLeftForWait(t *testing.T) {
	w, s, _, _ := idleRun(t, &recordHandle{})
	compact(w, "Ann", "keep X")
	m := s.next(t)
	m.in.next(t)
	w.Control(covemaster.Control{Kind: covemaster.Clear, By: "Bob"})
	time.Sleep(30 * time.Millisecond)
	w.cmds.mu.Lock()
	pending := w.cmds.clear
	w.cmds.mu.Unlock()
	if !pending {
		t.Fatal("the clear was taken during the maintenance episode")
	}
	m.emit(lnInit, lnResult)
	m.in.waitClosed(t)
	m.exit <- nil
}

func TestClearedNotice(t *testing.T) {
	want := "Your conversation was cleared at Bob's request (the Clear button in the session header). " +
		"Your workspace and memory files are as you left them."
	if got := clearedNotice("Bob"); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func clear(w *Workload, by string) {
	w.Control(covemaster.Control{Kind: covemaster.Clear, By: by})
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasCommand reports whether h recorded the session-header command record for
// command pressed by by and carried out.
func hasCommand(h *recordHandle, command, by string) bool { return hasIgnored(h, command, by, "") }

// hasIgnored reports whether h recorded the command record for command pressed
// by by and dropped for why ("" = carried out).
func hasIgnored(h *recordHandle, command, by, why string) bool {
	want := `{"type":"cove","subtype":"command","command":"` + command + `","by":"` + by + `"`
	if why != "" {
		want += `,"ignored":"` + why + `"`
	}
	want += "}"
	for _, ev := range h.eventList() {
		if ev.raw == want {
			return true
		}
	}
	return false
}

// commandCount is how many command records h holds.
func commandCount(h *recordHandle) int {
	n := 0
	for _, ev := range h.eventList() {
		if strings.HasPrefix(ev.raw, `{"type":"cove","subtype":"command"`) {
			n++
		}
	}
	return n
}

func TestClearNextEpisodeFresh(t *testing.T) {
	h := &recordHandle{}
	w, s, _, _ := idleRun(t, h)
	clear(w, "Bob")
	if !eventually(func() bool { return exists(w.cfg.ClearMarker) }) {
		t.Fatal("clear marker not written")
	}
	if b, err := os.ReadFile(w.cfg.ClearMarker); err != nil || string(b) != "Bob" {
		t.Fatalf("clear marker = %q, %v; want Bob", b, err)
	}
	if !eventually(func() bool { return hasCommand(h, "clear", "Bob") }) {
		t.Fatalf("no clear command record in %+v", h.eventList())
	}
	select {
	case p := <-s.procs:
		t.Fatalf("a clear started an episode: %v", p.args)
	case <-time.After(50 * time.Millisecond):
	}
	if n := h.count(covemaster.Running); n != 1 {
		t.Fatalf("Running reported %d times; a clear starts no turn", n)
	}
	squawk(w)
	p := s.next(t)
	if hasArg(p.args, "--continue") {
		t.Fatalf("the episode after a clear must start fresh: %v", p.args)
	}
	got := p.in.next(t)
	if !strings.HasPrefix(got, "do it\n\n") || !strings.Contains(got, "\n\n"+clearedNotice("Bob")+"\n\n") ||
		!strings.HasSuffix(got, residentResumePrompt) {
		t.Fatalf("first message = %q; want prompt, notice, resume prompt", got)
	}
}

func TestClearWhileLiveDropped(t *testing.T) {
	h := &recordHandle{}
	w, p := interruptRun(t, h)
	clear(w, "Bob")
	if !eventually(func() bool { return hasIgnored(h, "clear", "Bob", "live episode") }) {
		t.Fatalf("no ignored clear record in %+v", h.eventList())
	}
	p.emit(lnResult)
	p.in.waitClosed(t)
	p.exit <- nil
	if !eventually(func() bool { return h.count(covemaster.Waiting) == 1 }) {
		t.Fatal("not waiting")
	}
	if exists(w.cfg.ClearMarker) {
		t.Fatal("a clear pressed mid-turn wrote the marker")
	}
	squawk(w)
	s := w.spawner.(*streamSpawner)
	p2 := s.next(t)
	if !hasArg(p2.args, "--continue") {
		t.Fatalf("the next episode must continue: %v", p2.args)
	}
	if got := p2.in.next(t); got != residentResumePrompt {
		t.Fatalf("resume prompt = %q", got)
	}
}

// Review focus 4: a Clear pending when cove-master restarts wins over a
// standing session's conversation marker.
func TestClearSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	conv := filepath.Join(dir, ".cove-conversation")
	if err := os.WriteFile(conv, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) {
		c.Resident, c.SessionKind, c.ConversationMarker = true, "standing", conv
	})
	if err := os.WriteFile(w.cfg.ClearMarker, []byte("Bob"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, &recordHandle{})
	t.Cleanup(func() { cancel(); <-done })
	p := s.next(t)
	if hasArg(p.args, "--continue") {
		t.Fatalf("a pending clear must start fresh: %v", p.args)
	}
	if got, want := p.in.next(t), "do it\n\n"+clearedNotice("Bob"); got != want {
		t.Fatalf("first message = %q\nwant %q", got, want)
	}
	p.emit(lnInit)
	time.Sleep(20 * time.Millisecond)
	if !exists(w.cfg.ClearMarker) {
		t.Fatal("clear marker consumed before the agent replied")
	}
	p.emit(lnAssistant)
	if !eventually(func() bool { return !exists(w.cfg.ClearMarker) }) {
		t.Fatal("clear marker not consumed on the first reply")
	}
	if !exists(conv) {
		t.Fatal("conversation marker gone after the reply")
	}
}

func TestClearMarkerKeptIfNoReply(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	if err := os.WriteFile(w.cfg.ClearMarker, []byte("Bob"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, h)
	t.Cleanup(func() { cancel(); <-done })
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	p.exit <- errors.New("exit status 1")
	if !eventually(func() bool { return h.count(covemaster.Waiting) == 1 }) {
		t.Fatal("not waiting")
	}
	if !exists(w.cfg.ClearMarker) {
		t.Fatal("clear marker consumed without a reply")
	}
	// Still pending: the next episode starts fresh again.
	squawk(w)
	p2 := s.next(t)
	if hasArg(p2.args, "--continue") {
		t.Fatalf("the clear is still pending; the episode must start fresh: %v", p2.args)
	}
	if got := p2.in.next(t); !strings.Contains(got, clearedNotice("Bob")) {
		t.Fatalf("first message = %q; want the cleared notice", got)
	}
}

// An unreadable or empty marker's presser is "someone".
func TestClearNoticeSomeone(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	if err := os.WriteFile(w.cfg.ClearMarker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, &recordHandle{})
	t.Cleanup(func() { cancel(); <-done })
	if got := s.next(t).in.next(t); !strings.Contains(got, clearedNotice("someone")) {
		t.Fatalf("first message = %q; want the notice naming someone", got)
	}
}

func TestCompactSkippedWhenClearPending(t *testing.T) {
	h := &recordHandle{}
	w, s, _, _ := idleRun(t, h)
	clear(w, "Bob")
	if !eventually(func() bool { return exists(w.cfg.ClearMarker) }) {
		t.Fatal("clear marker not written")
	}
	compact(w, "Ann", "keep X")
	select {
	case p := <-s.procs:
		t.Fatalf("a compact with a clear pending spawned: %v", p.args)
	case <-time.After(100 * time.Millisecond):
	}
	if !hasIgnored(h, "compact", "Ann", "no conversation") {
		t.Fatalf("no ignored compact record in %+v", h.eventList())
	}
	// Still waiting for a Wake, which starts the fresh episode.
	squawk(w)
	if p := s.next(t); hasArg(p.args, "--continue") {
		t.Fatalf("the episode after a clear must start fresh: %v", p.args)
	}
}
