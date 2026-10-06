package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/relay"
)

func newTestStore(t *testing.T) *jam.MemStore {
	t.Helper()
	return jam.NewMemStore()
}

// relayKit is project acme chatting over Discord, with members alice (inbox
// inbox-A, bound to Discord id 111, Linear @alice.h), bob (inbox inbox-B,
// unbound) and carol (no Discord, Linear @carol.h); a ticket session on
// ACME-7, a personal session alice started, and a standing one — over a
// channel log, the intercom and the relay directory.
type relayKit struct {
	t                     *testing.T
	st                    *jam.MemStore
	lg                    *intercom.Log
	ic                    *jam.Intercom
	dir                   *directory
	linear, discord       jam.Connection
	alice, bob, carol     jam.User
	ticket, personal, std jam.Instance
	actor                 jam.Actor
}

func newRelayKit(t *testing.T) *relayKit {
	t.Helper()
	k := &relayKit{t: t, st: newTestStore(t)}
	st := k.st
	mustCreateProject(t, st, "acme")
	for _, h := range []jam.Human{
		{Name: "alice", Handle: "alice.h", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-A", UserID: "111"}}},
		{Name: "bob", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-B"}}},
		{Name: "carol", Handle: "carol.h"},
	} {
		if err := st.AddHuman("acme", h); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	k.linear, _ = st.ConnectionOfKind("linear")
	k.discord, _ = st.ConnectionOfKind("discord")
	for _, u := range []*jam.User{&k.alice, &k.bob, &k.carol} {
		name := map[*jam.User]string{&k.alice: "alice", &k.bob: "bob", &k.carol: "carol"}[u]
		id, _ := st.LookupName(ident.User, name)
		*u, _ = st.GetUser(id)
	}
	if err := st.PutRole("acme", jam.Role{Name: "impl", Scope: jam.Scope{Addressing: []string{"user:*", "channel:*"}}}); err != nil {
		t.Fatal(err)
	}
	k.actor = jam.Actor{ID: "a", Grants: []jam.Grant{{Project: "acme", Role: "impl"}}}
	k.ticket = jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Unit: "ACME-7", Phase: jam.PhaseLive}
	k.personal = jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", Owner: "alice", OwnerID: k.alice.ID,
		SessionKind: jam.SessionKindPersonal, Phase: jam.PhaseLive}
	k.std = jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "acme", Role: "impl", SessionKind: jam.SessionKindStanding, Phase: jam.PhaseLive}
	for _, inst := range []jam.Instance{k.ticket, k.personal, k.std} {
		if err := st.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	tracker := func() (ident.ID, bool) { return k.linear.ID, true }
	k.lg = intercom.NewMemLog(nil)
	k.ic = jam.NewIntercom(st, tracker, k.lg, nil, nil)
	if err := k.ic.SetUp(k.ticket); err != nil {
		t.Fatal(err)
	}
	receipts, err := newFileReceipts(filepath.Join(t.TempDir(), "receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	k.dir = &directory{store: st, ic: k.ic, project: "acme", selfIdentity: "Jam", tracker: tracker, discord: k.discord.ID, receipts: receipts}
	return k
}

// send posts body from a session to addr ("" = its default channel).
func (k *relayKit) send(inst jam.Instance, addr, body string) intercom.Squawk {
	k.t.Helper()
	a := k.actor
	pl, err := k.ic.Plan(jam.Poster{ID: ident.ID(inst.ActorID), Session: &inst, Actor: &a}, addr, time.Now())
	if err != nil {
		k.t.Fatalf("Plan(%q): %v", addr, err)
	}
	m, err := k.ic.Post(pl, intercom.Squawk{From: ident.ID(inst.ActorID), Body: body})
	if err != nil {
		k.t.Fatal(err)
	}
	return m
}

func addresses(ds []relay.Delivery) map[string]string {
	out := map[string]string{}
	for _, d := range ds {
		out[d.Service+":"+d.Address] = d.BodyPrefix
	}
	return out
}

func wantSurfaces(t *testing.T, what string, got []relay.Delivery, want map[string]string) {
	t.Helper()
	a := addresses(got)
	if len(a) != len(want) {
		t.Fatalf("%s: surfaces = %v, want %v", what, a, want)
	}
	for k, v := range want {
		if p, ok := a[k]; !ok || p != v {
			t.Fatalf("%s: surfaces = %v, want %v", what, a, want)
		}
	}
}

func TestSurfacesTicket(t *testing.T) {
	k := newRelayKit(t)
	m := k.send(k.ticket, "", "status")
	wantSurfaces(t, "session on its ticket", k.dir.Surfaces("linear", m), map[string]string{"linear:ACME-7": ""})
	wantSurfaces(t, "discord", k.dir.Surfaces("discord", m), nil)

	ch, _ := k.ic.DefaultChannel(k.ticket)
	byAlice, err := k.ic.PostTrusted(ch, intercom.Squawk{From: k.alice.ID, Body: "from /me"})
	if err != nil {
		t.Fatal(err)
	}
	wantSurfaces(t, "a person on the ticket", k.dir.Surfaces("linear", byAlice), map[string]string{"linear:ACME-7": "alice: "})
	in, err := k.ic.PostTrusted(ch, intercom.Squawk{ID: "in:linear:c1", From: k.alice.ID, Body: "a comment", Origin: k.linear.ID, OriginRef: "ACME-7"})
	if err != nil {
		t.Fatal(err)
	}
	wantSurfaces(t, "an ingested comment", k.dir.Surfaces("linear", in), nil)
}

func TestSurfacesChatOverDiscord(t *testing.T) {
	k := newRelayKit(t)
	label := k.ic.PartyOf(ident.ID(k.personal.ActorID)).Label
	m := k.send(k.personal, "", "done?")
	wantSurfaces(t, "personal session to its owner", k.dir.Surfaces("discord", m), map[string]string{"discord:inbox-A": label + ": "})
	wantSurfaces(t, "no linear fallback for alice", k.dir.Surfaces("linear", m), nil)

	group := k.send(k.std, "chat:user:alice,user:bob", "standup")
	wantSurfaces(t, "group chat", k.dir.Surfaces("discord", group), map[string]string{"discord:inbox-A": k.ic.PartyOf(ident.ID(k.std.ActorID)).Label + ": ", "discord:inbox-B": k.ic.PartyOf(ident.ID(k.std.ActorID)).Label + ": "})
	ch, _ := k.st.GetChannel(group.Channel)
	reply, err := k.ic.PostTrusted(ch, intercom.Squawk{ID: "in:discord:9", From: k.alice.ID, Body: "here", Origin: k.discord.ID, OriginRef: "inbox-A"})
	if err != nil {
		t.Fatal(err)
	}
	wantSurfaces(t, "alice's reply reaches bob, not back to her", k.dir.Surfaces("discord", reply), map[string]string{"discord:inbox-B": "alice: "})
}

// A person with no Discord inbox is @-mentioned on the ticket of a session in
// the chat — the pre-channel "user:x from a ticket session" delivery.
func TestSurfacesChatLinearFallback(t *testing.T) {
	k := newRelayKit(t)
	m := k.send(k.ticket, "user:carol", "can you look?")
	wantSurfaces(t, "carol via the ticket", k.dir.Surfaces("linear", m), map[string]string{"linear:ACME-7": "@carol.h "})
	wantSurfaces(t, "carol has no discord", k.dir.Surfaces("discord", m), nil)
	if s := k.dir.Surfaces("linear", k.send(k.std, "user:carol", "x")); len(s) != 0 {
		t.Fatalf("no session in the chat has a ticket: %+v", s)
	}
}

func TestSurfacesRooms(t *testing.T) {
	k := newRelayKit(t)
	for _, rc := range []jam.RosterChannel{{Name: "eng", Service: "discord", Ref: "chan-C"}, {Name: "ops", Service: "linear", Ref: "ACME-1"}} {
		if err := putRoom(k.st, "acme", rc.Name, rc.Service, rc.Ref); err != nil {
			t.Fatal(err)
		}
	}
	label := k.ic.PartyOf(ident.ID(k.std.ActorID)).Label
	wantSurfaces(t, "discord room", k.dir.Surfaces("discord", k.send(k.std, "channel:eng", "x")), map[string]string{"discord:chan-C": label + ": "})
	wantSurfaces(t, "linear room", k.dir.Surfaces("linear", k.send(k.std, "channel:ops", "x")), map[string]string{"linear:ACME-1": ""})
}

func TestRouteLinear(t *testing.T) {
	k := newRelayKit(t)
	ticket, _ := k.ic.DefaultChannel(k.ticket)
	ev := relay.Event{ForeignID: "c1", Surface: "ACME-7", Author: "Stranger", AuthorID: "lin-9", Body: "hi", ReplyToForeign: "c0"}
	r, ok := k.dir.Route("linear", "acme", ev)
	if !ok || r.Channel != ticket.ID || r.From.Kind() != ident.Account || r.ReplyTo != "in:linear:c0" || r.Origin != k.linear.ID || r.OriginRef != "ACME-7" {
		t.Fatalf("routed = %+v, %v", r, ok)
	}
	if err := k.dir.Post(r, intercom.Squawk{ID: "in:linear:c1", From: r.From, Body: ev.Body, Origin: r.Origin, OriginRef: r.OriginRef}); err != nil {
		t.Fatal(err)
	}
	if got := k.lg.InboxSince(ident.ID(k.ticket.ActorID), 0, 0); len(got) != 1 || got[0].ID != "in:linear:c1" {
		t.Fatalf("the session hears the comment: %+v", got)
	}
	// Once an operator links the account to a member, the comment is theirs.
	if err := k.st.LinkAccount(r.From, k.bob.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := k.dir.Route("linear", "acme", ev); r.From != k.bob.ID {
		t.Fatalf("linked author = %s", r.From)
	}
	for name, e := range map[string]relay.Event{
		"Jam's own":     {ForeignID: "c2", Surface: "ACME-7", Author: "Jam", AuthorID: "lin-jam"},
		"another issue": {ForeignID: "c3", Surface: "ACME-99", Author: "x", AuthorID: "lin-1"},
		"no author id":  {ForeignID: "c4", Surface: "ACME-7", Author: "x"},
	} {
		if _, ok := k.dir.Route("linear", "acme", e); ok {
			t.Errorf("%s: must be unroutable", name)
		}
	}
}

// The Discord reply loop: a nag posted to alice's inbox records a receipt; her
// reply routes back into the chat as hers, replying to the nag.
func TestRouteDiscordReplyLoop(t *testing.T) {
	k := newRelayKit(t)
	nag, err := k.ic.Notify(k.personal, jam.NagMessageID(k.personal.ActorID, time.Unix(1, 0)), "still there?")
	if err != nil {
		t.Fatal(err)
	}
	surfaces := k.dir.Surfaces("discord", nag)
	fc := &fakeDiscordClient{postID: "D1"}
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: k.dir.receipts}
	if _, err := s.Deliver(context.Background(), surfaces[0], nag); err != nil {
		t.Fatal(err)
	}
	ev := relay.Event{ForeignID: "D2", Surface: "inbox-A", Author: "Alice", AuthorID: "111", Body: "keep", ReplyToForeign: "D1"}
	r, ok := k.dir.Route("discord", "acme", ev)
	if !ok || r.Channel != nag.Channel || r.From != k.alice.ID || r.ReplyTo != nag.ID || r.Origin != k.discord.ID || r.OriginRef != "inbox-A" {
		t.Fatalf("routed = %+v, %v", r, ok)
	}
	if err := k.dir.Post(r, intercom.Squawk{ID: "in:discord:D2", From: r.From, Body: ev.Body, ReplyTo: r.ReplyTo, Origin: r.Origin, OriginRef: r.OriginRef}); err != nil {
		t.Fatal(err)
	}
	if got := k.lg.InboxSince(ident.ID(k.personal.ActorID), 0, 0); len(got) != 1 || got[0].ReplyTo != nag.ID || got[0].From != k.alice.ID {
		t.Fatalf("the session hears alice's reply to its nag: %+v", got)
	}
	// A stranger in alice's inbox is an account, never alice (she's bound).
	if r, _ := k.dir.Route("discord", "acme", relay.Event{ForeignID: "D3", Surface: "inbox-A", Author: "Alice", AuthorID: "999", ReplyToForeign: "D1"}); r.From == k.alice.ID || r.From.Kind() != ident.Account {
		t.Fatalf("stranger = %s", r.From)
	}
}

func TestRouteDiscordAttributesUnboundInbox(t *testing.T) {
	k := newRelayKit(t)
	m := k.send(k.std, "user:bob", "hi bob")
	fc := &fakeDiscordClient{postID: "DB"}
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: k.dir.receipts}
	if _, err := s.Deliver(context.Background(), k.dir.Surfaces("discord", m)[0], m); err != nil {
		t.Fatal(err)
	}
	r, ok := k.dir.Route("discord", "acme", relay.Event{ForeignID: "x", Surface: "inbox-B", Author: "Robert", AuthorID: "222", ReplyToForeign: "DB"})
	if !ok || r.From != k.bob.ID {
		t.Fatalf("unbound bob in his own inbox = %+v, %v", r, ok)
	}
}

func TestRouteDiscordOtherCases(t *testing.T) {
	k := newRelayKit(t)
	if err := putRoom(k.st, "acme", "eng", "discord", "chan-C"); err != nil {
		t.Fatal(err)
	}
	room, _ := k.st.ChannelByBinding(k.discord.ID, "chan-C")
	if r, ok := k.dir.Route("discord", "acme", relay.Event{ForeignID: "1", Surface: "chan-C", Author: "Bob", AuthorID: "222"}); !ok || r.Channel != room.ID {
		t.Fatalf("a post in a room's channel goes to the room: %+v, %v", r, ok)
	}
	// A receipt from before the channel log: the author session's default
	// channel while it lives; dropped once it has ended.
	if err := k.dir.receipts.Record("OLD", k.personal.ActorID, "", ""); err != nil {
		t.Fatal(err)
	}
	chat, _ := k.ic.DefaultChannel(k.personal)
	if r, ok := k.dir.Route("discord", "acme", relay.Event{ForeignID: "2", Surface: "inbox-A", AuthorID: "111", ReplyToForeign: "OLD"}); !ok || r.Channel != chat.ID || r.ReplyTo != "in:discord:OLD" {
		t.Fatalf("legacy receipt = %+v, %v", r, ok)
	}
	if err := k.st.RemoveInstance(k.personal.ActorID); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]relay.Event{
		"legacy receipt, session ended": {ForeignID: "3", Surface: "inbox-A", AuthorID: "111", ReplyToForeign: "OLD"},
		"a bot":                         {ForeignID: "4", Surface: "chan-C", AuthorID: "b", AuthorBot: true},
		"a non-reply in an inbox":       {ForeignID: "5", Surface: "inbox-A", AuthorID: "111"},
		"a reply to no receipt":         {ForeignID: "6", Surface: "inbox-A", AuthorID: "111", ReplyToForeign: "nope"},
	} {
		if _, ok := k.dir.Route("discord", "acme", e); ok {
			t.Errorf("%s: must be unroutable", name)
		}
	}
}

// Jam's notices reach the owner after the session is torn down: the chat
// outlives it.
func TestReclaimNoticeDeliversAfterTeardown(t *testing.T) {
	k := newRelayKit(t)
	if err := k.st.RemoveInstance(k.personal.ActorID); err != nil {
		t.Fatal(err)
	}
	m, err := k.ic.Notify(k.personal, "", "Reclaimed your personal session")
	if err != nil {
		t.Fatal(err)
	}
	if s := k.dir.Surfaces("discord", m); len(s) != 1 || s[0].Address != "inbox-A" {
		t.Fatalf("surfaces = %+v", s)
	}
}

// A person's private chat message is never made a public Linear comment: the
// mention fallback carries sessions' messages only.
func TestSurfacesChatFallbackOnlyForSessions(t *testing.T) {
	k := newRelayKit(t)
	m := k.send(k.ticket, "chat:user:alice,user:carol", "hi both")
	wantSurfaces(t, "the session's message", k.dir.Surfaces("linear", m), map[string]string{"linear:ACME-7": "@carol.h "})
	ch, _ := k.st.GetChannel(m.Channel)
	byAlice, err := k.ic.PostTrusted(ch, intercom.Squawk{From: k.alice.ID, Body: "just between us"})
	if err != nil {
		t.Fatal(err)
	}
	wantSurfaces(t, "alice's message", k.dir.Surfaces("linear", byAlice), nil)
}

// A reply to a post in a room that has since been archived goes nowhere, and
// a post into an archived channel is a permanent failure (never retried).
func TestRouteDiscordArchivedRoom(t *testing.T) {
	k := newRelayKit(t)
	if err := putRoom(k.st, "acme", "eng", "discord", "chan-C"); err != nil {
		t.Fatal(err)
	}
	m := k.send(k.std, "channel:eng", "x")
	fc := &fakeDiscordClient{postID: "DR"}
	s := &discordSurface{dial: func([]string) discordClient { return fc }, receipts: k.dir.receipts}
	if _, err := s.Deliver(context.Background(), k.dir.Surfaces("discord", m)[0], m); err != nil {
		t.Fatal(err)
	}
	if err := jam.RemoveRoom(k.st, "acme", "eng"); err != nil {
		t.Fatal(err)
	}
	if _, ok := k.dir.Route("discord", "acme", relay.Event{ForeignID: "x", Surface: "chan-C", AuthorID: "111", ReplyToForeign: "DR"}); ok {
		t.Fatal("a reply into an archived room must be unroutable")
	}
	err := k.dir.Post(relay.Routed{Channel: m.Channel, From: k.alice.ID}, intercom.Squawk{ID: "in:discord:y", From: k.alice.ID, Body: "late"})
	if !errors.Is(err, relay.ErrPermanent) {
		t.Fatalf("post into an archived channel: %v, want ErrPermanent", err)
	}
}

// putRoom adds a project's room on the connection of kind service.
func putRoom(st jam.Store, project, name, service, ref string) error {
	_, _, err := jam.PutRoom(st, project, jam.RoomBody{Name: name, Connection: service, Ref: ref})
	return err
}
