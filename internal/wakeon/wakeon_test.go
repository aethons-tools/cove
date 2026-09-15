package wakeon

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/intercom"
)

type fakeReg struct{ insts []harbor.Instance }

func (f *fakeReg) ListInstances() []harbor.Instance { return f.insts }

type fakeWaker struct{ woke []string }

func (f *fakeWaker) Wake(a string) { f.woke = append(f.woke, a) }

type fakeReaper struct{ down []string }

func (f *fakeReaper) Teardown(_ context.Context, a string) error {
	f.down = append(f.down, a)
	return nil
}

type fakeIdler struct {
	idled   []string
	resumed []string
}

func (f *fakeIdler) Idle(_ context.Context, a string) error { f.idled = append(f.idled, a); return nil }
func (f *fakeIdler) Resume(_ context.Context, a string) error {
	f.resumed = append(f.resumed, a)
	return nil
}

// fakeInbox scripts ReadInboxSince per actor ref, standing in for *intercom.Log.
// The Seq > afterSeq filter mirrors the real backend's append-order semantics
// (Seq, never the id, decides ordering — see extInbound/COV-184).
type fakeInbox struct {
	byActor map[string][]intercom.Squawk // actor ref → its inbox
}

func (f *fakeInbox) ReadInboxSince(t intercom.Target, afterSeq int64, limit int) []intercom.Squawk {
	var out []intercom.Squawk
	for _, m := range f.byActor[t.Ref] {
		if m.Seq <= afterSeq {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// extInbound is an external-origin (human) inbound message addressed to
// coveID, with the given append-order Seq (compared against WaitSeq by
// ReadInboxSince) and log id. The id is deliberately NOT required to sort
// lexically consistent with seq — see the COV-184 regression test below,
// which exploits exactly that to prove ordering is Seq-based, not id-based.
func extInbound(coveID string, seq int64, id string) intercom.Squawk {
	return intercom.Squawk{
		Seq:  seq,
		ID:   id,
		From: intercom.Target{Kind: "human", Ref: "alice"},
		To:   []intercom.Target{{Kind: "actor", Ref: coveID}},
		Body: "reply",
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestTick_ExternalReplyAfterWaitSeq_Wakes(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
		"a1": {extInbound("a1", 6, "id-6")},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: 10 * time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) }

	e.tick(context.Background())

	if !contains(wake.woke, "a1") {
		t.Fatalf("an external reply with Seq > WaitSeq must Wake the cove, got wake=%v", wake.woke)
	}
	if len(idler.idled) != 0 || len(idler.resumed) != 0 || len(reap.down) != 0 {
		t.Errorf("expected only Wake, got idle=%v resume=%v teardown=%v", idler.idled, idler.resumed, reap.down)
	}
}

// TestTick_COV184_ExternalReplyWakesRegardlessOfLexicalIDOrder is the
// COV-184 regression: before the append-Seq fix, a reply was judged "after"
// the wake-on baseline by comparing message ids lexically. That's wrong once
// ids can come from different, non-interleaved id-namespaces (e.g. an
// ingress adapter minting "in:discord:..." ids alongside the log's own
// "<unixnano>-<rand>" ids) — a later-arriving reply can easily have an id
// that sorts BEFORE an earlier baseline id, so the lexical compare would
// silently never wake the cove. Seq is the log's actual append order and is
// immune to this: here the reply's id ("in:discord:aaa") sorts lexically
// BEFORE a stand-in baseline id ("in:linear:zzz") it logically follows, yet
// its Seq (6) is still greater than WaitSeq (5) — and that must be enough to
// wake.
func TestTick_COV184_ExternalReplyWakesRegardlessOfLexicalIDOrder(t *testing.T) {
	const baselineID = "in:linear:zzz" // hypothetical id the WaitSeq=5 baseline would have carried
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
	}}
	replyID := "in:discord:aaa"
	if replyID >= baselineID {
		t.Fatalf("test setup invariant broken: replyID %q must sort lexically BEFORE baselineID %q", replyID, baselineID)
	}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
		"a1": {extInbound("a1", 6, replyID)},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: 10 * time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) }

	e.tick(context.Background())

	if !contains(wake.woke, "a1") {
		t.Fatalf("Seq(6) > WaitSeq(5) must wake even though the reply id %q sorts lexically before %q — got wake=%v", replyID, baselineID, wake.woke)
	}
}

func TestTick_PreBaselineInbound_NoWake_IdlesPastWarmTimeout(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
	}}
	// inbound at the WaitSeq baseline (Seq == WaitSeq, not >) → not a reply
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
		"a1": {extInbound("a1", 5, "id-5")},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: 30 * time.Second}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) } // > WaitingSince + WarmTimeout

	e.tick(context.Background())

	if contains(wake.woke, "a1") {
		t.Fatal("pre-baseline inbound (Seq <= WaitSeq) must not wake")
	}
	if !contains(idler.idled, "a1") {
		t.Fatal("no reply past warm-timeout → Idle")
	}
}

func TestTick_InternalOriginInbound_NoWake(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
	}}
	internal := intercom.Squawk{
		Seq:  6,
		ID:   "id-6",
		From: intercom.Target{Kind: "actor", Ref: "a2"},
		To:   []intercom.Target{{Kind: "actor", Ref: "a1"}},
		Body: "internal",
	}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{"a1": {internal}}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: time.Hour}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) }

	e.tick(context.Background())

	if contains(wake.woke, "a1") {
		t.Fatal("internal-origin inbound must not wake")
	}
}

func TestTick_IdledWaiting_ExternalReply_Resumes_NoDirectWake(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseIdled, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
		"a1": {extInbound("a1", 6, "id-6")},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: time.Hour}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) }

	e.tick(context.Background())

	if !contains(idler.resumed, "a1") {
		t.Fatalf("Idled + reply → Resume (not Wake), got resume=%v", idler.resumed)
	}
	if contains(wake.woke, "a1") {
		t.Fatal("must not directly Wake an Idled instance")
	}
	if len(idler.idled) != 0 {
		t.Errorf("Idle called unexpectedly: %v", idler.idled)
	}
}

func TestTick_NilInbox_NoWake_StillTeardownAtMaxWait(t *testing.T) {
	waitStart := time.Unix(0, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, nil /* nil inbox */, Config{MaxWait: time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(1_000_000, 0) } // way past max-wait

	e.tick(context.Background())

	if !contains(reap.down, "a1") {
		t.Fatal("nil inbox must still teardown at max-wait")
	}
	if contains(wake.woke, "a1") {
		t.Fatal("nil inbox must never wake")
	}
}

func TestTick_NilInbox_NoReplyWaking_ButPauseStillRuns(t *testing.T) {
	waitStart := time.Unix(0, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, nil /* nil inbox */, Config{MaxWait: time.Hour, WarmTimeout: time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(0, 0).Add(2 * time.Minute) } // past WarmTimeout, within MaxWait

	e.tick(context.Background())

	if contains(wake.woke, "a1") {
		t.Fatal("nil inbox must never wake")
	}
	if !contains(idler.idled, "a1") {
		t.Fatal("nil inbox must still allow warm-timeout Idle (pause)")
	}
	if len(reap.down) != 0 {
		t.Errorf("Teardown called unexpectedly: %v", reap.down)
	}
}

func TestTick_NonWaitingIgnored(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Activity: harbor.ActivityRunning, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
		"a1": {extInbound("a1", 1, "id-1")},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{}, nil)

	e.tick(context.Background())

	if len(wake.woke) != 0 || len(reap.down) != 0 || len(idler.idled) != 0 || len(idler.resumed) != 0 {
		t.Errorf("expected no side effects for non-Waiting instance, got wake=%v reap=%v idle=%v resume=%v",
			wake.woke, reap.down, idler.idled, idler.resumed)
	}
}

func TestTick_LiveWaiting_PastWarmTimeout_NoReply_Idles(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{}} // no reply
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: 30 * time.Minute, WarmTimeout: 1 * time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(0, 0).Add(2 * time.Minute) } // past WarmTimeout, within MaxWait

	e.tick(context.Background())

	if len(idler.idled) != 1 || idler.idled[0] != "a1" {
		t.Errorf("Idle = %v, want [a1]", idler.idled)
	}
	if len(idler.resumed) != 0 {
		t.Errorf("Resume called unexpectedly: %v", idler.resumed)
	}
	if len(wake.woke) != 0 {
		t.Errorf("Wake called unexpectedly: %v", wake.woke)
	}
	if len(reap.down) != 0 {
		t.Errorf("Teardown called unexpectedly: %v", reap.down)
	}
}

func TestTick_IdledWaiting_NoReply_WithinMaxWait_NoOp(t *testing.T) {
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseIdled, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{}} // no reply
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: 30 * time.Minute, WarmTimeout: 1 * time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(0, 0).Add(2 * time.Minute) } // past WarmTimeout, within MaxWait

	e.tick(context.Background())

	if len(idler.idled) != 0 {
		t.Errorf("Idle called unexpectedly on an already-Idled instance: %v", idler.idled)
	}
	if len(idler.resumed) != 0 {
		t.Errorf("Resume called unexpectedly: %v", idler.resumed)
	}
	if len(wake.woke) != 0 {
		t.Errorf("Wake called unexpectedly: %v", wake.woke)
	}
	if len(reap.down) != 0 {
		t.Errorf("Teardown called unexpectedly: %v", reap.down)
	}
}

func TestTick_PastMaxWait_TeardownRegardlessOfPhase(t *testing.T) {
	for _, phase := range []harbor.Phase{harbor.PhaseLive, harbor.PhaseIdled} {
		t.Run(string(phase), func(t *testing.T) {
			reg := &fakeReg{insts: []harbor.Instance{
				{ActorID: "a1", Phase: phase, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
			}}
			// even with a pending reply, max-wait teardown wins
			inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
				"a1": {extInbound("a1", 1, "id-1")},
			}}
			wake := &fakeWaker{}
			reap := &fakeReaper{}
			idler := &fakeIdler{}
			e := New(reg, wake, reap, idler, inbox, Config{MaxWait: 30 * time.Minute, WarmTimeout: 1 * time.Minute}, nil)
			e.now = func() time.Time { return time.Unix(0, 0).Add(31 * time.Minute) }

			e.tick(context.Background())

			if len(reap.down) != 1 || reap.down[0] != "a1" {
				t.Errorf("Teardown = %v, want [a1]", reap.down)
			}
			if len(idler.idled) != 0 || len(idler.resumed) != 0 || len(wake.woke) != 0 {
				t.Errorf("expected only Teardown, got idle=%v resume=%v wake=%v", idler.idled, idler.resumed, wake.woke)
			}
		})
	}
}
