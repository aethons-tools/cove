package jam_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// fakeLog is the msglog fake the pure projection reads: an ordered, in-memory
// slice of squawks. It satisfies jam.LogReader without a file, network, or VM.
type fakeLog []intercom.Squawk

func (f fakeLog) ListSince(afterSeq int64, limit int) []intercom.Squawk {
	var out []intercom.Squawk
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
		Channels: []jam.Channel{{Name: "eng", Service: "linear", Ref: "ACME-9"}},
	}
	instances := []jam.Instance{
		{ActorID: "cove-1", Project: "acme", Unit: "ACME-1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting},
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
	if studio.Project != "acme" || studio.Phase != string(jam.PhaseLive) || !studio.Waiting {
		t.Fatalf("studio tags = %+v; want project acme, phase live, waiting true", studio)
	}
	if studio.LastSeq != 2 {
		t.Fatalf("studio lastSeq = %d, want 2", studio.LastSeq)
	}
	// unread on ACME-1 past cursor 0 = seq 1 (from cove-1); seq 2 is alice's own,
	// which never counts as unread to alice.
	if studio.Unread != 1 {
		t.Fatalf("studio unread = %d, want 1", studio.Unread)
	}
	if studio.Bucket() != jam.BucketWaiting {
		t.Fatalf("studio bucket = %q, want waiting", studio.Bucket())
	}

	dm, ok := got[jam.DMChannelID(human("alice"), actor("cove-2"))]
	if !ok {
		t.Fatalf("missing DM channel; got %v", keys(got))
	}
	if dm.Kind != jam.ChannelDM {
		t.Fatalf("dm kind = %q", dm.Kind)
	}
	if dm.Project != "acme" || dm.Phase != string(jam.PhaseLive) || dm.Waiting {
		t.Fatalf("dm tags = %+v; want project acme, phase live, waiting false", dm)
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
	if eng.Kind != jam.ChannelNamed || eng.Waiting {
		t.Fatalf("named channel tags = %+v", eng)
	}
	if eng.Bucket() != jam.BucketChannels {
		t.Fatalf("named bucket = %q, want channels", eng.Bucket())
	}
}

func TestActiveRecipientsDirectory(t *testing.T) {
	_, roster, instances := projectionFixture()
	recs := jam.ActiveRecipients(roster, instances)
	// 2 humans + 1 named channel + 3 active instances × (session + studio) = 9.
	if len(recs) != 9 {
		t.Fatalf("recipients = %d, want 9: %+v", len(recs), recs)
	}
	want := map[string]string{
		"human:alice":    "human",
		"human:bob":      "human",
		"channel:eng":    "channel",
		"actor:cove-1":   "session",
		"channel:ACME-1": "studio",
		"actor:cove-2":   "session",
		"channel:ACME-2": "studio",
		"actor:cove-3":   "session",
		"channel:ACME-3": "studio",
	}
	for _, r := range recs {
		kind, ok := want[r.Target.String()]
		if !ok {
			t.Fatalf("unexpected recipient %q", r.Target.String())
		}
		if r.Kind != kind {
			t.Fatalf("recipient %q kind = %q, want %q", r.Target.String(), r.Kind, kind)
		}
		delete(want, r.Target.String())
	}
	if len(want) != 0 {
		t.Fatalf("missing recipients: %v", want)
	}
}

func TestActiveRecipientsSkipsGoneStudios(t *testing.T) {
	roster := jam.Roster{}
	instances := []jam.Instance{
		{ActorID: "live", Unit: "U1", Phase: jam.PhaseLive},
		{ActorID: "gone", Unit: "U2", Phase: jam.PhaseGone},
		{ActorID: "terminating", Unit: "U3", Phase: jam.PhaseTerminating},
	}
	recs := jam.ActiveRecipients(roster, instances)
	// only the live instance contributes (session + studio); gone/terminating skipped.
	if len(recs) != 2 {
		t.Fatalf("recipients = %d, want 2: %+v", len(recs), recs)
	}
}

func TestResolveSendTarget(t *testing.T) {
	_, roster, instances := projectionFixture()
	self := human("alice")
	tests := []struct {
		name   string
		ref    string
		want   intercom.Target // ignored when !wantOK
		wantOK bool
	}{
		// A studio — whether addressed as a recipient target ("channel:<unit>") or
		// a channel id ("studio:<unit>") — resolves to its SESSION actor, so an
		// external reply wakes it exactly as a relayed reply does.
		{"studio channel id → session actor", "studio:ACME-1", actor("cove-1"), true},
		{"studio recipient target → session actor", "channel:ACME-1", actor("cove-1"), true},
		{"running studio also resolves", "channel:ACME-2", actor("cove-2"), true},
		// A session DM is already the actor.
		{"session recipient → actor", "actor:cove-2", actor("cove-2"), true},
		// Named channels stay channel targets (external surface).
		{"named channel id", "named:eng", channel("eng"), true},
		{"named recipient target", "channel:eng", channel("eng"), true},
		// Humans stay human targets.
		{"human recipient", "human:bob", human("bob"), true},
		// Replying to a DM addresses the OTHER endpoint.
		{"dm with a session → the session actor", jam.DMChannelID(human("alice"), actor("cove-2")), actor("cove-2"), true},
		{"dm with a human → the other human", jam.DMChannelID(human("alice"), human("bob")), human("bob"), true},
		// Non-members and unknowns fail closed.
		{"dm the participant is not part of", jam.DMChannelID(human("bob"), actor("cove-2")), intercom.Target{}, false},
		{"unknown human", "human:nobody", intercom.Target{}, false},
		{"unknown studio unit", "channel:NOPE-9", intercom.Target{}, false},
		{"unknown session", "actor:ghost", intercom.Target{}, false},
		{"no kind prefix", "alice", intercom.Target{}, false},
		{"empty ref value", "human:", intercom.Target{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := jam.ResolveSendTarget(tc.ref, self, roster, instances)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got target %q)", ok, tc.wantOK, got.String())
			}
			if tc.wantOK && got != tc.want {
				t.Fatalf("target = %q, want %q", got.String(), tc.want.String())
			}
		})
	}
}

func TestResolveSendTargetSkipsInactiveStudio(t *testing.T) {
	roster := jam.Roster{}
	instances := []jam.Instance{
		{ActorID: "gone", Unit: "U-GONE", Phase: jam.PhaseGone},
		{ActorID: "term", Unit: "U-TERM", Phase: jam.PhaseTerminating},
	}
	for _, ref := range []string{"studio:U-GONE", "channel:U-GONE", "actor:gone", "studio:U-TERM"} {
		if got, ok := jam.ResolveSendTarget(ref, human("alice"), roster, instances); ok {
			t.Fatalf("ref %q resolved to %q; an inactive studio must not be addressable", ref, got.String())
		}
	}
}

func keys(m map[string]jam.ChannelView) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
