package main

import (
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/push"
)

func TestPushReader(t *testing.T) {
	k := newRelayKit(t)
	r := pushReader{store: k.st, ic: k.ic}
	m := k.send(k.std, "chat:user:alice,user:bob", "standup")
	ch, ok := r.Channel(m.Channel)
	if !ok || ch.Kind != jam.SourceChat {
		t.Fatalf("Channel = %+v, %v", ch, ok)
	}
	if got := r.Members(ch.ID); len(got) != 3 {
		t.Fatalf("Members = %v", got)
	}
	std := ident.ID(k.std.ActorID)
	if !r.CanSee(k.alice.ID, m) || !r.CanSee(k.bob.ID, m) {
		t.Fatal("members can't see their chat")
	}
	if r.CanSee(k.carol.ID, m) {
		t.Fatal("carol sees a chat she is not in")
	}
	if r.CanSee(std, m) {
		t.Fatal("a session is never a push reader")
	}
	if got, want := r.PartyLabel(k.alice.ID), k.ic.PartyOf(k.alice.ID).Label; got != want {
		t.Fatalf("PartyLabel = %q, want %q", got, want)
	}
	// A chat's label is the other members', as on the rail.
	if v, ok := jam.UserChannel(k.st, k.ic, k.lg, k.alice.ID, ch.ID); !ok || v.Label != r.ChannelLabel(ch, k.alice.ID) {
		t.Fatalf("rail label %q, push label %q", v.Label, r.ChannelLabel(ch, k.alice.ID))
	}

	// Leaving the project ends it at once, as for the live audience.
	p, _ := k.st.GetProject("acme")
	if err := k.st.RemoveMember(p.ID, k.bob.ID); err != nil {
		t.Fatal(err)
	}
	if r.CanSee(k.bob.ID, m) {
		t.Fatal("bob still sees the chat after leaving the project")
	}
	// An archived channel notifies nobody.
	if err := k.st.ArchiveChannel(ch.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Channel(ch.ID); ok || r.CanSee(k.alice.ID, m) {
		t.Fatal("an archived channel is still a push channel")
	}
}

func TestPushPayload(t *testing.T) {
	k := newRelayKit(t)
	r := pushReader{store: k.st, ic: k.ic}
	focus := push.NewFocus(nil)
	unread := func(u ident.ID) int { return jam.UnreadTotal(k.st, k.ic, k.lg, u) }
	payload := pushPayload(r, focus, unread, true)
	m := k.send(k.std, "chat:user:alice,user:bob", "standup **now**")

	p, ok := payload(m, k.alice.ID)
	if !ok {
		t.Fatal("alice is a recipient")
	}
	rail, _ := jam.UserChannel(k.st, k.ic, k.lg, k.alice.ID, m.Channel)
	if p.Body != "standup now" || p.Channel != string(m.Channel) || p.ID != m.ID || p.Unread != 1 ||
		p.Title != k.ic.PartyOf(ident.ID(k.std.ActorID)).Label+" · "+rail.Label {
		t.Fatalf("payload = %+v", p)
	}
	if _, ok := payload(m, k.carol.ID); ok {
		t.Fatal("carol is not in the chat")
	}
	focus.Set(k.bob.ID, m.Channel)
	if _, ok := payload(m, k.bob.ID); ok {
		t.Fatal("bob has the chat open: no push")
	}
	if p, _ := pushPayload(r, nil, unread, false)(m, k.alice.ID); p.Body != "New message" {
		t.Fatalf("preview off: body %q", p.Body)
	}
}

// Someone who read the message on another device before a retry sent it
// isn't buzzed for it; an unread message still buzzes.
func TestPushPayloadSkipsAlreadyRead(t *testing.T) {
	k := newRelayKit(t)
	r := pushReader{store: k.st, ic: k.ic}
	unread := func(u ident.ID) int { return jam.UnreadTotal(k.st, k.ic, k.lg, u) }
	payload := pushPayload(r, nil, unread, true)
	m1 := k.send(k.std, "chat:user:alice,user:bob", "first")
	m2 := k.send(k.std, "chat:user:alice,user:bob", "second")
	if got := r.ReadThrough(k.alice.ID, m1.Channel); got != 0 {
		t.Fatalf("ReadThrough before reading = %d", got)
	}
	if err := k.st.CommitChannelRead(k.alice.ID, m1.Channel, m1.Seq); err != nil {
		t.Fatal(err)
	}
	if got := r.ReadThrough(k.alice.ID, m1.Channel); got != m1.Seq {
		t.Fatalf("ReadThrough = %d, want %d", got, m1.Seq)
	}
	if _, ok := payload(m1, k.alice.ID); ok {
		t.Fatal("alice already read m1: no push")
	}
	if _, ok := payload(m2, k.alice.ID); !ok {
		t.Fatal("alice hasn't read m2: push")
	}
	if _, ok := payload(m1, k.bob.ID); !ok {
		t.Fatal("bob hasn't read m1: push")
	}
}
