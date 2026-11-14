package jam

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// UnreadTotal is the sum of the rail's per-channel unread: the same
// computation, visible channels only.
func TestUnreadTotalMatchesRail(t *testing.T) {
	f := newICFixture(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.store.JoinChannel(f.room.ID, f.alice.ID, 0))
	must(f.store.JoinChannel(f.room.ID, f.bob.ID, 0))
	post := func(ch Channel, from ident.ID, body string) intercom.Squawk {
		t.Helper()
		m, err := f.ic.PostTrusted(ch, intercom.Squawk{ID: string(ident.New(ident.Channel)) + body, From: from, Body: body})
		must(err)
		return m
	}
	first := post(f.room, f.bob.ID, "one")
	post(f.room, f.bob.ID, "two")
	post(f.room, f.alice.ID, "mine")
	pl, err := f.ic.PlanPersonChat(f.bob.ID, f.alice.ID)
	must(err)
	if _, err := f.ic.Post(pl, intercom.Squawk{ID: "chat-1", From: f.bob.ID, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	must(f.store.CommitChannelRead(f.alice.ID, f.room.ID, first.Seq))

	// A channel alice can no longer see: a room in a project she left.
	must(f.store.CreateProject("beta"))
	beta, _ := f.store.GetProject("beta")
	must(f.store.AddMember(beta.ID, f.alice.ID))
	must(f.store.AddMember(beta.ID, f.bob.ID))
	gone, err := f.store.CreateChannel(Channel{ProjectID: beta.ID, Kind: SourceRoom, Key: "ops", Label: "ops"})
	must(err)
	must(f.store.JoinChannel(gone.ID, f.alice.ID, 0))
	post(gone, f.bob.ID, "hidden")
	must(f.store.RemoveMember(beta.ID, f.alice.ID))

	sum := 0
	for _, v := range UserChannels(f.store, f.ic, f.log, f.alice.ID) {
		sum += v.Unread
	}
	if sum != 2 {
		t.Fatalf("rail unread sum = %d, want 2 (room 1, chat 1)", sum)
	}
	if got := UnreadTotal(f.store, f.ic, f.log, f.alice.ID); got != sum {
		t.Fatalf("UnreadTotal = %d, want %d", got, sum)
	}
	if got := UnreadTotal(f.store, f.ic, nil, f.alice.ID); got != 0 {
		t.Fatalf("UnreadTotal without a log = %d", got)
	}
}

// missionView is channel ch as alice sees it, after joining the given
// participants to it.
func (f *icFixture) missionView(ch Channel, members ...ident.ID) ChannelView {
	f.t.Helper()
	for _, m := range members {
		if err := f.store.JoinChannel(ch.ID, m, 0); err != nil {
			f.t.Fatal(err)
		}
	}
	v, ok := UserChannel(f.store, f.ic, f.log, f.alice.ID, ch.ID)
	if !ok {
		f.t.Fatalf("alice can't see %s", ch.ID)
	}
	return v
}

// putMission stores inst with mission m and phase.
func (f *icFixture) putMission(inst *Instance, m string, phase Phase) {
	f.t.Helper()
	inst.Mission, inst.Phase = m, phase
	if err := f.store.PutInstance(*inst); err != nil {
		f.t.Fatal(err)
	}
}

func TestChannelMissionSession(t *testing.T) {
	f := newICFixture(t)
	f.putMission(&f.personal, "m", PhaseLive)
	if err := f.ic.SetUp(f.personal); err != nil {
		t.Fatal(err)
	}
	if v := f.missionView(f.sessionChannel(f.personal)); v.Mission != "m" {
		t.Fatalf("Mission = %q, want %q", v.Mission, "m")
	}
}

func TestChannelMissionTicketLatest(t *testing.T) {
	f := newICFixture(t)
	ch, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceWork, Key: "t/ACME-9", Label: "ACME-9"})
	if err != nil {
		t.Fatal(err)
	}
	older, newer := f.ticket, f.ticket
	newer.ActorID = string(ident.New(ident.Session))
	older.RaisedAt, newer.RaisedAt = f.now.Add(-time.Hour), f.now
	f.putMission(&newer, "newer", PhaseIdled)
	f.putMission(&older, "older", PhaseLive)
	// Join the newer first so member order doesn't decide it.
	v := f.missionView(ch, f.alice.ID, ident.ID(newer.ActorID), ident.ID(older.ActorID))
	if v.Mission != "newer" {
		t.Fatalf("Mission = %q, want the later-raised session's %q", v.Mission, "newer")
	}
}

func TestChannelMissionInactiveHidden(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.personal); err != nil {
		t.Fatal(err)
	}
	f.putMission(&f.personal, "m", PhaseGone)
	if v := f.missionView(f.sessionChannel(f.personal)); v.Mission != "" {
		t.Fatalf("Mission = %q, want none for a gone session", v.Mission)
	}
}

func TestChannelMissionChatAndRoom(t *testing.T) {
	f := newICFixture(t)
	f.putMission(&f.personal, "solo", PhaseLive)
	f.putMission(&f.standing, "other", PhaseLive)
	chat := func(key string) Channel {
		t.Helper()
		ch, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceDM, Key: key})
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	tests := []struct {
		name    string
		ch      Channel
		members []ident.ID
		want    string
	}{
		{"chat with one session", chat("one"), ids(f.alice, f.personal), "solo"},
		{"chat with two sessions", chat("two"), ids(f.alice, f.personal, f.standing), ""},
		{"room", f.room, ids(f.alice, f.personal), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v := f.missionView(tt.ch, tt.members...); v.Mission != tt.want {
				t.Fatalf("Mission = %q, want %q", v.Mission, tt.want)
			}
		})
	}
}

func TestChannelCardSession(t *testing.T) {
	f := newICFixture(t)
	f.putMission(&f.personal, "m", PhaseLive)
	if err := f.ic.SetUp(f.personal); err != nil {
		t.Fatal(err)
	}
	if v := f.missionView(f.sessionChannel(f.personal)); v.CardSession != f.personal.ActorID {
		t.Fatalf("session channel CardSession = %q, want %q", v.CardSession, f.personal.ActorID)
	}
	ch, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceWork, Key: "t/ACME-9", Label: "ACME-9"})
	if err != nil {
		t.Fatal(err)
	}
	f.putMission(&f.ticket, "", PhaseLive)
	if v := f.missionView(ch, f.alice.ID, ident.ID(f.ticket.ActorID)); v.CardSession != "" {
		t.Fatalf("work log CardSession = %q, want none (a work log shows its name)", v.CardSession)
	}
	create := func(kind SourceKind, key string) Channel {
		t.Helper()
		c, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: kind, Key: key})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if v := f.missionView(create(SourceDM, "one"), ids(f.alice, f.standing)...); v.CardSession != f.standing.ActorID {
		t.Fatalf("dm with an agent: CardSession = %q, want %q", v.CardSession, f.standing.ActorID)
	}
	if v := f.missionView(create(SourceGroup, "three"), ids(f.alice, f.bob, f.standing)...); v.CardSession != "" {
		t.Fatalf("group CardSession = %q, want none", v.CardSession)
	}
	if v := f.missionView(create(SourceDM, "people"), ids(f.alice, f.bob)...); v.CardSession != "" {
		t.Fatalf("dm with a person: CardSession = %q, want none", v.CardSession)
	}
	ended := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Phase: PhaseGone}
	if err := f.store.PutInstance(ended); err != nil {
		t.Fatal(err)
	}
	if v := f.missionView(create(SourceDM, "ended"), ids(f.alice, ended)...); v.CardSession != "" {
		t.Fatalf("dm with an ended agent: CardSession = %q, want none", v.CardSession)
	}
}

// The rail's groups (conversations.adoc#kinds; the manual's inbox/channels):
// a dm or a group with a live, starting, or paused agent is under Agents;
// rooms, work logs, dms with people, and dms with ended agents are
// Conversations. Every view carries its conversation kind.
func TestChannelBucketByKind(t *testing.T) {
	f := newICFixture(t)
	ended := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Phase: PhaseGone}
	paused := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Phase: PhaseIdled}
	for _, inst := range []Instance{ended, paused} {
		if err := f.store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	create := func(kind SourceKind, key string) Channel {
		t.Helper()
		c, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: kind, Key: key, Label: key})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	tests := []struct {
		name    string
		ch      Channel
		members []ident.ID
		want    AttentionBucket
	}{
		{"dm with a live agent", create(SourceDM, "live"), ids(f.alice, f.standing), BucketActive},
		{"dm with a paused agent", create(SourceDM, "paused"), ids(f.alice, paused), BucketActive},
		{"group with a live agent", create(SourceGroup, "group"), ids(f.alice, f.bob, f.standing), BucketActive},
		{"dm with an ended agent", create(SourceDM, "ended"), ids(f.alice, ended), BucketChannels},
		{"dm with a person", create(SourceDM, "people"), ids(f.alice, f.bob), BucketChannels},
		{"work log with a live worker", create(SourceWork, "t/ACME-9"), ids(f.alice, f.ticket), BucketChannels},
		{"room with a live agent", f.room, ids(f.alice, f.standing), BucketChannels},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := f.missionView(tt.ch, tt.members...)
			if got := v.Bucket(); got != tt.want {
				t.Fatalf("Bucket = %q, want %q", got, tt.want)
			}
			if v.Source != tt.ch.Kind {
				t.Fatalf("Source = %q, want %q", v.Source, tt.ch.Kind)
			}
		})
	}
}
