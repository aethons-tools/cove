package jam_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// fakeLog is the msglog fake the pure projection reads: an ordered, in-memory
// slice of squawks. It satisfies jam.LogReader without a file, network, or VM.
type fakeLog []intercom.LegacySquawk

func (f fakeLog) ListSince(afterSeq int64, limit int) []intercom.LegacySquawk {
	var out []intercom.LegacySquawk
	for _, m := range f {
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

func actor(id string) intercom.Target    { return intercom.Target{Kind: "actor", Ref: id} }
func human(name string) intercom.Target  { return intercom.Target{Kind: "human", Ref: name} }
func channel(ref string) intercom.Target { return intercom.Target{Kind: "channel", Ref: ref} }

// projectionFixture builds a small world: two humans, a waiting studio (cove-1
// on ACME-1), a running studio (cove-2 on ACME-2), an idled studio (cove-3 on
// ACME-3), one named channel (eng), and a Log of five squawks.
func projectionFixture() (fakeLog, jam.Roster, []jam.Instance) {
	roster := jam.Roster{
		Humans:   []jam.Human{{Name: "alice"}, {Name: "bob"}},
		Channels: []jam.RosterChannel{{Name: "eng", Service: "linear", Ref: "ACME-9"}},
	}
	instances := []jam.Instance{
		{ActorID: "cove-1", Project: "acme", Unit: "ACME-1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, EscalationAsked: true},
		{ActorID: "cove-2", Project: "acme", Unit: "ACME-2", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "cove-3", Project: "acme", Unit: "ACME-3", Phase: jam.PhaseIdled},
	}
	log := fakeLog{
		{Seq: 1, From: actor("cove-1"), To: []intercom.Target{channel("ACME-1")}, Body: "status", Project: "acme"},
		{Seq: 2, From: human("alice"), To: []intercom.Target{channel("ACME-1")}, Body: "reply", Project: "acme"},
		{Seq: 3, From: human("alice"), To: []intercom.Target{actor("cove-2")}, Body: "dm", Project: "acme"},
		{Seq: 4, From: actor("cove-2"), To: []intercom.Target{human("alice")}, Body: "dm back", Project: "acme"},
		{Seq: 5, From: human("bob"), To: []intercom.Target{channel("eng")}, Body: "team", Project: "acme"},
	}
	return log, roster, instances
}

func byID(chs []jam.ChannelView) map[string]jam.ChannelView {
	m := map[string]jam.ChannelView{}
	for _, c := range chs {
		m[c.ID] = c
	}
	return m
}

func TestDMChannelIDDeterministic(t *testing.T) {
	a, b := human("alice"), actor("cove-2")
	if got, want := jam.DMChannelID(a, b), jam.DMChannelID(b, a); got != want {
		t.Fatalf("DM id not order-independent: %q vs %q", got, want)
	}
	// The two endpoints must both appear in the id so it is a stable derivation.
	if id := jam.DMChannelID(a, b); id != "dm:actor:cove-2|human:alice" {
		t.Fatalf("DM id = %q, want dm:actor:cove-2|human:alice", id)
	}
}

func TestProjectChannelsMembershipAndTags(t *testing.T) {
	log, roster, instances := projectionFixture()
	// alice is a member of the ACME-1 studio channel (she replied there) and of
	// her DM with the cove-2 session (a session ref, NOT the studio channel).
	// She is NOT a member of #eng (bob-only) or of cove-2's studio channel.
	cursors := map[string]int64{jam.StudioChannelID("ACME-1"): 0}
	chs := jam.ProjectChannels(human("alice"), log, roster, instances, cursors)
	got := byID(chs)
	if len(got) != 2 {
		t.Fatalf("alice channels = %d (%v), want 2", len(got), keys(got))
	}

	studio, ok := got[jam.StudioChannelID("ACME-1")]
	if !ok {
		t.Fatalf("missing studio channel; got %v", keys(got))
	}
	if studio.Kind != jam.ChannelStudio {
		t.Fatalf("studio kind = %q", studio.Kind)
	}
	if studio.Project != "acme" || studio.Phase != string(jam.PhaseLive) || !studio.NeedsYou {
		t.Fatalf("studio tags = %+v; want project acme, phase live, needs-you true", studio)
	}
	if studio.LastSeq != 2 {
		t.Fatalf("studio lastSeq = %d, want 2", studio.LastSeq)
	}
	// unread on ACME-1 past cursor 0 = seq 1 (from cove-1); seq 2 is alice's own,
	// which never counts as unread to alice.
	if studio.Unread != 1 {
		t.Fatalf("studio unread = %d, want 1", studio.Unread)
	}
	if studio.Bucket() != jam.BucketNeedsYou {
		t.Fatalf("studio bucket = %q, want needs-you", studio.Bucket())
	}

	dm, ok := got[jam.DMChannelID(human("alice"), actor("cove-2"))]
	if !ok {
		t.Fatalf("missing DM channel; got %v", keys(got))
	}
	if dm.Kind != jam.ChannelDM {
		t.Fatalf("dm kind = %q", dm.Kind)
	}
	if dm.Project != "acme" || dm.Phase != string(jam.PhaseLive) || dm.NeedsYou {
		t.Fatalf("dm tags = %+v; want project acme, phase live, needs-you false", dm)
	}
	if dm.LastSeq != 4 {
		t.Fatalf("dm lastSeq = %d, want 4", dm.LastSeq)
	}
	// no cursor for the DM (absent = 0); seq 3 is alice's own, seq 4 is cove-2's.
	if dm.Unread != 1 {
		t.Fatalf("dm unread = %d, want 1", dm.Unread)
	}
	if dm.Bucket() != jam.BucketActive {
		t.Fatalf("dm bucket = %q, want active", dm.Bucket())
	}
}

func TestProjectChannelsBobSeesNamedChannel(t *testing.T) {
	log, roster, instances := projectionFixture()
	chs := jam.ProjectChannels(human("bob"), log, roster, instances, nil)
	got := byID(chs)
	if len(got) != 1 {
		t.Fatalf("bob channels = %d (%v), want 1", len(got), keys(got))
	}
	eng, ok := got[jam.NamedChannelID("eng")]
	if !ok {
		t.Fatalf("missing named channel; got %v", keys(got))
	}
	if eng.Kind != jam.ChannelNamed || eng.NeedsYou {
		t.Fatalf("named channel tags = %+v", eng)
	}
	if eng.Bucket() != jam.BucketChannels {
		t.Fatalf("named bucket = %q, want channels", eng.Bucket())
	}
}

func keys(m map[string]jam.ChannelView) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestProjectChannelsSessions(t *testing.T) {
	log, roster, instances := projectionFixture()
	// cove-3 and cove-1 take part in #eng; a human never counts as a session.
	log = append(log,
		intercom.LegacySquawk{Seq: 6, From: actor("cove-3"), To: []intercom.Target{channel("eng")}, Body: "hi", Project: "acme"},
		intercom.LegacySquawk{Seq: 7, From: actor("cove-1"), To: []intercom.Target{channel("eng")}, Body: "hi", Project: "acme"},
	)
	alice := byID(jam.ProjectChannels(human("alice"), log, roster, instances, nil))
	if got := alice[jam.StudioChannelID("ACME-1")].Sessions; len(got) != 1 || got[0] != "cove-1" {
		t.Errorf("studio sessions = %v, want [cove-1]", got)
	}
	if got := alice[jam.DMChannelID(human("alice"), actor("cove-2"))].Sessions; len(got) != 1 || got[0] != "cove-2" {
		t.Errorf("dm sessions = %v, want [cove-2]", got)
	}
	bob := byID(jam.ProjectChannels(human("bob"), log, roster, instances, nil))
	if got := bob[jam.NamedChannelID("eng")].Sessions; len(got) != 2 || got[0] != "cove-1" || got[1] != "cove-3" {
		t.Errorf("named sessions = %v, want [cove-1 cove-3] (sorted)", got)
	}
}

// A session whose turn merely ended is idle; it needs a person only when it
// asked for one or is blocked, and only while live.
func TestNeedsPerson(t *testing.T) {
	asked := &jam.TicketReport{State: jam.ReportNeedsInput}
	cases := []struct {
		name string
		inst jam.Instance
		want bool
	}{
		{"idle", jam.Instance{Phase: jam.PhaseLive, Activity: jam.ActivityWaiting}, false},
		{"escalate called", jam.Instance{Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, EscalationAsked: true}, true},
		{"needs-input report", jam.Instance{Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, Report: asked}, true},
		{"blocked", jam.Instance{Phase: jam.PhaseLive, Activity: jam.ActivityBlocked}, true},
		{"running after asking", jam.Instance{Phase: jam.PhaseLive, Activity: jam.ActivityRunning, EscalationAsked: true}, false},
		{"paused", jam.Instance{Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting}, false},
		{"paused after asking", jam.Instance{Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting, EscalationAsked: true}, true},
		{"gone after asking", jam.Instance{Phase: jam.PhaseGone, Activity: jam.ActivityWaiting, EscalationAsked: true}, false},
	}
	for _, c := range cases {
		if got := jam.NeedsPerson(c.inst); got != c.want {
			t.Errorf("%s: NeedsPerson = %v, want %v", c.name, got, c.want)
		}
	}
}
