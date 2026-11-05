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
	// block, if set, runs before call n (1-based) answers; an error fails it.
	block func(ctx context.Context, n int) error
}

func (h *fakeHost) HostStats(ctx context.Context) (jam.HostStats, error) {
	h.mu.Lock()
	h.calls++
	n := h.calls
	h.mu.Unlock()
	if h.block != nil {
		if err := h.block(ctx, n); err != nil {
			return jam.HostStats{}, err
		}
	}
	if i := n - 1; i < len(h.errs) && h.errs[i] != nil {
		return jam.HostStats{}, h.errs[i]
	}
	return jam.HostStats{MemTotal: 8 << 30, MemUsed: uint64(n) << 30, DiskTotal: 100, DiskUsed: 50}, nil
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

// cachingHost also offers its latest stats without measuring.
type cachingHost struct {
	fakeHost
	mu     sync.Mutex
	cached *jam.HostStats
}

func (h *cachingHost) CachedHostStats() (jam.HostStats, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached == nil {
		return jam.HostStats{}, false
	}
	return *h.cached, true
}

// An on-demand report takes a HostCache's latest stats (a cleanup's record,
// say) without measuring; with none cached it keeps the last measurement.
func TestRunOnDemandUsesHostCache(t *testing.T) {
	h := &cachingHost{}
	changed := make(chan struct{}, 1)
	got := make(chan wire.StatusReport, 3)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		for range 3 {
			got <- statusOf(t, s.next(t))
		}
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{
		Name: "m", Launcher: &fakeLauncher{}, Host: h, StatusEvery: time.Hour, StatusChanged: changed,
		statusMinGap: time.Millisecond,
	})
	<-got
	changed <- struct{}{}
	if r := <-got; r.Host == nil || r.Host.MemUsed != 1<<30 {
		t.Fatalf("host = %+v, want the last measurement with nothing cached", r.Host)
	}
	h.mu.Lock()
	h.cached = &jam.HostStats{DiskTotal: 100, DiskUsed: 60, LastCleanup: &jam.CleanupRecord{Reason: "high", Freed: 30}}
	h.mu.Unlock()
	changed <- struct{}{}
	r := <-got
	if r.Host == nil || r.Host.LastCleanup == nil || r.Host.LastCleanup.Freed != 30 || r.Host.DiskUsed != 60 {
		t.Fatalf("host = %+v, want the cached stats", r.Host)
	}
	if n := h.n(); n != 1 {
		t.Fatalf("%d measurements, want 1", n)
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

// A measurement that hangs is cut off by its timeout: the report goes out
// with no host, the failure is warned about, and reports carry on.
func TestRunTimesOutHungMeasurement(t *testing.T) {
	h := &fakeHost{block: func(ctx context.Context, n int) error {
		if n == 1 {
			<-ctx.Done() // hung colima ssh: only the deadline ends it
			return ctx.Err()
		}
		return nil
	}}
	got := make(chan wire.StatusReport, 2)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		got <- statusOf(t, s.next(t))
		got <- statusOf(t, s.next(t))
		<-s.srv.Context().Done()
	}}
	var warns atomic.Int32
	log := slog.New(levelCounter{level: slog.LevelWarn, n: &warns})
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}, Host: h, StatusEvery: 50 * time.Millisecond,
		Log: log, hostStatsTimeout: 20 * time.Millisecond})
	if r := <-got; r.Host != nil {
		t.Fatalf("report after a hung measurement host = %+v, want nil", r.Host)
	}
	if r := <-got; r.Host == nil {
		t.Fatal("no host after the measurement recovered")
	}
	if n := warns.Load(); n != 1 {
		t.Fatalf("%d warnings, want 1 for the timeout", n)
	}
}

// An on-demand report never waits on a measurement in progress.
func TestRunOnDemandDoesNotWaitForMeasurement(t *testing.T) {
	release := make(chan struct{})
	measuring := make(chan struct{})
	h := &fakeHost{block: func(ctx context.Context, n int) error {
		if n == 2 {
			close(measuring)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return nil
	}}
	defer close(release)
	changed := make(chan struct{}, 1)
	got := make(chan wire.StatusReport, 2)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		got <- statusOf(t, s.next(t))
		got <- statusOf(t, s.next(t))
		<-s.srv.Context().Done()
	}}
	runAsync(t, start(t, fj), Config{Name: "m", Launcher: &fakeLauncher{}, Host: h, StatusEvery: 20 * time.Millisecond,
		StatusChanged: changed, statusMinGap: time.Millisecond, Activities: func() []jam.ManagerActivity { return nil }})
	<-got
	<-measuring // the periodic measurement is now stuck
	changed <- struct{}{}
	select {
	case r := <-got:
		if r.Host == nil || r.Host.MemUsed != 1<<30 {
			t.Fatalf("on-demand host = %+v, want the first stats", r.Host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("on-demand report waited on the measurement")
	}
}

// A closed StatusChanged ends on-demand reports; it does not spin sending.
func TestRunClosedStatusChangedSendsNoMore(t *testing.T) {
	changed := make(chan struct{})
	frames := make(chan struct{}, 64)
	fj := &fakeJam{fn: func(t *testing.T, s *session) {
		for {
			select {
			case m, ok := <-s.up:
				if !ok {
					return
				}
				if f := m.GetFrame(); f != nil && f.Op == wire.OpStatus {
					frames <- struct{}{}
				}
			case <-s.srv.Context().Done():
				return
			}
		}
	}}
	runAsync(t, start(t, fj), Config{
		Name: "m", Launcher: &fakeLauncher{}, StatusEvery: time.Hour, StatusChanged: changed,
		Activities: func() []jam.ManagerActivity { return nil }, statusMinGap: time.Millisecond,
	})
	<-frames // the report after Welcome
	close(changed)
	time.Sleep(100 * time.Millisecond)
	if n := len(frames); n > 1 {
		t.Fatalf("%d reports after StatusChanged closed, want at most 1", n)
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
