package agentrun

import (
	"encoding/json"
	"slices"
	"sync"
)

// trackerMaxLine caps one stdout line the idle tracker parses. Far above the
// session-event cap so a large result line is never truncated out of
// recognition.
const trackerMaxLine = 64 << 20

// idleAction is what the episode loop should do after the tracker changes.
type idleAction int

const (
	actWait        idleAction = iota // a turn is in progress
	actDeliverWake                   // turn over, a coalesced Wake is pending: write one resume prompt
	actHold                          // turn over, background tasks still outstanding
	actClose                         // idle: close stdin
)

// idleTracker follows claude's stream-json stdout to decide when the agent
// is truly idle: its turn has ended (a result with nothing queued) AND no
// background task is outstanding. Wakes arriving mid-turn are coalesced into
// one pending resume. Observe runs on the stdout copy goroutine; everything
// else on Run's goroutine — hence the mutex. It never blocks the stdout path.
type idleTracker struct {
	mu   sync.Mutex
	busy bool
	// tasks are the outstanding background tasks (id → description), per the
	// latest background_tasks_changed snapshot. awaiting holds tasks that left
	// that list but whose task_notification has not arrived yet: claude empties
	// the list BEFORE notifying, and the notification starts a turn.
	tasks, awaiting map[string]string
	pendingWake     bool
	changed         chan struct{} // cap 1; signalled on every state change from Observe
	warn            func(msg string, args ...any)
}

func newIdleTracker(warn func(msg string, args ...any)) *idleTracker {
	if warn == nil {
		warn = func(string, ...any) {}
	}
	return &idleTracker{busy: true, tasks: map[string]string{}, awaiting: map[string]string{},
		changed: make(chan struct{}, 1), warn: warn}
}

type trackedEvent struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	TaskID          string `json:"task_id"`
	QueuedTurnCount int    `json:"queued_turn_count"`
	Tasks           []struct {
		TaskID      string `json:"task_id"`
		Description string `json:"description"`
	} `json:"tasks"`
}

// Observe updates state from one stdout line.
func (t *idleTracker) Observe(line []byte) {
	if len(line) == 0 {
		return
	}
	var ev trackedEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		t.warn("agentrun: idle tracker ignored an unparseable stdout line", "err", err.Error())
		return
	}
	t.mu.Lock()
	switch {
	case ev.Type == "result":
		if ev.QueuedTurnCount == 0 {
			t.busy = false
		}
	case ev.Type == "system" && ev.Subtype == "background_tasks_changed":
		next := make(map[string]string, len(ev.Tasks))
		for _, k := range ev.Tasks {
			next[k.TaskID] = k.Description
		}
		for id, d := range t.tasks {
			if _, still := next[id]; !still {
				t.awaiting[id] = d
			}
		}
		t.tasks = next
	case ev.Type == "system" && ev.Subtype == "task_notification":
		delete(t.awaiting, ev.TaskID)
		t.busy = true
	case ev.Type == "system" && ev.Subtype == "init",
		ev.Type == "assistant", ev.Type == "user":
		t.busy = true
	default:
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

// Wrote records that a user message was written to stdin.
func (t *idleTracker) Wrote() {
	t.mu.Lock()
	t.busy = true
	t.mu.Unlock()
}

// Wake reports whether a Wake can be delivered now (claude is between turns);
// otherwise it is coalesced into the pending wake.
func (t *idleTracker) Wake() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy {
		t.pendingWake = true
		return false
	}
	return true
}

// Next returns the action for the current state. actDeliverWake consumes the
// pending wake and marks the tracker busy (the caller writes the prompt).
func (t *idleTracker) Next() (idleAction, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.busy:
		return actWait, nil
	case t.pendingWake:
		t.pendingWake, t.busy = false, true
		return actDeliverWake, nil
	case len(t.tasks)+len(t.awaiting) > 0:
		var descs []string
		for _, d := range t.tasks {
			descs = append(descs, d)
		}
		for _, d := range t.awaiting {
			descs = append(descs, d)
		}
		slices.Sort(descs)
		return actHold, descs
	default:
		return actClose, nil
	}
}

// PendingWake reports whether a coalesced wake is still undelivered.
func (t *idleTracker) PendingWake() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingWake
}
