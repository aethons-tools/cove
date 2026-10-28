package agentrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/studioagent"
)

// fakeTurnIO records TurnIO calls in order and serves inbox pages in turn
// (the last one repeats).
type fakeTurnIO struct {
	mu       sync.Mutex
	calls    []string
	pages    [][]InboxEntry
	inboxErr error
	muteErr  error
}

func (f *fakeTurnIO) rec(format string, a ...any) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
	f.mu.Unlock()
}

func (f *fakeTurnIO) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeTurnIO) Inbox(_ context.Context, limit int) ([]InboxEntry, bool, error) {
	f.rec("Inbox")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inboxErr != nil {
		return nil, false, f.inboxErr
	}
	if len(f.pages) == 0 {
		return nil, false, nil
	}
	p := f.pages[0]
	if len(f.pages) > 1 {
		f.pages = f.pages[1:]
	}
	if len(p) > limit {
		return p[:limit], true, nil
	}
	return p, false, nil
}

func (f *fakeTurnIO) Post(_ context.Context, text, to string) error {
	f.rec("Post %q %q", text, to)
	return nil
}
func (f *fakeTurnIO) Commit(_ context.Context, upTo string) error { f.rec("Commit %s", upTo); return nil }
func (f *fakeTurnIO) End(_ context.Context, reason string) error  { f.rec("End %q", reason); return nil }
func (f *fakeTurnIO) SetAlarm(_ context.Context, name, at, note string) error {
	f.rec("SetAlarm %s %s %q", name, at, note)
	return nil
}
func (f *fakeTurnIO) SetIdle(_ context.Context, d string) error { f.rec("SetIdle %s", d); return nil }
func (f *fakeTurnIO) Mute(_ context.Context, on bool) error {
	f.rec("Mute %v", on)
	return f.muteErr
}

func (f *fakeTurnIO) waitCalls(t *testing.T, n int) []string {
	t.Helper()
	waitFor(t, func() bool { return len(f.callList()) >= n })
	return f.callList()
}

func resultLine(text string) string {
	return fmt.Sprintf(`{"type":"result","subtype":"success","queued_turn_count":0,"result":%q}`, text)
}

const lnResultErr = `{"type":"result","subtype":"error_during_execution","is_error":true,"queued_turn_count":0}`

func turnWL(t *testing.T, s *streamSpawner, io *fakeTurnIO) *Workload {
	t.Helper()
	return streamWL(t, t.TempDir(), s, func(c *Config) { c.TurnIO = io })
}

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestTurnEndPostsAndCommits(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "hello")}}}
	w := turnWL(t, s, io)
	done := runAsyncEpisodes(w, &recordHandle{}, 2)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, resultLine("first"))
	p.in.waitClosed(t)
	p.exit <- nil
	// The raise prompt's turn has no batch: its reply is posted, nothing committed.
	wantCalls(t, io.waitCalls(t, 1), `Post "first" ""`)

	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	p = s.next(t)
	got := p.in.next(t)
	if !strings.Contains(got, "id m1\nhello") || strings.Contains(got, "use the intercom `read` tool") {
		t.Fatalf("resume = %q", got)
	}
	p.emit(lnInit, resultLine("Done."))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), `Post "first" ""`, "Inbox", `Post "Done." ""`, "Commit m1")
}

func TestWakeFallsBackWhenInboxFails(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{inboxErr: errors.New("down")}
	w := turnWL(t, s, io)
	done := runAsyncEpisodes(w, &recordHandle{}, 2)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, resultLine(""))
	p.in.waitClosed(t)
	p.exit <- nil
	waitFor(t, func() bool { return w.wake != nil })
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	p = s.next(t)
	if got := p.in.next(t); got != resumePrompt {
		t.Fatalf("resume = %q, want the legacy prompt", got)
	}
	p.emit(lnInit, resultLine("  \n"))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Empty text posts nothing; no batch was shown, so nothing is committed.
	wantCalls(t, io.callList(), "Inbox")
}

func TestTurnEndNotNormalNeitherPostsNorCommits(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "hello")}}}
	w := turnWL(t, s, io)
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	time.Sleep(10 * time.Millisecond)
	p.emit(resultLine("ok"))
	if got := p.in.next(t); !strings.Contains(got, "id m1") {
		t.Fatalf("resume = %q", got)
	}
	p.emit(lnInit, lnResultErr)
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), `Post "ok" ""`, "Inbox")
}

func TestTurnEndFinishesBeforeNextResume(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "a")}, {entry("m2", "b")}}}
	w := turnWL(t, s, io)
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	time.Sleep(10 * time.Millisecond)
	p.emit(resultLine(""))
	p.in.next(t) // m1's batch
	p.emit(lnInit)
	// A second wake coalesces mid-turn: it is delivered only after this
	// turn's commit, so its batch can't re-show m1.
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	time.Sleep(10 * time.Millisecond)
	p.emit(resultLine("seen a"))
	if got := p.in.next(t); !strings.Contains(got, "id m2") {
		t.Fatalf("second resume = %q", got)
	}
	p.emit(lnInit, resultLine("seen b"))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), "Inbox", `Post "seen a" ""`, "Commit m1", "Inbox", `Post "seen b" ""`, "Commit m2")
}

func TestCompactTurnIsQuiet(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{}
	w := turnWL(t, s, io)
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(studioagent.Control{Kind: studioagent.Compact, By: "you"})
	time.Sleep(10 * time.Millisecond)
	p.emit(resultLine("reply"))
	if got := p.in.next(t); got != "/compact" {
		t.Fatalf("got %q, want /compact", got)
	}
	p.emit(lnInit, resultLine("Compacted."))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), `Post "reply" ""`)
}

func TestInterruptAckCarriesBatch(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "stop that")}}}
	w := turnWL(t, s, io)
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	w.Control(studioagent.Control{Kind: studioagent.Interrupt, By: "you"})
	p.in.nextCtrl(t) // the interrupt control request
	p.emit(lnResultErr)
	got := p.in.next(t)
	if !strings.Contains(got, "pressed **Interrupt**") || !strings.Contains(got, "id m1\nstop that") || strings.Contains(got, "with `send`") {
		t.Fatalf("ack = %q", got)
	}
	p.emit(lnInit, resultLine("Stopped. What instead?"))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), "Inbox", `Post "Stopped. What instead?" ""`, "Commit m1")
}

// shortSock is a socket path under the unix-socket length limit.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

func bp(b bool) *bool { return &b }

// directiveTurn runs one woken turn that calls turn_end with d (through the
// socket) and ends with text; it returns the TurnIO calls after the wake.
func directiveTurn(t *testing.T, d Directives, text string) ([]string, *Workload) {
	t.Helper()
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "hello")}}}
	sock := shortSock(t)
	w := streamWL(t, t.TempDir(), s, func(c *Config) { c.TurnIO, c.AgentSocket = io, sock })
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	time.Sleep(10 * time.Millisecond)
	p.emit(resultLine(""))
	p.in.next(t)
	p.emit(lnInit)
	if err := DialAgentSocket(sock).SetTurnEnd(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	p.emit(resultLine(text))
	if d.Next == "compact" {
		if got := p.in.next(t); got != "/compact" {
			t.Fatalf("got %q, want /compact", got)
		}
		p.emit(lnInit, resultLine("Compacted."))
	}
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return io.callList(), w
}

func TestTurnEndDirectives(t *testing.T) {
	cases := []struct {
		name string
		d    Directives
		want []string
	}{
		{"defaults", Directives{}, []string{"Inbox", `Post "reply" ""`, "Commit m1"}},
		{"quiet", Directives{Post: bp(false)}, []string{"Inbox", "Commit m1"}},
		{"deny", Directives{Commit: bp(false)}, []string{"Inbox", `Post "reply" ""`}},
		{"to", Directives{To: "user:x"}, []string{"Inbox", `Post "reply" "user:x"`, "Commit m1"}},
		{"end", Directives{Next: "end", Reason: "all done"}, []string{"Inbox", `Post "reply" ""`, "Commit m1", `End "all done"`}},
		{"compact", Directives{Next: "compact"}, []string{"Inbox", `Post "reply" ""`, "Commit m1"}},
		{"mute", Directives{WakeOnMessage: bp(false)}, []string{"Inbox", `Post "reply" ""`, "Commit m1", "Mute true"}},
		{"wake_at", Directives{WakeAt: &WakeAt{At: "2026-10-28T09:00:00Z", Note: "ask about the preview"}},
			[]string{"Inbox", `Post "reply" ""`, "Commit m1", `SetAlarm turn-end 2026-10-28T09:00:00Z "ask about the preview"`}},
		{"idle", Directives{IdleTimeout: "2h"}, []string{"Inbox", `Post "reply" ""`, "Commit m1", "SetIdle 2h"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := directiveTurn(t, c.d, "reply")
			wantCalls(t, got, c.want...)
		})
	}
}

func TestTurnEndDirectivesResetAndUnmute(t *testing.T) {
	_, w := directiveTurn(t, Directives{WakeOnMessage: bp(false), Post: bp(false)}, "reply")
	if !w.muted {
		t.Fatal("muted not recorded")
	}
	if d := w.takeDirectives(); d != (Directives{}) {
		t.Fatalf("directives not reset: %+v", d)
	}
	// The next turn with the defaults unmutes, once.
	io := w.cfg.TurnIO.(*fakeTurnIO)
	before := len(io.callList())
	w.finishTurn(context.Background(), TurnResult{OK: true})
	w.finishTurn(context.Background(), TurnResult{OK: true})
	wantCalls(t, io.callList()[before:], "Mute false")
}

func TestAgentSocketValidates(t *testing.T) {
	sock := shortSock(t)
	w := New(Config{TurnIO: &fakeTurnIO{}}, nil)
	stop, err := serveAgentSocket(sock, w.agentSocketHandler(), w.log)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c := DialAgentSocket(sock)
	for _, d := range []Directives{{Next: "end"}, {Next: "later"}, {WakeAt: &WakeAt{At: "tomorrow"}}} {
		if err := c.SetTurnEnd(context.Background(), d); err == nil {
			t.Errorf("%+v: want an error", d)
		}
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	stop()
	if err := c.SetTurnEnd(context.Background(), Directives{}); !errors.Is(err, ErrNoAgentSocket) {
		t.Fatalf("no socket: %v", err)
	}
}

func TestTurnEndRefusedToFallsBackHome(t *testing.T) {
	io := &refusingIO{fakeTurnIO: &fakeTurnIO{}}
	w := New(Config{TurnIO: io}, nil)
	w.directives = &Directives{To: "user:nobody"}
	w.finishTurn(context.Background(), TurnResult{Text: "hi", OK: true})
	wantCalls(t, io.callList(), `Post "hi" "user:nobody"`, `Post "hi" ""`)
	if !strings.Contains(w.note, "could not be posted to user:nobody") {
		t.Fatalf("note = %q", w.note)
	}
}

type refusingIO struct{ *fakeTurnIO }

func (r *refusingIO) Post(ctx context.Context, text, to string) error {
	r.fakeTurnIO.Post(ctx, text, to)
	if to != "" {
		return errors.New("POST /squawks: 404 Not Found")
	}
	return nil
}
