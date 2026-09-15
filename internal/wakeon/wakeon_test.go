package wakeon

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/msglog"
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

// fakeInbox scripts ReadInboxSince per actor ref, standing in for *msglog.Log.
// The id > afterID filter mirrors the real backend's string-compare semantics.
type fakeInbox struct {
	byActor map[string][]msglog.Message // actor ref → its inbox
}

func (f *fakeInbox) ReadInboxSince(t msglog.Target, afterID string, limit int) []msglog.Message {
	var out []msglog.Message
	for _, m := range f.byActor[t.Ref] {
		if m.ID <= afterID {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// extInbound is an external-origin (human) inbound message addressed to coveID,
// with the given log id (compared lexically against WaitCursor by ReadInboxSince).
func extInbound(coveID, id string) msglog.Message {
	return msglog.Message{
		ID:   id,
		From: msglog.Target{Kind: "human", Ref: "alice"},
		To:   []msglog.Target{{Kind: "actor", Ref: coveID}},
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

func TestTick_ExternalReplyAfterWaitCursor_Wakes(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitCursor: "id-5"},
	}}
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{
		"a1": {extInbound("a1", "id-6")},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: 10 * time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) }

	e.tick(context.Background())

	if !contains(wake.woke, "a1") {
		t.Fatalf("an external reply with id > WaitCursor must Wake the cove, got wake=%v", wake.woke)
	}
	if len(idler.idled) != 0 || len(idler.resumed) != 0 || len(reap.down) != 0 {
		t.Errorf("expected only Wake, got idle=%v resume=%v teardown=%v", idler.idled, idler.resumed, reap.down)
	}
}

func TestTick_PreBaselineInbound_NoWake_IdlesPastWarmTimeout(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitCursor: "id-5"},
	}}
	// inbound at/before the WaitCursor baseline → not a reply
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{
		"a1": {extInbound("a1", "id-5")},
	}}
	wake := &fakeWaker{}
	reap := &fakeReaper{}
	idler := &fakeIdler{}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: 30 * time.Second}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) } // > WaitingSince + WarmTimeout

	e.tick(context.Background())

	if contains(wake.woke, "a1") {
		t.Fatal("pre-baseline inbound (id <= WaitCursor) must not wake")
	}
	if !contains(idler.idled, "a1") {
		t.Fatal("no reply past warm-timeout → Idle")
	}
}

func TestTick_InternalOriginInbound_NoWake(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "a1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitCursor: "id-5"},
	}}
	internal := msglog.Message{
		ID:   "id-6",
		From: msglog.Target{Kind: "actor", Ref: "a2"},
		To:   []msglog.Target{{Kind: "actor", Ref: "a1"}},
		Body: "internal",
	}
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{"a1": {internal}}}
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
		{ActorID: "a1", Phase: harbor.PhaseIdled, Activity: harbor.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitCursor: "id-5"},
	}}
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{
		"a1": {extInbound("a1", "id-6")},
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
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{
		"a1": {extInbound("a1", "id-1")},
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
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{}} // no reply
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
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{}} // no reply
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
			inbox := &fakeInbox{byActor: map[string][]msglog.Message{
				"a1": {extInbound("a1", "id-1")},
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
