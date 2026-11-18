package jam

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// idleRecorder is a ControlSink that also hears idle pushes, recorded as
// "timeout:<d>" and "deadline:<unix s>" ("deadline:none" when disarmed).
type idleRecorder struct {
	fakeSink
	pushes []string
}

func (r *idleRecorder) IdleTimeoutChanged(id string, d time.Duration) {
	r.pushes = append(r.pushes, fmt.Sprint("timeout:", d))
}

func (r *idleRecorder) IdleDeadlineChanged(id string, at time.Time) {
	if at.IsZero() {
		r.pushes = append(r.pushes, "deadline:none")
		return
	}
	r.pushes = append(r.pushes, fmt.Sprint("deadline:", at.Unix()))
}

// take returns the pushes since the last take.
func (r *idleRecorder) take() []string {
	p := r.pushes
	r.pushes = nil
	return p
}

func raiseWithIdleSink(t *testing.T, te TurnEndPolicy) (*Supervisor, Store, *time.Time, *idleRecorder) {
	t.Helper()
	sup, store, now := raiseWithTurnEnd(t, te)
	rec := &idleRecorder{}
	sup.SetControlSink(rec)
	return sup, store, now, rec
}

func wantPushes(t *testing.T, step string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: pushes %q, want %q", step, got, want)
	}
}

func TestIdlePushTurnEndArmsDeadline(t *testing.T) {
	sup, _, now, rec := raiseWithIdleSink(t, TurnEndPolicy{IdleTimeout: 30 * time.Minute})
	*now = time.Unix(2000, 0)
	_ = sup.Report(context.Background(), "w1", ActivityHolding)
	wantPushes(t, "turn end", rec.take(), "timeout:30m0s", "deadline:3800")
	// holding → waiting is the same turn end: the deadline is unchanged.
	*now = time.Unix(2500, 0)
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	wantPushes(t, "holding→waiting", rec.take(), "timeout:30m0s")
}

// A turn end with no timeout pushes the (disarmed) deadline too, so a studio
// never keeps one from an earlier turn end.
func TestIdlePushTurnEndWithoutTimeout(t *testing.T) {
	sup, _, _, rec := raiseWithIdleSink(t, TurnEndPolicy{})
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	wantPushes(t, "turn end", rec.take(), "timeout:0s", "deadline:none")
}

func TestIdlePushNothingWhenTurnStarts(t *testing.T) {
	sup, store, _, rec := raiseWithIdleSink(t, TurnEndPolicy{IdleTimeout: time.Minute})
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	rec.take()
	_ = sup.Report(context.Background(), "w1", ActivityRunning)
	if inst, _ := store.GetInstance("w1"); !inst.IdleDeadline.IsZero() {
		t.Fatalf("deadline %v survived the turn start", inst.IdleDeadline)
	}
	wantPushes(t, "turn start", rec.take(), "timeout:1m0s")
}

func TestIdlePushOverrideDuringTurn(t *testing.T) {
	sup, _, now, rec := raiseWithIdleSink(t, TurnEndPolicy{IdleTimeout: time.Hour})
	if err := sup.SetIdleOverride("w1", IdleOverride{Duration: 5 * time.Minute, Scope: IdleScopeNext}); err != nil {
		t.Fatal(err)
	}
	wantPushes(t, "override set", rec.take(), "timeout:5m0s")
	// The turn end uses the next-scoped override up: the role's applies again.
	*now = time.Unix(2000, 0)
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	wantPushes(t, "turn end", rec.take(), "timeout:1h0m0s", "deadline:2300")
}

func TestIdlePushRoleTimeoutChange(t *testing.T) {
	sup, store, _, rec := raiseWithIdleSink(t, TurnEndPolicy{IdleTimeout: time.Hour})
	role, _ := store.GetRole("default", "te")
	role.TurnEnd.IdleTimeout = 10 * time.Minute
	if err := store.PutRole("default", role); err != nil {
		t.Fatal(err)
	}
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPushes(t, "reconcile", rec.take(), "timeout:10m0s")
}

// Any change of the deadline while holding or waiting is pushed, disarming
// included; nothing is pushed while running.
func TestIdleDeadlinePush(t *testing.T) {
	at, later := time.Unix(5000, 0), time.Unix(6000, 0)
	cases := []struct {
		name     string
		old, cur Instance
		want     time.Time
		push     bool
	}{
		{"arm at turn end", Instance{Activity: ActivityRunning}, Instance{Activity: ActivityWaiting, IdleDeadline: at}, at, true},
		{"turn end without timeout", Instance{Activity: ActivityRunning}, Instance{Activity: ActivityHolding}, time.Time{}, true},
		{"arm while waiting", Instance{Activity: ActivityWaiting}, Instance{Activity: ActivityWaiting, IdleDeadline: at}, at, true},
		{"change while holding", Instance{Activity: ActivityHolding, IdleDeadline: at}, Instance{Activity: ActivityHolding, IdleDeadline: later}, later, true},
		{"disarm while waiting", Instance{Activity: ActivityWaiting, IdleDeadline: at}, Instance{Activity: ActivityWaiting}, time.Time{}, true},
		{"unchanged while waiting", Instance{Activity: ActivityHolding, IdleDeadline: at}, Instance{Activity: ActivityWaiting, IdleDeadline: at}, time.Time{}, false},
		{"turn start", Instance{Activity: ActivityWaiting, IdleDeadline: at}, Instance{Activity: ActivityRunning}, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, push := idleDeadlinePush(c.old, c.cur)
			if push != c.push || !got.Equal(c.want) {
				t.Fatalf("got %v, %v; want %v, %v", got, push, c.want, c.push)
			}
		})
	}
}
