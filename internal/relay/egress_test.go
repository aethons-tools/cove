package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

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

// backoffSurface is a fakeSurface whose first Deliver hits a rate limit
// lasting until `until` (a relay.Backoff).
type backoffSurface struct {
	fakeSurface
	until, limit time.Time
}

func (b *backoffSurface) Until() time.Time { return b.until }
func (b *backoffSurface) Deliver(ctx context.Context, d Delivery, m intercom.Squawk) (string, error) {
	if b.until.IsZero() {
		b.until = b.limit
		return "", errors.New("rate limited")
	}
	return b.fakeSurface.Deliver(ctx, d, m)
}

// While a rate limit holds, egress makes no deliveries and warns once for
// the whole wait, however many ticks and messages it spans.
func TestEgressWaitsOutBackoffWarningOnce(t *testing.T) {
	surf := &backoffSurface{fakeSurface: fakeSurface{service: "github"}, limit: time.Now().Add(time.Hour)}
	var buf bytes.Buffer
	e := New(surf, openLog(t), &fakeMarkers{}, &fakeCursors{}, &fakeDirectory{surfaces: map[ident.ID][]Delivery{
		chOps: {{Service: "github", Address: "acme/board#1"}, {Service: "github", Address: "acme/board#2"}},
	}}, Config{}, slog.New(slog.NewTextHandler(&buf, nil)))
	post(t, e.lg, intercom.Squawk{})
	post(t, e.lg, intercom.Squawk{})
	for range 3 {
		e.egressTick(context.Background())
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 || surf.deliverCount() != 0 {
		t.Fatalf("%d warns, %d delivers; want one warn and no delivery:\n%s", n, surf.deliverCount(), buf.String())
	}
	surf.until = time.Now().Add(-time.Second) // the wait is over
	e.egressTick(context.Background())
	if surf.deliverCount() != 4 {
		t.Fatalf("after the wait delivered %d; want 4", surf.deliverCount())
	}
}

// Final fix 3: a delivery that fails permanently (ErrPermanent: the surface
// can never take it) is skipped with one warn — it does not pin LastSeq and
// is not retried every tick.
func TestEgressPermanentDeliverErrorSkips(t *testing.T) {
	surf := &fakeSurface{service: "github", permanentFor: map[string]bool{"other/repo#1": true}}
	var buf bytes.Buffer
	mk := &fakeMarkers{}
	e := New(surf, openLog(t), mk, &fakeCursors{}, &fakeDirectory{surfaces: map[ident.ID][]Delivery{
		chOps: {{Service: "github", Address: "other/repo#1"}, {Service: "github", Address: "acme/board#2"}},
	}}, Config{}, slog.New(slog.NewTextHandler(&buf, nil)))
	post(t, e.lg, intercom.Squawk{})
	last := post(t, e.lg, intercom.Squawk{})
	for range 3 {
		e.egressTick(context.Background())
	}
	if got := mk.Egress("github").LastSeq; got != last.Seq {
		t.Fatalf("LastSeq = %d; want %d (past the permanently-failing surface)", got, last.Seq)
	}
	if surf.deliverCount() != 4 { // each message: one try at each surface
		t.Fatalf("delivers = %d; want 4 (the permanent failure tried once per message)", surf.deliverCount())
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 {
		t.Fatalf("%d warns; want one per skipped delivery:\n%s", n, buf.String())
	}
}
