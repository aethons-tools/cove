package agentrun

import (
	"context"
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
	w, p := interruptRun(t, &recordHandle{})
	interrupt(w, "Ann")
	p.in.nextCtrl(t)
	interrupt(w, "Ann") // taken on its own: the turn is already being interrupted
	p.in.noCtrl(t, 100*time.Millisecond)
	p.emit(lnToolRes, lnResult)
	if got := p.in.next(t); !strings.Contains(got, "pressed **Interrupt**") {
		t.Fatalf("acknowledge prompt = %q", got)
	}
	p.in.noMessage(t, 100*time.Millisecond)
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
	done := runAsync(ctx, w, &recordHandle{})
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
	cancel()
	<-done
}
