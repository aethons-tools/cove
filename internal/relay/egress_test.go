package relay

import (
	"context"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func newEgressEngine(t *testing.T, surf *fakeSurface, surfaces map[ident.ID][]Delivery) (*Engine, *fakeMarkers) {
	mk := &fakeMarkers{}
	e := New(surf, openLog(t), mk, &fakeCursors{}, &fakeDirectory{surfaces: surfaces}, Config{}, nil)
	return e, mk
}

func TestEgressDeliversEachSurfaceOfTheChannel(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	e, _ := newEgressEngine(t, surf, map[ident.ID][]Delivery{
		chOps: {{Service: "linear", Address: "ACME-1"}, {Service: "linear", Address: "ACME-2", BodyPrefix: "@bob "}},
	})
	post(t, e.lg, intercom.Squawk{})
	post(t, e.lg, intercom.Squawk{Channel: chOther}) // a channel with no surface here
	e.egressTick(context.Background())
	if surf.deliverCount() != 2 || surf.delivers[0].Address != "ACME-1" || surf.delivers[1].BodyPrefix != "@bob " {
		t.Fatalf("delivers = %+v", surf.delivers)
	}
}

// Every author's squawks are rendered — a person's included — but never back
// onto the surface they came from.
func TestEgressRendersPeopleButNotBackToOrigin(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	e, _ := newEgressEngine(t, surf, map[ident.ID][]Delivery{chOps: {{Service: "linear", Address: "ACME-1"}, {Service: "linear", Address: "ACME-9"}}})
	post(t, e.lg, intercom.Squawk{From: alice, Body: "from /me"})
	post(t, e.lg, intercom.Squawk{ID: "in:linear:c1", From: alice, Origin: "con_origin", OriginRef: "ACME-1"})
	e.egressTick(context.Background())
	var got []string
	for _, d := range surf.delivers {
		got = append(got, d.MsgID[:min(len(d.MsgID), 12)]+"→"+d.Address)
	}
	if surf.deliverCount() != 3 || surf.delivers[2].Address != "ACME-9" {
		t.Fatalf("delivers = %v; want the /me post on both, the ingested one only on ACME-9", got)
	}
}

func TestEgressExactlyOnceAcrossTicks(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	e, _ := newEgressEngine(t, surf, map[ident.ID][]Delivery{chOps: {{Service: "linear", Address: "ACME-1"}}})
	post(t, e.lg, intercom.Squawk{})
	e.egressTick(context.Background())
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 {
		t.Fatalf("exactly-once: 2 ticks delivered %d times", surf.deliverCount())
	}
}

func TestEgressCrashReplayRedelivers(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	e, mk := newEgressEngine(t, surf, map[ident.ID][]Delivery{chOps: {{Service: "linear", Address: "ACME-1"}}})
	post(t, e.lg, intercom.Squawk{})
	e.egressTick(context.Background()) // delivers + marks
	mk.m = map[string]EgressMark{}     // a crash: the mark never persisted
	e.egressTick(context.Background()) // re-delivers (the Service dedups on m.ID)
	if surf.deliverCount() != 2 || surf.delivers[0].MsgID == "" || surf.delivers[0].MsgID != surf.delivers[1].MsgID {
		t.Fatalf("crash replay must re-deliver with a stable m.ID: %+v", surf.delivers)
	}
}

func TestEgressPartialFailureRetriesOnlyFailed(t *testing.T) {
	surf := &fakeSurface{service: "linear", deliverErrFor: map[string]bool{"ACME-A": true}}
	e, _ := newEgressEngine(t, surf, map[ident.ID][]Delivery{chOps: {{Service: "linear", Address: "ACME-A"}, {Service: "linear", Address: "ACME-B"}}})
	post(t, e.lg, intercom.Squawk{})
	e.egressTick(context.Background()) // A fails, B succeeds
	surf.deliverErrFor = nil           // A recovers
	e.egressTick(context.Background()) // retries only A
	var a, b int
	for _, d := range surf.delivers {
		switch d.Address {
		case "ACME-A":
			a++
		case "ACME-B":
			b++
		}
	}
	if a != 2 || b != 1 {
		t.Fatalf("partial failure: A delivered %d (want 2, retried), B %d (want 1, once)", a, b)
	}
}

func TestEgressBacklogDrainsAcrossTicks(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	e, mk := newEgressEngine(t, surf, map[ident.ID][]Delivery{chOps: {{Service: "linear", Address: "ACME-1"}}})
	first := post(t, e.lg, intercom.Squawk{})
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 || mk.m[surf.service].LastSeq != first.Seq {
		t.Fatalf("setup: %d deliveries, LastSeq %d", surf.deliverCount(), mk.m[surf.service].LastSeq)
	}
	const backlog = egressBatch + 50
	var tail intercom.Squawk
	for range backlog {
		tail = post(t, e.lg, intercom.Squawk{})
	}
	for range backlog/egressBatch + 2 {
		e.egressTick(context.Background())
	}
	if surf.deliverCount() != 1+backlog || mk.m[surf.service].LastSeq != tail.Seq {
		t.Fatalf("drain: %d deliveries (want %d), LastSeq %d (want %d)", surf.deliverCount(), 1+backlog, mk.m[surf.service].LastSeq, tail.Seq)
	}
}

// The low-water tracks seq, not ids (ingress ids aren't ordered against
// generated ones), and a seeded mark from before the cutover (a legacy seq)
// never re-delivers anything: the new log's seqs all lie above it.
func TestEgressLowWaterUsesSeq(t *testing.T) {
	legacy := intercom.NewLegacyMemLog()
	for range 3 {
		if _, err := legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "c"}, To: []intercom.Target{{Kind: "human", Ref: "a"}}, Body: "old"}); err != nil {
			t.Fatal(err)
		}
	}
	lg := intercom.NewMemLog(legacy)
	surf := &fakeSurface{service: "linear"}
	mk := &fakeMarkers{m: map[string]EgressMark{"linear": {LastSeq: 3}}} // seeded at the legacy tail
	e := New(surf, lg, mk, &fakeCursors{}, &fakeDirectory{surfaces: map[ident.ID][]Delivery{chOps: {{Service: "linear", Address: "ACME-1"}}}}, Config{}, nil)
	post(t, lg, intercom.Squawk{ID: "in:linear:zzz", From: alice, Origin: "con_origin", OriginRef: "ACME-1"})
	tail := post(t, lg, intercom.Squawk{ID: "000-first"})
	e.egressTick(context.Background())
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 || mk.m["linear"].LastSeq != tail.Seq {
		t.Fatalf("delivers = %+v, LastSeq %d (want %d)", surf.delivers, mk.m["linear"].LastSeq, tail.Seq)
	}
}

func TestEgressSkipsOtherService(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	e, _ := newEgressEngine(t, surf, map[ident.ID][]Delivery{chOps: {{Service: "discord", Address: "chan-1"}}})
	post(t, e.lg, intercom.Squawk{})
	e.egressTick(context.Background())
	if surf.deliverCount() != 0 {
		t.Fatal("a surface on another Service must not be delivered by this engine")
	}
}
