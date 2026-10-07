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
	chat := f.mustPlan(f.personal, "user:alice").Channel
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
		{"a room takes no sessions", Poster{ID: f.alice.ID}, f.room.ID, "session:" + f.standing.ActorID, ErrFixedMembers},
	}
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 10); err != nil {
		t.Fatal(err)
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
