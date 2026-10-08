package jam

import (
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func TestIntercomPostRecordsAudience(t *testing.T) {
	f := newICFixture(t, "user:*")
	p := f.mustPlan(f.standing, "user:alice")
	m, err := f.ic.Post(p, intercom.Squawk{From: ident.ID(f.standing.ActorID), Body: "hello"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if m.Channel != p.Channel.ID || m.Seq == 0 {
		t.Fatalf("posted = %+v", m)
	}
	if got := f.log.InboxSince(f.alice.ID, 0, 0); len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("alice's inbox = %+v", got)
	}
	if got := f.log.InboxSince(ident.ID(f.standing.ActorID), 0, 0); len(got) != 0 {
		t.Fatalf("the author hears nothing of their own: %+v", got)
	}
}

// A person who posts in a ticket or room becomes a member (they hear what
// follows); a session posting to a room it may address does not.
func TestIntercomPostJoinsPeople(t *testing.T) {
	f := newICFixture(t, "channel:*")
	p, err := f.ic.PlanChannel(Poster{ID: f.bob.ID}, f.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.ic.Post(p, intercom.Squawk{From: f.bob.ID, Body: "hi room"})
	if err != nil {
		t.Fatal(err)
	}
	if ms := f.store.ChannelMembers(f.room.ID); len(ms) != 1 || ms[0] != (ChannelMember{ParticipantID: f.bob.ID, JoinedSeq: m.Seq}) {
		t.Fatalf("room members = %+v", ms)
	}
	if _, err := f.ic.Post(f.mustPlan(f.standing, "channel:eng"), intercom.Squawk{From: ident.ID(f.standing.ActorID), Body: "from a session"}); err != nil {
		t.Fatal(err)
	}
	if got := f.log.InboxSince(f.bob.ID, 0, 0); len(got) != 1 || got[0].Body != "from a session" {
		t.Fatalf("bob's inbox = %+v", got)
	}
	if isMember(f.store, f.room.ID, ident.ID(f.standing.ActorID)) {
		t.Fatal("a session doesn't join a room by posting")
	}
}

// Trusted posts (relay ingress, Jam's notices) skip CanPost but still record
// the audience, and never land in an archived channel.
func TestIntercomPostTrusted(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	ticket := f.mustPlan(f.ticket, "").Channel
	acc, err := f.store.UpsertAccount(Account{ConnectionID: f.tracker.ID, ServiceUID: "u-1", Label: "Stranger"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.ic.PostTrusted(ticket, intercom.Squawk{ID: "in:linear:c1", From: acc.ID, Body: "a comment", Origin: f.tracker.ID, OriginRef: "ACME-7"})
	if err != nil {
		t.Fatalf("PostTrusted: %v", err)
	}
	if got := f.log.InboxSince(ident.ID(f.ticket.ActorID), 0, 0); len(got) != 1 || got[0].ID != m.ID || got[0].OriginRef != "ACME-7" {
		t.Fatalf("session inbox = %+v", got)
	}
	if !isMember(f.store, ticket.ID, acc.ID) {
		t.Fatal("an account posting on a ticket joins it")
	}
	if err := f.store.ArchiveChannel(f.room.ID); err != nil {
		t.Fatal(err)
	}
	room, _ := f.store.GetChannel(f.room.ID)
	if _, err := f.ic.PostTrusted(room, intercom.Squawk{From: f.alice.ID, Body: "late"}); !errors.Is(err, ErrRemoved) {
		t.Fatalf("trusted post into an archived channel: %v", err)
	}
}

// Sessions set up before ticket channels existed get theirs at startup, so
// replies on their tickets have somewhere to land before they ever send.
func TestIntercomReconcile(t *testing.T) {
	f := newICFixture(t)
	gone := f.ticket
	gone.ActorID, gone.Unit, gone.Phase = string(ident.New(ident.Session)), "ACME-8", PhaseGone
	if err := f.store.PutInstance(gone); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if ch, ok := f.store.ChannelByBinding(f.tracker.ID, "ACME-7"); !ok || !isMember(f.store, ch.ID, ident.ID(f.ticket.ActorID)) {
		t.Fatalf("live ticket session's channel = %+v, %v", ch, ok)
	}
	if _, ok := f.store.ChannelByBinding(f.tracker.ID, "ACME-8"); ok {
		t.Fatal("an ended session gets no channel")
	}
}

// Jam's notices go as the session into its home channel — for a personal
// session, its own channel with its starter in it — and still arrive after
// teardown, from the instance alone; one who left the project hears none.
func TestIntercomNotify(t *testing.T) {
	f := newICFixture(t)
	if err := f.store.RemoveInstance(f.personal.ActorID); err != nil {
		t.Fatal(err)
	}
	m, err := f.ic.Notify(f.personal, "nag:x:1", "still there?")
	if err != nil {
		t.Fatalf("Notify after teardown: %v", err)
	}
	if got := f.log.InboxSince(f.alice.ID, 0, 0); len(got) != 1 || got[0].ID != "nag:x:1" || got[0].From != ident.ID(f.personal.ActorID) {
		t.Fatalf("alice's inbox = %+v (notice %+v)", got, m)
	}
	if err := f.store.RemoveMember(f.project.ID, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.Notify(f.personal, "", "anyone?"); err != nil {
		t.Fatalf("Notify after the owner left: %v", err)
	}
	if got := f.log.InboxSince(f.alice.ID, 0, 0); len(got) != 1 {
		t.Fatalf("one who left the project hears nothing more: %+v", got)
	}
}

func TestIntercomPlanPersonChat(t *testing.T) {
	f := newICFixture(t)
	if _, err := f.ic.PlanPersonChat(f.bob.ID, ident.ID(f.personal.ActorID)); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("a chat with a session: %v (a session is reached in its home channel: PlanHome)", err)
	}
	if p, err := f.ic.PlanPersonChat(f.bob.ID, f.alice.ID); err != nil || len(p.Audience) != 1 || p.Audience[0] != f.alice.ID {
		t.Fatalf("bob with alice = %+v, %v", p, err)
	}
	for name, c := range map[string][2]ident.ID{
		"with a non-member": {f.bob.ID, f.carol.ID},
		"from a non-member": {f.carol.ID, ident.ID(f.personal.ActorID)},
		"with oneself":      {f.bob.ID, f.bob.ID},
		"with no one":       {f.bob.ID, "usr_01j9q3zzzzzzzzzzzzzzzzzz"},
		"from a session":    {ident.ID(f.ticket.ActorID), f.bob.ID},
	} {
		if _, err := f.ic.PlanPersonChat(c[0], c[1]); !errors.Is(err, ErrSendUnresolved) {
			t.Errorf("%s: %v, want ErrSendUnresolved", name, err)
		}
	}
}

// A personal session recorded with only its starter's name still calls them in.
func TestIntercomHomeChannelStarterByName(t *testing.T) {
	f := newICFixture(t)
	old := f.personal
	old.OwnerID, old.Owner = "", "alice"
	ch, err := f.ic.HomeChannel(old)
	if err != nil || !isMember(f.store, ch.ID, f.alice.ID) {
		t.Fatalf("owner by name: %+v, %v", ch, err)
	}
}
