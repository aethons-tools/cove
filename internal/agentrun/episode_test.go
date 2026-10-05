package agentrun

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// pipeInput records stream-json user messages written to the agent's stdin.
type pipeInput struct {
	msgs   chan string
	closed chan struct{}
	once   sync.Once
	broken atomic.Bool // set: writes fail as if the process had exited (EPIPE)
}

func newPipeInput() *pipeInput {
	return &pipeInput{msgs: make(chan string, 16), closed: make(chan struct{})}
}

func (p *pipeInput) Write(b []byte) (int, error) {
	if p.broken.Load() {
		return 0, syscall.EPIPE
	}
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

func (p *streamProc) Wait() error           { return <-p.exit }
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
	cfg := Config{WorkDir: dir, Prompt: "do it", MaxWait: time.Minute, Harness: testClaude(t, dir), Spawner: s}
	if mut != nil {
		mut(&cfg)
	}
	return New(cfg, nil)
}

func TestEpisodePromptOnStdinAndCloseWhenIdle(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
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
	done := runAsyncEpisodes(w, h, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnStarted, lnResult)
	p.in.staysOpen(t, 50*time.Millisecond)
	// claude empties the task list BEFORE notifying: with the task awaiting its
	// notification, stdin must stay open (emitted apart from lnNotify so the
	// loop sees this state before the notification re-arms busy).
	p.emit(lnTasks0, lnUpdated)
	p.in.staysOpen(t, 50*time.Millisecond)
	p.emit(lnNotify) // starts the self-started turn
	p.in.staysOpen(t, 50*time.Millisecond)
	p.emit(lnInit, lnAssistant, lnResult)
	p.in.waitClosed(t)
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
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
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
	done := runAsyncEpisodes(w, &recordHandle{}, 1)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult)
	p.in.waitClosed(t)
	p.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A wake coalesced mid-turn must not be lost when the process exits before
// delivering it: the next episode resumes at once.
func TestEpisodePendingWakeSurvivesExit(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, nil)
	h := &recordHandle{}
	done := runAsyncEpisodes(w, h, 2)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	w.Control(covemaster.Control{Kind: covemaster.Wake})
	p.in.noMessage(t, 30*time.Millisecond)
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
	p2.exit <- nil
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A Wake the episode took out of w.wake must not be lost when claude exits
// before acting on the resume prompt: whether the write failed (EPIPE) or the
// prompt was written but no turn started, the next episode resumes at once.
func TestEpisodeUnansweredWakeSurvivesExit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		writeFails bool
	}{{"write fails", true}, {"written, never answered", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := newStreamSpawner()
			w := streamWL(t, dir, s, nil)
			done := runAsyncEpisodes(w, &recordHandle{}, 2)
			p := s.next(t)
			p.in.next(t)
			p.emit(lnInit, lnTasks1, lnResult) // turn over, holding for the task
			p.in.staysOpen(t, 30*time.Millisecond)
			if tc.writeFails {
				p.in.broken.Store(true)
			}
			w.Control(covemaster.Control{Kind: covemaster.Wake})
			if tc.writeFails {
				p.in.waitClosed(t) // the failed write closes stdin
			} else if got := p.in.next(t); got != resumePrompt {
				t.Fatalf("delivered %q, want resumePrompt", got)
			}
			p.exit <- nil // claude died before starting the resumed turn
			p2 := s.next(t)
			if !hasArg(p2.args, "--continue") {
				t.Fatalf("resume spawn missing --continue: %v", p2.args)
			}
			if got := p2.in.next(t); got != resumePrompt {
				t.Fatalf("resume prompt = %q", got)
			}
			p2.emit(lnInit, lnResult)
			p2.in.waitClosed(t)
			p2.exit <- nil
			if err := <-done; err != nil {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}

func TestUserMessageIsOneStreamJSONLine(t *testing.T) {
	b := Claude{}.EncodeInput("line1\nline2 \"q\"")
	if b[len(b)-1] != '\n' || bytesCount(b, '\n') != 1 {
		t.Fatalf("not a single line: %q", b)
	}
	var m struct {
		Type    string                         `json:"type"`
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

func eventually(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// Wakes coalesced mid-turn merge their reasons into the one resume prompt.
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

// A turn that ends with a background task outstanding reports Holding; the
// turn a delivered Wake starts reports Running again.
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
		t.Fatalf("Holding not reported on hold; holding=%d", h.count(covemaster.Holding))
	}
	runningBefore := h.count(covemaster.Running)
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "squawk"}}})
	p.in.next(t) // the resume prompt starts a turn
	if !eventually(func() bool { return h.count(covemaster.Running) == runningBefore+1 }) {
		t.Fatalf("Running not reported when the held episode resumed; running=%d (before %d)", h.count(covemaster.Running), runningBefore)
	}
	cancel()
	<-done
}

// A resume prompt written but never answered (the agent died before starting
// the turn) is re-delivered to the next episode with its reasons intact.
func TestEpisodeUnansweredWakeKeepsReasons(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident, c.SessionKind = true, "standing" })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, &recordHandle{})
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult) // turn over, holding for the task
	p.in.staysOpen(t, 30*time.Millisecond)
	w.Control(covemaster.Control{Kind: covemaster.Wake, Reasons: []covemaster.WakeReason{{Kind: "alarm", Alarm: "nightly", Note: "check"}}})
	want := "Alarm \"nightly\" fired: check\nContinue."
	if got := p.in.next(t); got != want {
		t.Fatalf("delivered %q, want %q", got, want)
	}
	p.exit <- nil // died before starting the resumed turn
	p2 := s.next(t)
	if got := p2.in.next(t); got != want {
		t.Fatalf("re-delivered %q, want %q", got, want)
	}
	cancel()
	<-done
}

// A wake signal whose reasons were already answered writes nothing.
func TestEpisodeStaleWakeSignalWritesNothing(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, &recordHandle{})
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult) // turn over, holding for the task
	p.in.staysOpen(t, 30*time.Millisecond)
	w.wake.repost() // a signal with nothing pending
	p.in.noMessage(t, 80*time.Millisecond)
	cancel()
	<-done
}

// A background task completing starts a self-turn: Holding → Running.
func TestEpisodeBackgroundSelfTurnReportsRunning(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnStarted, lnResult)
	if !eventually(func() bool { return h.count(covemaster.Holding) == 1 }) {
		t.Fatal("no Holding")
	}
	before := h.count(covemaster.Running)
	p.emit(lnTasks0, lnUpdated)
	p.emit(lnNotify) // the completion starts a self-turn
	if !eventually(func() bool { return h.count(covemaster.Running) == before+1 }) {
		t.Fatalf("self-turn did not report Running (running=%d, before=%d)", h.count(covemaster.Running), before)
	}
	cancel()
	<-done
}

// The BackgroundWait cap ends the hold: the agent is told to stop its tasks,
// so the session is busy (Running) until the episode exits, not Holding.
func TestEpisodeBackgroundWaitCapLeavesHolding(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true; c.BackgroundWait = 30 * time.Millisecond })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult)
	p.in.waitClosed(t) // the cap closed stdin
	if !eventually(func() bool { return h.count(covemaster.Running) >= 2 }) {
		t.Fatalf("cap did not report Running (running=%d)", h.count(covemaster.Running))
	}
	cancel()
	<-done
}

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
