package condition

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type fakePersister struct {
	mu     sync.Mutex
	saved  map[string]Condition // key+since → latest
	fail   bool
	loaded []Condition
	pruned []time.Time
	block  chan struct{} // when non-nil, Save waits on it
}

func newFake() *fakePersister { return &fakePersister{saved: map[string]Condition{}} }

func (f *fakePersister) Load(ctx context.Context, since time.Time) ([]Condition, error) {
	return f.loaded, nil
}
func (f *fakePersister) Save(ctx context.Context, c Condition) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("db down")
	}
	f.saved[c.Key+"@"+c.Since.Format(time.RFC3339Nano)] = c
	return nil
}
func (f *fakePersister) Prune(ctx context.Context, before time.Time) error {
	f.pruned = append(f.pruned, before)
	return nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func quiet() *slog.Logger            { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func newTracker(p Persister, c *clock) *Tracker {
	return New(Options{Persister: p, Log: quiet(), Now: c.now})
}

func cond(key string, sev Severity) Condition {
	return Condition{Key: key, Severity: sev, Summary: "s " + key, Fix: "fix it"}
}

func TestKey(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"cred.unavailable", "vertex-gcp"}:       "cred.unavailable:vertex-gcp",
		{"cred.unavailable", "Vertex GCP"}:       "cred.unavailable:vertex-gcp",
		{"standing.crashloop", "ACME/ic/spider"}: "standing.crashloop:acme/ic/spider",
		{"pool.account.refresh", "a\"b\\c"}:      "pool.account.refresh:a-b-c",
	} {
		if got := Key(in[0], in[1]); got != want {
			t.Errorf("Key(%q,%q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	if got := Key("k", strings.Repeat("x", 500)); len(got) > MaxKey {
		t.Errorf("Key not capped: %d bytes", len(got))
	}
}

func TestTrackerRaiseClearAndOrdering(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	tr := newTracker(newFake(), c)
	tr.Raise(cond("a.warn:x", Warning))
	c.add(time.Minute)
	tr.Raise(cond("a.crit:y", Critical))
	tr.Raise(cond("a.info:z", Info))
	open := tr.Open()
	if len(open) != 3 || open[0].Key != "a.crit:y" || open[1].Key != "a.warn:x" || open[2].Key != "a.info:z" {
		t.Fatalf("Open order = %v; want critical, warning, info", keys(open))
	}
	since := open[1].Since
	c.add(time.Minute)
	tr.Raise(cond("a.warn:x", Warning)) // re-raise keeps since, bumps last_seen
	got, _ := tr.Get("a.warn:x")
	if !got.Since.Equal(since) || !got.LastSeen.Equal(c.t) {
		t.Fatalf("re-raise: since %v last_seen %v", got.Since, got.LastSeen)
	}
	tr.Clear("a.warn:x")
	if tr.IsOpen("a.warn:x") {
		t.Fatal("cleared condition still open")
	}
	res := tr.Resolved()
	if len(res) != 1 || res[0].Key != "a.warn:x" || res[0].ResolvedAt == nil || !res[0].ResolvedAt.Equal(c.t) {
		t.Fatalf("Resolved = %+v", res)
	}
}

func TestTrackerRejectsInvalidAndClips(t *testing.T) {
	c := &clock{t: time.Unix(1, 0)}
	tr := newTracker(newFake(), c)
	tr.Raise(Condition{Key: "NoColon", Severity: Critical, Summary: "x"})
	tr.Raise(Condition{Key: "k:ok", Severity: "loud", Summary: "x"})
	if len(tr.Open()) != 0 {
		t.Fatalf("invalid conditions accepted: %v", keys(tr.Open()))
	}
	tr.Raise(Condition{Key: "k:ok", Severity: Warning, Summary: strings.Repeat("é", 300), Detail: strings.Repeat("d", 5000), Fix: strings.Repeat("f", 900)})
	got, _ := tr.Get("k:ok")
	if len(got.Summary) > MaxSummary || len(got.Detail) > MaxDetail || len(got.Fix) > MaxFix {
		t.Fatalf("not clipped: %d/%d/%d", len(got.Summary), len(got.Detail), len(got.Fix))
	}
	if !utf8.ValidString(got.Summary) {
		t.Fatal("summary clipped mid-rune")
	}
}

func TestTrackerFailOkDebounceAndRecurrence(t *testing.T) {
	c := &clock{t: time.Unix(1_000, 0)}
	tr := newTracker(newFake(), c)
	k := "cred.unavailable:x"
	for i := 0; i < 5; i++ { // flapping below threshold never raises
		tr.Fail(cond(k, Critical), 3)
		tr.Fail(cond(k, Critical), 3)
		tr.Ok(k)
	}
	if tr.IsOpen(k) {
		t.Fatal("raised below threshold")
	}
	for i := 0; i < 3; i++ {
		tr.Fail(cond(k, Critical), 3)
	}
	first, ok := tr.Get(k)
	if !ok {
		t.Fatal("not raised at threshold")
	}
	c.add(time.Hour)
	tr.Ok(k)
	c.add(time.Hour)
	for i := 0; i < 3; i++ {
		tr.Fail(cond(k, Critical), 3)
	}
	second, _ := tr.Get(k)
	if !second.Since.After(first.Since) {
		t.Fatal("recurrence did not open a new occurrence")
	}
	if res := tr.Resolved(); len(res) != 1 || !res[0].Since.Equal(first.Since) {
		t.Fatalf("earlier occurrence missing from resolved: %+v", res)
	}
}

func TestTrackerPersistFailureDoesNotBlockAndRetries(t *testing.T) {
	c := &clock{t: time.Unix(1, 0)}
	p := newFake()
	p.fail = true
	tr := newTracker(p, c)
	done := make(chan struct{})
	go func() { tr.Raise(cond("k:a", Critical)); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Raise blocked")
	}
	if err := tr.Flush(context.Background()); err == nil {
		t.Fatal("Flush hid the persist error")
	}
	if !tr.IsOpen("k:a") {
		t.Fatal("condition lost from memory after a failed save")
	}
	p.fail = false
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("retry flush: %v", err)
	}
	if len(p.saved) != 1 {
		t.Fatalf("not retried: saved=%d", len(p.saved))
	}
}

func TestTrackerLoadKeepsSinceAndRecentResolved(t *testing.T) {
	c := &clock{t: time.Unix(10*86400, 0)}
	since := c.t.Add(-3 * time.Hour)
	old := c.t.Add(-8 * 24 * time.Hour)
	recent := c.t.Add(-24 * time.Hour)
	p := newFake()
	p.loaded = []Condition{
		{Key: "k:open", Severity: Critical, Summary: "o", Since: since, LastSeen: since},
		{Key: "k:recent", Severity: Warning, Summary: "r", Since: recent.Add(-time.Hour), ResolvedAt: &recent},
		{Key: "k:old", Severity: Warning, Summary: "x", Since: old.Add(-time.Hour), ResolvedAt: &old},
	}
	tr := newTracker(p, c)
	if err := tr.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := tr.Get("k:open")
	if !ok || !got.Since.Equal(since) {
		t.Fatalf("open condition since = %v, want %v", got.Since, since)
	}
	if res := tr.Resolved(); len(res) != 1 || res[0].Key != "k:recent" {
		t.Fatalf("Resolved after load = %v; want only k:recent (7-day retention)", keys(res))
	}
}

func TestTrackerNilSafe(t *testing.T) {
	var tr *Tracker
	tr.Raise(cond("k:a", Critical))
	tr.Clear("k:a")
	tr.Fail(cond("k:a", Critical), 1)
	tr.Ok("k:a")
	if tr.IsOpen("k:a") || tr.Open() != nil || tr.Resolved() != nil {
		t.Fatal("nil tracker returned data")
	}
}

type blockablePersister struct {
	mu      sync.Mutex
	saved   map[string]Condition
	calls   int           // count Save calls in order
	entered chan struct{} // signaled when first Save is entered
	release chan struct{} // closed to unblock first Save
	saveLog []string      // log of save order: "open" or "resolved"
}

func (bp *blockablePersister) Load(ctx context.Context, since time.Time) ([]Condition, error) {
	return nil, nil
}

func (bp *blockablePersister) Save(ctx context.Context, cond Condition) error {
	bp.mu.Lock()
	bp.calls++
	isFirst := bp.calls == 1
	bp.mu.Unlock()

	if isFirst {
		close(bp.entered) // signal that first Save is in progress
		<-bp.release      // wait for release signal
	}

	bp.mu.Lock()
	defer bp.mu.Unlock()
	state := "open"
	if cond.ResolvedAt != nil {
		state = "resolved"
	}
	bp.saveLog = append(bp.saveLog, state)
	bp.saved[cond.Key+"@"+cond.Since.Format(time.RFC3339Nano)] = cond
	return nil
}

func (bp *blockablePersister) Prune(ctx context.Context, before time.Time) error {
	return nil
}

func TestTrackerConcurrentFlushOrdering(t *testing.T) {
	// Verify that concurrent Flushes see consistent snapshots when flushMu is held
	// at the top. Without the fix (flushMu after snapshot), the sequence is:
	// 1. Flush A snapshots {k:open}
	// 2. Clear runs, marks dirty with resolved
	// 3. Flush B snapshots {k:resolved}
	// 4. If B takes flushMu first, saves resolved
	// 5. Then A saves open, OVERWRITING B's state
	// Result: final state is open (wrong!)
	//
	// With the fix (flushMu at top), A holds flushMu through save, so Clear and
	// B's snapshot happen while A still has the lock, ensuring consistent order.
	c := &clock{t: time.Unix(1, 0)}

	bp := &blockablePersister{
		saved:   map[string]Condition{},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}

	tr := newTracker(bp, c)

	// Raise condition k:a at time t=1
	tr.Raise(cond("k:a", Critical))
	since1 := c.t

	// Start Flush A in a goroutine; it will block in the first Save
	flushA := make(chan error, 1)
	go func() { flushA <- tr.Flush(context.Background()) }()

	// Wait for Flush A to enter Save (first call)
	<-bp.entered

	// While Flush A is blocked in Save (and holds/waits for flushMu depending on fix),
	// Clear the condition. This marks the occurrence as resolved in dirty.
	tr.Clear("k:a")

	// Start Flush B. With the broken code (flushMu after snapshot), B can snapshot
	// the resolved state and may save before A does. With the fixed code (flushMu
	// at top), B blocks on flushMu until A finishes.
	flushB := make(chan error, 1)
	go func() { flushB <- tr.Flush(context.Background()) }()

	// Give B a moment to attempt flushMu
	time.Sleep(10 * time.Millisecond)

	// Unblock Flush A's Save
	close(bp.release)

	// Wait for both to complete
	if err := <-flushA; err != nil {
		t.Fatalf("flush A: %v", err)
	}
	if err := <-flushB; err != nil {
		t.Fatalf("flush B: %v", err)
	}

	// With the fix, the save order should be: open (A), resolved (B)
	// Without the fix, the order could be: resolved (B), open (A) if B got
	// flushMu first, causing A's stale open state to win.
	// The test verifies the final state is resolved (which it should be).
	bp.mu.Lock()
	saved, ok := bp.saved[("k:a")+"@"+since1.Format(time.RFC3339Nano)]
	saveOrder := bp.saveLog
	bp.mu.Unlock()

	if !ok {
		t.Fatal("occurrence not saved")
	}
	if saved.ResolvedAt == nil {
		t.Errorf("occurrence should be resolved, got open; save order: %v", saveOrder)
	}
}

func keys(cs []Condition) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}
