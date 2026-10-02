package wakeon

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// meSendStore is the participantSendStore the participant send handler needs,
// backing the wake-on-through-the-real-send-path integration test below.
type meSendStore struct {
	rosters   map[string]jam.Roster
	instances []jam.Instance
}

func (s meSendStore) GetRoster(project string) (jam.Roster, bool) {
	r, ok := s.rosters[project]
	return r, ok
}
func (s meSendStore) ListInstances() []jam.Instance { return s.instances }

// TestParticipantSendWakesWaitingStudio is the COV-200 wake-on integration
// check: a participant's send, appended to the SAME squawk Log through the real
// jam.ParticipantSendHandler and addressed to a waiting studio's session actor,
// wakes that studio on the next wake-on tick — exactly as a relayed reply does.
func TestParticipantSendWakesWaitingStudio(t *testing.T) {
	lg := intercom.NewMemLog()

	const issuer, subject = "https://idp.example", "sub-alice"
	inst := jam.Instance{
		ActorID: "a1", Project: "acme", Unit: "ACME-1",
		Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		WaitingSince: time.Unix(1990, 0), WaitSeq: 0, // baseline: empty log tail
	}
	store := meSendStore{
		rosters: map[string]jam.Roster{"acme": {Humans: []jam.Human{
			{Name: "alice", Identity: []jam.OIDCIdentity{{Issuer: issuer, Subject: subject}}},
		}}},
		instances: []jam.Instance{inst},
	}

	// The participant replies to the waiting studio via the real send handler.
	h := jam.NewParticipantSendHandler(store, lg, nil)
	r := httptest.NewRequest("POST", "/me/send", strings.NewReader(`{"to":"studio:ACME-1","body":"go ahead"}`))
	r = jam.WithParticipant(r, jam.Participant{Issuer: issuer, Subject: subject, Projects: []string{"acme"}, Name: "alice"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("send status = %d, want 204 (%s)", w.Code, w.Body.String())
	}

	// Wake-on, reading the same Log, must wake the studio (its reply landed at
	// Seq 1 > WaitSeq 0, external-origin, addressed to actor:a1).
	reg := &fakeReg{insts: []jam.Instance{inst}}
	wake := &fakeWaker{}
	e := New(reg, wake, &fakeReaper{}, &fakeIdler{}, lg, Config{MaxWait: time.Hour, WarmTimeout: 10 * time.Minute}, nil)
	e.now = func() time.Time { return time.Unix(2000, 0) }
	e.tick(context.Background())

	if !contains(wake.woke, "a1") {
		t.Fatalf("a participant reply appended through the send path must wake the studio, got wake=%v", wake.woke)
	}
}

type fakeReg struct{ insts []jam.Instance }

func (f *fakeReg) ListInstances() []jam.Instance { return f.insts }

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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart, WaitSeq: 5},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Activity: jam.ActivityRunning, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
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
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
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
	for _, phase := range []jam.Phase{jam.PhaseLive, jam.PhaseIdled} {
		t.Run(string(phase), func(t *testing.T) {
			reg := &fakeReg{insts: []jam.Instance{
				{ActorID: "a1", Phase: phase, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: time.Unix(0, 0)},
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

// TestTick_PersonalSessionNotReapedPastMaxWait: a personal session waits on its
// owner indefinitely — past MaxWait it is not torn down, but is still idled
// after the warm-timeout and woken (resumed, then woken) on a reply. An
// ephemeral cove past MaxWait is still torn down.
func TestTick_PersonalSessionNotReapedPastMaxWait(t *testing.T) {
	waitStart := time.Unix(1000, 0)
	now := func() time.Time { return waitStart.Add(2 * time.Hour) } // past MaxWait (1h)
	cfg := Config{MaxWait: time.Hour, WarmTimeout: time.Minute}

	t.Run("idled, not reaped", func(t *testing.T) {
		reg := &fakeReg{insts: []jam.Instance{
			{ActorID: "p1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, SessionKind: jam.SessionKindPersonal, Owner: "alice", WaitingSince: waitStart},
			{ActorID: "e1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Unit: "AET-1", WaitingSince: waitStart},
		}}
		reap, idler := &fakeReaper{}, &fakeIdler{}
		e := New(reg, &fakeWaker{}, reap, idler, &fakeInbox{}, cfg, nil)
		e.now = now
		e.tick(context.Background())
		if contains(reap.down, "p1") {
			t.Fatalf("personal session torn down for waiting: %v", reap.down)
		}
		if !contains(idler.idled, "p1") {
			t.Fatalf("personal session past warm-timeout must still be idled; idled=%v", idler.idled)
		}
		if !contains(reap.down, "e1") {
			t.Fatalf("ephemeral cove past MaxWait must still be torn down; down=%v", reap.down)
		}
	})

	t.Run("idled + reply resumes; live + reply wakes", func(t *testing.T) {
		inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
			"p1": {extInbound("p1", 6, "id-6")},
			"p2": {extInbound("p2", 6, "id-6")},
		}}
		reg := &fakeReg{insts: []jam.Instance{
			{ActorID: "p1", Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting, SessionKind: jam.SessionKindPersonal, WaitingSince: waitStart, WaitSeq: 5},
			{ActorID: "p2", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, SessionKind: jam.SessionKindPersonal, WaitingSince: waitStart, WaitSeq: 5},
		}}
		wake, reap, idler := &fakeWaker{}, &fakeReaper{}, &fakeIdler{}
		e := New(reg, wake, reap, idler, inbox, cfg, nil)
		e.now = now
		e.tick(context.Background())
		if len(reap.down) != 0 {
			t.Fatalf("personal sessions torn down: %v", reap.down)
		}
		if !contains(idler.resumed, "p1") || !contains(wake.woke, "p2") {
			t.Fatalf("want p1 resumed and p2 woken; resumed=%v woke=%v", idler.resumed, wake.woke)
		}
	})
}

// --- personal-session idle ladder (session-kinds slice 4) ---

type fakeRoles map[string]jam.Role // "project/role" → role

func (f fakeRoles) GetRole(project, name string) (jam.Role, bool) {
	r, ok := f[project+"/"+name]
	return r, ok
}

type fakeNagRecorder struct {
	at   map[string][]time.Time
	kept []keepCall
	regs *fakeReg // when set, RecordNag/KeepWaiting also update the registry's instance (as the real store would)
}

type keepCall struct {
	actor    string
	afterSeq int64
	at       time.Time
}

func (f *fakeNagRecorder) KeepWaiting(actorID string, afterSeq int64, at time.Time) error {
	f.kept = append(f.kept, keepCall{actorID, afterSeq, at})
	if f.regs != nil {
		for i := range f.regs.insts {
			if f.regs.insts[i].ActorID == actorID {
				f.regs.insts[i].WaitSeq = afterSeq
				f.regs.insts[i].WaitingSince = at
				f.regs.insts[i].LastNagAt = time.Time{}
				f.regs.insts[i].Nags = 0
			}
		}
	}
	return nil
}

func (f *fakeNagRecorder) RecordNag(actorID string, at time.Time) error {
	if f.at == nil {
		f.at = map[string][]time.Time{}
	}
	f.at[actorID] = append(f.at[actorID], at)
	if f.regs != nil {
		for i := range f.regs.insts {
			if f.regs.insts[i].ActorID == actorID {
				f.regs.insts[i].LastNagAt = at
				f.regs.insts[i].Nags++
			}
		}
	}
	return nil
}

type nagCall struct {
	kind  string // "nag" | "reclaimed" | "kept" | "released"
	actor string
	idle  time.Duration
}

type fakeNagger struct {
	calls []nagCall
	fail  error
	log   *[]string // shared event log, to assert notice-before-teardown ordering
}

func (f *fakeNagger) Nag(_ context.Context, inst jam.Instance, idle time.Duration) error {
	if f.fail != nil {
		return f.fail
	}
	f.calls = append(f.calls, nagCall{"nag", inst.ActorID, idle})
	return nil
}

func (f *fakeNagger) NotifyReclaimed(_ context.Context, inst jam.Instance, idle time.Duration) error {
	if f.fail != nil {
		return f.fail
	}
	f.calls = append(f.calls, nagCall{"reclaimed", inst.ActorID, idle})
	if f.log != nil {
		*f.log = append(*f.log, "notice:"+inst.ActorID)
	}
	return nil
}

func (f *fakeNagger) NotifyKept(_ context.Context, inst jam.Instance, next time.Duration) error {
	if f.fail != nil {
		return f.fail
	}
	f.calls = append(f.calls, nagCall{"kept", inst.ActorID, next})
	return nil
}

func (f *fakeNagger) NotifyReleased(_ context.Context, inst jam.Instance) error {
	if f.fail != nil {
		return f.fail
	}
	f.calls = append(f.calls, nagCall{"released", inst.ActorID, 0})
	if f.log != nil {
		*f.log = append(*f.log, "released:"+inst.ActorID)
	}
	return nil
}

type orderedReaper struct {
	fakeReaper
	log *[]string
}

func (o *orderedReaper) Teardown(ctx context.Context, a string) error {
	*o.log = append(*o.log, "teardown:"+a)
	return o.fakeReaper.Teardown(ctx, a)
}

func personal(id string, waitingSince time.Time) jam.Instance {
	return jam.Instance{
		ActorID: id, Project: "acme", Role: "pair", Owner: "alice",
		SessionKind: jam.SessionKindPersonal,
		Phase:       jam.PhaseIdled, Activity: jam.ActivityWaiting, WaitingSince: waitingSince,
	}
}

// ladderKit wires an engine with the idle ladder: role acme/pair with the given
// allocation, a registry holding insts, and a clock set by the returned setter.
func ladderKit(alloc jam.RoleAllocation, nagger Nagger, reap Reaper, insts ...jam.Instance) (*Engine, *fakeReg, *fakeNagRecorder, func(time.Time)) {
	reg := &fakeReg{insts: insts}
	rec := &fakeNagRecorder{regs: reg}
	e := New(reg, &fakeWaker{}, reap, &fakeIdler{}, &fakeInbox{}, Config{MaxWait: time.Minute, WarmTimeout: time.Minute}, nil)
	e.SetIdleLadder(fakeRoles{"acme/pair": {Name: "pair", Allocation: alloc}}, rec, nagger)
	now := time.Unix(0, 0)
	e.now = func() time.Time { return now }
	return e, reg, rec, func(t time.Time) { now = t }
}

func TestIdleLadder_NagCadence(t *testing.T) {
	start := time.Unix(100_000, 0)
	n := &fakeNagger{}
	e, _, rec, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Hour, NagEvery: 3 * time.Hour}, n, &fakeReaper{}, personal("p1", start))
	ctx := context.Background()

	setNow(start.Add(59 * time.Minute)) // before idle-after
	e.tick(ctx)
	if len(n.calls) != 0 {
		t.Fatalf("nagged before idle-after: %+v", n.calls)
	}

	setNow(start.Add(time.Hour)) // at idle-after: first nag
	e.tick(ctx)
	if len(n.calls) != 1 || n.calls[0] != (nagCall{"nag", "p1", time.Hour}) || len(rec.at["p1"]) != 1 {
		t.Fatalf("want first nag at idle-after; calls=%+v recorded=%v", n.calls, rec.at)
	}

	setNow(start.Add(3*time.Hour + 59*time.Minute)) // before nag-every since the last nag
	e.tick(ctx)
	if len(n.calls) != 1 {
		t.Fatalf("re-nagged before nag-every: %+v", n.calls)
	}

	setNow(start.Add(4 * time.Hour)) // nag-every after the last nag: second nag
	e.tick(ctx)
	if len(n.calls) != 2 || n.calls[1] != (nagCall{"nag", "p1", 4 * time.Hour}) || len(rec.at["p1"]) != 2 {
		t.Fatalf("want a second nag at nag-every; calls=%+v recorded=%v", n.calls, rec.at)
	}
}

// Defaults: idle-after 4h, nag-every 24h, never reclaimed — a role with no idle
// settings (or no role record at all) still gets the ladder.
func TestIdleLadder_DefaultsAndNeverReclaimed(t *testing.T) {
	start := time.Unix(100_000, 0)
	n := &fakeNagger{}
	reap := &fakeReaper{}
	e, _, _, setNow := ladderKit(jam.RoleAllocation{}, n, reap, personal("p1", start))
	setNow(start.Add(4*time.Hour - time.Second))
	e.tick(context.Background())
	if len(n.calls) != 0 {
		t.Fatalf("nagged before the default 4h: %+v", n.calls)
	}
	setNow(start.Add(4 * time.Hour))
	e.tick(context.Background())
	if len(n.calls) != 1 {
		t.Fatalf("want a nag at the default 4h: %+v", n.calls)
	}
	setNow(start.Add(27 * time.Hour)) // 23h after the nag: not yet
	e.tick(context.Background())
	if len(n.calls) != 1 {
		t.Fatalf("re-nagged before the default 24h: %+v", n.calls)
	}
	setNow(start.Add(365 * 24 * time.Hour)) // a year on: reclaim-after unset = never
	e.tick(context.Background())
	if len(reap.down) != 0 || contains(kinds(n.calls), "reclaimed") {
		t.Fatalf("reclaim-after unset must never reclaim; down=%v calls=%+v", reap.down, n.calls)
	}
}

func kinds(cs []nagCall) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.kind)
	}
	return out
}

// A reply starts a new Waiting period (Supervisor.Report sets a fresh
// WaitingSince and clears the nag state): no immediate nag.
func TestIdleLadder_ReplyResetsLadder(t *testing.T) {
	start := time.Unix(100_000, 0)
	inst := personal("p1", start.Add(10*time.Hour)) // re-entered Waiting after a reply; nags cleared
	n := &fakeNagger{}
	e, _, _, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Hour, NagEvery: time.Hour}, n, &fakeReaper{}, inst)
	setNow(start.Add(10*time.Hour + 30*time.Minute))
	e.tick(context.Background())
	if len(n.calls) != 0 {
		t.Fatalf("nagged right after a reply reset the ladder: %+v", n.calls)
	}
}

// A reply pending this tick wakes the cove — it is not nagged or reclaimed.
func TestIdleLadder_PendingReplyWakesInsteadOfNagging(t *testing.T) {
	start := time.Unix(100_000, 0)
	inst := personal("p1", start)
	inst.Phase = jam.PhaseLive
	inst.WaitSeq = 5
	n := &fakeNagger{}
	reap := &fakeReaper{}
	e, _, _, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Hour, ReclaimAfter: 2 * time.Hour}, n, reap, inst)
	e.inbox = &fakeInbox{byActor: map[string][]intercom.Squawk{"p1": {extInbound("p1", 6, "id-6")}}}
	wake := &fakeWaker{}
	e.wake = wake
	setNow(start.Add(3 * time.Hour))
	e.tick(context.Background())
	if len(n.calls) != 0 || len(reap.down) != 0 || !contains(wake.woke, "p1") {
		t.Fatalf("want wake only; calls=%+v down=%v woke=%v", n.calls, reap.down, wake.woke)
	}
}

func TestIdleLadder_ReclaimNoticeThenTeardown(t *testing.T) {
	start := time.Unix(100_000, 0)
	var events []string
	n := &fakeNagger{log: &events}
	reap := &orderedReaper{log: &events}
	e, _, _, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Hour, ReclaimAfter: 72 * time.Hour}, n, reap, personal("p1", start))
	setNow(start.Add(72*time.Hour - time.Second))
	e.tick(context.Background())
	if len(reap.down) != 0 {
		t.Fatalf("reclaimed before reclaim-after: %v", reap.down)
	}
	setNow(start.Add(72 * time.Hour))
	e.tick(context.Background())
	if len(events) != 2 || events[0] != "notice:p1" || events[1] != "teardown:p1" {
		t.Fatalf("want the notice then teardown; events=%v", events)
	}
	last := n.calls[len(n.calls)-1]
	if last != (nagCall{"reclaimed", "p1", 72 * time.Hour}) {
		t.Fatalf("reclaim notice = %+v", last)
	}
}

// A failed nag is logged and retried next tick; it never tears the session down
// and records no nag.
func TestIdleLadder_FailedNagRetriedNeverTearsDown(t *testing.T) {
	start := time.Unix(100_000, 0)
	n := &fakeNagger{fail: errors.New("log unavailable")}
	reap := &fakeReaper{}
	e, _, rec, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Hour}, n, reap, personal("p1", start))
	setNow(start.Add(2 * time.Hour))
	e.tick(context.Background())
	if len(reap.down) != 0 || len(rec.at) != 0 {
		t.Fatalf("failed nag: down=%v recorded=%v", reap.down, rec.at)
	}
	n.fail = nil
	e.tick(context.Background())
	if len(n.calls) != 1 || len(rec.at["p1"]) != 1 {
		t.Fatalf("want the nag retried next tick; calls=%+v recorded=%v", n.calls, rec.at)
	}
}

// A failed reclaim notice is retried next tick rather than reclaiming the
// session silently.
func TestIdleLadder_FailedReclaimNoticeDefersReclaim(t *testing.T) {
	start := time.Unix(100_000, 0)
	n := &fakeNagger{fail: errors.New("log unavailable")}
	reap := &fakeReaper{}
	e, _, _, setNow := ladderKit(jam.RoleAllocation{ReclaimAfter: time.Hour}, n, reap, personal("p1", start))
	setNow(start.Add(2 * time.Hour))
	e.tick(context.Background())
	if len(reap.down) != 0 {
		t.Fatalf("reclaimed without delivering the notice: %v", reap.down)
	}
	n.fail = nil
	e.tick(context.Background())
	if !contains(reap.down, "p1") {
		t.Fatalf("want reclaim once the notice lands; down=%v", reap.down)
	}
}

// With no nagger (no intercom log) there are no nags or notices, but a
// configured reclaim still happens.
func TestIdleLadder_NilNagger(t *testing.T) {
	start := time.Unix(100_000, 0)
	reap := &fakeReaper{}
	e, _, rec, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Hour, ReclaimAfter: 3 * time.Hour}, nil, reap, personal("p1", start))
	setNow(start.Add(2 * time.Hour))
	e.tick(context.Background()) // past idle-after: would nag, but no nagger
	if len(rec.at) != 0 || len(reap.down) != 0 {
		t.Fatalf("nil nagger: recorded=%v down=%v", rec.at, reap.down)
	}
	setNow(start.Add(3 * time.Hour))
	e.tick(context.Background())
	if !contains(reap.down, "p1") {
		t.Fatalf("nil nagger must still reclaim; down=%v", reap.down)
	}
}

// Ephemeral coves are untouched by the ladder: no nags, and they keep wait-max
// teardown.
func TestIdleLadder_EphemeralUnaffected(t *testing.T) {
	start := time.Unix(100_000, 0)
	eph := jam.Instance{ActorID: "e1", Project: "acme", Role: "pair", Unit: "AET-1", Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting, WaitingSince: start}
	n := &fakeNagger{}
	reap := &fakeReaper{}
	e, _, rec, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Second, ReclaimAfter: time.Hour}, n, reap, eph)
	e.cfg.MaxWait = 100 * time.Hour
	setNow(start.Add(10 * time.Hour)) // past idle-after and reclaim-after, within wait-max
	e.tick(context.Background())
	if len(n.calls) != 0 || len(rec.at) != 0 || len(reap.down) != 0 {
		t.Fatalf("ephemeral touched by the ladder: calls=%+v recorded=%v down=%v", n.calls, rec.at, reap.down)
	}
	setNow(start.Add(101 * time.Hour))
	e.tick(context.Background())
	if !contains(reap.down, "e1") || len(n.calls) != 0 {
		t.Fatalf("ephemeral must keep wait-max teardown only; down=%v calls=%+v", reap.down, n.calls)
	}
}

// A standing session is resident like a personal one: past wait-max it is not
// torn down (still idled), and it gets no idle-ladder nags or reclaim — it has
// no owner to nag.
func TestTick_StandingSessionResident_NoLadder(t *testing.T) {
	start := time.Unix(100_000, 0)
	st := jam.Instance{
		ActorID: "standing-acme-pair-bot", Project: "acme", Role: "pair", Name: "bot",
		SessionKind: jam.SessionKindStanding,
		Phase:       jam.PhaseLive, Activity: jam.ActivityWaiting, WaitingSince: start,
	}
	n := &fakeNagger{}
	reap := &fakeReaper{}
	e, _, rec, setNow := ladderKit(jam.RoleAllocation{IdleAfter: time.Second, NagEvery: time.Second, ReclaimAfter: time.Minute}, n, reap, st)
	idler := &fakeIdler{}
	e.idler = idler
	setNow(start.Add(100 * time.Hour)) // far past wait-max (1m), idle-after and reclaim-after
	e.tick(context.Background())
	if len(reap.down) != 0 {
		t.Fatalf("standing session torn down for waiting: %v", reap.down)
	}
	if len(n.calls) != 0 || len(rec.at) != 0 {
		t.Fatalf("standing session nagged: calls=%+v recorded=%v", n.calls, rec.at)
	}
	if !contains(idler.idled, st.ActorID) {
		t.Fatalf("standing session past warm-timeout must still be idled; idled=%v", idler.idled)
	}
}

// --- reply-to-act on nags: an owner's keep/release reply to a nag ---

// nagID is the id of one of actor's nags (as the nagger stamps it).
func nagID(actor string) string { return jam.NagMessageID(actor, time.Unix(50_000, 7)) }

// reply is an external reply from `from` to cove, replying to replyTo.
func reply(cove string, seq int64, from, replyTo, body string) intercom.Squawk {
	return intercom.Squawk{
		Seq: seq, ID: "in:discord:r" + body, ReplyTo: replyTo, Body: body,
		From: intercom.Target{Kind: "human", Ref: from},
		To:   []intercom.Target{{Kind: "actor", Ref: cove}},
	}
}

// failingReaper fails the first `fails` teardowns, then succeeds.
type failingReaper struct {
	fakeReaper
	fails, calls int
}

func (f *failingReaper) Teardown(ctx context.Context, a string) error {
	f.calls++
	if f.calls <= f.fails {
		return errors.New("launcher unavailable")
	}
	return f.fakeReaper.Teardown(ctx, a)
}

// cmdKit wires a ladder engine holding one Waiting personal session p1 (owner
// alice, WaitSeq 5, Idled unless phase says otherwise) whose inbox holds msgs.
type cmdKit struct {
	e     *Engine
	reg   *fakeReg
	rec   *fakeNagRecorder
	n     *fakeNagger
	wake  *fakeWaker
	idler *fakeIdler
	inbox *fakeInbox
	now   time.Time
}

func newCmdKit(t *testing.T, reap Reaper, inst jam.Instance, msgs ...intercom.Squawk) *cmdKit {
	t.Helper()
	k := &cmdKit{n: &fakeNagger{}, wake: &fakeWaker{}, idler: &fakeIdler{}, now: time.Unix(200_000, 0)}
	k.e, k.reg, k.rec, _ = ladderKit(jam.RoleAllocation{IdleAfter: 2 * time.Hour, NagEvery: time.Hour}, k.n, reap, inst)
	k.inbox = &fakeInbox{byActor: map[string][]intercom.Squawk{inst.ActorID: msgs}}
	k.e.inbox, k.e.wake, k.e.idler = k.inbox, k.wake, k.idler
	k.e.now = func() time.Time { return k.now }
	return k
}

func waitingPersonal() jam.Instance {
	inst := personal("p1", time.Unix(199_000, 0)) // idle 1000s: under idle-after, so no nag this tick
	inst.WaitSeq = 5
	inst.LastNagAt = time.Unix(199_500, 0)
	inst.Nags = 1
	return inst
}

// woke reports whether the cove was woken this tick (a Live cove is woken, an
// Idled one resumed).
func (k *cmdKit) woke(id string) bool {
	return contains(k.wake.woke, id) || contains(k.idler.resumed, id)
}

func TestReplyToAct_OwnerReleaseTearsDownAndNotifies(t *testing.T) {
	var events []string
	reap := &orderedReaper{log: &events}
	k := newCmdKit(t, reap, waitingPersonal(), reply("p1", 6, "alice", nagID("p1"), "release"))
	k.n.log = &events
	k.e.tick(context.Background())
	if len(events) != 2 || events[0] != "teardown:p1" || events[1] != "released:p1" {
		t.Fatalf("want teardown then the released notice; events=%v", events)
	}
	if k.woke("p1") || len(k.rec.kept) != 0 {
		t.Fatalf("release must not wake or keep: woke=%v resumed=%v kept=%+v", k.wake.woke, k.idler.resumed, k.rec.kept)
	}
}

func TestReplyToAct_CommandWordsNormalized(t *testing.T) {
	for _, tc := range []struct {
		body string
		cmd  string // "release" | "keep"
	}{
		{"Release!", "release"}, {" keep. ", "keep"}, {"KEEP", "keep"}, {"release\n", "release"}, {"Keep!!", "keep"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			reap := &fakeReaper{}
			k := newCmdKit(t, reap, waitingPersonal(), reply("p1", 6, "alice", nagID("p1"), tc.body))
			k.e.tick(context.Background())
			if k.woke("p1") {
				t.Fatalf("%q must be a command, not wake", tc.body)
			}
			if got := contains(reap.down, "p1"); got != (tc.cmd == "release") {
				t.Fatalf("%q: torn down = %v", tc.body, got)
			}
			if got := len(k.rec.kept) == 1; got != (tc.cmd == "keep") {
				t.Fatalf("%q: kept = %+v", tc.body, k.rec.kept)
			}
		})
	}
}

func TestReplyToAct_KeepRestartsIdleClockWithoutWaking(t *testing.T) {
	reap := &fakeReaper{}
	k := newCmdKit(t, reap, waitingPersonal(),
		reply("p1", 6, "alice", nagID("p1"), "keep"),
		reply("p1", 8, "alice", nagID("p1"), "keep"))
	k.e.tick(context.Background())
	if len(k.rec.kept) != 1 || k.rec.kept[0] != (keepCall{"p1", 8, k.now}) {
		t.Fatalf("want KeepWaiting(p1, 8, now); kept=%+v", k.rec.kept)
	}
	got := k.reg.insts[0]
	if got.WaitSeq != 8 || !got.WaitingSince.Equal(k.now) || got.Nags != 0 || !got.LastNagAt.IsZero() {
		t.Fatalf("ladder not reset: %+v", got)
	}
	if len(k.n.calls) != 1 || k.n.calls[0] != (nagCall{"kept", "p1", 2 * time.Hour}) {
		t.Fatalf("want one kept notice naming the next reminder (idle-after); calls=%+v", k.n.calls)
	}
	if k.woke("p1") || len(reap.down) != 0 {
		t.Fatalf("keep must not wake or tear down: woke=%v resumed=%v down=%v", k.wake.woke, k.idler.resumed, reap.down)
	}
	if got.Phase != jam.PhaseIdled {
		t.Fatalf("an Idled session stays paused after keep: %v", got.Phase)
	}
}

func TestReplyToAct_KeepNotRetriggeredNextTick(t *testing.T) {
	k := newCmdKit(t, &fakeReaper{}, waitingPersonal(), reply("p1", 6, "alice", nagID("p1"), "keep"))
	k.e.tick(context.Background())
	k.now = k.now.Add(time.Minute)
	k.e.tick(context.Background())
	if len(k.rec.kept) != 1 || len(k.n.calls) != 1 || k.woke("p1") {
		t.Fatalf("keep processed more than once: kept=%+v calls=%+v woke=%v", k.rec.kept, k.n.calls, k.wake.woke)
	}
}

func TestReplyToAct_KeepThenNextNagAfterIdleAfter(t *testing.T) {
	k := newCmdKit(t, &fakeReaper{}, waitingPersonal(), reply("p1", 6, "alice", nagID("p1"), "keep"))
	k.e.tick(context.Background())
	kept := k.now
	k.now = kept.Add(2*time.Hour - time.Second)
	k.e.tick(context.Background())
	if kinds(k.n.calls)[len(k.n.calls)-1] != "kept" {
		t.Fatalf("nagged before idle-after from the keep: %+v", k.n.calls)
	}
	k.now = kept.Add(2 * time.Hour)
	k.e.tick(context.Background())
	if last := k.n.calls[len(k.n.calls)-1]; last != (nagCall{"nag", "p1", 2 * time.Hour}) {
		t.Fatalf("want the next nag idle-after from the keep; calls=%+v", k.n.calls)
	}
}

func TestReplyToAct_NotACommand_Wakes(t *testing.T) {
	for name, m := range map[string]intercom.Squawk{
		"keep not replying to a nag":      reply("p1", 6, "alice", "00000000-some-cove-msg", "keep"),
		"keep replying to nothing":        reply("p1", 6, "alice", "", "keep"),
		"release from a non-owner":        reply("p1", 6, "mallory", nagID("p1"), "release"),
		"release by display name only":    reply("p1", 6, "Alice", nagID("p1"), "release"),
		"release replying to another nag": reply("p1", 6, "alice", nagID("p1x"), "release"),
		"release in a sentence":           reply("p1", 6, "alice", nagID("p1"), "release it please"),
	} {
		t.Run(name, func(t *testing.T) {
			reap := &fakeReaper{}
			k := newCmdKit(t, reap, waitingPersonal(), m)
			k.e.tick(context.Background())
			if !k.woke("p1") {
				t.Fatal("an ordinary reply must wake")
			}
			if len(reap.down) != 0 || len(k.rec.kept) != 0 || len(k.n.calls) != 0 {
				t.Fatalf("an ordinary reply must not act: down=%v kept=%+v calls=%+v", reap.down, k.rec.kept, k.n.calls)
			}
		})
	}
}

func TestReplyToAct_KeepWithOtherTextWakes(t *testing.T) {
	reap := &fakeReaper{}
	k := newCmdKit(t, reap, waitingPersonal(),
		reply("p1", 6, "alice", nagID("p1"), "keep"),
		reply("p1", 7, "alice", "", "actually, try the other branch"))
	k.e.tick(context.Background())
	if !k.woke("p1") || len(k.rec.kept) != 0 || len(reap.down) != 0 {
		t.Fatalf("keep + other text must wake only: woke=%v kept=%+v down=%v", k.woke("p1"), k.rec.kept, reap.down)
	}
}

func TestReplyToAct_ReleaseWinsOverOtherText(t *testing.T) {
	reap := &fakeReaper{}
	k := newCmdKit(t, reap, waitingPersonal(),
		reply("p1", 6, "alice", "", "one more thing"),
		reply("p1", 7, "alice", nagID("p1"), "keep"),
		reply("p1", 8, "alice", nagID("p1"), "release"))
	k.e.tick(context.Background())
	if !contains(reap.down, "p1") || k.woke("p1") || len(k.rec.kept) != 0 {
		t.Fatalf("release + other text must release: down=%v woke=%v kept=%+v", reap.down, k.woke("p1"), k.rec.kept)
	}
}

func TestReplyToAct_FailedTeardownRetriedNextTick(t *testing.T) {
	reap := &failingReaper{fails: 1}
	k := newCmdKit(t, reap, waitingPersonal(), reply("p1", 6, "alice", nagID("p1"), "release"))
	k.e.tick(context.Background())
	if len(reap.down) != 0 || k.woke("p1") || len(k.n.calls) != 0 {
		t.Fatalf("a failed teardown must not wake or notify: down=%v woke=%v calls=%+v", reap.down, k.woke("p1"), k.n.calls)
	}
	k.e.tick(context.Background())
	if !contains(reap.down, "p1") || len(k.n.calls) != 1 || k.n.calls[0].kind != "released" {
		t.Fatalf("want the release retried next tick; down=%v calls=%+v", reap.down, k.n.calls)
	}
}

func TestReplyToAct_FailedNotifyDoesNotUndo(t *testing.T) {
	t.Run("release", func(t *testing.T) {
		reap := &fakeReaper{}
		k := newCmdKit(t, reap, waitingPersonal(), reply("p1", 6, "alice", nagID("p1"), "release"))
		k.n.fail = errors.New("log unavailable")
		k.e.tick(context.Background())
		if !contains(reap.down, "p1") || k.woke("p1") {
			t.Fatalf("release stands despite a failed notice: down=%v woke=%v", reap.down, k.woke("p1"))
		}
	})
	t.Run("keep", func(t *testing.T) {
		k := newCmdKit(t, &fakeReaper{}, waitingPersonal(), reply("p1", 6, "alice", nagID("p1"), "keep"))
		k.n.fail = errors.New("log unavailable")
		k.e.tick(context.Background())
		if len(k.rec.kept) != 1 || k.woke("p1") {
			t.Fatalf("keep stands despite a failed notice: kept=%+v woke=%v", k.rec.kept, k.woke("p1"))
		}
		k.e.tick(context.Background())
		if len(k.rec.kept) != 1 {
			t.Fatalf("a failed notice must not re-run keep: kept=%+v", k.rec.kept)
		}
	})
}

// Only personal sessions act on commands: a standing or ephemeral session's
// "release" (even one shaped like a reply to its nag) wakes it as today.
func TestReplyToAct_NonPersonalWakes(t *testing.T) {
	for _, kind := range []string{jam.SessionKindStanding, ""} {
		t.Run("kind="+kind, func(t *testing.T) {
			inst := waitingPersonal()
			inst.SessionKind = kind
			reap := &fakeReaper{}
			k := newCmdKit(t, reap, inst, reply("p1", 6, "alice", nagID("p1"), "release"))
			k.e.cfg.MaxWait = 1000 * time.Hour
			k.e.tick(context.Background())
			if !k.woke("p1") || len(reap.down) != 0 || len(k.rec.kept) != 0 {
				t.Fatalf("want wake only: woke=%v down=%v kept=%+v", k.woke("p1"), reap.down, k.rec.kept)
			}
		})
	}
}

// fakeCursor records SetWaitSeq calls and applies them to a fakeReg.
type fakeCursor struct {
	reg *fakeReg
	set map[string]int64
}

func (f *fakeCursor) SetWaitSeq(actorID string, seq int64) error {
	if f.set == nil {
		f.set = map[string]int64{}
	}
	f.set[actorID] = seq
	for i := range f.reg.insts {
		if f.reg.insts[i].ActorID == actorID {
			f.reg.insts[i].WaitSeq = seq
		}
	}
	return nil
}

// A reply to a Running cove (its agent holding a live episode open for a
// background task, or mid-turn) must Wake it — the cove delivers the Wake into
// the live process or coalesces it — and advance its baseline past the reply so
// the next tick does not Wake again. It is never paused or reaped for it.
func TestTick_ReplyToRunningCoveWakesOnceAndAdvancesBaseline(t *testing.T) {
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityRunning, SessionKind: jam.SessionKindPersonal, Owner: "alice", WaitSeq: 5},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{
		"a1": {extInbound("a1", 6, "id-6"), extInbound("a1", 8, "id-8")},
	}}
	wake, reap, idler := &fakeWaker{}, &fakeReaper{}, &fakeIdler{}
	cur := &fakeCursor{reg: reg}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Minute, WarmTimeout: time.Second}, nil)
	e.SetRunningWake(cur)
	e.now = func() time.Time { return time.Unix(2000, 0) }

	e.tick(context.Background())
	if len(wake.woke) != 1 || wake.woke[0] != "a1" {
		t.Fatalf("a reply to a Running cove must Wake it once, got wake=%v", wake.woke)
	}
	if cur.set["a1"] != 8 {
		t.Fatalf("baseline advanced to %d, want 8 (the latest reply woken for)", cur.set["a1"])
	}
	e.tick(context.Background())
	if len(wake.woke) != 1 {
		t.Fatalf("an already-woken reply must not Wake again, got wake=%v", wake.woke)
	}
	if len(idler.idled) != 0 || len(reap.down) != 0 {
		t.Errorf("a Running cove is never paused or reaped, got idle=%v teardown=%v", idler.idled, reap.down)
	}
}

// Without SetRunningWake (no cursor to advance), Running coves are left alone.
func TestTick_RunningCoveIgnoredWithoutCursor(t *testing.T) {
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityRunning, WaitSeq: 5},
	}}
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{"a1": {extInbound("a1", 6, "id-6")}}}
	wake := &fakeWaker{}
	e := New(reg, wake, &fakeReaper{}, &fakeIdler{}, inbox, Config{MaxWait: time.Minute, WarmTimeout: time.Second}, nil)
	e.tick(context.Background())
	if len(wake.woke) != 0 {
		t.Fatalf("no cursor: Running coves must not be woken, got %v", wake.woke)
	}
}
