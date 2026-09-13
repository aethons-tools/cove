package wakeon

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

type fakeReg struct{ insts []harbor.Instance }

func (f *fakeReg) ListInstances() []harbor.Instance { return f.insts }

type fakeCur struct{ set map[string]string }

func (f *fakeCur) SetWaitCursor(a, c string) error { f.set[a] = c; return nil }

type fakeWaker struct{ woke []string }

func (f *fakeWaker) Wake(a string) { f.woke = append(f.woke, a) }

type fakeReaper struct{ down []string }

func (f *fakeReaper) Teardown(_ context.Context, a string) error {
	f.down = append(f.down, a)
	return nil
}

type fakeCmt struct{ n int } // Comments returns n items

func (f *fakeCmt) IssueByIdentifier(_ context.Context, id string) (string, error) {
	return "iss-" + id, nil
}
func (f *fakeCmt) Comments(_ context.Context, _ string) ([]harbor.Comment, error) {
	return make([]harbor.Comment, f.n), nil
}

func newTestEngine(reg *fakeReg, cur *fakeCur, wake *fakeWaker, reap *fakeReaper, cmt *fakeCmt, cfg Config) *Engine {
	return New(reg, cur, wake, reap, cmt, cfg, nil)
}

func TestTick_WaitingEmptyCursorSetsBaseline_NoWake(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Now(), WaitCursor: ""},
	}}
	cur := &fakeCur{set: map[string]string{}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	cmt := &fakeCmt{n: 3}
	e := newTestEngine(reg, cur, wake, reap, cmt, Config{})

	e.tick(context.Background())

	if got, want := cur.set["a1"], "3"; got != want {
		t.Errorf("SetWaitCursor(a1) = %q, want %q", got, want)
	}
	if len(wake.woke) != 0 {
		t.Errorf("Wake called unexpectedly: %v", wake.woke)
	}
	if len(reap.down) != 0 {
		t.Errorf("Teardown called unexpectedly: %v", reap.down)
	}
}

func TestTick_CursorUnchanged_NoWake(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Now(), WaitCursor: "2"},
	}}
	cur := &fakeCur{set: map[string]string{}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	cmt := &fakeCmt{n: 2}
	e := newTestEngine(reg, cur, wake, reap, cmt, Config{})

	e.tick(context.Background())

	if len(wake.woke) != 0 {
		t.Errorf("Wake called unexpectedly: %v", wake.woke)
	}
	if len(cur.set) != 0 {
		t.Errorf("SetWaitCursor called unexpectedly: %v", cur.set)
	}
}

func TestTick_CursorIncreased_Wakes(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Now(), WaitCursor: "2"},
	}}
	cur := &fakeCur{set: map[string]string{}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	cmt := &fakeCmt{n: 3}
	e := newTestEngine(reg, cur, wake, reap, cmt, Config{})

	e.tick(context.Background())

	if len(wake.woke) != 1 || wake.woke[0] != "a1" {
		t.Errorf("Wake = %v, want [a1]", wake.woke)
	}
}

func TestTick_PastMaxWait_TeardownNoWake(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Unix(0, 0), WaitCursor: ""},
	}}
	cur := &fakeCur{set: map[string]string{}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	cmt := &fakeCmt{n: 1}
	e := newTestEngine(reg, cur, wake, reap, cmt, Config{MaxWait: 30 * time.Minute})
	// Override the clock for determinism instead of relying on real elapsed time.
	e.now = func() time.Time { return time.Unix(0, 0).Add(31 * time.Minute) }

	e.tick(context.Background())

	if len(reap.down) != 1 || reap.down[0] != "a1" {
		t.Errorf("Teardown = %v, want [a1]", reap.down)
	}
	if len(wake.woke) != 0 {
		t.Errorf("Wake called unexpectedly: %v", wake.woke)
	}
	if len(cur.set) != 0 {
		t.Errorf("SetWaitCursor called unexpectedly: %v", cur.set)
	}
}

func TestTick_NonWaitingIgnored(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Activity: harbor.ActivityRunning, Unit: "AET-1", WaitingSince: time.Unix(0, 0), WaitCursor: ""},
	}}
	cur := &fakeCur{set: map[string]string{}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	cmt := &fakeCmt{n: 5}
	e := newTestEngine(reg, cur, wake, reap, cmt, Config{})

	e.tick(context.Background())

	if len(wake.woke) != 0 || len(reap.down) != 0 || len(cur.set) != 0 {
		t.Errorf("expected no side effects for non-Waiting instance, got wake=%v reap=%v cur=%v", wake.woke, reap.down, cur.set)
	}
}
