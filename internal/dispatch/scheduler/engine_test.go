package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
)

// newTestLogger builds a buffer-backed *logging.Logger for tests: Unattended
// mode so every record is a single JSON line, easy to substring-match.
func newTestLogger(w io.Writer) *logging.Logger {
	lg, err := logging.New(logging.Options{Mode: logging.Unattended, Stderr: w, Level: slog.LevelInfo})
	if err != nil {
		panic(err) // buffer-backed; New only errors on file-sink setup
	}
	return lg
}

func testConfig() kit.Config {
	return kit.Config{
		Tracker:  &kit.Tracker{Linear: &kit.LinearTracker{PollInterval: "1m"}},
		Dispatch: &kit.Dispatch{Concurrency: 4, DispatchOverhead: "15m"},
		Workers:  map[string]kit.Worker{"implement": {Prompt: "impl", Timeout: "30m"}},
	}
}

// newEngine builds an Engine against an explicit config (for tests that need to
// tweak concurrency, poll-interval, etc).
func newEngine(cfg kit.Config, tr Tracker, ex Executor) *Engine {
	return New(cfg, "/kits/implement", tr, ex, newTestLogger(io.Discard))
}

// newTestEngine builds an Engine against the default testConfig() (an autonomous
// "implement" class with a kit set, 30m timeout, and a 15m dispatch-overhead).
func newTestEngine(t *testing.T, tr Tracker, ex Executor) *Engine {
	t.Helper()
	return newEngine(testConfig(), tr, ex)
}

func TestHandleOKOpensReviewAndBuildsInput(t *testing.T) {
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{"pr-url":"https://x/pull/1","message":"opened PR"}},` +
		`"worker-result":{"status":{"ok":{"pull-request":{"title":"T","message":"did the thing"}}}}}`}
	eng := newTestEngine(t, tr, ex)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "Add X", Class: "implement"})

	// task.json the scheduler built (v2 nested shape)
	if !strings.Contains(ex.GotInput, `"work-branch": "implement/AET-9"`) ||
		!strings.Contains(ex.GotInput, `"key": "AET-9"`) ||
		!strings.Contains(ex.GotInput, `"class": "implement"`) {
		t.Fatalf("task.json wrong:\n%s", ex.GotInput)
	}
	joined := strings.Join(ex.GotArgv, " ")
	if !strings.Contains(joined, "at-cove work --project-dir /kits") || !strings.Contains(joined, "--timeout 30m") {
		t.Fatalf("argv wrong: %v", ex.GotArgv)
	}
	if tr.lastRole != RoleInReview {
		t.Errorf("role = %v; want IN REVIEW", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "https://x/pull/1") || !strings.Contains(tr.lastComment, "did the thing") {
		t.Errorf("comment missing PR/message: %q", tr.lastComment)
	}
}

func TestHandleNeedsInput(t *testing.T) {
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":{"needs-input":{"message":"WIP pushed","commit":"abc123"}},` +
		`"worker-result":{"status":{"needs-input":{"doing":"d","blocker":"b","need":"n","tried":"tr"}}}}`}
	eng := newTestEngine(t, tr, ex)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})
	if tr.lastRole != RoleNeedsInput {
		t.Errorf("role = %v; want NEEDS INPUT", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "**Blocker:** b") || !strings.Contains(tr.lastComment, "abc123") {
		t.Errorf("needs-input comment wrong: %q", tr.lastComment)
	}
}

func TestHandleMissingOutputIsError(t *testing.T) {
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: "", RunErr: errors.New("boom")} // writes no task-result.json
	eng := newTestEngine(t, tr, ex)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})
	if tr.lastRole != RoleNeedsInput {
		t.Errorf("role = %v; want NEEDS INPUT (error path)", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "⚠️") {
		t.Errorf("expected error comment, got %q", tr.lastComment)
	}
	// The result error ("no dispatch output") is the symptom; the run error is the
	// cause. Both must be surfaced so a failed run is self-diagnosing.
	if !strings.Contains(tr.lastComment, "no dispatch output") || !strings.Contains(tr.lastComment, "boom") {
		t.Errorf("error comment should surface both the result error and the run error; got %q", tr.lastComment)
	}
}

// The scheduler must log the exact `at-cove work` argv it execs, so a failed
// invocation is diagnosable from the scheduler's own output.
func TestHandleLogsExecArgv(t *testing.T) {
	var logs bytes.Buffer
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`}
	eng := New(testConfig(), "/kits/implement", tr, ex, newTestLogger(&logs))
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})
	if !strings.Contains(logs.String(), `"argv":"at-cove work --project-dir /kits`) {
		t.Fatalf("expected the exec argv to be logged as a structured field; got: %q", logs.String())
	}
}

// The scheduler must correlate every log line from one dispatch with a run
// id and the issue/class it's working, and tag the phase it's in with a
// "step" attr — so a failed run is diagnosable by grepping one run id out of
// interleaved concurrent dispatches.
func TestHandleLogsRunAndStepAttrs(t *testing.T) {
	var buf bytes.Buffer
	lg := newTestLogger(&buf)
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`}
	eng := New(testConfig(), "/kits/implement", tr, ex, lg)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})

	s := buf.String()
	if !strings.Contains(s, `"issue":"AET-9"`) || !strings.Contains(s, `"class":"implement"`) {
		t.Fatalf("expected issue/class attrs on dispatch logs; got %q", s)
	}
	if !strings.Contains(s, `"run":"run_AET-9`) {
		t.Fatalf("expected a run id attr; got %q", s)
	}
	if !strings.Contains(s, `"step":"dispatch"`) {
		t.Fatalf("expected a step attr on the exec log; got %q", s)
	}
}

// A tracker list failure during a poll must carry a step="poll" attr (COV-93):
// the scheduler's "each layer sets a step" contract means even the poll error
// joins the step vocabulary, so an operator can grep it.
func TestPollListErrorCarriesStep(t *testing.T) {
	var buf bytes.Buffer
	lg := newTestLogger(&buf)
	tr := &fakeTracker{failList: true, comments: map[string][]Comment{}}
	eng := New(testConfig(), "/kits/implement", tr, &fakeExecutor{}, lg)
	eng.tick(context.Background())
	s := buf.String()
	if !strings.Contains(s, "list ready failed") || !strings.Contains(s, `"step":"poll"`) {
		t.Fatalf("list-ready error must carry step=poll; got %q", s)
	}
}

// The scheduler must pass its per-dispatch run id into the `at-cove work`
// subprocess via COVE_RUN_ID, and it must be the SAME id it stamps on its own
// logs — so the work process (and the VM records it merges) join this dispatch's
// trace under one `run` (spec §7).
func TestHandlePassesRunIDToWork(t *testing.T) {
	var buf bytes.Buffer
	lg := newTestLogger(&buf)
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`}
	eng := New(testConfig(), "/kits/implement", tr, ex, lg)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})

	var runEnv string
	for _, e := range ex.GotEnv {
		if strings.HasPrefix(e, "COVE_RUN_ID=") {
			runEnv = strings.TrimPrefix(e, "COVE_RUN_ID=")
		}
	}
	if runEnv == "" {
		t.Fatalf("work subprocess did not receive COVE_RUN_ID; env=%v", ex.GotEnv)
	}
	if !strings.HasPrefix(runEnv, "run_AET-9_") {
		t.Fatalf("COVE_RUN_ID = %q; want a run_AET-9_* id", runEnv)
	}
	if !strings.Contains(buf.String(), `"run":"`+runEnv+`"`) {
		t.Fatalf("COVE_RUN_ID %q must match the run id stamped on the scheduler's own logs; got %q", runEnv, buf.String())
	}
}

func TestHandleFailedClaimStops(t *testing.T) {
	tr := &fakeTracker{failClaim: true}
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`}
	e := newTestEngine(t, tr, ex)

	e.handle(context.Background(), Issue{ID: "i4", Identifier: "AET-4", Class: "implement"})

	if got := tr.roles("i4"); len(got) != 0 {
		t.Fatalf("transitions = %v; want none (claim failed)", got)
	}
}

func TestTickDispatchesReadyByClass(t *testing.T) {
	// ListReady only ever returns dispatchable READY issues (blockers Done — the
	// tracker gates that). The scheduler dispatches the autonomous worker class and
	// leaves interactive/unknown classes alone; it never promotes from the backlog.
	tr := &fakeTracker{
		ready: []Issue{
			{ID: "i1", Identifier: "AET-1", Class: "implement"},
			{ID: "s1", Identifier: "AET-S1", Class: "spec"},    // interactive → skipped
			{ID: "x1", Identifier: "AET-X1", Class: "unknown"}, // unknown → skipped
		},
	}
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`}
	e := newTestEngine(t, tr, ex)

	e.tick(context.Background())
	e.wait()

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
	cfg.Dispatch.Concurrency = 1
	tr := &fakeTracker{ready: []Issue{
		{ID: "i1", Identifier: "AET-1", Class: "implement"},
		{ID: "i2", Identifier: "AET-2", Class: "implement"},
	}}
	release := make(chan struct{})
	started := make(chan struct{})
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`, release: release, started: started}
	e := newEngine(cfg, tr, ex)

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
	e := newTestEngine(t, tr, &fakeExecutor{panicMsg: "kaboom"})

	e.tick(context.Background())
	e.wait() // must not crash; the panic is recovered

	if got := tr.roles("p1"); len(got) != 2 || got[0] != RoleInProgress || got[1] != RoleNeedsInput {
		t.Fatalf("transitions = %v; want [InProgress NeedsInput] after a recovered panic", got)
	}
}

// The reaper runs on each poll pass: an orphaned IN PROGRESS issue whose
// time-in-state exceeds reaper-timeout is moved to NEEDS INPUT with a comment,
// while a fresh one and a live-owned one (a dispatch this process still runs)
// are left untouched.
func TestTickReapsStaleOrphansOnly(t *testing.T) {
	cfg := testConfig()
	cfg.Dispatch.ReaperTimeout = "45m"
	tr := newFakeTracker()
	tr.inProgress = []InProgressIssue{
		{Issue: Issue{ID: "stale", Identifier: "AET-STALE", Class: "implement"}, StartedAt: time.Now().Add(-2 * time.Hour)},
		{Issue: Issue{ID: "fresh", Identifier: "AET-FRESH", Class: "implement"}, StartedAt: time.Now().Add(-1 * time.Minute)},
		{Issue: Issue{ID: "live", Identifier: "AET-LIVE", Class: "implement"}, StartedAt: time.Now().Add(-2 * time.Hour)},
		{Issue: Issue{ID: "noclass", Identifier: "AET-NOCLASS"}, StartedAt: time.Now().Add(-2 * time.Hour)},
	}
	e := newEngine(cfg, tr, &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`})
	e.markLive("live") // this process still owns the "live" run — never reap it

	e.tick(context.Background())
	e.wait()

	// over-age orphan (a dispatchable worker class) → NEEDS INPUT with an
	// explanatory comment naming the timeout
	if got := tr.roles("stale"); len(got) != 1 || got[0] != RoleNeedsInput {
		t.Fatalf("stale transitions = %v; want [NeedsInput]", got)
	}
	if c := tr.lastPost("stale"); !strings.Contains(c, "Reaped") || !strings.Contains(c, "45m") {
		t.Errorf("reaped comment missing marker/timeout: %q", c)
	}
	// under-age left untouched
	if got := tr.roles("fresh"); len(got) != 0 {
		t.Errorf("fresh (under-age) transitions = %v; want none", got)
	}
	// live-owned over-age left untouched
	if got := tr.roles("live"); len(got) != 0 {
		t.Errorf("live-owned transitions = %v; want none (never reap a live dispatch)", got)
	}
	// A stale IN PROGRESS issue with no dispatchable worker class was never the
	// dispatcher's to claim, so the reaper must leave it for the human (COV-55).
	if got := tr.roles("noclass"); len(got) != 0 {
		t.Errorf("no-worker-class transitions = %v; want none (reaper must not touch non-dispatch issues)", got)
	}
}

// With no reaper-timeout configured the reaper is a no-op, even for a long-stale
// IN PROGRESS issue.
func TestTickReapDisabledWhenUnset(t *testing.T) {
	cfg := testConfig()
	cfg.Dispatch.ReaperTimeout = ""
	tr := newFakeTracker()
	tr.inProgress = []InProgressIssue{
		{Issue: Issue{ID: "stale", Identifier: "AET-STALE"}, StartedAt: time.Now().Add(-99 * time.Hour)},
	}
	e := newEngine(cfg, tr, &fakeExecutor{})

	e.tick(context.Background())
	e.wait()

	if got := tr.roles("stale"); len(got) != 0 {
		t.Fatalf("reaper disabled: transitions = %v; want none", got)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	cfg := testConfig()
	cfg.Tracker.Linear.PollInterval = "1h" // long, so only the immediate first tick runs
	tr := &fakeTracker{}
	e := newEngine(cfg, tr, &fakeExecutor{OutJSON: `{"status":{"ok":{}}}`})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if err := e.Run(ctx); err == nil {
		t.Fatal("Run should return ctx.Err() when cancelled")
	}
}
