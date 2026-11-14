package jam

import (
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestChannelSession(t *testing.T) {
	f := newICFixture(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.ic.SetUp(f.personal))
	must(f.ic.SetUp(f.ticket))
	dm := f.starterDM(f.personal)
	ticketCh, ok := f.ic.TicketChannelOf(f.ticket)
	if !ok {
		t.Fatal("no ticket channel")
	}

	gone := Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", OwnerID: f.alice.ID, SessionKind: "personal", Phase: PhaseLive}
	must(f.store.PutInstance(gone))
	must(f.ic.SetUp(gone))
	goneDM := f.starterDM(gone)
	gone.Phase = PhaseGone
	must(f.store.PutInstance(gone))

	must(f.store.CreateProject("beta"))
	beta, _ := f.store.GetProject("beta")
	otherTicket, err := f.store.CreateChannel(Channel{ProjectID: beta.ID, Kind: SourceWork, Key: ticketCh.Key, Label: "ACME-7"})
	must(err)
	people, err := f.ic.chat(f.project, []ident.ID{f.alice.ID, f.bob.ID})
	must(err)
	agents, err := f.ic.chat(f.project, []ident.ID{ident.ID(f.ticket.ActorID), ident.ID(f.standing.ActorID)})
	must(err)
	old := f.legacySessionChannel(f.standing)

	for _, tc := range []struct {
		name string
		ch   ident.ID
		want string // "" = none
	}{
		{"a dm with a live session", dm.ID, f.personal.ActorID},
		{"a dm with a gone session", goneDM.ID, ""},
		{"a work log worked by a live session", ticketCh.ID, f.ticket.ActorID},
		{"the same unit in another project", otherTicket.ID, ""},
		{"a dm of people", people.ID, ""},
		{"a dm of two sessions", agents.ID, ""},
		{"an old session channel", old.ID, ""},
		{"an unknown channel", ident.New(ident.Channel), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, ok := ChannelSession(f.store, tc.ch)
			if tc.want == "" {
				if ok {
					t.Fatalf("ChannelSession = %+v, want none", inst)
				}
				return
			}
			if !ok || inst.ActorID != tc.want {
				t.Fatalf("ChannelSession = %+v, %v; want %s", inst, ok, tc.want)
			}
		})
	}
}

// A work log's session is its current worker, while an older session on the
// same unit still lives.
func TestChannelSessionIsTheWorker(t *testing.T) {
	f := newICFixture(t)
	for range 5 {
		next := f.ticket
		next.ActorID = string(ident.New(ident.Session))
		if err := f.store.PutInstance(next); err != nil {
			t.Fatal(err)
		}
		if err := f.ic.SetUp(next); err != nil {
			t.Fatal(err)
		}
		ch, _ := f.ic.TicketChannelOf(next)
		if inst, ok := ChannelSession(f.store, ch.ID); !ok || inst.ActorID != next.ActorID {
			t.Fatalf("ChannelSession = %+v, %v; want the worker %s", inst, ok, next.ActorID)
		}
	}
}

// A session's conversation for a person is its unit's log, else their dm
// with it; it creates none.
func TestSessionConversation(t *testing.T) {
	f := newICFixture(t)
	if _, ok := f.ic.SessionConversation(f.personal, f.alice.ID); ok {
		t.Fatal("a conversation before any was made")
	}
	if err := f.ic.SetUp(f.personal); err != nil {
		t.Fatal(err)
	}
	if ch, ok := f.ic.SessionConversation(f.personal, f.alice.ID); !ok || ch.ID != f.starterDM(f.personal).ID {
		t.Fatalf("personal for alice = %+v, %v", ch, ok)
	}
	if _, ok := f.ic.SessionConversation(f.personal, f.bob.ID); ok {
		t.Fatal("bob has no conversation with alice's session")
	}
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	log, _ := f.ic.TicketChannelOf(f.ticket)
	if ch, ok := f.ic.SessionConversation(f.ticket, f.bob.ID); !ok || ch.ID != log.ID {
		t.Fatalf("ticket for bob = %+v, %v; want its log", ch, ok)
	}
}
