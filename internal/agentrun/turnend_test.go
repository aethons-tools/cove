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

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
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
func (f *fakeTurnIO) Commit(_ context.Context, upTo string) error {
	f.rec("Commit %s", upTo)
	return nil
}
func (f *fakeTurnIO) End(_ context.Context, reason string) error { f.rec("End %q", reason); return nil }
func (f *fakeTurnIO) SetAlarm(_ context.Context, name, at, note string) error {
	f.rec("SetAlarm %s %s %q", name, at, note)
	return nil
}
func (f *fakeTurnIO) SetIdle(_ context.Context, d string) error { f.rec("SetIdle %s", d); return nil }
func (f *fakeTurnIO) SetKept(_ context.Context, n int) error {
	f.rec("SetKept %d", n)
	return nil
}

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
	return streamWL(t, t.TempDir(), s, func(c *Config) { c.TurnIO = io; structuredContext(t, c) })
}

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A turn that ends with text it sent nowhere is returned at once, its batch
// shown again uncommitted; the returned turn ending that way too is
// committed and recorded as unaddressed. Nothing is ever posted for it.
func TestTurnEndReturnsUnsentText(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "hello")}}}
	w := turnWL(t, s, io)
	h := &recordHandle{}
	done := runAsyncEpisodes(w, h, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	time.Sleep(10 * time.Millisecond)
	p.emit(resultLine("")) // the raise turn: no text, not returned
	if got := p.in.next(t); !strings.Contains(got, "id m1\nhello") || strings.Contains(got, "sent no message") {
		t.Fatalf("resume = %q", got)
	}
	p.emit(lnInit, resultLine("Hi there."))
	got := p.in.next(t)
	if !strings.Contains(got, returnedText) || !strings.Contains(got, "id m1\nhello") {
		t.Fatalf("returned = %q", got)
	}
	p.emit(lnInit, resultLine("Hi there."))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), "Inbox", "Inbox", "Commit m1")
	var outcomes []string
	for _, e := range h.eventList() {
		if strings.Contains(e.raw, `"type":"cove"`) {
			outcomes = append(outcomes, e.raw)
		}
	}
	if len(outcomes) != 1 || outcomes[0] != `{"type":"cove","subtype":"turn","outcome":"unaddressed"}` {
		t.Fatalf("cove events = %q", outcomes)
	}
}

// What the model says before a tool call is thinking: never posted. Only
// turn_end's to posts the final text.
func TestTurnEndNeverPostsMidTurnText(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "hello")}}}
	sock := shortSock(t)
	w := streamWL(t, t.TempDir(), s, func(c *Config) { c.TurnIO, c.AgentSocket = io, sock; structuredContext(t, c) })
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, assistantText("Looking into it."), lnToolUse)
	waitFor(t, func() bool { return w.structuredOn.Load() })
	if err := DialAgentSocket(sock).SetTurnEnd(context.Background(), Directives{To: "user:bob"}); err != nil {
		t.Fatal(err)
	}
	p.emit(lnToolRes, assistantText("Found it."), lnToolUse, lnToolRes, assistantText("Fixed."), resultLine("Fixed."))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), `Post "Fixed." "user:bob"`)
}

// A turn that sent a message (send, ask, report) isn't returned: its final
// text is thinking. Each prompt starts a fresh record, and the last send's
// address is the turn's default to.
func TestTurnEndSentTurnIsNotReturned(t *testing.T) {
	io := &fakeTurnIO{}
	w := unitWL(io)
	sock := shortSock(t)
	stop, err := serveAgentSocket(sock, w.agentSocketHandler(), w.log)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c := DialAgentSocket(sock)
	ctx := context.Background()
	w.newTurn(turnMeta{upTo: "m1", structured: true})
	if st, err := c.Turn(ctx); err != nil || st.Structured || st.LastTo != "" {
		t.Fatalf("fresh turn = %+v %v", st, err)
	}
	c.NoteSent(ctx, "send", "user:ann")
	c.NoteSent(ctx, "ask", "")
	if st, _ := c.Turn(ctx); st.LastTo != "user:ann" {
		t.Fatalf("last to = %q", st.LastTo)
	}
	w.finishTurn(ctx, TurnResult{Text: "Sent it.", OK: true, Gen: w.inflight.gen})
	wantCalls(t, io.callList(), "Commit m1")
	if w.wake.has() {
		t.Fatal("a turn that sent was returned")
	}
	w.newTurn(turnMeta{structured: true})
	if st, _ := c.Turn(ctx); st.LastTo != "" {
		t.Fatalf("a new turn kept the last to: %q", st.LastTo)
	}
	c.NoteSent(ctx, "report", "")
	w.finishTurn(ctx, TurnResult{Text: "Reported.", OK: true, Gen: w.inflight.gen})
	if w.wake.has() {
		t.Fatal("a turn that reported was returned")
	}
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
	p.emit(resultLine(""))
	if got := p.in.next(t); !strings.Contains(got, "id m1") {
		t.Fatalf("resume = %q", got)
	}
	// An error result with text is neither committed nor returned.
	p.emit(lnInit, `{"type":"result","subtype":"error_during_execution","is_error":true,"queued_turn_count":0,"result":"half"}`)
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), "Inbox")
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
	p.emit(resultLine(""))
	if got := p.in.next(t); !strings.Contains(got, "id m2") {
		t.Fatalf("second resume = %q", got)
	}
	p.emit(lnInit, resultLine(""))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), "Inbox", "Commit m1", "Inbox", "Commit m2")
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
	p.emit(resultLine(""))
	if got := p.in.next(t); got != "/compact" {
		t.Fatalf("got %q, want /compact", got)
	}
	// The compaction's text is neither posted nor returned.
	p.emit(lnInit, resultLine("Compacted."))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList())
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
	if !strings.Contains(got, "pressed **Interrupt**") || !strings.Contains(got, "id m1\nstop that") || !strings.Contains(got, "`turn_end` and a `to`") {
		t.Fatalf("ack = %q", got)
	}
	p.emit(lnInit, resultLine(""))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList(), "Inbox", "Commit m1")
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
	w := streamWL(t, t.TempDir(), s, func(c *Config) { c.TurnIO, c.AgentSocket = io, sock; structuredContext(t, c) })
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
		{"no_reply", Directives{NoReply: true}, []string{"Inbox", "Commit m1"}},
		{"post false reads as no_reply", Directives{Post: bp(false)}, []string{"Inbox", "Commit m1"}},
		{"deny", Directives{NoReply: true, Commit: bp(false)}, []string{"Inbox"}},
		{"to", Directives{To: "user:x"}, []string{"Inbox", `Post "reply" "user:x"`, "Commit m1"}},
		{"to and deny", Directives{To: "user:x", Commit: bp(false)}, []string{"Inbox", `Post "reply" "user:x"`}},
		{"end", Directives{Next: "end", Reason: "all done"}, []string{"Inbox", "Commit m1", `End "all done"`}},
		{"compact", Directives{Next: "compact", NoReply: true}, []string{"Inbox", "Commit m1"}},
		{"mute", Directives{WakeOnMessage: bp(false), NoReply: true}, []string{"Inbox", "Commit m1", "Mute true"}},
		{"wake_at", Directives{WakeAt: &WakeAt{At: "2026-10-28T09:00:00Z", Note: "ask about the preview"}, NoReply: true},
			[]string{"Inbox", "Commit m1", `SetAlarm turn-end 2026-10-28T09:00:00Z "ask about the preview"`}},
		{"idle", Directives{IdleTimeout: "2h", NoReply: true}, []string{"Inbox", "Commit m1", "SetIdle 2h"}},
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
	c0 := DialAgentSocket(sock)
	stop, err := serveAgentSocket(sock, w.agentSocketHandler(), w.log)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := c0.SetTurnEnd(context.Background(), Directives{}); err == nil || !strings.Contains(err.Error(), "doesn't support turn_end yet") {
		t.Fatalf("legacy context: %v", err)
	}
	w.structuredOn.Store(true)
	c := c0
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

// A final text whose to is refused reached no one: nothing is committed and
// the turn is returned with why; a returned turn's refused post isn't
// returned again, and its messages stay pending.
func TestTurnEndRefusedToIsReturned(t *testing.T) {
	io := &refusingIO{fakeTurnIO: &fakeTurnIO{}}
	w := unitWL(io)
	w.newTurn(turnMeta{upTo: "m1", structured: true})
	w.directives = &Directives{To: "user:nobody"}
	w.finishTurn(context.Background(), TurnResult{Text: "hi", OK: true, Gen: w.inflight.gen})
	wantCalls(t, io.callList(), `Post "hi" "user:nobody"`)
	rs := w.wake.take()
	if len(rs) != 1 || rs[0].Kind != "returned" || !strings.Contains(rs[0].Detail, "could not be posted to user:nobody") {
		t.Fatalf("wake = %+v", rs)
	}
	w.newTurn(turnMeta{upTo: "m1", structured: true})
	if !w.inflight.returned {
		t.Fatal("the next prompt's turn isn't the returned one")
	}
	w.directives = &Directives{To: "user:nobody"}
	w.finishTurn(context.Background(), TurnResult{Text: "hi", OK: true, Gen: w.inflight.gen})
	wantCalls(t, io.callList(), `Post "hi" "user:nobody"`, `Post "hi" "user:nobody"`)
	if w.wake.has() || !strings.Contains(w.note, "could not be posted to user:nobody") {
		t.Fatalf("returned again (%v) or no note (%q)", w.wake.has(), w.note)
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

// structuredContext gives c a session context compiled for the structured
// turn end, so its Workload turns it on.
func structuredContext(t *testing.T, c *Config) {
	t.Helper()
	b := sessionctx.Compile(sessionctx.Inputs{Session: sessionctx.SessionFacts{Kind: "standing", Name: "n", Project: "p", Role: "r", TurnProtocol: TurnProtocol}})
	c.Context, c.ContextDir = &b, filepath.Join(t.TempDir(), "context")
	c.ContextSource = &seqContext{bundles: []sessionctx.Bundle{b}}
}

func TestLegacyContextKeepsLegacyTurns(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "hello")}}}
	one := compileRole("ONE") // protocol 0: an older Jam's guidance
	w := streamWL(t, t.TempDir(), s, func(c *Config) {
		c.TurnIO = io
		c.Context, c.ContextDir = &one, filepath.Join(t.TempDir(), "context")
		c.ContextSource = &seqContext{bundles: []sessionctx.Bundle{one}}
	})
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: "squawk"}}})
	time.Sleep(10 * time.Millisecond)
	p.emit(resultLine("hi"))
	if got := p.in.next(t); got != resumePrompt {
		t.Fatalf("resume = %q, want the legacy prompt", got)
	}
	p.emit(lnInit, resultLine("bye"))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wantCalls(t, io.callList())
}

// unitWL is a Workload with the structured turn end on, for finishTurn
// unit tests.
func unitWL(io TurnIO) *Workload {
	w := New(Config{TurnIO: io}, nil)
	w.ctxr = &contextRefresher{last: sessionctx.Bundle{TurnProtocol: TurnProtocol}}
	return w
}

func TestTurnEndOnlyCommitsItsOwnPrompt(t *testing.T) {
	io := &fakeTurnIO{}
	w := unitWL(io)
	w.newTurn(turnMeta{upTo: "m1", structured: true})
	stale := w.inflight.gen - 1 // a turn a background task started before this prompt
	w.finishTurn(context.Background(), TurnResult{OK: true, Gen: stale})
	w.finishTurn(context.Background(), TurnResult{OK: true, Gen: w.inflight.gen})
	wantCalls(t, io.callList(), "Commit m1")
}

func TestTurnEndNotNormalDropsDirectives(t *testing.T) {
	io := &fakeTurnIO{}
	w := unitWL(io)
	w.newTurn(turnMeta{upTo: "m1", structured: true})
	w.directives = &Directives{Next: "end", Reason: "done", WakeOnMessage: bp(false)}
	w.finishTurn(context.Background(), TurnResult{Text: "partial", OK: false, Gen: w.inflight.gen})
	wantCalls(t, io.callList())
	if d := w.takeDirectives(); d != (Directives{}) {
		t.Fatalf("directives survived an interrupted turn: %+v", d)
	}
}

func TestNewPromptDropsStaleDirectives(t *testing.T) {
	w := unitWL(&fakeTurnIO{})
	w.directives = &Directives{Next: "end", Reason: "for a turn that died"}
	w.newTurn(turnMeta{})
	if d := w.takeDirectives(); d != (Directives{}) {
		t.Fatalf("directives carried into a new prompt: %+v", d)
	}
}

func TestTurnEndMutesEveryTime(t *testing.T) {
	io := &fakeTurnIO{}
	w := unitWL(io)
	for range 2 {
		// Jam unmutes by itself when the session runs: each mute is sent.
		w.newTurn(turnMeta{structured: true})
		w.directives = &Directives{WakeOnMessage: bp(false)}
		w.finishTurn(context.Background(), TurnResult{OK: true, Gen: w.inflight.gen})
	}
	w.newTurn(turnMeta{structured: true})
	w.finishTurn(context.Background(), TurnResult{OK: true, Gen: w.inflight.gen})
	wantCalls(t, io.callList(), "Mute true", "Mute true", "Mute false")
}

func TestResumeFailureDoesNotCommitRestartBatch(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{pages: [][]InboxEntry{{entry("m1", "while you were away")}}}
	dir := t.TempDir()
	marker := filepath.Join(dir, ".cove-conversation")
	os.WriteFile(marker, nil, 0o600)
	w := streamWL(t, dir, s, func(c *Config) {
		c.TurnIO, c.SessionKind, c.ConversationMarker = io, "standing", marker
		structuredContext(t, c)
	})
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	if got := p.in.next(t); !strings.Contains(got, "id m1") {
		t.Fatalf("restart prompt = %q", got)
	}
	p.exit <- errors.New("no conversation to continue") // --continue fails before replying
	p = s.next(t)
	if got := p.in.next(t); got != "do it" {
		t.Fatalf("fresh prompt = %q", got)
	}
	p.emit(lnInit, resultLine(""))
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// m1 was never shown to the fresh conversation: it stays pending.
	wantCalls(t, io.callList(), "Inbox")
}
