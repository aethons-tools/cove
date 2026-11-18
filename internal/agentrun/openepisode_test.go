package agentrun

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/studioagent"
)

// swapSource is a ConnectorSource whose connector (or error) a test changes.
type swapSource struct {
	mu  sync.Mutex
	c   snippet.Connector
	err error
}

func (s *swapSource) Fetch(context.Context) (snippet.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c, s.err
}

func (s *swapSource) set(c snippet.Connector, err error) {
	s.mu.Lock()
	s.c, s.err = c, err
	s.mu.Unlock()
}

func pushTimeout(w *Workload, d time.Duration) {
	w.Control(studioagent.Control{Kind: studioagent.IdleTimeout, IdleTimeout: d})
}

func pushDeadline(w *Workload, at time.Time) {
	w.Control(studioagent.Control{Kind: studioagent.IdleDeadline, IdleDeadline: at})
}

func wakeWith(w *Workload, kind string) {
	w.Control(studioagent.Control{Kind: studioagent.Wake, Reasons: []studioagent.WakeReason{{Kind: kind}}})
}

// openRun starts a resident Run with an idle timeout of timeout pushed, ends
// its first turn and waits until the episode is open (Waiting reported, stdin
// still open).
func openRun(t *testing.T, h *recordHandle, timeout time.Duration, mut func(*Config)) (*Workload, *streamSpawner, *streamProc) {
	t.Helper()
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) {
		c.Resident = true
		if mut != nil {
			mut(c)
		}
	})
	pushTimeout(w, timeout)
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
	if !eventually(func() bool { return h.count(studioagent.Waiting) == 1 }) {
		t.Fatal("not waiting")
	}
	return w, s, p
}

// noSpawn fails if an episode starts within d.
func noSpawn(t *testing.T, s *streamSpawner, d time.Duration) {
	t.Helper()
	select {
	case p := <-s.procs:
		t.Fatalf("an episode started: %v", p.args)
	case <-time.After(d):
	}
}

// A turn that ends with an idle timeout keeps stdin open and reports Waiting
// once; at the deadline stdin closes, and the next wake starts a continued
// episode.
func TestOpenEpisodeStaysOpenUntilDeadline(t *testing.T) {
	h := &recordHandle{}
	w, s, p := openRun(t, h, 200*time.Millisecond, nil)
	p.in.staysOpen(t, 100*time.Millisecond)
	p.in.waitClosed(t)
	p.exit <- nil
	noSpawn(t, s, 50*time.Millisecond)
	if n := h.count(studioagent.Waiting); n != 1 {
		t.Fatalf("Waiting reported %d times, want once", n)
	}
	squawk(w)
	p2 := s.next(t)
	if !hasArg(p2.args, "--continue") {
		t.Fatalf("the next episode must continue: %v", p2.args)
	}
	if got := p2.in.next(t); got != residentResumePrompt {
		t.Fatalf("first message = %q", got)
	}
}

// Jam's IdleDeadline push replaces the agent's deadline: a later one keeps the
// episode open past the turn's end plus the timeout; 0, or one already past,
// closes it at once.
func TestOpenEpisodeDeadlinePush(t *testing.T) {
	t.Run("later", func(t *testing.T) {
		h := &recordHandle{}
		w, _, p := openRun(t, h, 100*time.Millisecond, nil)
		pushDeadline(w, time.Now().Add(time.Hour))
		p.in.staysOpen(t, 250*time.Millisecond)
	})
	t.Run("earlier", func(t *testing.T) {
		h := &recordHandle{}
		w, _, p := openRun(t, h, time.Hour, nil)
		p.in.staysOpen(t, 30*time.Millisecond)
		pushDeadline(w, time.Now().Add(50*time.Millisecond))
		p.in.waitClosed(t)
	})
	for name, at := range map[string]time.Time{"zero": {}, "past": time.Now().Add(-time.Minute)} {
		t.Run(name, func(t *testing.T) {
			h := &recordHandle{}
			w, _, p := openRun(t, h, time.Hour, nil)
			p.in.staysOpen(t, 30*time.Millisecond)
			pushDeadline(w, at)
			p.in.waitClosed(t)
		})
	}
}

// A push received during a turn does not outlive it: the turn's end arms the
// deadline from the timeout.
func TestOpenEpisodeTurnEndReplacesPushedDeadline(t *testing.T) {
	h := &recordHandle{}
	w, _, p := openRun(t, h, time.Hour, nil)
	squawk(w)
	p.in.next(t)
	p.emit(lnInit)
	pushDeadline(w, time.Now().Add(-time.Minute)) // a late push, mid-turn
	p.emit(lnResult)
	p.in.staysOpen(t, 100*time.Millisecond)
	if _, dl := w.idle.get(); time.Until(dl) < 50*time.Minute {
		t.Fatalf("deadline %v, want the turn's end plus 1h", dl)
	}
}

// A wake reaching an open episode is written into its stdin: no new process,
// Running reported for the turn it starts, and the episode is open again when
// that turn ends.
func TestOpenEpisodeWakeWrittenIn(t *testing.T) {
	h := &recordHandle{}
	w, s, p := openRun(t, h, time.Hour, nil)
	squawk(w)
	if got := p.in.next(t); got != residentResumePrompt {
		t.Fatalf("delivered %q", got)
	}
	if !eventually(func() bool { return h.count(studioagent.Running) == 2 }) {
		t.Fatalf("Running reported %d times, want 2", h.count(studioagent.Running))
	}
	p.emit(lnInit, lnResult)
	if !eventually(func() bool { return h.count(studioagent.Waiting) == 2 }) {
		t.Fatal("not waiting again")
	}
	p.in.staysOpen(t, 50*time.Millisecond)
	noSpawn(t, s, 10*time.Millisecond)
}

// keep-warm goes into the open episode: keeping the cache warm starts no process.
func TestOpenEpisodeKeepWarmReusesProcess(t *testing.T) {
	h := &recordHandle{}
	w, s, p := openRun(t, h, time.Hour, nil)
	wakeWith(w, "keep-warm")
	if got := p.in.next(t); got != keepWarmText {
		t.Fatalf("delivered %q", got)
	}
	p.emit(lnInit, lnResult)
	p.in.staysOpen(t, 50*time.Millisecond)
	noSpawn(t, s, 10*time.Millisecond)
}

// A wake that needs a new episode closes the open one without writing it;
// once the process exits, the next episode continues with that wake.
func TestOpenEpisodeWakeNeedsNewEpisode(t *testing.T) {
	src := &swapSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	for _, tc := range []struct {
		name  string
		setup func(w *Workload)
		kind  string
		want  string
	}{
		{"idle reason", nil, "idle", "Idle timeout: no other wake arrived.\nContinue."},
		{"deadline past", func(w *Workload) {
			w.idle.arm(time.Now().Add(-time.Second)) // frozen past it by a pause: no push, no timer yet
		}, "squawk", residentResumePrompt},
		{"connector changed", func(*Workload) {
			src.set(snippet.Connector{Env: map[string]string{"GH_HOST": "{host}", "EXTRA": "x"}}, nil)
		}, "squawk", residentResumePrompt},
		{"model spec changed", func(*Workload) {
			src.set(snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"},
				ModelSpec: specWith(func(s *modelspec.Spec) { s.Name, s.Model.ID = "opus", "claude-opus-5-5" })}, nil)
		}, "keep-warm", keepWarmText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src.set(snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}, nil)
			h := &recordHandle{}
			w, s, p := openRun(t, h, time.Hour, func(c *Config) {
				c.Connector = &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t",
					Environ: func() []string { return nil }}
				c.Harness = specClaude(t, t.TempDir(), modelspec.DefaultClaudeVersion, nil)
			})
			if tc.setup != nil {
				tc.setup(w)
			}
			wakeWith(w, tc.kind)
			p.in.waitClosed(t)
			p.in.noMessage(t, 20*time.Millisecond)
			p.exit <- nil
			p2 := s.next(t)
			if !hasArg(p2.args, "--continue") {
				t.Fatalf("the new episode must continue: %v", p2.args)
			}
			if got := p2.in.next(t); got != tc.want {
				t.Fatalf("first message = %q, want %q", got, tc.want)
			}
			if n := h.count(studioagent.Waiting); n != 1 {
				t.Fatalf("Waiting reported %d times, want once", n)
			}
		})
	}
}

// A failed connector fetch keeps the open episode: the wake is written in.
func TestOpenEpisodeConnectorFetchFailureKeepsIt(t *testing.T) {
	src := &swapSource{c: snippet.Connector{Env: map[string]string{"GH_HOST": "{host}"}}}
	h := &recordHandle{}
	w, s, p := openRun(t, h, time.Hour, func(c *Config) {
		c.Connector = &ConnectorConfig{Source: src, Git: &fakeGit{}, BaseURL: "https://jam.example", Token: "t",
			Environ: func() []string { return nil }}
	})
	src.set(snippet.Connector{}, errors.New("503"))
	squawk(w)
	if got := p.in.next(t); got != residentResumePrompt {
		t.Fatalf("delivered %q", got)
	}
	noSpawn(t, s, 30*time.Millisecond)
}

func TestOpenEpisodeInterruptIgnored(t *testing.T) {
	h := &recordHandle{}
	w, _, p := openRun(t, h, time.Hour, nil)
	interrupt(w, "Ann")
	if !eventually(func() bool { return hasIgnored(h, "interrupt", "Ann", "not in a turn") }) {
		t.Fatalf("no ignored interrupt record in %+v", h.eventList())
	}
	p.in.noCtrl(t, 30*time.Millisecond)
	p.in.staysOpen(t, 20*time.Millisecond)
}

// Compact is written into the open episode at once (no maintenance episode),
// and the episode stays open afterward, still waiting.
func TestOpenEpisodeCompactWrittenIn(t *testing.T) {
	h := &recordHandle{}
	w, s, p := openRun(t, h, time.Hour, nil)
	compact(w, "Ann", "keep X")
	if got := p.in.next(t); got != "/compact keep X" {
		t.Fatalf("delivered %q", got)
	}
	p.emit(lnInit, lnResult)
	p.in.staysOpen(t, 50*time.Millisecond)
	noSpawn(t, s, 10*time.Millisecond)
	if !hasCommand(h, "compact", "Ann") {
		t.Fatalf("no compact record in %+v", h.eventList())
	}
	if r, wt := h.count(studioagent.Running), h.count(studioagent.Waiting); r != 1 || wt != 1 {
		t.Fatalf("running=%d waiting=%d; a compaction reports neither", r, wt)
	}
	squawk(w) // still open: the wake goes in
	if got := p.in.next(t); got != residentResumePrompt {
		t.Fatalf("delivered %q", got)
	}
}

// Clear closes the open episode; the next one starts without --continue.
func TestOpenEpisodeClearCloses(t *testing.T) {
	h := &recordHandle{}
	w, s, p := openRun(t, h, time.Hour, nil)
	pressClear(w, "Bob")
	p.in.waitClosed(t)
	p.exit <- nil
	if !eventually(func() bool { return hasCommand(h, "clear", "Bob") }) {
		t.Fatalf("no clear record in %+v", h.eventList())
	}
	if b, err := os.ReadFile(w.cfg.ClearMarker); err != nil || string(b) != "Bob" {
		t.Fatalf("clear marker = %q, %v", b, err)
	}
	noSpawn(t, s, 30*time.Millisecond)
	squawk(w)
	p2 := s.next(t)
	if hasArg(p2.args, "--continue") {
		t.Fatalf("the episode after a clear must start fresh: %v", p2.args)
	}
	if got := p2.in.next(t); !strings.Contains(got, clearedNotice("Bob")) {
		t.Fatalf("first message = %q", got)
	}
}

// A hold whose tasks finish is kept open the same way; a hold whose
// BackgroundWait elapses closes stdin as before.
func TestOpenEpisodeAfterHold(t *testing.T) {
	t.Run("tasks finish", func(t *testing.T) {
		dir := t.TempDir()
		s := newStreamSpawner()
		w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
		pushTimeout(w, time.Hour)
		h := &recordHandle{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := runAsync(ctx, w, h)
		p := s.next(t)
		p.in.next(t)
		p.emit(lnInit, lnTasks1, lnStarted, lnResult)
		if !eventually(func() bool { return h.count(studioagent.Holding) == 1 }) {
			t.Fatal("no Holding")
		}
		p.emit(lnTasks0, lnUpdated, lnNotify, lnInit, lnResult)
		if !eventually(func() bool { return h.count(studioagent.Waiting) == 1 }) {
			t.Fatal("not waiting")
		}
		p.in.staysOpen(t, 50*time.Millisecond)
		cancel()
		<-done
	})
	t.Run("background wait elapses", func(t *testing.T) {
		dir := t.TempDir()
		s := newStreamSpawner()
		w := streamWL(t, dir, s, func(c *Config) { c.Resident = true; c.BackgroundWait = 30 * time.Millisecond })
		pushTimeout(w, time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := runAsync(ctx, w, &recordHandle{})
		p := s.next(t)
		p.in.next(t)
		p.emit(lnInit, lnTasks1, lnResult)
		p.in.waitClosed(t)
		cancel()
		<-done
	})
}

// With no IdleTimeout received (an older Jam), or one of 0, stdin closes when
// the turn ends, as before; a deadline push alone changes nothing.
func TestOpenEpisodeNoTimeoutClosesAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		push func(*Workload)
	}{
		{"none", func(*Workload) {}},
		{"zero", func(w *Workload) { pushTimeout(w, 0) }},
		{"deadline only", func(w *Workload) { pushDeadline(w, time.Now().Add(time.Hour)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := newStreamSpawner()
			w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
			tc.push(w)
			h := &recordHandle{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := runAsync(ctx, w, h)
			p := s.next(t)
			p.in.next(t)
			p.emit(lnInit, lnResult)
			p.in.waitClosed(t)
			if n := h.count(studioagent.Waiting); n != 0 {
				t.Fatalf("Waiting reported before the exit (%d)", n)
			}
			p.exit <- nil
			if !eventually(func() bool { return h.count(studioagent.Waiting) == 1 }) {
				t.Fatal("not waiting")
			}
			cancel()
			<-done
		})
	}
}

// A teardown cancels an open episode's run.
func TestOpenEpisodeTeardown(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	pushTimeout(w, time.Hour)
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnResult)
	if !eventually(func() bool { return h.count(studioagent.Waiting) == 1 }) {
		t.Fatal("not waiting")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}
