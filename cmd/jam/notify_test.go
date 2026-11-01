package main

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/notify"
	"github.com/aethons-tools/cove/internal/jam/push"
	"github.com/aethons-tools/cove/internal/relay"
)

func noUnread(ident.ID) int { return 0 }

// The notify surfaces are the push rule's recipients, one delivery per
// person (no devices), with focus suppressing as it does for push.
func TestSurfacesNotify(t *testing.T) {
	k := newRelayKit(t)
	k.dir.notifyOn, k.dir.pushFocus = true, push.NewFocus(nil)
	m := k.send(k.personal, "", "done?") // alice's personal session to her
	wantSurfaces(t, "a session message to alice", k.dir.Surfaces(notify.Service, m), map[string]string{"notify:" + string(k.alice.ID): ""})

	ch, _ := k.st.GetChannel(m.Channel)
	byAlice, err := k.ic.PostTrusted(ch, intercom.Squawk{From: k.alice.ID, Body: "on it"})
	if err != nil {
		t.Fatal(err)
	}
	wantSurfaces(t, "alice's own message", k.dir.Surfaces(notify.Service, byAlice), map[string]string{})

	k.dir.pushFocus.Set(k.alice.ID, m.Channel)
	wantSurfaces(t, "alice focused", k.dir.Surfaces(notify.Service, m), map[string]string{})
	k.dir.pushFocus = push.NewFocus(nil)

	k.dir.notifyOn = false
	wantSurfaces(t, "notify off", k.dir.Surfaces(notify.Service, m), nil)
	k.dir.notifyOn = true

	// An ask naming bob in a ticket channel: not paused for parts.
	tch, _ := k.ticketChannel()
	for _, u := range []ident.ID{k.alice.ID, k.bob.ID} {
		if err := k.st.JoinChannel(tch.ID, u, 1); err != nil {
			t.Fatal(err)
		}
	}
	ev := intercom.AskEvent{Event: intercom.AskEventAsk, Question: "Merge?", Of: []ident.ID{k.bob.ID},
		Options: []intercom.AskOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}}
	ask, err := k.ic.PostTrusted(tch, intercom.Squawk{From: ident.ID(k.ticket.ActorID), Body: "asked: Merge?", Parts: []intercom.Part{intercom.AskPart(ev)}})
	if err != nil {
		t.Fatal(err)
	}
	wantSurfaces(t, "an ask naming bob", k.dir.Surfaces(notify.Service, ask), map[string]string{"notify:" + string(k.bob.ID): ""})

	// Notify never leaks onto the other services.
	k.dir.pushOn = true
	for _, svc := range []string{"linear", "discord", push.Service} {
		for _, d := range k.dir.Surfaces(svc, m) {
			if d.Service == notify.Service {
				t.Fatalf("%s surfaces carry a notify delivery: %+v", svc, d)
			}
		}
	}
}

func TestNotifyEventCarriesSessionAndID(t *testing.T) {
	k := newRelayKit(t)
	r := pushReader{store: k.st, ic: k.ic}
	m := k.send(k.personal, "", "done?")
	e, ok := notifyEvent(r, nil, noUnread, true)(m, k.alice.ID)
	if !ok {
		t.Fatal("alice is not notified")
	}
	if e.Session != k.personal.ActorID || e.ID != m.ID || e.Channel != string(m.Channel) || e.Body != "done?" {
		t.Fatalf("event = %+v, want session %s id %s", e, k.personal.ActorID, m.ID)
	}

	chat := k.send(k.std, "chat:user:alice,user:bob", "standup")
	e, ok = notifyEvent(r, nil, noUnread, true)(chat, k.alice.ID)
	if !ok || e.Session != "" || e.ID != chat.ID {
		t.Fatalf("chat event = %+v, %v; want no session", e, ok)
	}

	e, ok = notifyEvent(r, nil, noUnread, false)(m, k.alice.ID)
	if !ok || e.Body != "New message" {
		t.Fatalf("no-preview event = %+v, %v", e, ok)
	}
}

// Focus that arrives after Surfaces listed alice still suppresses her
// notification: the recipient is re-checked at delivery.
func TestNotifyEventRechecksAtDelivery(t *testing.T) {
	k := newRelayKit(t)
	focus := push.NewFocus(nil)
	k.dir.notifyOn, k.dir.pushFocus = true, focus
	m := k.send(k.personal, "", "done?")
	wantSurfaces(t, "listed", k.dir.Surfaces(notify.Service, m), map[string]string{"notify:" + string(k.alice.ID): ""})
	focus.Set(k.alice.ID, m.Channel)
	if e, ok := notifyEvent(pushReader{store: k.st, ic: k.ic}, focus, noUnread, true)(m, k.alice.ID); ok {
		t.Fatalf("focused alice got %+v", e)
	}
}

func TestNotifyEngineDeliversToTheRecipientsStream(t *testing.T) {
	k := newRelayKit(t)
	k.dir.notifyOn = true
	hub := notify.NewHub(nil)
	alice, ca := hub.Subscribe(k.alice.ID)
	defer ca()
	bob, cb := hub.Subscribe(k.bob.ID)
	defer cb()
	m := k.send(k.personal, "", "done?") // alice's personal session to her
	r := pushReader{store: k.st, ic: k.ic}
	want, ok := pushPayload(r, nil, noUnread, true)(m, k.alice.ID)
	if !ok {
		t.Fatal("alice is not a recipient")
	}
	markers := newMemMarkers(notify.Service, relay.EgressMark{LastSeq: m.Seq - 1})
	surf := &notify.Surface{Hub: hub, Event: notifyEvent(r, nil, noUnread, true), Now: time.Now}
	eng := relay.New(surf, k.lg, markers, noCursors{}, k.dir, relay.Config{EgressEnabled: true, EgressPoll: 5 * time.Millisecond, IngressPoll: time.Hour}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for markers.Egress(notify.Service).LastSeq < m.Seq {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("notify mark stuck at %d, want %d", markers.Egress(notify.Service).LastSeq, m.Seq)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	select {
	case e := <-alice:
		if e.Title != want.Title || e.Channel != string(m.Channel) || e.ID != m.ID {
			t.Fatalf("alice's event = %+v, want title %q channel %s", e, want.Title, m.Channel)
		}
	default:
		t.Fatal("alice received nothing")
	}
	select {
	case e := <-alice:
		t.Fatalf("alice received a second event: %+v", e)
	case e := <-bob:
		t.Fatalf("bob received %+v", e)
	default:
	}
}

func TestNotifyHasNoIngress(t *testing.T) {
	k := newRelayKit(t)
	if p := k.dir.Projects(notify.Service); p != nil {
		t.Fatalf("Projects(notify) = %v", p)
	}
	if _, ok := k.dir.Route(notify.Service, "", relay.Event{ForeignID: "x"}); ok {
		t.Fatal("notify routed an event")
	}
	var c noCursors
	if c.Ingress(notify.Service, "acme") != "" || c.SetIngress(notify.Service, "acme", "x") != nil || c.Ingress(notify.Service, "acme") != "" {
		t.Fatal("noCursors kept a cursor")
	}
}

// memMarkers holds marks in memory only: the type has no path to write to.
func TestMemMarkers(t *testing.T) {
	seed := relay.EgressMark{LastSeq: 42}
	mk := newMemMarkers(notify.Service, seed)
	if got := mk.Egress(notify.Service); got.LastSeq != 42 {
		t.Fatalf("seeded = %+v", got)
	}
	if got := mk.Egress("other"); got.LastSeq != 0 || got.Pending != nil {
		t.Fatalf("other = %+v", got)
	}
	next := relay.EgressMark{LastSeq: 43, Pending: map[string]map[string]bool{"sqk_1": {"usr_a": true}}}
	if err := mk.SetEgress(notify.Service, next); err != nil {
		t.Fatal(err)
	}
	if got := mk.Egress(notify.Service); got.LastSeq != 43 || !got.Pending["sqk_1"]["usr_a"] {
		t.Fatalf("round trip = %+v", got)
	}
}
