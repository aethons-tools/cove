package agentrun

import (
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

// idleTracker follows the agent's stdout, normalized by the Harness into
// Events, to decide when the agent is truly idle: its turn has ended (a TurnEnd
// with nothing queued) AND no background task is outstanding. Wakes arriving
// mid-turn are coalesced into one pending resume. Observe runs on the stdout
// copy goroutine; everything else on Run's goroutine — hence the mutex. It
// never blocks the stdout path.
type idleTracker struct {
	mu   sync.Mutex
	busy bool
	// tasks are the outstanding background tasks (id → description), per the
	// latest BackgroundTasks snapshot. awaiting holds tasks that left that list
	// but whose BackgroundDone has not arrived yet: the snapshot can drop a task
	// BEFORE its completion is delivered, and the delivery starts a turn.
	tasks, awaiting map[string]string
	pendingWake     bool
	// resumeOwed: a resume prompt was written but the agent has not started the
	// turn it asked for (no TurnStart/TurnEnd seen since).
	resumeOwed bool
	// replied: the agent has produced a reply this episode (Event.Reply);
	// onReply, if set, runs once when it first does (on the stdout goroutine).
	replied bool
	onReply func()
	changed chan struct{} // cap 1; signalled on every state change from Observe
	parse   func(line []byte) (Event, error)
	warn    func(msg string, args ...any)
}

// newIdleTracker builds a tracker that maps stdout lines to Events with parse
// (the Harness's ParseEvent).
func newIdleTracker(parse func(line []byte) (Event, error), warn func(msg string, args ...any)) *idleTracker {
	if warn == nil {
		warn = func(string, ...any) {}
	}
	return &idleTracker{busy: true, tasks: map[string]string{}, awaiting: map[string]string{},
		changed: make(chan struct{}, 1), parse: parse, warn: warn}
}

// Observe updates state from one stdout line.
func (t *idleTracker) Observe(line []byte) {
	if len(line) == 0 {
		return
	}
	ev, err := t.parse(line)
	if err != nil {
		t.warn("agentrun: idle tracker ignored an unparseable stdout line", "err", err.Error())
		return
	}
	t.mu.Lock()
	if ev.Reply && !t.replied {
		t.replied = true
		if t.onReply != nil {
			defer t.onReply() // after the unlock below (defers run last)
		}
	}
	switch ev.Kind {
	case EventTurnEnd:
		t.resumeOwed = false
		if ev.QueuedEmpty {
			t.busy = false
		}
	case EventBackgroundTasks:
		next := make(map[string]string, len(ev.Tasks))
		for _, k := range ev.Tasks {
			next[k.ID] = k.Description
		}
		for id, d := range t.tasks {
			if _, still := next[id]; !still {
				t.awaiting[id] = d
			}
		}
		t.tasks = next
	case EventBackgroundDone:
		delete(t.awaiting, ev.TaskID)
		t.busy = true
	case EventTurnStart:
		t.busy, t.resumeOwed = true, false
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

// Wrote records that a resume prompt was written to stdin: the agent is busy, and
// the wake stays owed until its turn starts.
func (t *idleTracker) Wrote() {
	t.mu.Lock()
	t.busy, t.resumeOwed = true, true
	t.mu.Unlock()
}

// Wake reports whether a Wake can be delivered now (the agent is between turns);
// otherwise it is coalesced into the pending wake. Delivering now also serves
// any wake coalesced earlier (one resume prompt answers every Wake so far), so
// it clears the pending wake rather than leave it to send a second prompt.
func (t *idleTracker) Wake() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy {
		t.pendingWake = true
		return false
	}
	t.pendingWake = false
	return true
}

// Next returns the action for the current state. actDeliverWake consumes the
// pending wake, marks the tracker busy and the resume owed (the caller writes
// the prompt).
func (t *idleTracker) Next() (idleAction, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.busy:
		return actWait, nil
	case t.pendingWake:
		t.pendingWake, t.busy, t.resumeOwed = false, true, true
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

// WakeOwed reports whether a Wake is still unserved: coalesced but not yet
// delivered, or delivered as a resume prompt that the agent has not started a
// turn for. Run hands an owed wake back to the post-exit wait so a process that dies
// before acting on it never loses it.
func (t *idleTracker) WakeOwed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingWake || t.resumeOwed
}

// Replied reports whether the agent produced a reply this episode.
func (t *idleTracker) Replied() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.replied
}

// ResumeOwed reports whether a resume prompt was written that the agent never
// started the turn for (as opposed to a wake coalesced and not yet written).
func (t *idleTracker) ResumeOwed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resumeOwed
}
