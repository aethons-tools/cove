package scheduler

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/config"
)

func testConfig() config.Config {
	return config.Config{
		Tracker: config.TrackerConfig{PollInterval: "1m"},
		Repo:    config.RepoConfig{Slug: "aethons-tools/cove"},
		Classes: map[string]config.Class{
			"implement": {Mode: "autonomous", Command: []string{"true"}, Timeout: "30m"},
			"spec":      {Mode: "interactive"},
		},
		Concurrency: 4,
	}
}

func newTestEngine(cfg config.Config, tr Tracker, ex Executor) *Engine {
	return New(cfg, tr, ex, func([]string) (string, error) { return "", nil }, log.New(io.Discard, "", 0))
}

func TestHandleOK(t *testing.T) {
	tr := &fakeTracker{}
	ex := &fakeExecutor{resultJSON: `{"status":"ok","artifacts":{"prUrl":"https://x/pr/9"},"summary":"done"}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i1", Identifier: "AET-9", Class: "implement"})

	if got := tr.roles("i1"); len(got) != 2 || got[0] != RoleInProgress || got[1] != RoleInReview {
		t.Fatalf("transitions = %v; want [InProgress InReview]", got)
	}
	if p := tr.lastPost("i1"); !strings.Contains(p, "https://x/pr/9") {
		t.Fatalf("comment = %q; want the prUrl", p)
	}
}

func TestHandleNeedsInput(t *testing.T) {
	tr := &fakeTracker{}
	ex := &fakeExecutor{resultJSON: `{"status":"needs_input","needsInput":{"blocker":"ambiguous","need":"pick A or B"}}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i2", Identifier: "AET-2", Class: "implement"})

	if got := tr.roles("i2"); len(got) != 2 || got[1] != RoleNeedsInput {
		t.Fatalf("transitions = %v; want ...NeedsInput", got)
	}
	if p := tr.lastPost("i2"); !strings.Contains(p, "pick A or B") || !strings.Contains(p, "NEEDS INPUT") {
		t.Fatalf("comment = %q; want the needs-input block", p)
	}
}

func TestHandleCommandErrorGoesToNeedsInput(t *testing.T) {
	tr := &fakeTracker{}
	ex := &fakeExecutor{runErr: errors.New("boom")} // no result file written → error
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i3", Identifier: "AET-3", Class: "implement"})

	if got := tr.roles("i3"); len(got) != 2 || got[1] != RoleNeedsInput {
		t.Fatalf("transitions = %v; want ...NeedsInput", got)
	}
}

func TestHandleFailedClaimStops(t *testing.T) {
	tr := &fakeTracker{failClaim: true}
	ex := &fakeExecutor{resultJSON: `{"status":"ok"}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.handle(context.Background(), Issue{ID: "i4", Identifier: "AET-4", Class: "implement"})

	if got := tr.roles("i4"); len(got) != 0 {
		t.Fatalf("transitions = %v; want none (claim failed)", got)
	}
}

func TestTickReconcilesAndDispatches(t *testing.T) {
	tr := &fakeTracker{
		unblockable: []Issue{{ID: "b1", Identifier: "AET-B1"}},
		ready: []Issue{
			{ID: "i1", Identifier: "AET-1", Class: "implement"},
			{ID: "s1", Identifier: "AET-S1", Class: "spec"},    // interactive → skipped
			{ID: "x1", Identifier: "AET-X1", Class: "unknown"}, // unknown → skipped
		},
	}
	ex := &fakeExecutor{resultJSON: `{"status":"ok"}`}
	e := newTestEngine(testConfig(), tr, ex)

	e.tick(context.Background())
	e.wait()

	// unblockable moved to READY
	if got := tr.roles("b1"); len(got) != 1 || got[0] != RoleReady {
		t.Fatalf("unblock transitions = %v; want [Ready]", got)
	}
	// implement issue dispatched (claimed + brokered)
	if got := tr.roles("i1"); len(got) != 2 || got[0] != RoleInProgress || got[1] != RoleInReview {
		t.Fatalf("i1 transitions = %v; want [InProgress InReview]", got)
	}
	// interactive + unknown skipped (no transitions)
	if len(tr.roles("s1")) != 0 || len(tr.roles("x1")) != 0 {
		t.Fatalf("interactive/unknown should not be touched")
	}
}

func TestTickRespectsGlobalConcurrency(t *testing.T) {
	cfg := testConfig()
	cfg.Concurrency = 1
	tr := &fakeTracker{ready: []Issue{
		{ID: "i1", Identifier: "AET-1", Class: "implement"},
		{ID: "i2", Identifier: "AET-2", Class: "implement"},
	}}
	release := make(chan struct{})
	started := make(chan struct{})
	ex := &fakeExecutor{resultJSON: `{"status":"ok"}`, release: release, started: started}
	e := newTestEngine(cfg, tr, ex)

	e.tick(context.Background()) // one slot: only one issue claimed, the other skipped this tick
	<-started                    // the single claimed issue's command has begun (its claim is recorded)
	claimed := 0
	for _, id := range []string{"i1", "i2"} {
		if len(tr.roles(id)) >= 1 {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed = %d; want exactly 1 (global cap = 1)", claimed)
	}
	close(release)
	e.wait()
}

func TestTickRecoversPanic(t *testing.T) {
	tr := &fakeTracker{ready: []Issue{{ID: "p1", Identifier: "AET-P1", Class: "implement"}}}
	e := newTestEngine(testConfig(), tr, &fakeExecutor{panicMsg: "kaboom"})

	e.tick(context.Background())
	e.wait() // must not crash; the panic is recovered

	if got := tr.roles("p1"); len(got) != 2 || got[0] != RoleInProgress || got[1] != RoleNeedsInput {
		t.Fatalf("transitions = %v; want [InProgress NeedsInput] after a recovered panic", got)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	cfg := testConfig()
	cfg.Tracker.PollInterval = "1h" // long, so only the immediate first tick runs
	tr := &fakeTracker{}
	e := newTestEngine(cfg, tr, &fakeExecutor{resultJSON: `{"status":"ok"}`})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if err := e.Run(ctx); err == nil {
		t.Fatal("Run should return ctx.Err() when cancelled")
	}
}
