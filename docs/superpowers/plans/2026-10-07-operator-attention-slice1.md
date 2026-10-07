# Operator attention — slice 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jam turns lapsed brokered credentials and failing pool refreshes into persistent, severity-tagged *conditions*, shows them in the admin UI (rail badge + a Health tab), lists them over the admin API / CLI, and exports them on a token-gated `/metrics` that a shipped Prometheus + Alertmanager bundle turns into Discord alerts.

**Architecture:** A leaf package `internal/jam/condition` owns the model, the in-memory `Tracker` (async write-through to a `Persister`), the Prometheus text exposition and the admin JSON handler. `internal/jam/condition/conditionpg` persists to Postgres with its own embedded migrations (the `allocpg` pattern). Producers in `internal/jam` (a `WatchedResolver` wrapping the broker's credential resolver; the pool `Refresher`) call `Fail`/`Ok`. `cmd/at-jam` wires it: serve-config `metrics:`, `/metrics` on the broker (cove-facing) listener, `GET /admin/attention`, `at-jam attention list`. The admin UI feeds open conditions into its *existing* attention system (#393) and adds a Jam **Health** tab.

**Tech Stack:** Go 1.26 stdlib (`net/http`, `log/slog`, `crypto/subtle`), pgx v5 (`pgxpool`), html/template + htmx (admin UI), Prometheus v3 + Alertmanager v0.28 (Docker, bundle only). No Prometheus client library.

**Spec:** [`docs/superpowers/specs/2026-10-06-operator-attention-design.md`](../specs/2026-10-06-operator-attention-design.md) — read §1–§6. §4 is amended by Task 8 (see "Deviation" below).

**Deviation from the spec (flagged to the operator 2026-10-07):** the spec's "banner on every admin page" is replaced by the admin UI's existing attention system (rail/tab badges + "Needs attention" cards, `internal/jam/adminui/attention.go`, landed in #393 after the spec). Open critical/warning conditions become Jam-scope attention items; the Health tab carries the full list (incl. info), fix text and resolved history. Task 8 updates spec §4 to match.

**Out of this slice (by design):** level checkers and `Tracker.Reconcile` arrive with slice 2's first level producer (standing crash-loop); slice 1's producers are all edge-triggered.

## Global Constraints

- Condition key: `<kind>:<subject>`, charset `[a-z0-9._:/-]`, ≤ 200 bytes; build keys only with `condition.Key(kind, subject)`.
- Severities: exactly `critical`, `warning`, `info`. Info is never an attention item and never pushed (Alertmanager drops it).
- Field caps: summary ≤ 200 bytes, detail ≤ 2048 bytes, fix ≤ 500 bytes (clipped on a rune boundary).
- Conditions name things only — credential/account/project/role/kit names and short error codes. **Never** secret values, tokens, or raw upstream bodies.
- Resolved conditions are kept **7 days**.
- `Raise`/`Clear`/`Fail`/`Ok` never block on I/O and never return errors; persistence is async, coalesced, retried; every method is nil-receiver safe.
- `/metrics` is served only when `metrics.token-cred` is configured; auth is `Authorization: Bearer <scrape token>` compared in constant time; anything else → 401. Cove identity tokens are not accepted.
- Edge debounce thresholds: `cred.unavailable` 3 consecutive failures; `pool.account.refresh` 2 consecutive failed passes; clear on first success.
- Alertmanager routing: critical `repeat_interval: 1h`, warning `12h`, info → no receiver, `send_resolved: true`, `group_by: [alertname, key]`.
- Go: `GOPROXY=https://proxy.golang.org,direct` in this sandbox. Tests hermetic (`just test`); Postgres tests behind `//go:build integration` + `JAM_TEST_POSTGRES_DSN`.
- Every task updates docs it changes behavior for (repo rule: no task complete until docs are updated); Task 8 owns the new docs pages.

## Review Focus

1. **Postgres down or slow while the broker is under load** → `Raise`/`Fail` must still return immediately and the condition must still show in memory/UI/metrics; the write is retried later. *(Task 1: `TestTrackerPersistFailureDoesNotBlockAndRetries`.)*
2. **Jam restarts with a credential still lapsed** → the condition keeps its original `since` (no re-alert as "new"), and a condition resolved yesterday still shows in Health. *(Task 2: `TestConditionpgRoundTrip` + Task 1: `TestTrackerLoadKeepsSinceAndRecentResolved`.)*
3. **Summary/fix text containing `"`, `\` or newlines** (operator-facing strings built from names) → `/metrics` must stay valid exposition, not break the whole scrape. *(Task 3: `TestMetricsEscapesLabelValues`.)*
4. **A flapping credential** (fail, ok, fail, ok…) → never raises below threshold; a recurrence after resolve opens a *new* occurrence with a new `since`, and the earlier one stays in resolved history. *(Task 1: `TestTrackerFailOkDebounceAndRecurrence`.)*
5. **A resolver error that contains secret material** (e.g. a `command:` printing a token on failure) → the condition's summary/detail must not carry it. *(Task 4: `TestWatchedResolverNeverCopiesErrorText`.)*

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/condition/condition.go` | Model: `Condition`, `Severity`, `Key`, validation + clipping |
| `internal/jam/condition/tracker.go` | `Tracker`: Raise/Clear/Fail/Ok/Get/IsOpen/Open/Resolved, Load, Flush, Run |
| `internal/jam/condition/metrics.go` | `MetricsHandler` — Prometheus text exposition + bearer auth |
| `internal/jam/condition/admin.go` | `AdminHandler` — `GET /admin/attention?state=` JSON |
| `internal/jam/condition/conditionpg/{conditionpg.go,migrations.go,migrations/0001_conditions.sql}` | Postgres `Persister` |
| `internal/jam/credwatch.go` | `WatchedResolver` — `cred.unavailable` producer |
| `internal/jam/refresher.go` (modify) | `pool.account.refresh` producer |
| `cmd/at-jam/config.go`, `cmd/at-jam/main.go`, `cmd/at-jam/mux.go` (modify) | serve-config `metrics:`, wiring, `/metrics` mount, `attention` verb |
| `internal/jam/adminclient/adminclient.go` (modify) | `ListConditions` |
| `internal/jam/adminui/{attention.go,nav.go,adminui.go,health.go}`, `templates/{health.html,attention.html,layout.html}` | condition items + Health tab |
| `deploy/monitoring/*`, `justfile` | the bundle + recipes |
| `docs/usage/jam/monitoring.md`, `docs/usage/jam/serve.md`, `docs/usage/jam/ui.md`, `docs/usage/jam/INDEX.md`, spec §4 | docs |

---

### Task 1: Condition model and Tracker

**Files:**
- Create: `internal/jam/condition/condition.go`, `internal/jam/condition/tracker.go`
- Test: `internal/jam/condition/tracker_test.go`

**Interfaces:**
- Consumes: nothing (leaf package — must NOT import `internal/jam`).
- Produces:
  - `type Severity string`; consts `Critical`, `Warning`, `Info`
  - `type Condition struct { Key string; Severity Severity; Summary, Detail, Fix string; Since, LastSeen time.Time; ResolvedAt *time.Time }` (JSON tags `key,severity,summary,detail,fix,since,last_seen,resolved_at`)
  - `func (c Condition) Kind() string`, `func (c Condition) IsOpen() bool`
  - `func Key(kind, subject string) string`
  - `type Persister interface { Load(ctx context.Context, resolvedSince time.Time) ([]Condition, error); Save(ctx context.Context, c Condition) error; Prune(ctx context.Context, resolvedBefore time.Time) error }`
  - `type Options struct { Persister Persister; Log *slog.Logger; Now func() time.Time; Retention time.Duration }`
  - `func New(opt Options) *Tracker`
  - `(*Tracker) Raise(Condition)`, `Clear(key string)`, `Fail(c Condition, threshold int)`, `Ok(key string)`, `Get(key string) (Condition, bool)`, `IsOpen(key string) bool`, `Open() []Condition`, `Resolved() []Condition`, `Load(ctx) error`, `Flush(ctx) error`, `Run(ctx, every time.Duration)` — all nil-receiver safe.

- [ ] **Step 1: Write the failing tests**

```go
// internal/jam/condition/tracker_test.go
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

func (c *clock) now() time.Time         { return c.t }
func (c *clock) add(d time.Duration)    { c.t = c.t.Add(d) }
func quiet() *slog.Logger               { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func newTracker(p Persister, c *clock) *Tracker {
	return New(Options{Persister: p, Log: quiet(), Now: c.now})
}

func cond(key string, sev Severity) Condition {
	return Condition{Key: key, Severity: sev, Summary: "s " + key, Fix: "fix it"}
}

func TestKey(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"cred.unavailable", "vertex-gcp"}:      "cred.unavailable:vertex-gcp",
		{"cred.unavailable", "Vertex GCP"}:      "cred.unavailable:vertex-gcp",
		{"standing.crashloop", "ACME/ic/spider"}: "standing.crashloop:acme/ic/spider",
		{"pool.account.refresh", "a\"b\\c"}:     "pool.account.refresh:a-b-c",
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

func keys(cs []Condition) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}
```

(Add `"unicode/utf8"` to the test imports.)

- [ ] **Step 2: Run to verify failure**

Run: `GOPROXY=https://proxy.golang.org,direct go test ./internal/jam/condition/`
Expected: FAIL — package has no non-test Go files / undefined: `New`, `Key`, …

- [ ] **Step 3: Implement `condition.go`**

```go
// Package condition tracks operator-attention conditions: named, severity-tagged
// problems Jam itself detects (a lapsed brokered credential, a failing pool
// refresh, …). It owns the model, the in-memory Tracker with async
// write-through persistence, the Prometheus exposition and the admin JSON
// view. It is a leaf package: producers in internal/jam call into it.
// See docs/usage/jam/monitoring.md.
package condition

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Severity is how urgently a condition needs an operator.
type Severity string

const (
	Critical Severity = "critical" // agents are failing now
	Warning  Severity = "warning"  // degraded, or will fail
	Info     Severity = "info"     // shown, never pushed
)

// Field caps (bytes).
const (
	MaxKey     = 200
	MaxSummary = 200
	MaxDetail  = 2048
	MaxFix     = 500
)

// Condition is one problem that is (or was) true. Text fields name things
// only — never secret values, tokens or raw upstream bodies.
type Condition struct {
	Key        string     `json:"key"`
	Severity   Severity   `json:"severity"`
	Summary    string     `json:"summary"`
	Detail     string     `json:"detail,omitempty"`
	Fix        string     `json:"fix,omitempty"`
	Since      time.Time  `json:"since"`
	LastSeen   time.Time  `json:"last_seen"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// Kind is the key's prefix (before the first ':').
func (c Condition) Kind() string { k, _, _ := strings.Cut(c.Key, ":"); return k }

// IsOpen reports whether the condition has not been resolved.
func (c Condition) IsOpen() bool { return c.ResolvedAt == nil }

var keyRE = regexp.MustCompile(`^[a-z0-9._-]+:[a-z0-9._:/-]+$`)

var keyUnsafe = regexp.MustCompile(`[^a-z0-9._:/-]+`)

// Key builds a valid condition key from a kind and a subject (a name):
// lowercased, runs of other characters become '-', capped at MaxKey.
func Key(kind, subject string) string {
	k := kind + ":" + keyUnsafe.ReplaceAllString(strings.ToLower(subject), "-")
	if len(k) > MaxKey {
		k = k[:MaxKey]
	}
	return k
}

func validKey(k string) bool { return len(k) <= MaxKey && keyRE.MatchString(k) }

func validSeverity(s Severity) bool { return s == Critical || s == Warning || s == Info }

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func sevRank(s Severity) int {
	switch s {
	case Critical:
		return 0
	case Warning:
		return 1
	}
	return 2
}
```

- [ ] **Step 4: Implement `tracker.go`**

```go
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
	opt      Options
	mu       sync.Mutex
	open     map[string]Condition
	resolved []Condition // newest resolution first
	fails    map[string]int
	dirty    map[string]Condition // occurrence id → latest state, awaiting Save
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
	t.mu.Lock()
	batch := t.dirty
	t.dirty = map[string]Condition{}
	t.mu.Unlock()
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
			if err := t.opt.Persister.Prune(ctx, now.Add(-t.opt.Retention)); err == nil {
				lastPrune = now
			}
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/jam/condition/ -count=1` (and `CGO_ENABLED=1 go test -race ./internal/jam/condition/` where a C compiler exists)
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/jam/condition/
git commit -m "feat(jam): condition tracker — operator-attention model with async persistence"
```

---

### Task 2: Postgres persister (`conditionpg`)

**Files:**
- Create: `internal/jam/condition/conditionpg/conditionpg.go`, `internal/jam/condition/conditionpg/migrations.go`, `internal/jam/condition/conditionpg/migrations/0001_conditions.sql`
- Test: `internal/jam/condition/conditionpg/conditionpg_integration_test.go`

**Interfaces:**
- Consumes: `condition.Condition`, `condition.Persister` (Task 1).
- Produces: `func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error)`; `*Store` implements `condition.Persister`.

- [ ] **Step 1: Write the migration**

```sql
-- 0001_conditions.sql — operator-attention condition occurrences (one row per
-- (key, since)); open rows have resolved_at NULL. See docs/usage/jam/monitoring.md.
CREATE TABLE IF NOT EXISTS attention_conditions (
    key         text        NOT NULL,
    since       timestamptz NOT NULL,
    doc         jsonb       NOT NULL,
    resolved_at timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key, since)
);
CREATE INDEX IF NOT EXISTS attention_conditions_resolved ON attention_conditions (resolved_at);
```

`migrations.go`:

```go
package conditionpg

import "embed"

//go:embed migrations/*.sql
var migrationFiles embed.FS
```

- [ ] **Step 2: Write the failing integration test**

```go
//go:build integration

package conditionpg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the conditionpg integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := New(context.Background(), pool, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE attention_conditions`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func TestConditionpgRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	open := condition.Condition{Key: "cred.unavailable:x", Severity: condition.Critical, Summary: "s", Fix: "f", Since: now.Add(-time.Hour), LastSeen: now}
	if err := s.Save(ctx, open); err != nil {
		t.Fatal(err)
	}
	open.LastSeen = now.Add(time.Minute) // upsert same occurrence
	if err := s.Save(ctx, open); err != nil {
		t.Fatal(err)
	}
	r1 := now.Add(-48 * time.Hour)
	old := now.Add(-10 * 24 * time.Hour)
	for _, c := range []condition.Condition{
		{Key: "k:recent", Severity: condition.Warning, Summary: "r", Since: r1.Add(-time.Hour), LastSeen: r1, ResolvedAt: &r1},
		{Key: "k:old", Severity: condition.Warning, Summary: "o", Since: old.Add(-time.Hour), LastSeen: old, ResolvedAt: &old},
	} {
		if err := s.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Load(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Load = %d rows, want open + recent", len(got))
	}
	for _, c := range got {
		if c.Key == "cred.unavailable:x" && (!c.Since.Equal(open.Since) || !c.LastSeen.Equal(open.LastSeen)) {
			t.Fatalf("open occurrence not upserted: %+v", c)
		}
	}
	if err := s.Prune(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Load(ctx, time.Time{})
	if len(all) != 2 {
		t.Fatalf("after prune = %d rows, want 2 (old pruned)", len(all))
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test -tags integration ./internal/jam/condition/conditionpg/` (with `JAM_TEST_POSTGRES_DSN` set, e.g. against `docker run -e POSTGRES_PASSWORD=x -p 5432:5432 postgres:17`)
Expected: FAIL — undefined: `New`, `Store`

- [ ] **Step 4: Implement `conditionpg.go`**

Copy `migrate` verbatim from `internal/allocator/allocpg/allocpg.go:264-326`, renaming `alloc_schema_migrations` → `attention_schema_migrations`, the `allocpg:` error prefixes → `conditionpg:`, and the lock constant:

```go
// Package conditionpg is the Postgres Persister for internal/jam/condition. It
// shares the control-plane pool and owns its own migrations (the allocpg
// pattern), so the condition package stays free of pgx.
package conditionpg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// migrateAdvisoryLock is distinct from the jam, intercom and alloc locks.
const migrateAdvisoryLock = 0x617474656e74 // "attent"

// Store persists condition occurrences in attention_conditions.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ condition.Persister = (*Store)(nil)

// New applies the embedded migrations and returns a ready Store. It does not own the pool.
func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Store{pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Save upserts one occurrence by (key, since).
func (s *Store) Save(ctx context.Context, c condition.Condition) error {
	doc, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("conditionpg: marshal: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO attention_conditions (key, since, doc, resolved_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (key, since) DO UPDATE SET doc = EXCLUDED.doc, resolved_at = EXCLUDED.resolved_at, updated_at = now()`,
		c.Key, c.Since, doc, c.ResolvedAt)
	if err != nil {
		return fmt.Errorf("conditionpg: save %s: %w", c.Key, err)
	}
	return nil
}

// Load returns every open occurrence and those resolved at or after resolvedSince.
func (s *Store) Load(ctx context.Context, resolvedSince time.Time) ([]condition.Condition, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT doc FROM attention_conditions WHERE resolved_at IS NULL OR resolved_at >= $1`, resolvedSince)
	if err != nil {
		return nil, fmt.Errorf("conditionpg: load: %w", err)
	}
	defer rows.Close()
	var out []condition.Condition
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var c condition.Condition
		if err := json.Unmarshal(doc, &c); err != nil {
			return nil, fmt.Errorf("conditionpg: decode: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Prune deletes occurrences resolved before resolvedBefore.
func (s *Store) Prune(ctx context.Context, resolvedBefore time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM attention_conditions WHERE resolved_at < $1`, resolvedBefore)
	return err
}

// migrate: copied from allocpg (see Step 4 intro), table attention_schema_migrations.
func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return fmt.Errorf("conditionpg: advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS attention_schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("conditionpg: create attention_schema_migrations: %w", err)
		}
		applied := map[int]bool{}
		rows, err := tx.Query(ctx, `SELECT version FROM attention_schema_migrations`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			applied[v] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		entries, err := fs.ReadDir(migrationFiles, "migrations")
		if err != nil {
			return err
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			i := strings.IndexByte(name, '_')
			if i <= 0 {
				return fmt.Errorf("conditionpg: bad migration name %q", name)
			}
			ver, err := strconv.Atoi(name[:i])
			if err != nil {
				return fmt.Errorf("conditionpg: bad migration version in %q: %w", name, err)
			}
			if applied[ver] {
				continue
			}
			sqlText, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return fmt.Errorf("conditionpg: apply %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO attention_schema_migrations (version) VALUES ($1)`, ver); err != nil {
				return err
			}
			s.log.Info("conditionpg: applied migration", "version", ver, "file", name)
		}
		return nil
	})
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test -tags integration ./internal/jam/condition/conditionpg/ -count=1` (DSN set); `go build ./...`
Expected: PASS (CI's `store-integration` job already runs `./internal/jam/...` with the tag).

- [ ] **Step 6: Commit**

```bash
git add internal/jam/condition/conditionpg/
git commit -m "feat(jam): conditionpg — Postgres persistence for operator-attention conditions"
```

---

### Task 3: `/metrics` exposition and the admin JSON handler

**Files:**
- Create: `internal/jam/condition/metrics.go`, `internal/jam/condition/admin.go`
- Test: `internal/jam/condition/metrics_test.go`, `internal/jam/condition/admin_test.go`

**Interfaces:**
- Consumes: `*Tracker` (Task 1).
- Produces:
  - `type Gauge struct { Name, Help string; Value float64 }`
  - `func MetricsHandler(t *Tracker, token string, gauges func() []Gauge) http.Handler` — `gauges` may be nil.
  - `func AdminHandler(t *Tracker) http.Handler` — `GET` with `?state=open|resolved|all` (default open), JSON `[]Condition`; other state → 400.

- [ ] **Step 1: Write the failing tests**

```go
// metrics_test.go
package condition

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(h http.Handler, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/metrics", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsAuth(t *testing.T) {
	h := MetricsHandler(New(Options{}), "scrape-tok", nil)
	for name, auth := range map[string]string{"none": "", "wrong": "Bearer nope", "cove identity": "Bearer jam-identity-abc", "basic": "Basic c2NyYXBlLXRvaw=="} {
		if rec := scrape(h, auth); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}
	if rec := scrape(h, "Bearer scrape-tok"); rec.Code != http.StatusOK {
		t.Fatalf("right token: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", rec.Code)
	}
}

func TestMetricsExposition(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	tr := New(Options{Now: c.now})
	tr.Raise(Condition{Key: "cred.unavailable:vertex-gcp", Severity: Critical, Summary: "credential vertex-gcp unavailable", Fix: "gcloud auth application-default login"})
	tr.Raise(Condition{Key: "pool.account.refresh:a", Severity: Warning, Summary: "pool a"})
	tr.Raise(Condition{Key: "k:gone", Severity: Warning, Summary: "gone"})
	tr.Clear("k:gone")
	rec := scrape(MetricsHandler(tr, "t", func() []Gauge { return []Gauge{{Name: "jam_studios", Help: "Studios Jam knows of.", Value: 3}} }), "Bearer t")
	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content-type %q", ct)
	}
	for _, want := range []string{
		"# TYPE jam_up gauge\njam_up 1\n",
		`jam_attention_condition{key="cred.unavailable:vertex-gcp",kind="cred.unavailable",severity="critical",summary="credential vertex-gcp unavailable",fix="gcloud auth application-default login"} 1`,
		`jam_attention_condition{key="pool.account.refresh:a",kind="pool.account.refresh",severity="warning",summary="pool a",fix=""} 1`,
		`jam_attention_open{severity="critical"} 1`,
		`jam_attention_open{severity="warning"} 1`,
		`jam_attention_open{severity="info"} 0`,
		"# HELP jam_studios Studios Jam knows of.\n# TYPE jam_studios gauge\njam_studios 3\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "k:gone") {
		t.Error("resolved condition exported")
	}
}

func TestMetricsEscapesLabelValues(t *testing.T) {
	tr := New(Options{})
	tr.Raise(Condition{Key: "k:x", Severity: Warning, Summary: "a \"quoted\" back\\slash\nnewline", Fix: "run \"x\""})
	body := scrape(MetricsHandler(tr, "t", nil), "Bearer t").Body.String()
	want := `summary="a \"quoted\" back\\slash\nnewline",fix="run \"x\""`
	if !strings.Contains(body, want) {
		t.Fatalf("label values not escaped; want %s in:\n%s", want, body)
	}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "jam_") {
			t.Fatalf("broken exposition line %q", line)
		}
	}
}
```

```go
// admin_test.go
package condition

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminHandlerStates(t *testing.T) {
	tr := New(Options{})
	tr.Raise(Condition{Key: "k:open", Severity: Critical, Summary: "o"})
	tr.Raise(Condition{Key: "k:done", Severity: Warning, Summary: "d"})
	tr.Clear("k:done")
	h := AdminHandler(tr)
	for state, want := range map[string]int{"": 1, "open": 1, "resolved": 1, "all": 2} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/attention?state="+state, nil))
		var got []Condition
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 || len(got) != want {
			t.Errorf("state %q: code %d len %d err %v", state, rec.Code, len(got), err)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/attention?state=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bogus state: %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/condition/ -run 'Metrics|Admin'`
Expected: FAIL — undefined: `MetricsHandler`, `Gauge`, `AdminHandler`

- [ ] **Step 3: Implement `metrics.go`**

```go
package condition

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// Gauge is an extra single-value gauge the exposition appends (e.g. jam_studios).
type Gauge struct {
	Name, Help string
	Value      float64
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// MetricsHandler serves the Prometheus text exposition of the open conditions,
// gated on a static bearer token (constant-time compare). Only GET/HEAD.
func MetricsHandler(t *Tracker, token string, gauges func() []Gauge) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var b strings.Builder
		b.WriteString("# HELP jam_up Jam is serving.\n# TYPE jam_up gauge\njam_up 1\n")
		open := t.Open()
		b.WriteString("# HELP jam_attention_condition An open operator-attention condition (always 1).\n# TYPE jam_attention_condition gauge\n")
		count := map[Severity]int{}
		for _, c := range open {
			count[c.Severity]++
			fmt.Fprintf(&b, "jam_attention_condition{key=%q,kind=%q,severity=%q,summary=\"%s\",fix=\"%s\"} 1\n",
				c.Key, c.Kind(), string(c.Severity), labelEscaper.Replace(c.Summary), labelEscaper.Replace(c.Fix))
		}
		b.WriteString("# HELP jam_attention_open Open conditions by severity.\n# TYPE jam_attention_open gauge\n")
		for _, s := range []Severity{Critical, Warning, Info} {
			fmt.Fprintf(&b, "jam_attention_open{severity=%q} %d\n", string(s), count[s])
		}
		if gauges != nil {
			for _, g := range gauges() {
				fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", g.Name, g.Help, g.Name, g.Name, g.Value)
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}
```

(Keys and kinds pass `%q` safely: they are restricted to `[a-z0-9._:/-]`, so Go quoting equals Prometheus quoting. Summary/fix are free text and go through `labelEscaper`.)

- [ ] **Step 4: Implement `admin.go`**

```go
package condition

import (
	"encoding/json"
	"net/http"
)

// AdminHandler serves GET /admin/attention?state=open|resolved|all (default
// open) as a JSON array of conditions. Mount it behind the admin gate.
func AdminHandler(t *Tracker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out []Condition
		switch r.URL.Query().Get("state") {
		case "", "open":
			out = t.Open()
		case "resolved":
			out = t.Resolved()
		case "all":
			out = append(t.Open(), t.Resolved()...)
		default:
			http.Error(w, "state must be open, resolved or all", http.StatusBadRequest)
			return
		}
		if out == nil {
			out = []Condition{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
```

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/jam/condition/ -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/jam/condition/
git commit -m "feat(jam): condition metrics exposition (bearer-gated) and admin JSON view"
```

---

### Task 4: Producers — `cred.unavailable` and `pool.account.refresh`

**Files:**
- Create: `internal/jam/credwatch.go`
- Modify: `internal/jam/refresher.go` (RefresherOptions + RefreshDue)
- Test: `internal/jam/credwatch_test.go`, `internal/jam/refresher_conditions_test.go`

**Interfaces:**
- Consumes: `condition.Tracker`, `condition.Key`, `condition.Condition` (Task 1); `CredResolver`, `IdentityCredResolver`, `PoolCredResolver` (`internal/jam/creds.go`).
- Produces:
  - `func NewWatchedResolver(base CredResolver, t *condition.Tracker, fix func(name string) string) *WatchedResolver` — implements `CredResolver`, `IdentityCredResolver`, `PoolCredResolver`.
  - `const CredFailThreshold = 3`, `const PoolFailThreshold = 2`
  - `RefresherOptions.Conditions *condition.Tracker` (nil = no conditions).

- [ ] **Step 1: Write the failing tests**

```go
// credwatch_test.go
package jam

import (
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

type flakyCreds struct{ err error }

func (f *flakyCreds) Resolve(name string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "VAL", nil
}

func TestWatchedResolverRaisesAfterThresholdAndClears(t *testing.T) {
	tr := condition.New(condition.Options{})
	base := &flakyCreds{err: errors.New("boom")}
	w := NewWatchedResolver(base, tr, func(n string) string { return "check " + n })
	key := condition.Key("cred.unavailable", "vertex-gcp")
	for i := 0; i < CredFailThreshold-1; i++ {
		_, _ = w.Resolve("vertex-gcp")
	}
	if tr.IsOpen(key) {
		t.Fatal("raised below threshold")
	}
	_, _ = w.ResolveFor("vertex-gcp", "idhash") // ResolveFor counts too
	c, ok := tr.Get(key)
	if !ok || c.Severity != condition.Critical || c.Fix != "check vertex-gcp" {
		t.Fatalf("condition = %+v, ok=%v", c, ok)
	}
	base.err = nil
	if v, err := w.Resolve("vertex-gcp"); err != nil || v != "VAL" {
		t.Fatalf("passthrough: %q %v", v, err)
	}
	if tr.IsOpen(key) {
		t.Fatal("success did not clear")
	}
}

func TestWatchedResolverNeverCopiesErrorText(t *testing.T) {
	tr := condition.New(condition.Options{})
	w := NewWatchedResolver(&flakyCreds{err: errors.New("resolver printed sekrit-token-123")}, tr, func(string) string { return "" })
	for i := 0; i < CredFailThreshold; i++ {
		_, _ = w.Resolve("c")
	}
	c, ok := tr.Get(condition.Key("cred.unavailable", "c"))
	if !ok {
		t.Fatal("not raised")
	}
	if strings.Contains(c.Summary+c.Detail+c.Fix, "sekrit") {
		t.Fatalf("secret copied into condition: %+v", c)
	}
}

func TestWatchedResolverKeepsPoolAndIdentity(t *testing.T) {
	pool := &ChainResolver{base: &flakyCreds{}, poolCred: "anthropic-sub"}
	w := NewWatchedResolver(pool, condition.New(condition.Options{}), func(string) string { return "" })
	if !w.PoolCredential("anthropic-sub") || w.PoolCredential("other") {
		t.Fatal("PoolCredential not delegated")
	}
	if w2 := NewWatchedResolver(&flakyCreds{}, nil, nil); w2.PoolCredential("x") {
		t.Fatal("non-pool base reported a pool credential")
	}
	var _ IdentityCredResolver = w
	var _ PoolCredResolver = w
}
```

```go
// refresher_conditions_test.go
package jam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

type memPoolStore struct{ accts []PoolAccount }

func (m *memPoolStore) Accounts() ([]PoolAccount, error) { return m.accts, nil }
func (m *memPoolStore) SetAccount(a PoolAccount) error {
	for i := range m.accts {
		if m.accts[i].Name == a.Name {
			m.accts[i] = a
		}
	}
	return nil
}
func (m *memPoolStore) Bindings() (map[string]string, error) { return map[string]string{}, nil }
func (m *memPoolStore) Bind(identityHash, accountName string) error { return nil }

func TestRefresherRaisesPoolConditions(t *testing.T) {
	failing := map[string]bool{"a": true, "b": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ RefreshToken string `json:"refresh_token"` }
		_ = jsonDecode(r, &body)
		if failing[body.RefreshToken] {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Write([]byte(`{"access_token":"new","expires_in":3600}`))
	}))
	defer srv.Close()
	now := time.Unix(1_000_000, 0)
	st := &memPoolStore{accts: []PoolAccount{{Name: "a", RefreshToken: "a", ExpiresAt: now}, {Name: "b", RefreshToken: "b", ExpiresAt: now}}}
	tr := condition.New(condition.Options{})
	r := NewRefresher(st, RefresherOptions{TokenURL: srv.URL, Now: func() time.Time { return now }, Conditions: tr})
	ka, kb := condition.Key("pool.account.refresh", "a"), condition.Key("pool.account.refresh", "b")

	_ = r.RefreshDue(context.Background()) // 1st failing pass: below threshold
	if tr.IsOpen(ka) {
		t.Fatal("raised after one pass")
	}
	_ = r.RefreshDue(context.Background()) // 2nd: both open, and every account failing ⇒ critical
	ca, _ := tr.Get(ka)
	cb, _ := tr.Get(kb)
	if ca.Severity != condition.Critical || cb.Severity != condition.Critical {
		t.Fatalf("all failing should be critical: %s %s", ca.Severity, cb.Severity)
	}
	failing["b"] = false // b recovers ⇒ b clears, a drops back to warning
	_ = r.RefreshDue(context.Background())
	if tr.IsOpen(kb) {
		t.Fatal("recovered account not cleared")
	}
	if ca, _ = tr.Get(ka); ca.Severity != condition.Warning {
		t.Fatalf("a should be warning once not all fail: %s", ca.Severity)
	}
}
```

Add a tiny helper in the test file: `func jsonDecode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }` (import `encoding/json`). If `internal/jam` already has a `PoolStore` fake in `refresher_test.go`, reuse it instead of `memPoolStore` (grep `func (.*) Accounts() (\[\]PoolAccount` in `internal/jam/*_test.go` first).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/ -run 'WatchedResolver|RefresherRaisesPool'`
Expected: FAIL — undefined: `NewWatchedResolver`, `CredFailThreshold`, `RefresherOptions.Conditions`

- [ ] **Step 3: Implement `credwatch.go`**

```go
package jam

import "github.com/aethons-tools/cove/internal/jam/condition"

// CredFailThreshold is how many consecutive resolve failures of one
// credential raise cred.unavailable:<name>.
const CredFailThreshold = 3

// WatchedResolver wraps the broker's credential resolver and turns repeated
// resolve failures into a cred.unavailable:<name> condition (critical),
// cleared by the next success. The condition names the credential only — the
// resolver's error text may carry secret material and is never copied.
type WatchedResolver struct {
	base CredResolver
	t    *condition.Tracker
	fix  func(name string) string
}

// NewWatchedResolver wraps base. fix returns the remedy for a credential; nil means none.
func NewWatchedResolver(base CredResolver, t *condition.Tracker, fix func(name string) string) *WatchedResolver {
	if fix == nil {
		fix = func(string) string { return "" }
	}
	return &WatchedResolver{base: base, t: t, fix: fix}
}

func (w *WatchedResolver) Resolve(name string) (string, error) {
	v, err := w.base.Resolve(name)
	w.note(name, err)
	return v, err
}

// ResolveFor delegates to the base's identity-aware resolve when it has one.
func (w *WatchedResolver) ResolveFor(name, identityHash string) (string, error) {
	var v string
	var err error
	if ir, ok := w.base.(IdentityCredResolver); ok {
		v, err = ir.ResolveFor(name, identityHash)
	} else {
		v, err = w.base.Resolve(name)
	}
	w.note(name, err)
	return v, err
}

// PoolCredential delegates to the base when it is a pool resolver.
func (w *WatchedResolver) PoolCredential(name string) bool {
	if pr, ok := w.base.(PoolCredResolver); ok {
		return pr.PoolCredential(name)
	}
	return false
}

func (w *WatchedResolver) note(name string, err error) {
	key := condition.Key("cred.unavailable", name)
	if err == nil {
		w.t.Ok(key)
		return
	}
	w.t.Fail(condition.Condition{
		Key:      key,
		Severity: condition.Critical,
		Summary:  "credential " + name + " cannot be resolved; brokered requests using it fail (502)",
		Fix:      w.fix(name),
	}, CredFailThreshold)
}
```

- [ ] **Step 4: Modify `refresher.go`**

Add the import `"github.com/aethons-tools/cove/internal/jam/condition"`, a field to `RefresherOptions`:

```go
	// Conditions, when set, receives pool.account.refresh:<account> conditions
	// (warning; critical while every account is failing). nil = none.
	Conditions *condition.Tracker
```

and replace `RefreshDue` with:

```go
// PoolFailThreshold is how many consecutive failed refresh passes of one
// account raise pool.account.refresh:<account>.
const PoolFailThreshold = 2

// RefreshDue refreshes every account whose ExpiresAt is within Margin of Now.
// Per-account failures are logged and do not stop the others; with
// Conditions set they raise pool.account.refresh:<account> (warning, or
// critical while every account is failing) and a success clears it. Never
// logs tokens.
func (r *Refresher) RefreshDue(ctx context.Context) error {
	accts, err := r.store.Accounts()
	if err != nil {
		return err
	}
	deadline := r.opt.Now().Add(r.opt.Margin)
	t := r.opt.Conditions
	for _, a := range accts {
		if a.ExpiresAt.After(deadline) {
			continue
		}
		key := condition.Key("pool.account.refresh", a.Name)
		if err := r.refreshOne(ctx, a); err != nil {
			r.opt.Log.Warn("pool token refresh failed", "account", a.Name, "error", err.Error())
			sev := condition.Warning
			if cur, ok := t.Get(key); ok {
				sev = cur.Severity // keep it; the all-failing pass below decides
			}
			t.Fail(condition.Condition{
				Key: key, Severity: sev,
				Summary: "pool account " + a.Name + " cannot refresh its token",
				Detail:  err.Error(), // OAuth error code + description only (see refreshOne)
				Fix:     "re-seed it: at-jam pool add --name " + a.Name + " --from-file <credentials.json>",
			}, PoolFailThreshold)
			continue
		}
		t.Ok(key)
	}
	// Every account failing means the pool cannot serve: critical; otherwise warning.
	if t != nil && len(accts) > 0 {
		all := true
		for _, a := range accts {
			if !t.IsOpen(condition.Key("pool.account.refresh", a.Name)) {
				all = false
				break
			}
		}
		want := condition.Warning
		if all {
			want = condition.Critical
		}
		for _, a := range accts {
			if c, ok := t.Get(condition.Key("pool.account.refresh", a.Name)); ok && c.Severity != want {
				c.Severity = want
				t.Raise(c)
			}
		}
	}
	return nil
}
```

(`t` may be nil — every Tracker method is nil-safe, and the escalation block is skipped.)

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/jam/ -count=1`
Expected: PASS (whole package, so existing refresher/broker tests still pass)

- [ ] **Step 6: Commit**

```bash
git add internal/jam/credwatch.go internal/jam/credwatch_test.go internal/jam/refresher.go internal/jam/refresher_conditions_test.go
git commit -m "feat(jam): cred.unavailable and pool.account.refresh condition producers"
```

---

### Task 5: Serve wiring — `metrics:` config, tracker, `/metrics`, `/admin/attention`

**Files:**
- Modify: `cmd/at-jam/config.go` (serveConfig + validation + fix hints), `cmd/at-jam/main.go` (serve wiring), `cmd/at-jam/mux.go` (`withMetrics`)
- Test: `cmd/at-jam/config_test.go`, `cmd/at-jam/mux_test.go`

**Interfaces:**
- Consumes: Tasks 1–4 (`condition.New/Load/Run/MetricsHandler/AdminHandler/Gauge`, `conditionpg.New`, `jam.NewWatchedResolver`, `RefresherOptions.Conditions`).
- Produces:
  - `type metricsConfig struct { TokenCred string \`yaml:"token-cred"\`; AlertmanagerURL string \`yaml:"alertmanager-url"\` }`; `serveConfig.Metrics *metricsConfig \`yaml:"metrics"\``
  - `func (c serveConfig) validateMetrics() error`
  - `func (c serveConfig) credFixHint(name string) string`
  - `func withMetrics(next, metrics http.Handler) http.Handler` (nil metrics → next)
  - serve passes `adminui.WithConditions(conds, alertmanagerURL)` (Task 6 defines it — add that argument in Task 6, not here).

- [ ] **Step 1: Write the failing tests**

```go
// config_test.go — append
func TestValidateMetrics(t *testing.T) {
	ok, err := parseServeConfig([]byte("credentials:\n  prom-scrape:\nmetrics:\n  token-cred: prom-scrape\n  alertmanager-url: http://localhost:9093\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.validateMetrics(); err != nil {
		t.Fatalf("valid metrics refused: %v", err)
	}
	for name, yml := range map[string]string{
		"no token-cred":    "metrics: {}\n",
		"undemanded cred":  "metrics:\n  token-cred: nope\n",
		"bad alertmanager": "credentials:\n  t:\nmetrics:\n  token-cred: t\n  alertmanager-url: ftp://x\n",
	} {
		c, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := c.validateMetrics(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	none, _ := parseServeConfig([]byte("listen: :443\n"))
	if err := none.validateMetrics(); err != nil {
		t.Fatalf("absent metrics must validate: %v", err)
	}
}

func TestCredFixHint(t *testing.T) {
	c, _ := parseServeConfig([]byte("credentials:\n  vertex-gcp: { exchange: gcp }\n  git-pat:\npool: { store: /tmp/p.json, cred-name: anthropic-sub }\n"))
	if h := c.credFixHint("vertex-gcp"); !strings.Contains(h, "gcloud auth application-default login") {
		t.Errorf("gcp hint = %q", h)
	}
	if h := c.credFixHint("anthropic-sub"); !strings.Contains(h, "at-jam pool list") {
		t.Errorf("pool hint = %q", h)
	}
	if h := c.credFixHint("git-pat"); !strings.Contains(h, "git-pat") || !strings.Contains(h, "credentials") {
		t.Errorf("default hint = %q", h)
	}
}
```

```go
// mux_test.go — append (or create if absent; package main)
func TestWithMetricsRoutesOnlyExactPath(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	metrics := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(298) })
	h := withMetrics(next, metrics)
	for path, want := range map[string]int{"/metrics": 298, "/metrics/x": 299, "/anthropic/v1/messages": 299} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s → %d, want %d", path, rec.Code, want)
		}
	}
	if withMetrics(next, nil) == nil {
		t.Fatal("nil metrics must return next")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-jam/ -run 'ValidateMetrics|CredFixHint|WithMetrics'`
Expected: FAIL — undefined: `validateMetrics`, `credFixHint`, `withMetrics`

- [ ] **Step 3: Implement config (`cmd/at-jam/config.go`)**

Add to `serveConfig` (next to `Pool`):

```go
	// Metrics, when set, serves the operator-attention exposition at /metrics
	// on the broker listener, gated on a scrape token (a demanded credential).
	// See docs/usage/jam/monitoring.md.
	Metrics *metricsConfig `yaml:"metrics"`
```

and below `poolConfig`:

```go
// metricsConfig enables /metrics. TokenCred names a demanded credential (the
// scrape token's value comes from the credentials file). AlertmanagerURL, when
// set, is linked from the admin UI's Health tab.
type metricsConfig struct {
	TokenCred       string `yaml:"token-cred"`
	AlertmanagerURL string `yaml:"alertmanager-url"`
}

// validateMetrics checks a set metrics block: token-cred is required and
// demanded; alertmanager-url, if set, is an http(s) URL.
func (c serveConfig) validateMetrics() error {
	m := c.Metrics
	if m == nil {
		return nil
	}
	if m.TokenCred == "" {
		return fmt.Errorf("metrics.token-cred is required")
	}
	if _, ok := c.Credentials[m.TokenCred]; !ok {
		return fmt.Errorf("metrics.token-cred %q is not a demanded credential", m.TokenCred)
	}
	if m.AlertmanagerURL != "" {
		u, err := url.Parse(m.AlertmanagerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("metrics.alertmanager-url %q must be an http(s) URL", m.AlertmanagerURL)
		}
	}
	return nil
}

// credFixHint is the remedy a cred.unavailable condition shows for name.
func (c serveConfig) credFixHint(name string) string {
	switch {
	case slices.Contains(c.gcpCredentials(), name):
		return "if it supplies a user ADC: run `gcloud auth application-default login` on the Jam host (Jam re-reads it within 10s); otherwise replace the Google credentials JSON for " + name
	case c.Pool != nil && name == c.Pool.CredName:
		return "check the pool's accounts: `at-jam pool list --store " + c.Pool.Store + "`"
	}
	return "check the credentials file entry " + name + " (" + c.credentialsFilePath() + ")"
}
```

(Add `net/url` to imports if missing; `slices` is already imported.)

- [ ] **Step 4: Implement `withMetrics` (`cmd/at-jam/mux.go`)**

```go
// withMetrics serves exactly /metrics with metrics (when non-nil) ahead of next.
func withMetrics(next, metrics http.Handler) http.Handler {
	if metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			metrics.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 5: Run the unit tests to verify pass**

Run: `go test ./cmd/at-jam/ -run 'ValidateMetrics|CredFixHint|WithMetrics' -count=1`
Expected: PASS

- [ ] **Step 6: Wire serve (`cmd/at-jam/main.go`)**

1. Next to `cfg.validateWake()` (≈ line 1603), add:

```go
	if err := cfg.validateMetrics(); err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 2
	}
```

(match the exit code the neighbouring validations use).

2. Right after `pgPool = ps.Pool()` is set and the store is ready (before the credential resolver is built, ≈ line 1770 `var base jam.CredResolver = …`), add:

```go
	// Operator-attention conditions (docs/usage/jam/monitoring.md): persisted
	// alongside the store; a load failure starts empty rather than failing serve.
	condStore, err := conditionpg.New(context.Background(), pgPool, log)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam: conditions store:", err)
		return 1
	}
	conds := condition.New(condition.Options{Persister: condStore, Log: log})
	if err := conds.Load(context.Background()); err != nil {
		log.Warn("conditions not loaded; starting empty", "reason", err.Error())
	}
	go conds.Run(context.Background(), 5*time.Second)
```

3. After the pool block that sets `creds` (just before `broker := jam.NewBroker(st, creds, log)`), add:

```go
	creds = jam.NewWatchedResolver(creds, conds, cfg.credFixHint)
```

and pass `Conditions: conds,` in the `jam.RefresherOptions{…}` literal.

4. Where `httpHandler := coveHTTPHandler(…)` is built (≈ line 1916), follow it with:

```go
	if cfg.Metrics != nil {
		tok, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specs[cfg.Metrics.TokenCred]})
		if err != nil {
			fmt.Fprintln(stderr, "at-jam: metrics.token-cred:", err)
			return 1
		}
		httpHandler = withMetrics(httpHandler, condition.MetricsHandler(conds, tok[cfg.Metrics.TokenCred], func() []condition.Gauge {
			return []condition.Gauge{{Name: "jam_studios", Help: "Studios Jam knows of.", Value: float64(len(jam.CoveSummaries(st, sup)))}}
		}))
		log.Info("Jam metrics: mounted", "path", "/metrics") // never the token
	}
```

(If `specs` is not in scope at that point, it is the map returned by `planCredentials(cfg)` earlier in serve — keep a reference.)

5. In the `jam.NewAdminHandler(…)` options (≈ line 2259) add:

```go
			jam.WithAdminRoute("GET /admin/attention", condition.AdminHandler(conds)),
```

6. Imports: `github.com/aethons-tools/cove/internal/jam/condition` and `.../condition/conditionpg`.

- [ ] **Step 7: Build and run the package tests**

Run: `go build ./... && go test ./cmd/at-jam/ -count=1 && go vet ./cmd/at-jam/`
Expected: PASS

- [ ] **Step 8: Docs — `docs/usage/jam/serve.md`**

Add a row to the serve-config key table (after `pool`):

```markdown
| `metrics` | no | Serves the operator-attention exposition at `/metrics` on the broker listener: `token-cred` (required) names a demanded credential holding the Prometheus scrape token; `alertmanager-url` (optional) is linked from the admin UI's Health tab. Unset ⇒ no `/metrics`. See [monitoring.md](monitoring.md). |
```

Bump its `updated:` date.

- [ ] **Step 9: Commit**

```bash
git add cmd/at-jam/ docs/usage/jam/serve.md
git commit -m "feat(at-jam): serve conditions — metrics: config, /metrics, /admin/attention, producers wired"
```

---

### Task 6: Admin UI — conditions in the attention system + Health tab

**Files:**
- Create: `internal/jam/adminui/health.go`, `internal/jam/adminui/templates/health.html`
- Modify: `internal/jam/adminui/attention.go` (new kind + `conditionItems`), `internal/jam/adminui/nav.go` (`jamTabs`, `frameSource.conds`, `frameSource.items`, `frameFor`), `internal/jam/adminui/adminui.go` (option, page, call sites), `internal/jam/adminui/templates/attention.html` (chip label)
- Modify: `cmd/at-jam/main.go` (pass `adminui.WithConditions`)
- Test: `internal/jam/adminui/health_test.go`

**Interfaces:**
- Consumes: `condition.Tracker` (`Open`, `Resolved`), `condition.Condition`.
- Produces: `func WithConditions(t *condition.Tracker, alertmanagerURL string) Option`; page `"health"` at `GET /ui/health`; attention kind `attnAlert = "alert"`.

- [ ] **Step 1: Write the failing tests**

```go
// health_test.go
package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

func TestHealthTabAndRailBadge(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "cred.unavailable:vertex-gcp", Severity: condition.Critical, Summary: "credential vertex-gcp cannot be resolved", Fix: "gcloud auth application-default login"})
	tr.Raise(condition.Condition{Key: "image.stale:cove-ic", Severity: condition.Info, Summary: "kit cove-ic image is stale"})
	tr.Raise(condition.Condition{Key: "pool.account.refresh:a", Severity: condition.Warning, Summary: "pool account a cannot refresh"})
	tr.Clear("pool.account.refresh:a")
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, credAny, nil, adminui.WithConditions(tr, "http://localhost:9093"))

	body := get(t, h, "/ui/health").Body.String()
	for _, want := range []string{
		"credential vertex-gcp cannot be resolved", "gcloud auth application-default login", // open + fix
		"kit cove-ic image is stale",              // info shown on Health
		"pool account a cannot refresh",           // resolved history
		`href="/ui/health"`,                       // the Health tab
		"http://localhost:9093/#/silences",        // silences link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("health page missing %q", want)
		}
	}
	dash := get(t, h, "/ui/").Body.String()
	if !strings.Contains(dash, "credential vertex-gcp cannot be resolved") {
		t.Error("critical condition not in the dashboard's Needs attention card")
	}
	if strings.Contains(dash, "kit cove-ic image is stale") {
		t.Error("info condition must not be an attention item")
	}
}

func TestHealthWithoutConditions(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, credAny, nil)
	rec := get(t, h, "/ui/health")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Nothing needs attention") {
		t.Fatalf("health without a tracker: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestHealthPollFragment(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "k:x", Severity: condition.Warning, Summary: "polled"})
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, credAny, nil, adminui.WithConditions(tr, ""))
	rec := getHX(t, h, "/ui/health")
	if !strings.Contains(rec.Body.String(), "polled") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("poll should return only the fragment:\n%s", rec.Body.String())
	}
}
```

Add `getHX` to the test helpers file if absent (same as `get` but sets `HX-Request: true`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/jam/adminui/ -run Health`
Expected: FAIL — undefined: `adminui.WithConditions`

- [ ] **Step 3: Implement**

`adminui.go` — option + page + call sites:

```go
// WithConditions shows operator-attention conditions: open critical/warning
// ones as Jam attention items, and all of them on the Health tab, which links
// alertmanagerURL's silences when set.
func WithConditions(t *condition.Tracker, alertmanagerURL string) Option {
	return func(o *options) { o.conds, o.alertmanagerURL = t, strings.TrimRight(alertmanagerURL, "/") }
}
```

Add `conds *condition.Tracker; alertmanagerURL string` to `options`; in `Handler` set `src := frameSource{store: store, img: sup, name: o.displayName, conds: o.conds}`; register `registerHealth(mux, o.conds, o.alertmanagerURL)`; add `"health": mustParse(jamPage("health"), "health.html"),` to `pages`. Replace both `attention(store, sup)` (rail handler) and `attention(src.store, src.img)` (renderFragment, ≈ line 323) with `src.items()`.

`nav.go`:

```go
var jamTabs = []tabDef{
	{"dashboard", "Dashboard", "/ui/"},
	{"agents", "Agents", "/ui/agents"},
	{"users", "Users", "/ui/users"},
	{"specs", "Specs", "/ui/specs"},
	{"intercom", "Intercom", "/ui/intercom"},
	{"health", "Health", "/ui/health"},
}
```

add `conds *condition.Tracker` to `frameSource`, and

```go
// items is every attention item: Jam's open operator conditions first, then
// attention()'s.
func (s frameSource) items() []attnItem {
	return append(conditionItems(s.conds.Open()), attention(s.store, s.img)...)
}
```

and in `frameFor` replace `items := attention(src.store, src.img)` with `items := src.items()`.

`attention.go`:

```go
	attnAlert  attnKind = "alert"  // an operator condition at warning severity
```

(in the const block), extend `badgeOf` with an `alert` counter labelled `"warning"` in the title list, and add:

```go
// conditionItems turns open operator conditions into Jam-scope attention
// items on the Health tab: critical is broken (red), warning is alert
// (amber); info is never an item.
func conditionItems(cs []condition.Condition) []attnItem {
	var out []attnItem
	for _, c := range cs {
		var k attnKind
		switch c.Severity {
		case condition.Critical:
			k = attnBroken
		case condition.Warning:
			k = attnAlert
		default:
			continue
		}
		out = append(out, attnItem{Kind: k, Tab: "health", Subject: "cond:" + c.Key, Href: "/ui/health", Why: c.Summary})
	}
	return out
}
```

`templates/attention.html` chip: `{{if eq .Kind "broken"}}broken{{else if eq .Kind "stale"}}out of date{{else if eq .Kind "alert"}}warning{{else}}config{{end}}`.

`health.go`:

```go
package adminui

import (
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

type healthRow struct {
	condition.Condition
	Age, Lasted string
}

func healthData(t *condition.Tracker, amURL string) map[string]any {
	now := time.Now()
	var open, resolved []healthRow
	for _, c := range t.Open() {
		open = append(open, healthRow{Condition: c, Age: fmtDur(now.Sub(c.Since))})
	}
	for _, c := range t.Resolved() {
		resolved = append(resolved, healthRow{Condition: c, Age: fmtDur(now.Sub(*c.ResolvedAt)), Lasted: fmtDur(c.ResolvedAt.Sub(c.Since))})
	}
	silences := ""
	if amURL != "" {
		silences = amURL + "/#/silences"
	}
	return map[string]any{"Title": "Health", "Open": open, "Resolved": resolved, "Silences": silences}
}

// registerHealth serves the Jam Health tab; an htmx poll gets the body fragment.
func registerHealth(mux *http.ServeMux, t *condition.Tracker, amURL string) {
	mux.HandleFunc("GET /ui/health", func(w http.ResponseWriter, r *http.Request) {
		data := healthData(t, amURL)
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, r, "health", "health-body", data)
			return
		}
		render(w, r, "health", data)
	})
}
```

`templates/health.html`:

```html
{{define "content"}}
<div class="page-head"><h1>Health</h1><span class="sub">Problems Jam has detected in itself — each with how to fix it. Pushed to operators by Alertmanager when configured.</span>{{with .Silences}} <a href="{{.}}">Silences ↗</a>{{end}}</div>
{{template "health-body" .}}
{{end}}

{{define "health-body"}}
<div id="health-body" hx-get="/ui/health" hx-trigger="every 10s" hx-swap="outerHTML">
<section class="card full">
  <header><h2>Open</h2>{{with .Open}}<span class="sub">{{len .}}</span>{{end}}</header>
  <div class="body">
  {{range .Open}}
    <div class="health-row" id="cond-{{.Key}}">
      <span class="chip sev-{{.Severity}}">{{.Severity}}</span> <strong>{{.Summary}}</strong> <span class="unset">for {{.Age}}</span>
      {{with .Fix}}<pre class="fix">{{.}}</pre>{{end}}
      {{with .Detail}}<details><summary>Detail</summary><pre>{{.}}</pre></details>{{end}}
      <div class="unset mono">{{.Key}}</div>
    </div>
  {{else}}<span class="unset">Nothing needs attention.</span>{{end}}
  </div>
</section>
<section class="card full">
  <header><h2>Resolved (7 days)</h2></header>
  <div class="body">
  {{range .Resolved}}<div class="health-row"><span class="chip">{{.Severity}}</span> {{.Summary}} <span class="unset">— lasted {{.Lasted}}, resolved {{.Age}} ago</span></div>
  {{else}}<span class="unset">None.</span>{{end}}
  </div>
</section>
</div>
<style>
  .health-row{padding:6px 0;border-bottom:1px solid var(--border)}
  .health-row pre.fix{margin:6px 0;padding:6px 8px;background:var(--panel, #f6f6f6);user-select:all;white-space:pre-wrap}
  .sev-critical{background:#c62828;color:#fff}.sev-warning{background:#f9a825}.sev-info{background:#90a4ae;color:#fff}
</style>
{{end}}
```

`cmd/at-jam/main.go`: append `adminui.WithConditions(conds, alertmanagerURL(cfg))` to the `adminui.Handler(…)` options, with `func alertmanagerURL(c serveConfig) string { if c.Metrics == nil { return "" }; return c.Metrics.AlertmanagerURL }` in `config.go`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/jam/adminui/ ./cmd/at-jam/ -count=1`
Expected: PASS (existing adminui tests too — the extra Jam tab must not break tab assertions; if a test pins the exact tab list, update it to include Health)

- [ ] **Step 5: Docs — `docs/usage/jam/ui.md`**

Add one line where it lists the Jam tabs: "**Health** — operator-attention conditions (open with their fix, resolved in the last 7 days); open critical/warning ones also count in the rail badge — see [monitoring.md](monitoring.md)." Bump `updated:`.

- [ ] **Step 6: Commit**

```bash
git add internal/jam/adminui/ cmd/at-jam/ docs/usage/jam/ui.md
git commit -m "feat(adminui): operator conditions in the attention rail + Jam Health tab"
```

---

### Task 7: `at-jam attention list`

**Files:**
- Modify: `internal/jam/adminclient/adminclient.go` (`ListConditions`), `cmd/at-jam/main.go` (verb), create `cmd/at-jam/attention.go`
- Test: `cmd/at-jam/attention_test.go`

**Interfaces:**
- Consumes: `GET /admin/attention` (Task 5), `condition.Condition`.
- Produces: `func (c *Client) ListConditions(state string) ([]condition.Condition, error)`; verb `attention list [--all]`.

- [ ] **Step 1: Write the failing test**

```go
// attention_test.go
package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

func TestAttentionList(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "cred.unavailable:vertex-gcp", Severity: condition.Critical, Summary: "credential vertex-gcp cannot be resolved", Fix: "gcloud auth application-default login"})
	tr.Raise(condition.Condition{Key: "k:old", Severity: condition.Warning, Summary: "old one"})
	tr.Clear("k:old")
	h := jam.NewAdminHandler(jam.NewMemStore(), nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, jam.WithAdminRoute("GET /admin/attention", condition.AdminHandler(tr)))
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	if code := run([]string{"attention", "list", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "critical") || !strings.Contains(got, "cred.unavailable:vertex-gcp") || !strings.Contains(got, "fix: gcloud auth application-default login") {
		t.Fatalf("list output:\n%s", got)
	}
	if strings.Contains(got, "old one") {
		t.Fatal("resolved shown without --all")
	}
	out.Reset()
	_ = run([]string{"attention", "list", "--all", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb)
	if !strings.Contains(out.String(), "old one") || !strings.Contains(out.String(), "resolved") {
		t.Fatalf("--all output:\n%s", out.String())
	}
	_ = http.StatusOK
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-jam/ -run TestAttentionList`
Expected: FAIL — unknown command "attention"

- [ ] **Step 3: Implement**

`adminclient.go`:

```go
// ListConditions lists operator-attention conditions (state: open|resolved|all).
func (c *Client) ListConditions(state string) ([]condition.Condition, error) {
	var out []condition.Condition
	err := c.do("GET", "/admin/attention?state="+url.QueryEscape(state), nil, &out)
	return out, err
}
```

`cmd/at-jam/attention.go` — mirror `cmdDestination`'s flag/app/admin-url/token handling exactly (copy its first ~25 lines: flag set, `--app`, `--admin-url`, `--token`, `cli.ParseFlags`, `validateApp`, `adminclient.New(...)`):

```go
// cmdAttention lists operator-attention conditions via the admin API.
func cmdAttention(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "at-jam attention: expected list")
		return 2
	}
	fs := flag.NewFlagSet("attention list", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token for an OIDC-gated admin API (env: AT_JAM_ADMIN_TOKEN)")
	all := fs.Bool("all", false, "include conditions resolved in the last 7 days")
	if _, code, ok := cli.ParseFlags(fs, args[1:], stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam attention:", err)
		return 2
	}
	c := adminclient.New(firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL), resolveToken(*app, *token, stderr))
	state := "open"
	if *all {
		state = "all"
	}
	cs, err := c.ListConditions(state)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	if len(cs) == 0 {
		fmt.Fprintln(stdout, "nothing needs attention")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SEV\tKEY\tSINCE\tSUMMARY")
	for _, k := range cs {
		sev := string(k.Severity)
		if !k.IsOpen() {
			sev = "resolved"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", sev, k.Key, k.Since.Local().Format("2006-01-02 15:04"), k.Summary)
		if k.Fix != "" && k.IsOpen() {
			fmt.Fprintf(tw, "\t\t\tfix: %s\n", k.Fix)
		}
	}
	_ = tw.Flush()
	return 0
}
```

Register in the command table next to `destination`:

```go
			{Name: "attention", Brief: "list operator-attention conditions (list [--all]) via the admin API", Run: cmdAttention},
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./cmd/at-jam/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/jam/adminclient/adminclient.go cmd/at-jam/
git commit -m "feat(at-jam): attention list — operator conditions over the admin API"
```

---

### Task 8: Monitoring bundle, docs, spec amendment

**Files:**
- Create: `deploy/monitoring/compose.yml`, `deploy/monitoring/prometheus.yml`, `deploy/monitoring/jam-rules.yml`, `deploy/monitoring/alertmanager.yml`, `deploy/monitoring/README.md` (one paragraph pointing at the doc), `docs/usage/jam/monitoring.md`
- Modify: `justfile`, `.gitignore`, `docs/usage/jam/INDEX.md`, `docs/superpowers/specs/2026-10-06-operator-attention-design.md` (§4)

**Interfaces:**
- Consumes: the `/metrics` series names from Task 3 (`jam_up`, `jam_attention_condition{key,kind,severity,summary,fix}`, `jam_attention_open{severity}`).
- Produces: `just monitoring-up <jam-host>` / `just monitoring-down`.

- [ ] **Step 1: Write the bundle**

`deploy/monitoring/compose.yml`:

```yaml
# Local Prometheus + Alertmanager for a Jam's operator-attention conditions.
# Run via `just monitoring-up <jam-host>`; secrets live in .local/ (gitignored).
services:
  prometheus:
    image: prom/prometheus:v3.5.0
    command: ["--config.file=/etc/prometheus/prometheus.yml", "--storage.tsdb.retention.time=15d"]
    volumes:
      - ./.local/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - ./jam-rules.yml:/etc/prometheus/jam-rules.yml:ro
      - ./.local:/etc/prometheus/secrets:ro
    extra_hosts: ["${JAM_HOST}:host-gateway"]
    ports: ["127.0.0.1:9090:9090"]
  alertmanager:
    image: prom/alertmanager:v0.28.1
    command: ["--config.file=/etc/alertmanager/alertmanager.yml"]
    volumes:
      - ./alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro
      - ./.local:/etc/alertmanager/secrets:ro
    ports: ["127.0.0.1:9093:9093"]
```

`deploy/monitoring/prometheus.yml` (template; `@JAM_HOST@` is substituted by the recipe):

```yaml
global:
  scrape_interval: 30s
  evaluation_interval: 30s
rule_files: [/etc/prometheus/jam-rules.yml]
alerting:
  alertmanagers:
    - static_configs: [{targets: ["alertmanager:9093"]}]
scrape_configs:
  - job_name: jam
    scheme: https
    metrics_path: /metrics
    authorization:
      credentials_file: /etc/prometheus/secrets/scrape-token
    tls_config:
      ca_file: /etc/prometheus/secrets/jam-ca.pem
      server_name: "@JAM_HOST@"
    static_configs: [{targets: ["@JAM_HOST@:443"]}]
```

`deploy/monitoring/jam-rules.yml`:

```yaml
groups:
  - name: jam
    rules:
      - alert: JamAttention
        expr: jam_attention_condition == 1
        annotations:
          summary: "{{ $labels.summary }}"
          fix: "{{ $labels.fix }}"
      - alert: JamDown
        expr: up{job="jam"} == 0
        for: 2m
        labels:
          severity: critical
          key: jam.down
        annotations:
          summary: "Jam is unreachable from Prometheus"
          fix: "check that at-jam serve is running and its broker listener is reachable"
```

`deploy/monitoring/alertmanager.yml`:

```yaml
route:
  receiver: discord
  group_by: [alertname, key]
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 12h
  routes:
    - matchers: ['severity="info"']
      receiver: drop
    - matchers: ['severity="critical"']
      repeat_interval: 1h
receivers:
  - name: discord
    discord_configs:
      - webhook_url_file: /etc/alertmanager/secrets/discord-webhook
        send_resolved: true
        title: '{{ if eq .Status "firing" }}🔴{{ else }}✅{{ end }} {{ .CommonLabels.severity }}: {{ .CommonAnnotations.summary }}'
        message: '{{ range .Alerts }}{{ .Annotations.summary }}{{ if .Annotations.fix }}
fix: {{ .Annotations.fix }}{{ end }}
{{ end }}'
  - name: drop
```

`justfile`:

```make
# operator-attention monitoring: local Prometheus + Alertmanager scraping <jam-host>
# (put scrape-token, jam-ca.pem and discord-webhook in deploy/monitoring/.local/ first)
monitoring-up jam_host:
    mkdir -p deploy/monitoring/.local
    for f in scrape-token jam-ca.pem discord-webhook; do test -s deploy/monitoring/.local/$f || { echo "missing deploy/monitoring/.local/$f (see docs/usage/jam/monitoring.md)"; exit 1; }; done
    sed 's/@JAM_HOST@/{{jam_host}}/g' deploy/monitoring/prometheus.yml > deploy/monitoring/.local/prometheus.yml
    JAM_HOST={{jam_host}} docker compose -f deploy/monitoring/compose.yml up -d

monitoring-down:
    JAM_HOST=unused docker compose -f deploy/monitoring/compose.yml down
```

`.gitignore`: add `deploy/monitoring/.local/`.

- [ ] **Step 2: Validate the bundle (where Docker is available)**

```bash
docker run --rm -v "$PWD/deploy/monitoring:/m" --entrypoint promtool prom/prometheus:v3.5.0 check rules /m/jam-rules.yml
mkdir -p /tmp/am && echo https://discord.example/x > /tmp/am/discord-webhook
docker run --rm -v "$PWD/deploy/monitoring/alertmanager.yml:/a.yml" -v /tmp/am:/etc/alertmanager/secrets --entrypoint amtool prom/alertmanager:v0.28.1 check-config /a.yml
```

Expected: `SUCCESS: 2 rules found`; `Checking '/a.yml'  SUCCESS`. (No Docker in the agent sandbox — record this as a manual check in the PR if it can't run.)

- [ ] **Step 3: Write `docs/usage/jam/monitoring.md`**

```markdown
---
summary: Operator attention — the conditions Jam raises about itself (lapsed credentials, failing pool refreshes, …), where they show (admin UI Health tab + rail badge, `at-jam attention list`, `/metrics`), and the shipped Prometheus + Alertmanager bundle that pushes them to Discord.
read_when: You want to be alerted when Jam needs a human, are setting up `metrics:` or the deploy/monitoring bundle, see a condition on the Health tab and want to know what it means, or a Discord alert from Jam arrived.
owns: the condition model (keys, severities, lifecycle), the v1 condition kinds, the `/metrics` exposition and its scrape token, the Health tab, `at-jam attention list`, and the deploy/monitoring bundle
prereqs: serve.md for the serve config and credentials
tier: leaf
updated: 2026-10-07
---

# Operator attention

Jam turns problems it detects in itself into **conditions** — a key, a
severity, a one-line summary and a **fix** — and shows them in three places:
the admin UI (rail badge, "Needs attention", **Health** tab), `at-jam attention
list`, and `/metrics` for Prometheus. Delivery (Discord, reminders, ack) is
Alertmanager's job, via the bundle in `deploy/monitoring/`.

## Severities and lifecycle

| Severity | Meaning | Pushed |
|---|---|---|
| `critical` | agents are failing now | yes, re-sent hourly |
| `warning` | degraded, or will fail | yes, re-sent every 12h |
| `info` | shown on Health only | never |

A condition is **open** until its producer sees success, then **resolved** (kept
7 days on Health). The same problem recurring later opens a new occurrence.
Conditions survive a Jam restart and keep their original start time.
**Ack** = an Alertmanager silence (its UI, or the Health tab's "Silences" link).

## v1 conditions

| Key | Severity | Raised when | Cleared when |
|---|---|---|---|
| `cred.unavailable:<cred>` | critical | 3 consecutive failures resolving a brokered credential | the next successful resolve |
| `pool.account.refresh:<account>` | warning; critical while every account fails | 2 consecutive failed refresh passes | the next successful refresh |

Each carries its fix (e.g. `gcloud auth application-default login` for a
gcp-exchange ADC). Conditions name things only — never secret values.

## `/metrics`

Enable with the serve config `metrics: { token-cred: <name> }` (the name must be
under `credentials:`; its value — the scrape token — comes from the credentials
file). Jam then serves `/metrics` on the **broker listener** (the TLS port
studios use), requiring `Authorization: Bearer <scrape token>`; studio identity
tokens are refused. Series: `jam_up`, `jam_attention_condition{key,kind,severity,summary,fix}`
(one per open condition), `jam_attention_open{severity}`, `jam_studios`.
Unset ⇒ no `/metrics`.

## The local bundle

1. Put three files in `deploy/monitoring/.local/` (gitignored): `scrape-token`
   (the token's value), `jam-ca.pem` (the CA that signed Jam's `tls:` cert) and
   `discord-webhook` (a Discord channel webhook URL).
2. `just monitoring-up <jam-host>` — starts Prometheus (`127.0.0.1:9090`) and
   Alertmanager (`127.0.0.1:9093`), reaching Jam the way studios do
   (`<jam-host>` → `host-gateway`).
3. Set `metrics.alertmanager-url: http://localhost:9093` to link silences from Health.

The bundle also alerts `JamDown` (critical) when Prometheus cannot scrape Jam
for 2 minutes. `just monitoring-down` stops it. In other environments, point
your own Prometheus at the same endpoint and load `deploy/monitoring/jam-rules.yml`.
```

Add to `docs/usage/jam/INDEX.md` (after the `pool.md` row):

```markdown
| [monitoring.md](monitoring.md) | You want to be alerted when Jam needs a human, are setting up `metrics:` or the deploy/monitoring bundle, or a condition on the Health tab / a Discord alert from Jam needs explaining. |
```

- [ ] **Step 4: Amend spec §4**

In `docs/superpowers/specs/2026-10-06-operator-attention-design.md` replace the `**Banner**` bullet with:

```markdown
- **Rail badge instead of a banner** *(amended 2026-10-07)*: open critical
  and warning conditions are Jam-scope items in the admin UI's existing
  attention system (#393) — red for critical, amber for warning — so they count
  in the rail and Health-tab badges and the dashboard's "Needs attention"
  card. Info conditions appear on the Health tab only.
```

and change `**`/ui/health`**` to `**`/ui/health` (Jam "Health" tab)**`.

- [ ] **Step 5: Docs audit + full suite**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -3` (no new errors vs. main), then `just test && just lint`
Expected: PASS; audit error count unchanged or lower.

- [ ] **Step 6: Commit**

```bash
git add deploy/monitoring/ justfile .gitignore docs/
git commit -m "feat(monitoring): Prometheus + Alertmanager bundle and operator-attention docs"
```

---

### Task 9: Slice-1 acceptance (manual, on the Jam host)

Not code — the acceptance check from spec §6. Record the result in the PR description.

- [ ] **Step 1:** Add to the serve config `credentials: { prom-scrape: }`, `metrics: { token-cred: prom-scrape, alertmanager-url: http://localhost:9093 }`, and `prom-scrape: { value: "<random>" }` to the credentials file; restart `at-jam serve`. Expect log `Jam metrics: mounted`.
- [ ] **Step 2:** `curl -s --cacert <ca> -H "Authorization: Bearer <random>" https://<jam-host>/metrics | head` → `jam_up 1` and `jam_attention_open{severity="critical"} 0`; without the header → `401`.
- [ ] **Step 3:** `just monitoring-up <jam-host>`; Prometheus UI → Status → Targets shows `jam` **UP**.
- [ ] **Step 4:** Force a condition: point a test destination at a credential whose `command:` is `["false"]`, send 3 requests through it from a studio (or `curl` with an identity token). Expect: Health tab shows `cred.unavailable:<name>` with its fix; rail badge red; within ~1 min Alertmanager shows `JamAttention` and Discord receives it.
- [ ] **Step 5:** Fix the command; send one request → condition resolves on Health, Discord receives the resolved notice.
- [ ] **Step 6:** Restart `at-jam serve` while a condition is open → it is still open with the same "for …" age after restart.
```
