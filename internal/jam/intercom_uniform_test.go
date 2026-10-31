package jam

import (
	"context"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/ident"
)

// ticketChannelOf is the fixture ticket's channel (it must exist).
func (f *icFixture) ticketChannelOf() Channel {
	f.t.Helper()
	ch, ok := f.store.ChannelByKey(f.project.ID, SourceTicket, string(f.tracker.ID)+"/ACME-7")
	if !ok {
		f.t.Fatal("no ticket channel")
	}
	return ch
}

// Every session's home is its own session channel; a ticket session is also a
// member of its ticket's channel.
func TestIntercomTicketSessionHomeIsSessionChannel(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	home, err := f.ic.HomeChannel(f.ticket)
	if err != nil || home.Kind != SourceSession || home.Key != f.ticket.ActorID {
		t.Fatalf("home = %+v, %v; want its session channel", home, err)
	}
	if !isMember(f.store, f.ticketChannelOf().ID, ident.ID(f.ticket.ActorID)) {
		t.Fatal("ticket session is not a member of its ticket channel")
	}
	if p := f.mustPlan(f.ticket, ""); p.Channel.ID != home.ID {
		t.Fatalf("send without to = %+v, want home", p.Channel)
	}
}

// An issue comment (relayed into the ticket channel) still reaches the session.
func TestIntercomTicketSessionHearsIssueComments(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.PostTrusted(f.ticketChannelOf(), intercom.Squawk{ID: "c1", From: f.alice.ID, Body: "please also fix X"}); err != nil {
		t.Fatal(err)
	}
	got := f.log.InboxSince(ident.ID(f.ticket.ActorID), 0, 0)
	if len(got) != 1 || got[0].Body != "please also fix X" {
		t.Fatalf("inbox = %+v", got)
	}
}

// ticket:<own key> still posts on the ticket.
func TestIntercomOwnTicketSendGoesToTicket(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if p := f.mustPlan(f.ticket, "ticket:ACME-7"); p.Channel.Kind != SourceTicket {
		t.Fatalf("ticket:ACME-7 = %+v", p.Channel)
	}
}

// user:<x> from a ticket session lands in its home when x is already there.
func TestIntercomTicketSessionUserSendJoinsHome(t *testing.T) {
	f := newICFixture(t, "user:*")
	home := f.mustPlan(f.ticket, "").Channel
	if err := f.ic.JoinChannel(f.alice.ID, home.ID); err != nil {
		t.Fatal(err)
	}
	if p := f.mustPlan(f.ticket, "user:alice"); p.Channel.ID != home.ID {
		t.Fatalf("user:alice = %+v, want home %s", p.Channel, home.ID)
	}
}

// An ended ticket session leaves its ticket channel and its own is archived.
func TestIntercomEndedTicketSessionLeavesBoth(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	home, _ := f.ic.HomeChannel(f.ticket)
	if err := f.ic.Ended(f.ticket); err != nil {
		t.Fatal(err)
	}
	if isCurrentMember(f.store, f.ticketChannelOf().ID, ident.ID(f.ticket.ActorID)) {
		t.Fatal("still in the ticket channel")
	}
	if ch, _ := f.store.GetChannel(home.ID); ch.Status == StatusLive {
		t.Fatal("session channel not archived")
	}
}

// Escalation calls people into a ticket session's own channel.
func TestIntercomEscalateTicketSessionIntoSessionChannel(t *testing.T) {
	f := newICFixture(t)
	f.ticket.Activity = ActivityWaiting
	if err := f.store.PutInstance(f.ticket); err != nil {
		t.Fatal(err)
	}
	alice, _ := MemberOf(f.store, f.project.ID, f.alice.ID)
	if err := f.ic.Escalate(context.Background(), f.ticket, 0, "", []Member{alice}); err != nil {
		t.Fatal(err)
	}
	home, _ := f.ic.HomeChannel(f.ticket)
	if !isMember(f.store, home.ID, f.alice.ID) {
		t.Fatal("alice not called into the session channel")
	}
	got := f.log.InboxSince(f.alice.ID, 0, 0)
	if len(got) != 1 || strings.Contains(got[0].Body, "its issue included") {
		t.Fatalf("alice's inbox = %+v", got)
	}
}
