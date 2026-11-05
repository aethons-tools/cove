package client

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

type fakeHost struct {
	mu    sync.Mutex
	calls int
	errs  []error // per call; nil or missing = success
}

func (h *fakeHost) HostStats(context.Context) (jam.HostStats, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if i := h.calls - 1; i < len(h.errs) && h.errs[i] != nil {
		return jam.HostStats{}, h.errs[i]
	}
	return jam.HostStats{MemTotal: 8 << 30, MemUsed: uint64(h.calls) << 30, DiskTotal: 100, DiskUsed: 50}, nil
}

func (h *fakeHost) n() int { h.mu.Lock(); defer h.mu.Unlock(); return h.calls }

func statusOf(t *testing.T, f *managerpb.Frame) wire.StatusReport {
	t.Helper()
	if f.Op != wire.OpStatus || f.Id != 0 || f.More {
		t.Fatalf("frame = id %d op %q more %v, want a single status frame with id 0", f.Id, f.Op, f.More)
	}
	var r wire.StatusReport
	if err := json.Unmarshal(f.Data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// A manager with a HostReporter reports right after Welcome, then every
// StatusEvery, each with fresh stats.
func TestRunReportsHostAfterWelcomeAndPeriodically(t *testing.T) {
	h := &fakeHost{}
	got := make(chan wire.StatusReport, 2)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		got <- statusOf(t, s.next(t))
		got <- statusOf(t, s.next(t))
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}, Host: h, StatusEvery: 20 * time.Millisecond})
	first, second := <-got, <-got
	if first.Host == nil || first.Host.MemUsed != 1<<30 || first.Host.DiskTotal != 100 {
		t.Fatalf("first report host = %+v", first.Host)
	}
	if second.Host == nil || second.Host.MemUsed != 2<<30 {
		t.Fatalf("second report host = %+v, want fresh stats", second.Host)
	}
	if first.Activities != nil {
		t.Fatalf("activities = %v, want none", first.Activities)
	}
}

// A failed measurement reports Host nil and warns once, not every tick.
func TestRunReportsNoHostOnErrorAndWarnsOnce(t *testing.T) {
	boom := errors.New("colima ssh: exit 1")
	h := &fakeHost{errs: []error{boom, boom, boom}}
	got := make(chan wire.StatusReport, 4)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		for range 4 {
			got <- statusOf(t, s.next(t))
		}
		<-s.srv.Context().Done()
	}}
	var warns atomic.Int32
	log := slog.New(levelCounter{level: slog.LevelWarn, n: &warns})
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}, Host: h, StatusEvery: 10 * time.Millisecond, Log: log})
	for i := range 3 {
		if r := <-got; r.Host != nil {
			t.Fatalf("report %d host = %+v, want nil on error", i, r.Host)
		}
	}
	if r := <-got; r.Host == nil {
		t.Fatal("report after recovery has no host")
	}
	if n := warns.Load(); n != 1 {
		t.Fatalf("%d warnings, want 1", n)
	}
}

// A StatusChanged signal sends a report at once with the current activities,
// reusing the last host stats rather than measuring again.
func TestRunReportsOnDemand(t *testing.T) {
	h := &fakeHost{}
	changed := make(chan struct{}, 1)
	var mu sync.Mutex
	acts := []jam.ManagerActivity(nil)
	got := make(chan wire.StatusReport, 2)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		got <- statusOf(t, s.next(t))
		got <- statusOf(t, s.next(t))
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{
		Name: "m", Launcher: &fakeLauncher{}, Host: h, StatusEvery: time.Hour, StatusChanged: changed,
		Activities: func() []jam.ManagerActivity { mu.Lock(); defer mu.Unlock(); return acts },
	})
	<-got
	mu.Lock()
	acts = []jam.ManagerActivity{{ID: "a1", Kind: "kit-prepare", Step: "build"}}
	mu.Unlock()
	changed <- struct{}{}
	r := <-got
	if len(r.Activities) != 1 || r.Activities[0].ID != "a1" {
		t.Fatalf("activities = %v", r.Activities)
	}
	if r.Host == nil || r.Host.MemUsed != 1<<30 || h.n() != 1 {
		t.Fatalf("host = %+v after %d measurements, want the first stats reused", r.Host, h.n())
	}
}

// On-demand reports are throttled: a burst of signals sends one report, no
// sooner than StatusMinGap after the last.
func TestRunThrottlesOnDemandReports(t *testing.T) {
	changed := make(chan struct{}, 1)
	frames := make(chan time.Time, 8)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		for {
			select {
			case m, ok := <-s.up:
				if !ok {
					return
				}
				if f := m.GetFrame(); f != nil && f.Op == wire.OpStatus {
					frames <- time.Now()
				}
			case <-s.srv.Context().Done():
				return
			}
		}
	}}
	runAsync(t, start(t, fj), Config{
		Name: "m", Launcher: &fakeLauncher{}, StatusEvery: time.Hour, StatusChanged: changed,
		Activities: func() []jam.ManagerActivity { return nil }, statusMinGap: 100 * time.Millisecond,
	})
	t0 := <-frames // the report after Welcome
	for range 5 {
		changed <- struct{}{}
	}
	t1 := <-frames
	if gap := t1.Sub(t0); gap < 90*time.Millisecond {
		t.Fatalf("on-demand report %v after the last, want ≥ the min gap", gap)
	}
	select {
	case <-frames:
		t.Fatal("a burst sent more than one report")
	case <-time.After(250 * time.Millisecond):
	}
}

// A manager with neither host stats nor activities sends no reports.
func TestRunWithoutSourcesSendsNoStatus(t *testing.T) {
	saw := make(chan string, 1)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		s.request(1, wire.OpProbe, wire.InstanceRequest{})
		saw <- s.next(t).Op
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}, StatusEvery: time.Millisecond})
	if op := <-saw; op != wire.OpResult {
		t.Fatalf("first frame op = %q, want the probe's result", op)
	}
}

// levelCounter counts records at exactly level.
type levelCounter struct {
	level slog.Level
	n     *atomic.Int32
}

func (h levelCounter) Enabled(context.Context, slog.Level) bool { return true }
func (h levelCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Level == h.level {
		h.n.Add(1)
	}
	return nil
}
func (h levelCounter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h levelCounter) WithGroup(string) slog.Handler      { return h }
