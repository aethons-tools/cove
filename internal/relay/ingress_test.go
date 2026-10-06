package relay

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func routeTo(ch ident.ID) Routed {
	return Routed{Channel: ch, From: alice, Origin: "con_origin", OriginRef: "ACME-1"}
}

func newIngressEngine(t *testing.T, surf *fakeSurface, route map[string]Routed) (*Engine, *fakeDirectory, *fakeCursors) {
	lg := openLog(t)
	dir := &fakeDirectory{projects: []string{"acme"}, route: route, lg: lg, audience: []ident.ID{reader}}
	cur := &fakeCursors{}
	return New(surf, lg, &fakeMarkers{}, cur, dir, Config{}, nil), dir, cur
}

func TestIngressPostsRoutedEvent(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c1", Body: "reply", At: time.Unix(10, 0)}}, next: "cur1"}
	e, _, cur := newIngressEngine(t, surf, map[string]Routed{"c1": {Channel: chOps, From: alice, ReplyTo: "r0", Origin: "con_origin", OriginRef: "ACME-1"}})
	e.ingressTick(context.Background())
	inbox := e.lg.InboxSince(reader, 0, 0)
	if len(inbox) != 1 {
		t.Fatalf("inbox = %+v", inbox)
	}
	m := inbox[0]
	if m.ID != "in:linear:c1" || m.Channel != chOps || m.From != alice || m.Body != "reply" || m.ReplyTo != "r0" || m.Origin != "con_origin" || m.OriginRef != "ACME-1" {
		t.Fatalf("posted = %+v", m)
	}
	if cur.c["linear/acme"] != "cur1" {
		t.Fatalf("cursor must advance: %v", cur.c)
	}
}

func TestIngressIdempotentAcrossRestart(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c1", Body: "r", At: time.Unix(1, 0)}}}
	e, dir, _ := newIngressEngine(t, surf, map[string]Routed{"c1": routeTo(chOps)})
	e.ingressTick(context.Background())
	e2 := New(surf, e.lg, &fakeMarkers{}, &fakeCursors{}, dir, Config{}, nil) // a restart over the same log
	e2.ingressTick(context.Background())
	if n := len(e.lg.ListSince(0, 0)); n != 1 {
		t.Fatalf("a replayed event must not post twice, got %d", n)
	}
}

func TestIngressUnroutedSkipped(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "c9", Body: "r"}}, next: "cur2"}
	e, _, cur := newIngressEngine(t, surf, map[string]Routed{})
	e.ingressTick(context.Background())
	if n := len(e.lg.ListSince(0, 0)); n != 0 || cur.c["linear/acme"] != "cur2" {
		t.Fatalf("unrouted: %d posted, cursor %v", n, cur.c)
	}
}

func TestIngressPostErrorHoldsCursor(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{{ForeignID: "bad1", Body: "r", At: time.Unix(5, 0)}}, next: "cur3"}
	e, dir, cur := newIngressEngine(t, surf, map[string]Routed{"bad1": routeTo(chOps)})
	dir.postErr = true
	e.ingressTick(context.Background())
	if _, ok := cur.c["linear/acme"]; ok {
		t.Fatal("a post failure must not advance the cursor")
	}
	dir.postErr = false
	e.ingressTick(context.Background()) // retried: not marked seen
	if n := len(e.lg.ListSince(0, 0)); n != 1 {
		t.Fatalf("retry posted %d", n)
	}
}

func TestIngressPollErrorSkipsProject(t *testing.T) {
	surf := &fakeSurface{service: "linear", pollErr: context.DeadlineExceeded}
	e, _, cur := newIngressEngine(t, surf, nil)
	e.ingressTick(context.Background())
	if _, ok := cur.c["linear/acme"]; ok {
		t.Fatal("a Poll error must not advance the cursor")
	}
}

func TestIngressCarriesContentType(t *testing.T) {
	surf := &fakeSurface{service: "linear", events: []Event{
		{ForeignID: "c1", Body: "**md**", At: time.Unix(10, 0)},
		{ForeignID: "c2", Body: "a_b", At: time.Unix(11, 0), ContentType: intercom.ContentPlain},
	}}
	e, _, _ := newIngressEngine(t, surf, map[string]Routed{"c1": routeTo(chOps), "c2": routeTo(chOps)})
	e.ingressTick(context.Background())
	inbox := e.lg.InboxSince(reader, 0, 0)
	if len(inbox) != 2 || inbox[0].ContentType != intercom.ContentMarkdown || inbox[1].ContentType != intercom.ContentPlain {
		t.Fatalf("content types = %+v", inbox)
	}
}
