package condition

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Persister stores condition occurrences, keyed by (Key, Since).
type Persister interface {
	// Load returns every open condition and those resolved at or after resolvedSince.
	Load(ctx context.Context, resolvedSince time.Time) ([]Condition, error)
	// Save upserts one occurrence (identified by Key and Since).
	Save(ctx context.Context, c Condition) error
	// Prune deletes occurrences resolved before resolvedBefore.
	Prune(ctx context.Context, resolvedBefore time.Time) error
}

// Options configures a Tracker. Persister nil keeps conditions in memory only.
type Options struct {
	Persister Persister
	Log       *slog.Logger
	Now       func() time.Time
	Retention time.Duration // resolved history; default 7 days
}

// Tracker holds the open conditions and recent history. Raise, Clear, Fail
// and Ok are O(1), never block on I/O and never fail; changes are written to
// the Persister by Flush (Run calls it periodically). A nil *Tracker is a
// valid no-op tracker.
type Tracker struct {
	opt           Options
	mu            sync.Mutex
	open          map[string]Condition
	resolved      []Condition // newest resolution first
	fails         map[string]int
	dirty         map[string]Condition // occurrence id → latest state, awaiting Save
	flushMu       sync.Mutex           // serializes Flush saves to prevent reordering
	afterSnapshot func()               // hook called after snapshot in Flush; test only
}

// New builds a Tracker, filling defaults.
func New(opt Options) *Tracker {
	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Retention <= 0 {
		opt.Retention = 7 * 24 * time.Hour
	}
	return &Tracker{opt: opt, open: map[string]Condition{}, fails: map[string]int{}, dirty: map[string]Condition{}}
}

func occurrence(c Condition) string { return c.Key + "@" + c.Since.UTC().Format(time.RFC3339Nano) }

// Raise opens c (or refreshes it if already open: Since is kept, LastSeen
// bumped, severity and text updated). Invalid keys or severities are dropped
// with a WARN.
func (t *Tracker) Raise(c Condition) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.raiseLocked(c)
}

func (t *Tracker) raiseLocked(c Condition) {
	if !validKey(c.Key) || !validSeverity(c.Severity) {
		t.opt.Log.Warn("condition dropped: invalid key or severity", "key", clip(c.Key, MaxKey), "severity", string(c.Severity))
		return
	}
	c.Summary, c.Detail, c.Fix = clip(c.Summary, MaxSummary), clip(c.Detail, MaxDetail), clip(c.Fix, MaxFix)
	now := t.opt.Now()
	cur, ok := t.open[c.Key]
	if ok {
		c.Since = cur.Since
	} else {
		c.Since = now
		t.opt.Log.Warn("condition raised", "key", c.Key, "severity", string(c.Severity))
	}
	c.LastSeen, c.ResolvedAt = now, nil
	t.open[c.Key] = c
	t.dirty[occurrence(c)] = c
}

// Clear resolves key if open.
func (t *Tracker) Clear(key string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearLocked(key)
}

func (t *Tracker) clearLocked(key string) {
	c, ok := t.open[key]
	if !ok {
		return
	}
	now := t.opt.Now()
	c.ResolvedAt = &now
	delete(t.open, key)
	t.resolved = append([]Condition{c}, t.resolved...)
	t.dirty[occurrence(c)] = c
	t.opt.Log.Info("condition resolved", "key", key, "open_for", now.Sub(c.Since).Round(time.Second).String())
}

// Fail records one failure for c.Key and raises c once threshold consecutive
// failures have been seen (and on every failure after that).
func (t *Tracker) Fail(c Condition, threshold int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fails[c.Key]++
	if t.fails[c.Key] >= threshold {
		t.raiseLocked(c)
	}
}

// Ok resets key's failure count and resolves it if open.
func (t *Tracker) Ok(key string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, key)
	t.clearLocked(key)
}

// Get returns key's open condition.
func (t *Tracker) Get(key string) (Condition, bool) {
	if t == nil {
		return Condition{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.open[key]
	return c, ok
}

// IsOpen reports whether key is open.
func (t *Tracker) IsOpen(key string) bool { _, ok := t.Get(key); return ok }

// OpenKeys lists the keys of open conditions of the given kind, sorted.
func (t *Tracker) OpenKeys(kind string) []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	var out []string
	for k, c := range t.open {
		if c.Kind() == kind {
			out = append(out, k)
		}
	}
	t.mu.Unlock()
	slices.Sort(out)
	return out
}

// Open lists open conditions: critical, warning, info; oldest first within.
func (t *Tracker) Open() []Condition {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	out := make([]Condition, 0, len(t.open))
	for _, c := range t.open {
		out = append(out, c)
	}
	t.mu.Unlock()
	slices.SortFunc(out, func(a, b Condition) int {
		if r := sevRank(a.Severity) - sevRank(b.Severity); r != 0 {
			return r
		}
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return compareStr(a.Key, b.Key)
	})
	return out
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Resolved lists conditions resolved within the retention window, newest first.
func (t *Tracker) Resolved() []Condition {
	if t == nil {
		return nil
	}
	cutoff := t.opt.Now().Add(-t.opt.Retention)
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Condition
	for _, c := range t.resolved {
		if c.ResolvedAt.Before(cutoff) {
			break // newest first: the rest are older
		}
		out = append(out, c)
	}
	return out
}

// Load replaces state with the Persister's: open conditions keep their Since.
func (t *Tracker) Load(ctx context.Context) error {
	if t == nil || t.opt.Persister == nil {
		return nil
	}
	cs, err := t.opt.Persister.Load(ctx, t.opt.Now().Add(-t.opt.Retention))
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.open, t.resolved = map[string]Condition{}, nil
	cutoff := t.opt.Now().Add(-t.opt.Retention)
	for _, c := range cs {
		switch {
		case c.IsOpen():
			t.open[c.Key] = c
		case !c.ResolvedAt.Before(cutoff):
			t.resolved = append(t.resolved, c)
		}
	}
	slices.SortFunc(t.resolved, func(a, b Condition) int { return b.ResolvedAt.Compare(*a.ResolvedAt) })
	return nil
}

// Flush saves every changed occurrence. Failed saves stay queued (unless a
// newer state superseded them) and the joined error is returned.
func (t *Tracker) Flush(ctx context.Context) error {
	if t == nil || t.opt.Persister == nil {
		return nil
	}
	t.flushMu.Lock()
	defer t.flushMu.Unlock()

	t.mu.Lock()
	batch := t.dirty
	t.dirty = map[string]Condition{}
	t.mu.Unlock()

	if t.afterSnapshot != nil {
		t.afterSnapshot()
	}

	var errs []error
	for id, c := range batch {
		if err := t.opt.Persister.Save(ctx, c); err != nil {
			errs = append(errs, err)
			t.mu.Lock()
			if _, newer := t.dirty[id]; !newer {
				t.dirty[id] = c
			}
			t.mu.Unlock()
		}
	}
	return errors.Join(errs...)
}

// Run flushes every `every` and prunes history hourly, until ctx ends. A
// failing Persister is logged once per streak, never fatal.
func (t *Tracker) Run(ctx context.Context, every time.Duration) {
	if t == nil {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	var lastPrune time.Time
	failing := false
	pruneFailing := false
	for {
		select {
		case <-ctx.Done():
			_ = t.Flush(context.Background())
			return
		case <-tick.C:
		}
		if err := t.Flush(ctx); err != nil {
			if !failing {
				t.opt.Log.Warn("conditions not persisted; retrying", "reason", err.Error())
			}
			failing = true
		} else if failing {
			failing = false
			t.opt.Log.Info("conditions persisted again")
		}
		if now := t.opt.Now(); t.opt.Persister != nil && now.Sub(lastPrune) > time.Hour {
			if err := t.opt.Persister.Prune(ctx, now.Add(-t.opt.Retention)); err != nil {
				if !pruneFailing {
					t.opt.Log.Warn("conditions history not pruned; retrying", "reason", err.Error())
				}
				pruneFailing = true
			} else {
				if pruneFailing {
					t.opt.Log.Info("conditions history pruned again")
				}
				pruneFailing = false
				lastPrune = now
			}
		}
	}
}
