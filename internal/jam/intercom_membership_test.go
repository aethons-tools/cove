package jam

import (
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

// A member calls someone in: they join from the tail and hear the notice.
func TestIntercomCallIn(t *testing.T) {
	f := newICFixture(t, "user:*", "session:*")
	f.standing.Name = "spider"
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	// The personal session's starter (alice) calls bob into its channel.
	home := f.mustPlan(f.personal, "").Channel
	ch, who, err := f.ic.CallIn(Poster{ID: f.alice.ID}, home.ID, "user:bob")
	if err != nil || ch.ID != home.ID || who != f.bob.ID {
		t.Fatalf("alice calls in bob = %+v, %s, %v", ch, who, err)
	}
	if !isCurrentMember(f.store, home.ID, f.bob.ID) {
		t.Fatal("bob is in")
	}
	inbox := f.log.InboxSince(f.bob.ID, 0, 0)
	if len(inbox) != 1 || inbox[0].From != f.alice.ID || inbox[0].Channel != home.ID {
		t.Fatalf("bob's inbox = %+v, want the call-in notice", inbox)
	}
	// Again: a no-op, no second notice.
	if _, _, err := f.ic.CallIn(Poster{ID: f.alice.ID}, home.ID, "user:"+string(f.bob.ID)); err != nil {
		t.Fatal(err)
	}
	if got := f.log.InboxSince(f.bob.ID, 0, 0); len(got) != 1 {
		t.Fatalf("calling in a member again notified them: %+v", got)
	}
	// The session calls another session into its home ("" channel).
	ch, who, err = f.ic.CallIn(f.poster(f.personal), "", "session:spider")
	if err != nil || ch.ID != home.ID || who != ident.ID(f.standing.ActorID) {
		t.Fatalf("session calls in spider = %+v, %s, %v", ch, who, err)
	}
	if got := f.log.InboxSince(ident.ID(f.standing.ActorID), 0, 0); len(got) != 1 {
		t.Fatalf("spider's inbox = %+v", got)
	}
}

func TestIntercomCallInRefusals(t *testing.T) {
	f := newICFixture(t, "user:alice")
	home := f.mustPlan(f.personal, "").Channel
	chat, err := f.ic.chat(f.project, []ident.ID{ident.ID(f.personal.ActorID), f.alice.ID})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		p    Poster
		ch   ident.ID
		who  string
		want error
	}{
		{"not a member", Poster{ID: f.bob.ID}, home.ID, "user:carol", ErrSendDenied},
		{"no such channel", Poster{ID: f.alice.ID}, "chn_01j9q3zzzzzzzzzzzzzzzzzz", "user:bob", ErrSendDenied},
		{"invitee outside the project", Poster{ID: f.alice.ID}, home.ID, "user:carol", ErrSendUnresolved},
		{"no such person", Poster{ID: f.alice.ID}, home.ID, "user:nobody", ErrSendUnresolved},
		{"malformed", Poster{ID: f.alice.ID}, home.ID, "bob", ErrSendDenied},
		{"a chat is fixed", Poster{ID: f.alice.ID}, chat.ID, "user:bob", ErrFixedMembers},
		{"session beyond its addressing", f.poster(f.personal), "", "user:bob", ErrSendDenied},
		{"session: needs an explicit glob", f.poster(f.personal), "", "session:" + f.standing.ActorID, ErrSendDenied},
		{"session into a room beyond its addressing", f.poster(f.personal), f.room.ID, "session:" + f.standing.ActorID, ErrSendDenied},
	}
	for _, p := range []ident.ID{f.alice.ID, ident.ID(f.personal.ActorID)} {
		if err := f.store.JoinChannel(f.room.ID, p, 10); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cases {
		if _, _, err := f.ic.CallIn(c.p, c.ch, c.who); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

// People join what they can see (never a personal session's channel or a
// chat) and leave anything but a chat; a session never leaves its home.
func TestIntercomJoinAndLeave(t *testing.T) {
	f := newICFixture(t)
	standing := f.mustPlan(f.standing, "").Channel
	personal := f.mustPlan(f.personal, "").Channel
	chat, err := f.ic.PlanPersonChat(f.alice.ID, f.bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ic.JoinChannel(f.carol.ID, chat.Channel.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("carol joins a chat: %v", err)
	}
	if err := f.ic.LeaveChannel(f.alice.ID, chat.Channel.ID); !errors.Is(err, ErrFixedMembers) {
		t.Fatalf("leaving a chat: %v, want ErrFixedMembers", err)
	}
	if err := f.ic.JoinChannel(f.bob.ID, standing.ID); err != nil || !isCurrentMember(f.store, standing.ID, f.bob.ID) {
		t.Fatalf("bob joins the standing session's channel: %v", err)
	}
	if err := f.ic.JoinChannel(f.bob.ID, personal.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("bob joins a personal session's channel: %v, want ErrSendDenied", err)
	}
	if err := f.ic.JoinChannel(f.carol.ID, standing.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("carol (no member) joins: %v", err)
	}
	if err := f.ic.LeaveChannel(f.bob.ID, standing.ID); err != nil || isCurrentMember(f.store, standing.ID, f.bob.ID) {
		t.Fatalf("bob leaves: %v", err)
	}
	if err := f.ic.LeaveChannel(f.bob.ID, standing.ID); err != nil {
		t.Fatalf("leaving again is a no-op: %v", err)
	}
	// The starter may leave her personal session's channel; the session can't.
	if err := f.ic.LeaveChannel(f.alice.ID, personal.ID); err != nil {
		t.Fatalf("alice leaves: %v", err)
	}
	if err := f.ic.LeaveChannel(ident.ID(f.personal.ActorID), personal.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a session leaves its own channel: %v", err)
	}
	ticket := f.mustPlan(f.ticket, "").Channel
	if err := f.ic.LeaveChannel(ident.ID(f.ticket.ActorID), ticket.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a session leaves its own ticket: %v", err)
	}
}

// Calling in is no back door into someone's personal session: a person may
// call in only a session they could reach themselves (MayReach).
func TestIntercomCallInPersonalSessionNeedsReach(t *testing.T) {
	f := newICFixture(t)
	standing := f.mustPlan(f.standing, "").Channel
	if err := f.ic.JoinChannel(f.bob.ID, standing.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.ic.CallIn(Poster{ID: f.bob.ID}, standing.ID, "session:"+f.personal.ActorID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("bob calls in alice's personal session: %v, want ErrSendDenied", err)
	}
	if err := f.ic.JoinChannel(f.alice.ID, standing.ID); err != nil {
		t.Fatal(err)
	}
	f.mustPlan(f.personal, "") // alice is in her session's channel
	if _, _, err := f.ic.CallIn(Poster{ID: f.alice.ID}, standing.ID, "session:"+f.personal.ActorID); err != nil {
		t.Fatalf("alice calls in her own personal session: %v", err)
	}
}

// The call-in notice is Jam's own: marked so the relays never render it onto
// a ticket's issue or a room's surface.
func TestIntercomCallInNoticeIsMarked(t *testing.T) {
	f := newICFixture(t)
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.ic.CallIn(Poster{ID: f.alice.ID}, f.room.ID, "user:bob"); err != nil {
		t.Fatal(err)
	}
	got := f.log.InboxSince(f.bob.ID, 0, 0)
	if len(got) != 1 || !IsCallInNotice(got[0].ID) {
		t.Fatalf("notice = %+v", got)
	}
}

// Leaving a channel one can't see is refused like any other unknown channel:
// a refusal never says whether it exists.
func TestIntercomLeaveUnseenChannel(t *testing.T) {
	f := newICFixture(t)
	personal := f.mustPlan(f.personal, "").Channel
	if err := f.ic.LeaveChannel(f.bob.ID, personal.ID); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("bob leaves a channel he can't see: %v, want ErrSendDenied", err)
	}
	if err := f.ic.LeaveChannel(f.bob.ID, f.room.ID); err != nil {
		t.Fatalf("leaving a visible channel one isn't in is a no-op: %v", err)
	}
}

// memberWake is p's current membership of ch: its wake mode, ok=false when
// p is not a member.
func memberWake(s Store, ch, p ident.ID) (WakeMode, bool) {
	for _, m := range s.ChannelMembers(ch) {
		if m.ParticipantID == p && !m.Left {
			return m.Wake, true
		}
	}
	return "", false
}

// A session is called into a room by a person, or by a session member its
// addressing allows; it joins with wake: named, and may leave again.
func TestIntercomCallInSessionToRoom(t *testing.T) {
	f := newICFixture(t, "session:*")
	f.standing.Name = "spider"
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, who, err := f.ic.CallIn(Poster{ID: f.alice.ID}, f.room.ID, "session:"+f.ticket.ActorID); err != nil || who != ident.ID(f.ticket.ActorID) {
		t.Fatalf("alice calls in the ticket session: %s, %v", who, err)
	}
	if w, ok := memberWake(f.store, f.room.ID, ident.ID(f.ticket.ActorID)); !ok || w != WakeNamed {
		t.Fatalf("ticket session's membership = %q, %v; want named", w, ok)
	}
	if got := f.log.InboxSince(ident.ID(f.ticket.ActorID), 0, 0); len(got) != 1 || !IsCallInNotice(got[0].ID) {
		t.Fatalf("ticket session's inbox = %+v, want the call-in notice", got)
	}
	// The ticket session, now a member, calls in spider: its addressing allows it.
	if _, _, err := f.ic.CallIn(f.poster(f.ticket), f.room.ID, "session:spider"); err != nil {
		t.Fatalf("session calls in spider: %v", err)
	}
	if w, ok := memberWake(f.store, f.room.ID, ident.ID(f.standing.ActorID)); !ok || w != WakeNamed {
		t.Fatalf("spider's membership = %q, %v; want named", w, ok)
	}
	if err := f.ic.LeaveChannel(ident.ID(f.ticket.ActorID), f.room.ID); err != nil {
		t.Fatalf("a session leaves a room it was called into: %v", err)
	}
	if isCurrentMember(f.store, f.room.ID, ident.ID(f.ticket.ActorID)) {
		t.Fatal("ticket session is still in the room")
	}
}

// A role's sessions join the rooms its scope.rooms lists when they're set up
// (raised, or at serve start for running ones), with the listed wake; they
// can't leave them. A listed room that is gone is skipped.
func TestIntercomRoleRoomsJoinAtSetUp(t *testing.T) {
	f := newICFixture(t)
	ops, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceRoom, Key: "ops", Label: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	role := Role{Name: "impl", Scope: Scope{Rooms: []RoomMembership{{Name: "eng", Wake: WakeAll}, {Name: "ops", Wake: WakeNamed}, {Name: "gone", Wake: WakeNamed}}}}
	if err := f.store.PutRole("acme", role); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatalf("SetUp: %v", err)
	}
	if err := f.ic.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, inst := range []Instance{f.ticket, f.personal, f.standing} {
		self := ident.ID(inst.ActorID)
		if w, ok := memberWake(f.store, f.room.ID, self); !ok || w != WakeAll {
			t.Errorf("%s in eng: %q, %v; want all", inst.ActorID, w, ok)
		}
		if w, ok := memberWake(f.store, ops.ID, self); !ok || w != WakeNamed {
			t.Errorf("%s in ops: %q, %v; want named", inst.ActorID, w, ok)
		}
	}
	if err := f.ic.LeaveChannel(ident.ID(f.ticket.ActorID), f.room.ID); !errors.Is(err, ErrFixedMembers) {
		t.Fatalf("a session leaves a room its role lists: %v, want ErrFixedMembers", err)
	}
	// The role's wake changes: the next set-up follows it.
	role.Scope.Rooms[0].Wake = WakeNamed
	if err := f.store.PutRole("acme", role); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if w, _ := memberWake(f.store, f.room.ID, ident.ID(f.ticket.ActorID)); w != WakeNamed {
		t.Fatalf("after the role changed: %q, want named", w)
	}
}

func TestCallInInvitee(t *testing.T) {
	for id, want := range map[string]string{
		"callin:chn_x:usr_y:123":                    "usr_y",
		"callin:chn_x:standing-acme-impl-spider:12": "standing-acme-impl-spider",
		"callin:chn_x:usr_y":                        "",
		"callin:chn_x::123":                         "",
		"notice:chn_x:usr_y:123":                    "",
		"":                                          "",
	} {
		got, ok := CallInInvitee(id)
		if string(got) != want || ok != (want != "") {
			t.Errorf("CallInInvitee(%q) = %q, %v; want %q", id, got, ok, want)
		}
	}
}
