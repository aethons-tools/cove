package jam

import (
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// ticketChannelOf is the fixture ticket's channel (it must exist).
func (f *icFixture) ticketChannelOf() Channel {
	f.t.Helper()
	ch, ok := f.store.ChannelByKey(f.project.ID, SourceWork, string(f.tracker.ID)+"/ACME-7")
	if !ok {
		f.t.Fatal("no ticket channel")
	}
	return ch
}

// A ticket session has no channel of its own: it is a member of its
// ticket's work log, where its post with no address goes.
func TestIntercomTicketSessionIsInItsLog(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if !isMember(f.store, f.ticketChannelOf().ID, ident.ID(f.ticket.ActorID)) {
		t.Fatal("ticket session is not a member of its ticket channel")
	}
	if p := f.mustPlan(f.ticket, ""); p.Channel.ID != f.ticketChannelOf().ID {
		t.Fatalf("send without to = %+v, want its log", p.Channel)
	}
	if got := f.store.ListChannels(f.project.ID, SourceSession); len(got) != 0 {
		t.Fatalf("session channels = %+v", got)
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
	if p := f.mustPlan(f.ticket, "ticket:ACME-7"); p.Channel.Kind != SourceWork {
		t.Fatalf("ticket:ACME-7 = %+v", p.Channel)
	}
}

// An ended ticket session leaves its ticket channel, which stays.
func TestIntercomEndedTicketSessionLeavesItsLog(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Ended(f.ticket); err != nil {
		t.Fatal(err)
	}
	log := f.ticketChannelOf()
	if isCurrentMember(f.store, log.ID, ident.ID(f.ticket.ActorID)) || log.Worker != "" || log.Status != StatusLive {
		t.Fatalf("after Ended: %+v", log)
	}
}
