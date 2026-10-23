package jam

import (
	"testing"

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
