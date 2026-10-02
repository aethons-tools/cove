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
