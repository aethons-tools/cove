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
		Repo: config.RepoConfig{Slug: "aethons-tools/cove"},
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
