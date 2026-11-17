package jam

import (
	"context"
	"testing"
	"time"
)

// raiseWithCache raises w1 under a role carrying the given cache profile and
// a 30-minute idle timeout; *now is the supervisor's clock.
func raiseWithCache(t *testing.T, c CachePolicy) (*Supervisor, Store, *time.Time) {
	t.Helper()
	sup, store, now := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutRole("default", Role{Name: "kw", Scope: Scope{Destinations: []string{"anthropic"}},
		TurnEnd: TurnEndPolicy{IdleTimeout: 30 * time.Minute}, Cache: c}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "kw"}); err != nil {
		t.Fatal(err)
	}
	return sup, store, now
}

func report(t *testing.T, sup *Supervisor, a Activity) {
	t.Helper()
	if err := sup.Report(context.Background(), "w1", a); err != nil {
		t.Fatal(err)
	}
}

func TestReportTurnEndArmsKeepWarm(t *testing.T) {
	sup, store, now := raiseWithCache(t, CachePolicy{TTL: time.Hour, KeepWarm: 4 * time.Hour})
	*now = time.Unix(2000, 0)
	report(t, sup, ActivityWaiting)
	inst, _ := store.GetInstance("w1")
	if want := (KeepWarm{Until: time.Unix(2000, 0).Add(4 * time.Hour), Next: time.Unix(2000, 0).Add(59 * time.Minute)}); inst.Warm != want {
		t.Fatalf("warm = %+v, want %+v", inst.Warm, want)
	}
}

// Without a ttl the harness's default (5m) sets the cadence.
func TestReportTurnEndKeepWarmDefaultTTL(t *testing.T) {
	sup, store, now := raiseWithCache(t, CachePolicy{KeepWarm: 30 * time.Minute})
	*now = time.Unix(2000, 0)
	report(t, sup, ActivityWaiting)
	if inst, _ := store.GetInstance("w1"); !inst.Warm.Next.Equal(time.Unix(2000, 0).Add(4 * time.Minute)) {
		t.Fatalf("next = %v, want the turn end + 4m", inst.Warm.Next)
	}
}

// A role without cache.keep-warm arms nothing.
func TestReportTurnEndWithoutKeepWarmArmsNothing(t *testing.T) {
	sup, store, _ := raiseWithCache(t, CachePolicy{TTL: time.Hour})
	report(t, sup, ActivityWaiting)
	if inst, _ := store.GetInstance("w1"); inst.Warm != (KeepWarm{}) {
		t.Fatalf("warm = %+v, want none", inst.Warm)
	}
}

// A turn a keep-warm wake starts leaves everything about the session's wait
// as it was: when it began, its escalation, its nags, its wake baseline and
// its idle deadline. Only the schedule moves on.
func TestKeepWarmTurnLeavesWaitingState(t *testing.T) {
	sup, store, now := raiseWithCache(t, CachePolicy{TTL: time.Hour, KeepWarm: 4 * time.Hour})
	*now = time.Unix(2000, 0)
	report(t, sup, ActivityWaiting)
	inst, _ := store.GetInstance("w1")
	inst.EscalationTier, inst.TierPingedAt, inst.EscalationAsked = 2, time.Unix(2100, 0), true
	inst.LastNagAt, inst.Nags, inst.WaitSeq = time.Unix(2200, 0), 3, 42
	inst.MessagesMuted = true
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetInstance("w1")

	*now = before.Warm.Next
	if err := sup.MarkWarmWake("w1", WakeKeepWarm, 0); err != nil {
		t.Fatal(err)
	}
	report(t, sup, ActivityRunning)
	*now = now.Add(10 * time.Second)
	report(t, sup, ActivityWaiting)

	got, _ := store.GetInstance("w1")
	if !got.WaitingSince.Equal(before.WaitingSince) || got.EscalationTier != 2 || !got.TierPingedAt.Equal(before.TierPingedAt) ||
		!got.EscalationAsked || !got.LastNagAt.Equal(before.LastNagAt) || got.Nags != 3 || got.WaitSeq != 42 || !got.MessagesMuted ||
		!got.IdleDeadline.Equal(before.IdleDeadline) || !got.TurnEndedAt.Equal(before.TurnEndedAt) {
		t.Fatalf("a keep-warm turn changed the wait:\nbefore %+v\nafter  %+v", before, got)
	}
	if want := (KeepWarm{Until: before.Warm.Until, Next: before.Warm.Next.Add(59 * time.Minute)}); got.Warm != want {
		t.Fatalf("warm = %+v, want %+v", got.Warm, want)
	}
}

// Any other wake cancels the schedule: the turn it starts is an ordinary
// one, and its end arms a fresh schedule.
func TestOtherWakeCancelsKeepWarm(t *testing.T) {
	sup, store, now := raiseWithCache(t, CachePolicy{TTL: time.Hour, KeepWarm: 4 * time.Hour})
	*now = time.Unix(2000, 0)
	report(t, sup, ActivityWaiting)
	if err := sup.MarkWarmWake("w1", WakeKeepWarm, 0); err != nil {
		t.Fatal(err)
	}
	if err := sup.CancelWarm("w1"); err != nil { // a reply woke it too
		t.Fatal(err)
	}
	*now = time.Unix(3000, 0)
	report(t, sup, ActivityRunning)
	inst, _ := store.GetInstance("w1")
	if inst.Warm != (KeepWarm{}) || !inst.IdleDeadline.IsZero() {
		t.Fatalf("an ordinary turn: warm %+v, idle deadline %v", inst.Warm, inst.IdleDeadline)
	}
	*now = time.Unix(3100, 0)
	report(t, sup, ActivityWaiting)
	inst, _ = store.GetInstance("w1")
	if !inst.Warm.Until.Equal(time.Unix(3100, 0).Add(4*time.Hour)) || !inst.WaitingSince.Equal(time.Unix(3100, 0)) {
		t.Fatalf("turn end: warm %+v, waiting since %v", inst.Warm, inst.WaitingSince)
	}
}

// The end of a compact-due turn is when Jam checks it compacted.
func TestCompactDueTurnEndMarksCheck(t *testing.T) {
	sup, store, now := raiseWithCache(t, CachePolicy{TTL: time.Hour, KeepWarm: 4 * time.Hour})
	*now = time.Unix(2000, 0)
	report(t, sup, ActivityWaiting)
	if err := sup.MarkWarmWake("w1", WakeCompactDue, 2); err != nil {
		t.Fatal(err)
	}
	report(t, sup, ActivityRunning)
	*now = time.Unix(2030, 0)
	report(t, sup, ActivityWaiting)
	inst, _ := store.GetInstance("w1")
	if !inst.Warm.CompactCheck.Equal(time.Unix(2030, 0)) || inst.Warm.Resets != 2 || inst.Warm.Turn != "" || inst.Warm.Pending != "" {
		t.Fatalf("warm = %+v, want a compact check at the turn's end", inst.Warm)
	}
	if !inst.WaitingSince.Equal(time.Unix(2000, 0)) {
		t.Fatalf("waiting since %v moved", inst.WaitingSince)
	}
}

func TestKeepWarmDue(t *testing.T) {
	k := KeepWarm{Until: time.Unix(10000, 0), Next: time.Unix(5000, 0)}
	for _, c := range []struct {
		at   int64
		want string
	}{{4999, ""}, {5000, WakeKeepWarm}, {9939, WakeKeepWarm}, {9940, WakeCompactDue}, {20000, WakeCompactDue}} {
		if got := k.Due(time.Unix(c.at, 0)); got != c.want {
			t.Errorf("Due(%d) = %q, want %q", c.at, got, c.want)
		}
	}
	if got := (KeepWarm{}).Due(time.Unix(1, 0)); got != "" {
		t.Errorf("unarmed Due = %q", got)
	}
}

// A reply that lands during a keep-warm turn makes it an ordinary turn: the
// cancel applies the turn-start resets the warm wake skipped (an escalate ask
// is answered, the idle deadline disarmed), and its end starts a new wait.
func TestReplyDuringKeepWarmTurnStartsOrdinaryTurn(t *testing.T) {
	sup, store, now := raiseWithCache(t, CachePolicy{TTL: time.Hour, KeepWarm: 4 * time.Hour})
	*now = time.Unix(2000, 0)
	report(t, sup, ActivityWaiting)
	inst, _ := store.GetInstance("w1")
	inst.EscalationAsked, inst.MessagesMuted = true, true
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	if err := sup.MarkWarmWake("w1", WakeKeepWarm, 0); err != nil {
		t.Fatal(err)
	}
	*now = time.Unix(5000, 0)
	report(t, sup, ActivityRunning)
	if err := sup.CancelWarm("w1"); err != nil { // the reply's wake, mid-turn
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	if got.EscalationAsked || got.MessagesMuted || !got.IdleDeadline.IsZero() || got.Warm != (KeepWarm{}) {
		t.Fatalf("after the reply: asked %v, muted %v, idle deadline %v, warm %+v",
			got.EscalationAsked, got.MessagesMuted, got.IdleDeadline, got.Warm)
	}
	*now = time.Unix(5100, 0)
	report(t, sup, ActivityWaiting)
	got, _ = store.GetInstance("w1")
	if !got.WaitingSince.Equal(time.Unix(5100, 0)) || !got.IdleDeadline.Equal(time.Unix(5100, 0).Add(30*time.Minute)) ||
		!got.Warm.Until.Equal(time.Unix(5100, 0).Add(4*time.Hour)) {
		t.Fatalf("turn end: waiting since %v, idle deadline %v, warm %+v", got.WaitingSince, got.IdleDeadline, got.Warm)
	}
}
