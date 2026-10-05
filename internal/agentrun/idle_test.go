package agentrun

import (
	"slices"
	"testing"
)

// Lines below mirror real Claude Code 2.1.284 stream-json output (trimmed to
// the fields the tracker reads).
const (
	lnInit      = `{"type":"system","subtype":"init","session_id":"s"}`
	lnAssistant = `{"type":"assistant","message":{"content":[]}}`
	lnToolRes   = `{"type":"user","message":{"content":[]}}`
	lnResult    = `{"type":"result","subtype":"success","queued_turn_count":0,"terminal_reason":"completed"}`
	lnResultQ1  = `{"type":"result","subtype":"success","queued_turn_count":1}`
	lnTasks1    = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b1","task_type":"local_bash","description":"Sleep 25 seconds"}]}`
	lnTasks0    = `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`
	lnStarted   = `{"type":"system","subtype":"task_started","task_id":"b1","is_backgrounded":true}`
	lnUpdated   = `{"type":"system","subtype":"task_updated","task_id":"b1","patch":{"status":"completed"}}`
	lnNotify    = `{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed"}`
	lnRate      = `{"type":"rate_limit_event"}`
)

func feed(tr *idleTracker, lines ...string) {
	for _, l := range lines {
		tr.Observe([]byte(l))
	}
}

func wantAct(t *testing.T, tr *idleTracker, want idleAction) []string {
	t.Helper()
	got, tasks := tr.Next()
	if got != want {
		t.Fatalf("Next() = %v, want %v", got, want)
	}
	return tasks
}

func TestIdleTrackerStartsBusy(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	wantAct(t, tr, actWait)
}

func TestIdleTrackerPlainTurnThenIdle(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnAssistant, lnToolRes, lnAssistant, lnRate)
	wantAct(t, tr, actWait)
	feed(tr, lnResult)
	wantAct(t, tr, actClose)
}

func TestIdleTrackerQueuedTurnStaysBusy(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnResultQ1)
	wantAct(t, tr, actWait)
	feed(tr, lnInit, lnResult)
	wantAct(t, tr, actClose)
}

// The verified background sequence: result while a task runs → hold; task
// completes and claude self-starts a turn → not idle until that turn's result.
func TestIdleTrackerBackgroundTaskSequence(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnAssistant, lnTasks1, lnStarted, lnToolRes, lnAssistant, lnResult)
	if tasks := wantAct(t, tr, actHold); !slices.Equal(tasks, []string{"Sleep 25 seconds"}) {
		t.Fatalf("hold tasks = %v", tasks)
	}
	// claude empties the task list BEFORE the completion notification; the
	// task is then awaiting its notification, so we must still hold.
	feed(tr, lnTasks0, lnUpdated)
	if tasks := wantAct(t, tr, actHold); !slices.Equal(tasks, []string{"Sleep 25 seconds"}) {
		t.Fatalf("awaiting-notification tasks = %v", tasks)
	}
	feed(tr, lnNotify) // starts the self-started turn
	wantAct(t, tr, actWait)
	feed(tr, lnInit, lnAssistant, lnResult)
	wantAct(t, tr, actClose)
}

// A task that leaves the list but never gets a notification keeps the episode
// on hold (the BackgroundWait cap is the backstop), never closes early.
func TestIdleTrackerMissingNotificationHolds(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnTasks1, lnResult, lnTasks0)
	wantAct(t, tr, actHold)
}

// A notification for a task we never saw listed must not wedge anything.
func TestIdleTrackerUnknownNotification(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnNotify, lnResult)
	wantAct(t, tr, actClose)
}

func TestIdleTrackerWakeWhileBusyIsCoalesced(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit)
	for i := 0; i < 3; i++ {
		if tr.Wake() {
			t.Fatal("Wake while busy must not deliver now")
		}
	}
	if !tr.PendingWake() {
		t.Fatal("PendingWake = false after Wake while busy")
	}
	feed(tr, lnResult)
	wantAct(t, tr, actDeliverWake)
	if tr.PendingWake() {
		t.Fatal("pending wake not cleared by actDeliverWake")
	}
	wantAct(t, tr, actWait) // delivering marked us busy
	feed(tr, lnInit, lnResult)
	wantAct(t, tr, actClose) // exactly one delivery for three wakes
}

func TestIdleTrackerPendingWakeBeatsHold(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnTasks1)
	tr.Wake()
	feed(tr, lnResult)
	wantAct(t, tr, actDeliverWake)
}

func TestIdleTrackerWakeDuringHoldDeliversNow(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnTasks1, lnResult)
	wantAct(t, tr, actHold)
	if !tr.Wake() {
		t.Fatal("Wake between turns must deliver now")
	}
	tr.Wrote()
	wantAct(t, tr, actWait)
}

func TestIdleTrackerUnparseableLineIgnored(t *testing.T) {
	var warned int
	tr := newIdleTracker(Claude{}.ParseEvent, func(string, ...any) { warned++ })
	feed(tr, lnInit, lnResult)
	tr.Observe([]byte("not json"))
	tr.Observe(nil)
	wantAct(t, tr, actClose)
	if warned != 1 {
		t.Fatalf("warned %d times, want 1 (empty line is silent)", warned)
	}
}

func TestIdleTrackerSignalsChange(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnResult)
	select {
	case <-tr.changed:
	default:
		t.Fatal("no change signal after a result")
	}
	feed(tr, lnRate) // irrelevant line: no signal
	select {
	case <-tr.changed:
		t.Fatal("signalled on an irrelevant line")
	default:
	}
}

// A wake delivered at a turn boundary (between turns, with one already
// coalesced mid-turn) serves that pending wake too: the resumed turn's result
// must close, not send a second resume prompt.
func TestIdleTrackerWakeAtTurnBoundaryServesPendingWake(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit)
	if tr.Wake() {
		t.Fatal("Wake while busy must not deliver now")
	}
	feed(tr, lnResult)
	if !tr.Wake() { // the episode loop picked this wake before the result's change signal
		t.Fatal("Wake between turns must deliver now")
	}
	tr.Wrote()
	if tr.PendingWake() {
		t.Fatal("the delivered resume must consume the coalesced wake")
	}
	feed(tr, lnInit, lnResult)
	wantAct(t, tr, actClose)
}

// A resume prompt is owed until claude starts the turn it asked for: if the
// process exits first, the wake must be handed back.
func TestIdleTrackerResumeOwedUntilTurnStarts(t *testing.T) {
	tr := newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit, lnTasks1, lnResult)
	if tr.WakeOwed() {
		t.Fatal("nothing owed before any wake")
	}
	if !tr.Wake() {
		t.Fatal("Wake between turns must deliver now")
	}
	tr.Wrote()
	if !tr.WakeOwed() {
		t.Fatal("a written resume is owed until claude starts its turn")
	}
	feed(tr, lnInit)
	if tr.WakeOwed() {
		t.Fatal("the resumed turn started: nothing owed")
	}

	// The coalesced path: actDeliverWake owes the resume until the turn starts.
	tr = newIdleTracker(Claude{}.ParseEvent, nil)
	feed(tr, lnInit)
	tr.Wake()
	feed(tr, lnResult)
	wantAct(t, tr, actDeliverWake)
	if !tr.WakeOwed() {
		t.Fatal("a delivered coalesced resume is owed until claude starts its turn")
	}
	feed(tr, lnAssistant)
	if tr.WakeOwed() {
		t.Fatal("the resumed turn started: nothing owed")
	}
}
