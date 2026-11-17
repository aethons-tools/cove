package wakeon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// fakeSizes serves each actor's context size.
type fakeSizes map[string]ContextSize

func (f fakeSizes) ContextSize(actorID string) (ContextSize, error) {
	cs, ok := f[actorID]
	if !ok {
		return ContextSize{}, errors.New("no events")
	}
	return cs, nil
}

// fakeWarm mirrors Supervisor.MarkWarmWake and CancelWarm on the registry's
// instances.
type fakeWarm struct {
	reg       *fakeReg
	cancelled []string
}

func (f *fakeWarm) inst(id string) *jam.Instance {
	for i := range f.reg.insts {
		if f.reg.insts[i].ActorID == id {
			return &f.reg.insts[i]
		}
	}
	return nil
}

func (f *fakeWarm) MarkWarmWake(id, kind string, resets int) error {
	if i := f.inst(id); i != nil {
		i.Warm.Pending, i.Warm.Resets = kind, resets
	}
	return nil
}

func (f *fakeWarm) CancelWarm(id string) error {
	f.cancelled = append(f.cancelled, id)
	if i := f.inst(id); i != nil {
		i.Warm = jam.KeepWarm{}
	}
	return nil
}

type fakeCompactor struct{ compacted []string }

func (f *fakeCompactor) Compact(_ context.Context, inst jam.Instance) error {
	f.compacted = append(f.compacted, inst.ActorID)
	return nil
}

// warmNow is the engine's clock in these tests (turnEndEngine's).
var warmNow = time.Unix(10000, 0)

// warmRoles keeps p/r warm above 30000 tokens.
var warmRoles = fakeRoles{"p/r": {Cache: jam.CachePolicy{TTL: time.Hour, KeepWarm: 4 * time.Hour, Threshold: 30000},
	TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Hour}}}

// waitingWarm is a session waiting since 9000 with the given schedule.
func waitingWarm(k jam.KeepWarm) jam.Instance {
	return jam.Instance{ActorID: "a1", Project: "p", Role: "r", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0), WaitSeq: 5, Warm: k}
}

func warmEngine(inst jam.Instance, inbox Inbox, sizes fakeSizes) (*Engine, *fakeWaker, *fakeIdler, *fakeWarm, *fakeCompactor) {
	e, wake, _, idler, _ := turnEndEngine([]jam.Instance{inst}, inbox, warmRoles)
	e.cfg.WarmTimeout = time.Minute
	warm, comp := &fakeWarm{reg: e.reg.(*fakeReg)}, &fakeCompactor{}
	e.SetKeepWarm(sizes, warm, comp)
	return e, wake, idler, warm, comp
}

func (e *Engine) instance() jam.Instance { return e.reg.(*fakeReg).insts[0] }

func wantWake(t *testing.T, wake *fakeWaker, kinds ...string) {
	t.Helper()
	var got []string
	for _, rs := range wake.reasons {
		for _, r := range rs {
			got = append(got, r.Kind)
		}
	}
	if len(got) != len(kinds) {
		t.Fatalf("wakes = %v, want %v", got, kinds)
	}
	for i := range got {
		if got[i] != kinds[i] {
			t.Fatalf("wakes = %v, want %v", got, kinds)
		}
	}
}

func TestKeepWarm_WakesWhenDue(t *testing.T) {
	e, wake, idler, _, _ := warmEngine(waitingWarm(jam.KeepWarm{Until: warmNow.Add(3 * time.Hour), Next: warmNow}), nil,
		fakeSizes{"a1": {Tokens: 40000}})
	e.tick(context.Background())
	wantWake(t, wake, jam.WakeKeepWarm)
	if got := e.instance().Warm.Pending; got != jam.WakeKeepWarm {
		t.Fatalf("pending = %q", got)
	}
	if len(idler.idled) != 0 {
		t.Fatalf("paused the session it just woke: %v", idler.idled)
	}
	// Re-sent until the session runs, as the idle wake is.
	e.tick(context.Background())
	wantWake(t, wake, jam.WakeKeepWarm, jam.WakeKeepWarm)
}

func TestKeepWarm_NotYetDue(t *testing.T) {
	e, wake, _, warm, _ := warmEngine(waitingWarm(jam.KeepWarm{Until: warmNow.Add(3 * time.Hour), Next: warmNow.Add(time.Second)}), nil,
		fakeSizes{"a1": {Tokens: 40000}})
	e.tick(context.Background())
	wantWake(t, wake)
	if len(warm.cancelled) != 0 {
		t.Fatalf("cancelled = %v", warm.cancelled)
	}
}

// Below the role's threshold the session isn't kept warm, nor asked to
// compact.
func TestKeepWarm_BelowThresholdCancels(t *testing.T) {
	for _, k := range []jam.KeepWarm{
		{Until: warmNow.Add(3 * time.Hour), Next: warmNow},
		{Until: warmNow.Add(30 * time.Second), Next: warmNow.Add(time.Hour)},
	} {
		e, wake, _, warm, _ := warmEngine(waitingWarm(k), nil, fakeSizes{"a1": {Tokens: 29999}})
		e.tick(context.Background())
		wantWake(t, wake)
		if len(warm.cancelled) != 1 {
			t.Fatalf("cancelled = %v, want the schedule dropped", warm.cancelled)
		}
	}
}

// A turn that ended with turn_end next: compact leaves no context size (a
// reset is the latest change): no schedule, even with no threshold.
func TestKeepWarm_CompactedTurnStartsNone(t *testing.T) {
	e, wake, _, warm, _ := warmEngine(waitingWarm(jam.KeepWarm{Until: warmNow.Add(3 * time.Hour), Next: warmNow}), nil,
		fakeSizes{"a1": {Tokens: 0, Resets: 1}})
	e.roles = fakeRoles{"p/r": {Cache: jam.CachePolicy{TTL: time.Hour, KeepWarm: 4 * time.Hour}}}
	e.tick(context.Background())
	wantWake(t, wake)
	if len(warm.cancelled) != 1 {
		t.Fatalf("cancelled = %v", warm.cancelled)
	}
}

func TestKeepWarm_CompactDue(t *testing.T) {
	e, wake, _, _, _ := warmEngine(waitingWarm(jam.KeepWarm{Until: warmNow.Add(time.Minute), Next: warmNow.Add(time.Hour)}), nil,
		fakeSizes{"a1": {Tokens: 40000, Resets: 2}})
	e.tick(context.Background())
	wantWake(t, wake, jam.WakeCompactDue)
	if k := e.instance().Warm; k.Pending != jam.WakeCompactDue || k.Resets != 2 {
		t.Fatalf("warm = %+v", k)
	}
}

// A paused studio is resumed first, and woken once it is live again.
func TestKeepWarm_ResumesPausedSession(t *testing.T) {
	inst := waitingWarm(jam.KeepWarm{Until: warmNow.Add(3 * time.Hour), Next: warmNow})
	inst.Phase = jam.PhaseIdled
	e, wake, idler, _, _ := warmEngine(inst, nil, fakeSizes{"a1": {Tokens: 40000}})
	e.tick(context.Background())
	if len(idler.resumed) != 1 || len(wake.woke) != 0 {
		t.Fatalf("resumed=%v woke=%v; want resume now, wake later", idler.resumed, wake.woke)
	}
	e.reg.(*fakeReg).insts[0].Phase = jam.PhaseLive
	e.tick(context.Background())
	wantWake(t, wake, jam.WakeKeepWarm)
}

// Any other wake cancels the schedule, and the keep-warm reason isn't sent
// beside it.
func TestKeepWarm_OtherWakeCancels(t *testing.T) {
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{"a1": {extInbound("a1", 6, "id-6")}}}
	e, wake, _, warm, _ := warmEngine(waitingWarm(jam.KeepWarm{Until: warmNow.Add(3 * time.Hour), Next: warmNow, Pending: jam.WakeKeepWarm}), inbox,
		fakeSizes{"a1": {Tokens: 40000}})
	e.tick(context.Background())
	wantWake(t, wake, jam.WakeSquawk)
	if len(warm.cancelled) != 1 || e.instance().Warm.Armed() {
		t.Fatalf("cancelled = %v, warm %+v", warm.cancelled, e.instance().Warm)
	}

	idle := waitingWarm(jam.KeepWarm{Until: warmNow.Add(3 * time.Hour), Next: warmNow})
	idle.IdleDeadline = warmNow
	e, wake, _, warm, _ = warmEngine(idle, nil, fakeSizes{"a1": {Tokens: 40000}})
	e.tick(context.Background())
	wantWake(t, wake, jam.WakeIdle)
	if len(warm.cancelled) != 1 {
		t.Fatalf("idle: cancelled = %v", warm.cancelled)
	}
}

// A compact-due turn that ended without compacting: once its events are in,
// Jam compacts the session itself, and the schedule ends.
func TestKeepWarm_IgnoredCompactDueCompacts(t *testing.T) {
	k := jam.KeepWarm{Until: warmNow.Add(30 * time.Second), Next: warmNow.Add(time.Hour), Resets: 2, CompactCheck: warmNow.Add(-5 * time.Second)}
	e, wake, idler, warm, comp := warmEngine(waitingWarm(k), nil, fakeSizes{"a1": {Tokens: 45000, Resets: 2}})
	e.tick(context.Background())
	if len(comp.compacted) != 0 || len(wake.woke) != 0 || len(idler.idled) != 0 {
		t.Fatalf("within the grace: compacted=%v woke=%v paused=%v; want nothing yet", comp.compacted, wake.woke, idler.idled)
	}
	e.now = func() time.Time { return warmNow.Add(compactCheckDelay) }
	e.tick(context.Background())
	if len(comp.compacted) != 1 || len(warm.cancelled) != 1 || len(wake.woke) != 0 {
		t.Fatalf("compacted=%v cancelled=%v woke=%v", comp.compacted, warm.cancelled, wake.woke)
	}
}

// A compact-due turn that compacted (its events show a reset since the
// wake) only ends the schedule.
func TestKeepWarm_AnsweredCompactDueEnds(t *testing.T) {
	k := jam.KeepWarm{Until: warmNow.Add(30 * time.Second), Next: warmNow.Add(time.Hour), Resets: 2, CompactCheck: warmNow.Add(-time.Minute)}
	e, wake, _, warm, comp := warmEngine(waitingWarm(k), nil, fakeSizes{"a1": {Tokens: 3000, Resets: 4}})
	e.tick(context.Background())
	if len(comp.compacted) != 0 || len(warm.cancelled) != 1 || len(wake.woke) != 0 {
		t.Fatalf("compacted=%v cancelled=%v woke=%v", comp.compacted, warm.cancelled, wake.woke)
	}
}

// Without a schedule (a role without cache.keep-warm) nothing changes.
func TestKeepWarm_NoScheduleNoWake(t *testing.T) {
	e, wake, _, warm, comp := warmEngine(waitingWarm(jam.KeepWarm{}), nil, fakeSizes{"a1": {Tokens: 40000}})
	e.tick(context.Background())
	if len(wake.woke) != 0 || len(warm.cancelled) != 0 || len(comp.compacted) != 0 {
		t.Fatalf("woke=%v cancelled=%v compacted=%v", wake.woke, warm.cancelled, comp.compacted)
	}
}

// A keep-warm wake never resumes the agent-to-agent limit's window.
func TestKeepWarm_LeavesAgentLimitAlone(t *testing.T) {
	e, wake, _, _, _ := warmEngine(waitingWarm(jam.KeepWarm{Until: warmNow.Add(3 * time.Hour), Next: warmNow}), nil,
		fakeSizes{"a1": {Tokens: 40000}})
	res := &recordingResumes{}
	e.SetSessionWakes(nil, res, AgentWakes{Count: 3, Window: time.Minute})
	e.tick(context.Background())
	wantWake(t, wake, jam.WakeKeepWarm)
	if res.set != 0 {
		t.Fatalf("resumed the limit %d times", res.set)
	}
}

type recordingResumes struct{ set int }

func (r *recordingResumes) GetChannel(id ident.ID) (jam.Channel, bool) { return jam.Channel{}, false }
func (r *recordingResumes) SetChannelResumed(id ident.ID, at time.Time) error {
	r.set++
	return nil
}
