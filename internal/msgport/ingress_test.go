package msgport

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

func routeTo(coveID string) routed {
	return routed{from: msglog.Target{Kind: "human", Ref: "alice"}, to: []msglog.Target{{Kind: "actor", Ref: coveID}}}
}

func TestIngressAppendsRoutedEvent(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c1", Body: "reply", At: time.Unix(10, 0)}}, next: "cur1"}
	dir := &fakeDirectory{projects: []string{"acme"}, route: map[string]routed{"c1": routeTo("cove-1")}}
	cur := &fakeCursors{}
	e := New(surf, openLog(t), &fakeMarkers{}, cur, dir, Config{}, nil)
	e.ingressTick(context.Background())
	inbox := e.lg.ReadInbox(msglog.Target{Kind: "actor", Ref: "cove-1"})
	if len(inbox) != 1 || inbox[0].ID != "in:linear:c1" || inbox[0].Body != "reply" || inbox[0].From.Ref != "alice" {
		t.Fatalf("expected 1 routed inbound message, got %+v", inbox)
	}
	if cur.c["linear/acme"] != "cur1" {
		t.Fatalf("cursor must advance to next: %v", cur.c)
	}
}

func TestIngressIdempotentAcrossRestart(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c1", Body: "r", At: time.Unix(1, 0)}}}
	dir := &fakeDirectory{projects: []string{"acme"}, route: map[string]routed{"c1": routeTo("cove-1")}}
	lg := openLog(t)
	e := New(surf, lg, &fakeMarkers{}, &fakeCursors{}, dir, Config{}, nil)
	e.ingressTick(context.Background()) // appends in:linear:c1
	// simulate a restart: a fresh engine over the SAME log rebuilds `seen`; the surface replays c1
	e2 := New(surf, lg, &fakeMarkers{}, &fakeCursors{}, dir, Config{}, nil)
	e2.ingressTick(context.Background())
	if n := len(lg.List(msglog.Filter{})); n != 1 {
		t.Fatalf("idempotent ingress: replayed event must not double-append, got %d", n)
	}
}

func TestIngressUnroutedSkipped(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c9", Body: "r"}}, next: "cur2"}
	dir := &fakeDirectory{projects: []string{"acme"}, route: map[string]routed{}} // no route for c9
	cur := &fakeCursors{}
	lg := openLog(t)
	e := New(surf, lg, &fakeMarkers{}, cur, dir, Config{}, nil)
	e.ingressTick(context.Background())
	if n := len(lg.List(msglog.Filter{})); n != 0 {
		t.Fatalf("unrouted event must not append, got %d", n)
	}
	if cur.c["linear/acme"] != "cur2" {
		t.Fatal("cursor still advances past an unrouted event")
	}
}

func TestIngressPollErrorSkipsProject(t *testing.T) {
	surf := &fakeSurface{service: "linear", pollErr: context.DeadlineExceeded}
	dir := &fakeDirectory{projects: []string{"acme"}}
	cur := &fakeCursors{}
	e := New(surf, openLog(t), &fakeMarkers{}, cur, dir, Config{}, nil)
	e.ingressTick(context.Background())
	if _, ok := cur.c["linear/acme"]; ok {
		t.Fatal("a Poll error must not advance the cursor")
	}
}
