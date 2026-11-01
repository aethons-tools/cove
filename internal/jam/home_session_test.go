package jam

import (
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestHomeSession(t *testing.T) {
	f := newICFixture(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.ic.SetUp(f.personal))
	must(f.ic.SetUp(f.ticket))
	sessCh := f.sessionChannel(f.personal)
	ticketCh, ok := f.ic.TicketChannelOf(f.ticket)
	if !ok {
		t.Fatal("no ticket channel")
	}

	gone := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", OwnerID: f.alice.ID, SessionKind: "personal", Phase: PhaseLive}
	must(f.store.PutInstance(gone))
	must(f.ic.SetUp(gone))
	goneCh := f.sessionChannel(gone)
	gone.Phase = PhaseGone
	must(f.store.PutInstance(gone))

	must(f.store.CreateProject("beta"))
	beta, _ := f.store.GetProject("beta")
	otherTicket, err := f.store.CreateChannel(Channel{ProjectID: beta.ID, Kind: SourceTicket, Key: ticketCh.Key, Label: "ACME-7"})
	must(err)
	chat, err := f.store.CreateChannel(Channel{ProjectID: f.project.ID, Kind: SourceChat, Key: "chat-ab", Label: "chat"})
	must(err)

	for _, tc := range []struct {
		name string
		ch   ident.ID
		want string // "" = none
	}{
		{"a session channel with a live instance", sessCh.ID, f.personal.ActorID},
		{"a gone instance", goneCh.ID, ""},
		{"a ticket channel worked by a live session", ticketCh.ID, f.ticket.ActorID},
		{"the same unit in another project", otherTicket.ID, ""},
		{"a chat", chat.ID, ""},
		{"an unknown channel", ident.New(ident.Channel), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, ok := HomeSession(f.store, tc.ch)
			if tc.want == "" {
				if ok {
					t.Fatalf("HomeSession = %+v, want none", inst)
				}
				return
			}
			if !ok || inst.ActorID != tc.want {
				t.Fatalf("HomeSession = %+v, %v; want %s", inst, ok, tc.want)
			}
		})
	}
}
