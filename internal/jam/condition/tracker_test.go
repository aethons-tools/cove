package condition

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type fakePersister struct {
	mu      sync.Mutex
	saved   map[string]Condition // key+since → latest
	fail    bool
	loaded  []Condition
	pruned  []time.Time
	saveLog []string // log of save states: "open" or "resolved"
}

func newFake() *fakePersister { return &fakePersister{saved: map[string]Condition{}} }

func (f *fakePersister) Load(ctx context.Context, since time.Time) ([]Condition, error) {
	return f.loaded, nil
}

func (f *fakePersister) Save(ctx context.Context, c Condition) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("db down")
	}
	state := "open"
	if c.ResolvedAt != nil {
		state = "resolved"
	}
	f.saveLog = append(f.saveLog, state)
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

func TestTrackerConcurrentFlushOrdering(t *testing.T) {
	// Flush A snapshots {k:open}. Inside the afterSnapshot seam (after A's
	// snapshot, before A's saves) the hook resolves k and starts Flush B,
	// then waits up to 200ms for B to finish.
	//
	//   - flushMu at the top of Flush (correct): B blocks on flushMu until A
	//     returns, so the wait times out, A saves {open}, then B saves
	//     {resolved}. The last save is resolved.
	//   - flushMu below the seam (broken): B snapshots {resolved}, takes the
	//     free lock and saves first; A then saves its stale {open} last, so the
	//     last save is open and the assertion fails.
	//
	// The hook is gated by an atomic CAS so only A's call blocks; B's own call
	// to the hook returns immediately.
	c := &clock{t: time.Unix(1, 0)}
	p := newFake()
	tr := newTracker(p, c)

	tr.Raise(cond("k:a", Critical))
	since := c.t

	var fired atomic.Bool
	bDone := make(chan struct{})
	tr.afterSnapshot = func() {
		if !fired.CompareAndSwap(false, true) {
			return
		}
		tr.Clear("k:a")
		go func() {
			defer close(bDone)
			_ = tr.Flush(context.Background())
		}()
		select {
		case <-bDone:
		case <-time.After(200 * time.Millisecond):
		}
	}

	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("flush A: %v", err)
	}
	<-bDone

	p.mu.Lock()
	last, ok := p.saved["k:a@"+since.Format(time.RFC3339Nano)]
	saveLog := append([]string(nil), p.saveLog...)
	p.mu.Unlock()

	if !ok {
		t.Fatal("occurrence not saved")
	}
	if last.ResolvedAt == nil {
		t.Fatalf("last saved state is open, want resolved; save order: %v", saveLog)
	}
}

func TestTrackerClearAndReraiseCreatesTwoOccurrences(t *testing.T) {
	// Verify that Clear + re-Raise creates two distinct occurrences:
	// one resolved, one open, with different Since timestamps.
	c := &clock{t: time.Unix(1, 0)}
	p := newFake()
	tr := newTracker(p, c)

	// Raise condition k:a at time t1
	tr.Raise(cond("k:a", Critical))
	t1 := c.t

	// Flush to persist
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Clear the condition at time t1
	tr.Clear("k:a")

	// Advance time
	c.add(time.Hour)

	// Re-raise with a new Since at time t2
	tr.Raise(cond("k:a", Critical))
	t2 := c.t

	// Flush to persist both occurrences
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Verify both occurrences are saved
	p.mu.Lock()
	defer p.mu.Unlock()

	occ1, ok1 := p.saved[("k:a")+"@"+t1.Format(time.RFC3339Nano)]
	occ2, ok2 := p.saved[("k:a")+"@"+t2.Format(time.RFC3339Nano)]

	if !ok1 {
		t.Fatal("first occurrence not saved")
	}
	if !ok2 {
		t.Fatal("second occurrence not saved")
	}

	if occ1.ResolvedAt == nil {
		t.Fatal("first occurrence should be resolved")
	}
	if occ2.ResolvedAt != nil {
		t.Fatal("second occurrence should be open")
	}
}

func keys(cs []Condition) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}
