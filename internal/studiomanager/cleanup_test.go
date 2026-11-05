package studiomanager

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// fakeDisk is a 100-byte disk: HostStats reports used, and each cleanup step
// frees what the test set for it.
type fakeDisk struct {
	mu         sync.Mutex
	used       uint64
	measureErr error
	calls      []string
	frees      map[string]uint64 // step or "rm <id>" -> bytes freed
	errs       map[string]error
	images     []jam.KitImage
	block      chan struct{} // non-nil: PruneDanglingImages waits on it
	entered    chan struct{}
	onRemove   func(id string)   // nil: none; runs inside RemoveImage
	onStep     func(name string) // nil: none; called as each step starts
}

func (d *fakeDisk) HostStats(context.Context) (jam.HostStats, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.measureErr != nil {
		return jam.HostStats{}, d.measureErr
	}
	return jam.HostStats{DiskTotal: 100, DiskUsed: d.used}, nil
}

func (d *fakeDisk) step(name string) error {
	if d.onStep != nil {
		d.onStep(name)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, name)
	if err := d.errs[name]; err != nil {
		return err
	}
	d.used -= min(d.used, d.frees[name])
	return nil
}

func (d *fakeDisk) PruneDanglingImages(context.Context) error {
	if d.block != nil {
		d.entered <- struct{}{}
		<-d.block
	}
	return d.step("dangling")
}
func (d *fakeDisk) PruneVolumes(context.Context) error    { return d.step("volumes") }
func (d *fakeDisk) PruneBuildCache(context.Context) error { return d.step("build cache") }
func (d *fakeDisk) RemoveImage(_ context.Context, id string) error {
	if d.onRemove != nil {
		d.onRemove(id)
	}
	return d.step("rm " + id)
}
func (d *fakeDisk) KitImages(context.Context) ([]jam.KitImage, error) {
	if err := d.step("kit images"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]jam.KitImage(nil), d.images...), nil
}

func (d *fakeDisk) set(used uint64) {
	d.mu.Lock()
	d.used = used
	d.mu.Unlock()
}

func (d *fakeDisk) takeCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.calls
	d.calls = nil
	return c
}

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func defaultCleanup() CleanupConfig {
	c := CleanupConfig{}
	c.defaults()
	return c
}

func newPolicy(t *testing.T, d *fakeDisk, cfg CleanupConfig) (*diskPolicy, *clock) {
	t.Helper()
	clk := &clock{t: t0}
	var cl jam.HostCleaner = d
	p := newDiskPolicy(context.Background(), d, cl, cfg, slog.New(slog.DiscardHandler))
	p.now = clk.now
	p.nextDaily = t0.Add(dailyFirst)
	t.Cleanup(p.wait)
	return p, clk
}

// sample takes one stats sample and waits for any cleanup it started.
func sample(t *testing.T, p *diskPolicy) jam.HostStats {
	t.Helper()
	h, err := p.HostStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.wait()
	return h
}

func lastCleanup(t *testing.T, p *diskPolicy) *jam.CleanupRecord {
	t.Helper()
	return sample(t, p).LastCleanup
}

func TestPolicyIdleBelowHigh(t *testing.T) {
	d := &fakeDisk{used: 84}
	p, _ := newPolicy(t, d, defaultCleanup())
	if h := sample(t, p); h.LastCleanup != nil || h.DiskUsed != 84 {
		t.Fatalf("stats = %+v", h)
	}
	if c := d.takeCalls(); len(c) != 0 {
		t.Fatalf("cleaned below high: %v", c)
	}
	if n, full := p.full(); full {
		t.Fatalf("full at %d%%", n)
	}
}

// Over high the steps run in order, re-measuring after each, and stop once
// under low; kit images go oldest first, one at a time, skipping any in use
// or younger than kitImageMinAge.
func TestPolicyStopsUnderLow(t *testing.T) {
	d := &fakeDisk{used: 90,
		frees: map[string]uint64{"dangling": 5, "rm old": 10, "rm older": 6, "rm new": 10},
		images: []jam.KitImage{
			{ID: "new", Created: t0.Add(-2 * time.Hour)},
			{ID: "used", Created: t0.Add(-90 * 24 * time.Hour), InUse: true},
			{ID: "old", Created: t0.Add(-48 * time.Hour)},
			{ID: "fresh", Created: t0.Add(-30 * time.Minute)},
			{ID: "older", Created: t0.Add(-72 * time.Hour)},
		}}
	changed := make(chan struct{}, 1)
	p, _ := newPolicy(t, d, defaultCleanup())
	p.changed = changed
	sample(t, p)
	want := []string{"dangling", "kit images", "rm older", "rm old"}
	if c := d.takeCalls(); !reflect.DeepEqual(c, want) {
		t.Fatalf("calls = %v, want %v", c, want)
	}
	select {
	case <-changed:
	default:
		t.Fatal("no status report asked for after the cleanup")
	}
	// The report the cleanup asks for carries it and its last measurement.
	if h, ok := p.CachedHostStats(); !ok || h.DiskUsed != 69 || h.LastCleanup == nil || h.LastCleanup.Freed != 21 {
		t.Fatalf("CachedHostStats = %+v, %v", h, ok)
	}
	rec := lastCleanup(t, p)
	if rec == nil || rec.Reason != "high" || rec.Freed != 21 || rec.Err != "" || !rec.At.Equal(t0) {
		t.Fatalf("LastCleanup = %+v", rec)
	}
}

func TestPolicyRunsEveryStep(t *testing.T) {
	d := &fakeDisk{used: 90, images: []jam.KitImage{{ID: "a", Created: t0.Add(-2 * time.Hour)}}}
	p, _ := newPolicy(t, d, defaultCleanup())
	sample(t, p)
	want := []string{"dangling", "kit images", "rm a", "volumes", "build cache"}
	if c := d.takeCalls(); !reflect.DeepEqual(c, want) {
		t.Fatalf("calls = %v, want %v", c, want)
	}
}

// A failing step is recorded and the rest still run; an image docker refuses
// to remove is skipped, not an error.
func TestPolicyRecordsErrors(t *testing.T) {
	d := &fakeDisk{used: 90,
		errs:   map[string]error{"dangling": errors.New("daemon busy"), "rm a": errors.New("image is in use")},
		frees:  map[string]uint64{"rm b": 1, "build cache": 30},
		images: []jam.KitImage{{ID: "a", Created: t0.Add(-3 * time.Hour)}, {ID: "b", Created: t0.Add(-2 * time.Hour)}}}
	p, _ := newPolicy(t, d, defaultCleanup())
	sample(t, p)
	want := []string{"dangling", "kit images", "rm a", "rm b", "volumes", "build cache"}
	if c := d.takeCalls(); !reflect.DeepEqual(c, want) {
		t.Fatalf("calls = %v, want %v", c, want)
	}
	rec := lastCleanup(t, p)
	if rec == nil || rec.Freed != 31 || !strings.Contains(rec.Err, "daemon busy") || strings.Contains(rec.Err, "in use") {
		t.Fatalf("LastCleanup = %+v", rec)
	}
}

// While a kit build or raise is in flight, kit images and dangling images
// (a build's not yet tagged) are left alone.
func TestPolicySkipsImagesWhileBuilding(t *testing.T) {
	d := &fakeDisk{used: 90, images: []jam.KitImage{{ID: "a", Created: t0.Add(-2 * time.Hour)}}}
	p, _ := newPolicy(t, d, defaultCleanup())
	p.work = &kitWork{}
	defer p.work.start()()
	sample(t, p)
	want := []string{"volumes", "build cache"}
	if c := d.takeCalls(); !reflect.DeepEqual(c, want) {
		t.Fatalf("calls = %v, want %v", c, want)
	}
}

func TestPolicyCritical(t *testing.T) {
	d := &fakeDisk{used: 96}
	p, _ := newPolicy(t, d, defaultCleanup())
	sample(t, p)
	if n, full := p.full(); !full || n != 96 {
		t.Fatalf("full = %d, %v", n, full)
	}
	if rec := lastCleanup(t, p); rec == nil || rec.Reason != "critical" {
		t.Fatalf("LastCleanup = %+v", rec)
	}
	d.set(50)
	sample(t, p)
	if _, full := p.full(); full {
		t.Fatal("still full at 50%")
	}
	// An unmeasurable disk is not full: raises are not declined on stale data.
	d.set(99)
	sample(t, p)
	d.mu.Lock()
	d.measureErr = errors.New("ssh failed")
	d.mu.Unlock()
	if _, err := p.HostStats(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if _, full := p.full(); full {
		t.Fatal("full after a failed measurement")
	}
	if _, ok := p.CachedHostStats(); ok {
		t.Fatal("cached stats after a failed measurement")
	}
}

// One cleanup at a time: samples during a run start none.
func TestPolicySingleFlight(t *testing.T) {
	d := &fakeDisk{used: 90, block: make(chan struct{}), entered: make(chan struct{}, 2)}
	p, _ := newPolicy(t, d, defaultCleanup())
	if _, err := p.HostStats(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-d.entered
	done := make(chan struct{})
	go func() { // a sample during the run returns at once
		defer close(done)
		_, _ = p.HostStats(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HostStats blocked on the running cleanup")
	}
	close(d.block)
	p.wait()
	if c := d.takeCalls(); len(c) == 0 || c[0] != "dangling" || len(c) != 4 {
		t.Fatalf("calls = %v (want one run)", c)
	}
	select {
	case <-d.entered:
		t.Fatal("a second cleanup ran")
	default:
	}
}

// A run that could not get under low is not retried for pressureCooldown,
// unless the disk turns critical.
func TestPolicyCooldown(t *testing.T) {
	d := &fakeDisk{used: 90}
	p, clk := newPolicy(t, d, defaultCleanup())
	sample(t, p)
	d.takeCalls()
	clk.add(pressureCooldown - time.Second)
	sample(t, p)
	if c := d.takeCalls(); len(c) != 0 {
		t.Fatalf("ran inside the cooldown: %v", c)
	}
	d.set(96)
	sample(t, p)
	if rec := lastCleanup(t, p); rec == nil || rec.Reason != "critical" {
		t.Fatalf("critical inside a high run's cooldown: %+v", rec)
	}
	d.takeCalls()
	sample(t, p)
	if c := d.takeCalls(); len(c) != 0 {
		t.Fatalf("critical reran inside its cooldown: %v", c)
	}
	clk.add(pressureCooldown)
	sample(t, p)
	if c := d.takeCalls(); len(c) == 0 {
		t.Fatal("no run after the cooldown")
	}
}

// A critical sample runs at once after a high run even when a daily pass came
// in between: only a critical run's own cooldown holds it.
func TestPolicyCriticalAfterHighThenDaily(t *testing.T) {
	d := &fakeDisk{used: 90}
	p, clk := newPolicy(t, d, defaultCleanup())
	clk.add(dailyFirst - time.Minute)
	sample(t, p) // high
	if rec := lastCleanup(t, p); rec == nil || rec.Reason != "high" {
		t.Fatalf("LastCleanup = %+v", rec)
	}
	d.set(20)
	clk.add(time.Minute)
	sample(t, p) // daily
	if rec := lastCleanup(t, p); rec == nil || rec.Reason != "daily" {
		t.Fatalf("LastCleanup = %+v", rec)
	}
	d.set(96)
	clk.add(time.Minute)
	sample(t, p)
	if rec := lastCleanup(t, p); rec == nil || rec.Reason != "critical" {
		t.Fatalf("critical held by a high run's cooldown: %+v", rec)
	}
}

// The daily light pass prunes dangling images and anonymous volumes only,
// whatever the disk, dailyFirst after start and then every dailyEvery.
func TestPolicyDaily(t *testing.T) {
	d := &fakeDisk{used: 20, frees: map[string]uint64{"volumes": 2}, images: []jam.KitImage{{ID: "a"}}}
	p, clk := newPolicy(t, d, defaultCleanup())
	clk.add(dailyFirst - time.Second)
	sample(t, p)
	if c := d.takeCalls(); len(c) != 0 {
		t.Fatalf("daily ran early: %v", c)
	}
	clk.add(time.Second)
	sample(t, p)
	if c := d.takeCalls(); !reflect.DeepEqual(c, []string{"dangling", "volumes"}) {
		t.Fatalf("daily calls = %v", c)
	}
	if rec := lastCleanup(t, p); rec == nil || rec.Reason != "daily" || rec.Freed != 2 {
		t.Fatalf("LastCleanup = %+v", rec)
	}
	clk.add(dailyEvery - time.Second)
	sample(t, p)
	if c := d.takeCalls(); len(c) != 0 {
		t.Fatalf("daily reran early: %v", c)
	}
	clk.add(time.Second)
	sample(t, p)
	if c := d.takeCalls(); len(c) != 2 {
		t.Fatalf("no second daily: %v", c)
	}
}

func TestPolicyDailyOff(t *testing.T) {
	d := &fakeDisk{used: 20}
	cfg := defaultCleanup()
	off := false
	cfg.Daily = &off
	p, clk := newPolicy(t, d, cfg)
	clk.add(dailyFirst)
	sample(t, p)
	if c := d.takeCalls(); len(c) != 0 {
		t.Fatalf("daily ran while off: %v", c)
	}
}

// Disabled: no automatic cleanup, but raises are still declined at critical.
func TestPolicyDisabled(t *testing.T) {
	d := &fakeDisk{used: 97}
	cfg := defaultCleanup()
	cfg.Disabled = true
	p, clk := newPolicy(t, d, cfg)
	clk.add(dailyFirst)
	sample(t, p)
	if c := d.takeCalls(); len(c) != 0 {
		t.Fatalf("cleaned while disabled: %v", c)
	}
	if _, full := p.full(); !full {
		t.Fatal("not full at 97% while disabled")
	}
}

// Without a cleaner the policy only gates.
func TestPolicyNoCleaner(t *testing.T) {
	d := &fakeDisk{used: 97}
	p := newDiskPolicy(context.Background(), d, nil, defaultCleanup(), slog.New(slog.DiscardHandler))
	if _, err := p.HostStats(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.wait()
	if _, full := p.full(); !full {
		t.Fatal("not full at 97%")
	}
}

func TestGatedDeclinesWhenDiskFull(t *testing.T) {
	ops := &fakeBackend{}
	g := newGated(ops, 0)
	g.disk = func() (int, bool) { return 96, true }
	_, err := g.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKit}, jam.LaunchCreds{})
	if err == nil || err.Error() != "disk full (96%)" {
		t.Fatalf("err = %v, want disk full (96%%)", err)
	}
	ops.snapshot(func(f *fakeBackend) {
		if f.runs != 0 {
			t.Fatalf("RunEphemeral called %d times", f.runs)
		}
	})
}

// Kit work cannot start between the policy's busy check and its removal:
// start waits for a removal in progress, and no removal runs once it has
// returned.
func TestPolicyRemovalExcludesKitWork(t *testing.T) {
	d := &fakeDisk{used: 90, images: []jam.KitImage{
		{ID: "a", Created: t0.Add(-4 * time.Hour)}, {ID: "b", Created: t0.Add(-3 * time.Hour)}, {ID: "c", Created: t0.Add(-2 * time.Hour)}}}
	p, _ := newPolicy(t, d, defaultCleanup())
	work := &kitWork{}
	p.work = work
	var inWork atomic.Bool
	started := make(chan func(), 1)
	d.onRemove = func(id string) {
		if inWork.Load() {
			t.Errorf("removed %s while kit work was in flight", id)
		}
		if id != "a" {
			return
		}
		go func() {
			done := work.start()
			inWork.Store(true)
			started <- done
		}()
		time.Sleep(20 * time.Millisecond) // start must wait out this removal
		if inWork.Load() {
			t.Error("kit work started during a removal")
		}
	}
	sample(t, p)
	(<-started)()
	if c := d.takeCalls(); len(c) < 3 || c[2] != "rm a" {
		t.Fatalf("calls = %v", c)
	}
}

func TestKitWork(t *testing.T) {
	var w kitWork
	ran := false
	if !w.whileIdle(func() { ran = true }) || !ran {
		t.Fatal("idle work did not run")
	}
	done := w.start()
	if w.whileIdle(func() { t.Fatal("ran while busy") }) {
		t.Fatal("whileIdle true while busy")
	}
	done()
	if !w.whileIdle(func() {}) {
		t.Fatal("busy after done")
	}
}

// A raise in flight is kit work: the disk policy keeps kit images meanwhile.
func TestGatedKitBusyDuringRaise(t *testing.T) {
	ops := &fakeBackend{entered: make(chan struct{}, 1), block: make(chan struct{})}
	g := newGated(ops, 0)
	idle := func() bool { return g.work.whileIdle(func() {}) }
	if !idle() {
		t.Fatal("busy before any work")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = g.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKit}, jam.LaunchCreds{})
	}()
	select {
	case <-ops.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("raise never reached RunEphemeral")
	}
	if idle() {
		t.Fatal("not busy during a raise")
	}
	close(ops.block)
	<-done
	if !idle() {
		t.Fatal("busy after the raise")
	}
}

// A cleanup is a "cleanup" activity while it runs, its step naming what it
// removes; it ends with the run.
func TestPolicyReportsCleanupActivity(t *testing.T) {
	d := &fakeDisk{used: 90, images: []jam.KitImage{{ID: "a", Created: t0.Add(-2 * time.Hour)}}}
	p, _ := newPolicy(t, d, defaultCleanup())
	p.acts = newActivities(nil)
	var seen []string
	d.onStep = func(name string) {
		snap := p.acts.Snapshot()
		if len(snap) != 1 || snap[0].Kind != jam.ActivityCleanup {
			t.Errorf("at %s: activities = %+v", name, snap)
			return
		}
		seen = append(seen, name+": "+snap[0].Step)
	}
	sample(t, p)
	want := []string{"dangling: removing dangling images", "kit images: removing unused kit images", "rm a: removing unused kit images",
		"volumes: removing anonymous volumes", "build cache: pruning the build cache"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("steps = %q, want %q", seen, want)
	}
	if snap := p.acts.Snapshot(); len(snap) != 0 {
		t.Fatalf("after the run: %+v", snap)
	}
}
