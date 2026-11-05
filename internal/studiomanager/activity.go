package studiomanager

import (
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// activities is the manager's work in progress — kit prepares, kit draft
// builds, disk cleanups — as reported to Jam in each status report
// (client.Config.Activities is Snapshot). Every change signals changed
// without blocking, so a report follows it (throttled by the client). A nil
// *activities tracks nothing.
type activities struct {
	changed chan<- struct{} // nil: no signals
	now     func() time.Time

	mu   sync.Mutex
	next int
	list []*activity // by start
}

func newActivities(changed chan<- struct{}) *activities {
	return &activities{changed: changed, now: time.Now}
}

// activity is one tracked piece of work; its methods are safe on nil.
type activity struct {
	t    *activities
	a    jam.ManagerActivity // guarded by t.mu
	done bool                // guarded by t.mu
}

// Start tracks a new activity of kind for kit (zero for none) until Done.
func (t *activities) Start(kind string, kit jam.KitRef) *activity {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	t.next++
	h := &activity{t: t, a: jam.ManagerActivity{ID: kind + "-" + strconv.Itoa(t.next), Kind: kind, Kit: kit, Started: t.now()}}
	t.list = append(t.list, h)
	t.mu.Unlock()
	t.signal()
	return h
}

// Snapshot is the activities in progress, oldest first; nil for none.
func (t *activities) Snapshot() []jam.ManagerActivity {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []jam.ManagerActivity
	for _, h := range t.list {
		out = append(out, h.a)
	}
	return out
}

func (t *activities) signal() {
	if t.changed == nil {
		return
	}
	select {
	case t.changed <- struct{}{}:
	default: // a report is already pending
	}
}

// Step records the activity's current step and detail; an unchanged one, or
// one after Done, is ignored.
func (h *activity) Step(step, detail string) {
	if h == nil {
		return
	}
	t := h.t
	t.mu.Lock()
	if h.done || (h.a.Step == step && h.a.Detail == detail) {
		t.mu.Unlock()
		return
	}
	h.a.Step, h.a.Detail = step, detail
	t.mu.Unlock()
	t.signal()
}

// Done ends the activity; later calls do nothing.
func (h *activity) Done() {
	if h == nil {
		return
	}
	t := h.t
	t.mu.Lock()
	if h.done {
		t.mu.Unlock()
		return
	}
	h.done = true
	t.list = slices.DeleteFunc(t.list, func(x *activity) bool { return x == h })
	t.mu.Unlock()
	t.signal()
}
