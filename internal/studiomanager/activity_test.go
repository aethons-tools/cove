package studiomanager

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// signals drains ch, counting the pending signal (0 or 1).
func signals(ch chan struct{}) int {
	select {
	case <-ch:
		return 1
	default:
		return 0
	}
}

func TestActivitiesTrackStepsAndSignal(t *testing.T) {
	ch := make(chan struct{}, 1)
	at := time.Unix(1000, 0)
	a := newActivities(ch)
	a.now = func() time.Time { return at }
	if got := a.Snapshot(); len(got) != 0 {
		t.Fatalf("idle snapshot = %v", got)
	}
	kit := jam.KitRef{ID: "web", Version: 2}
	p := a.Start(jam.ActivityPrepareKit, kit)
	if signals(ch) != 1 {
		t.Fatal("Start did not signal")
	}
	c := a.Start(jam.ActivityCleanup, jam.KitRef{})
	p.Step("building", "step 3/9")
	snap := a.Snapshot()
	if len(snap) != 2 || snap[0].Kind != jam.ActivityPrepareKit || snap[0].Kit != kit || snap[0].Step != "building" ||
		snap[0].Detail != "step 3/9" || !snap[0].Started.Equal(at) || snap[1].Kind != jam.ActivityCleanup || snap[0].ID == snap[1].ID {
		t.Fatalf("snapshot = %+v", snap)
	}
	if signals(ch) != 1 {
		t.Fatal("Step did not signal")
	}
	p.Step("building", "step 3/9")
	if signals(ch) != 0 {
		t.Fatal("an unchanged step signalled")
	}
	snap[0].Step = "mutated"
	if a.Snapshot()[0].Step != "building" {
		t.Fatal("the snapshot aliases the tracker")
	}
	p.Done()
	p.Done()           // idempotent
	p.Step("late", "") // after Done: ignored
	if snap := a.Snapshot(); len(snap) != 1 || snap[0].Kind != jam.ActivityCleanup {
		t.Fatalf("after Done = %+v", snap)
	}
	if signals(ch) != 1 {
		t.Fatal("Done did not signal")
	}
	c.Done()
}

// A nil tracker (and the nil handle it starts) does nothing.
func TestActivitiesNilSafe(t *testing.T) {
	var a *activities
	h := a.Start(jam.ActivityCleanup, jam.KitRef{})
	h.Step("x", "y")
	h.Done()
	if a.Snapshot() != nil {
		t.Fatal("nil tracker has activities")
	}
	// No channel: never blocks.
	b := newActivities(nil)
	b.Start(jam.ActivityCleanup, jam.KitRef{}).Done()
}
