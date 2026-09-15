package msgport

import (
	"context"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
)

func actorMsg(to ...msglog.Target) msglog.Message {
	return msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, To: to, Body: "hi", Project: "acme"}
}

func newEgressEngine(t *testing.T, surf *fakeSurface, dir *fakeDirectory) (*Engine, *fakeMarkers) {
	mk := &fakeMarkers{}
	e := New(surf, openLog(t), mk, &fakeCursors{}, dir, Config{}, nil)
	return e, mk
}

func TestEgressDeliversExternalSkipsInternal(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{
		"human:alice": {Service: "linear", Address: "ACME-1", SenderName: "cove-1"},
	}}
	e, _ := newEgressEngine(t, surf, dir)
	// human:alice is External+owned → delivered; actor:cove-2 is Internal → skipped
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}, msglog.Target{Kind: "actor", Ref: "cove-2"}))
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 || surf.delivers[0].Address != "ACME-1" {
		t.Fatalf("expected 1 delivery to ACME-1, got %+v", surf.delivers)
	}
}

func TestEgressEchoGuardSkipsExternalAuthored(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"channel:ops": {Service: "linear", Address: "ACME-9"}}}
	e, _ := newEgressEngine(t, surf, dir)
	// an ingested human->channel message must NOT be re-egressed
	_, _ = e.lg.Append(msglog.Message{From: msglog.Target{Kind: "human", Ref: "bob"}, To: []msglog.Target{{Kind: "channel", Ref: "ops"}}, Body: "x", Project: "acme"})
	e.egressTick(context.Background())
	if surf.deliverCount() != 0 {
		t.Fatalf("echo guard: externally-authored message must not egress, got %+v", surf.delivers)
	}
}

func TestEgressExactlyOnceAcrossTicks(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "linear", Address: "ACME-1"}}}
	e, _ := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	e.egressTick(context.Background())
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 {
		t.Fatalf("exactly-once: 2 ticks delivered %d times", surf.deliverCount())
	}
}

func TestEgressCrashReplayRedelivers(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "linear", Address: "ACME-1"}}}
	e, mk := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	e.egressTick(context.Background()) // delivers + marks
	mk.m = map[string]EgressMark{}     // simulate a crash: the mark never persisted
	e.egressTick(context.Background()) // must re-deliver (Service dedups on m.ID)
	if surf.deliverCount() != 2 {
		t.Fatalf("crash replay: expected re-delivery, got %d", surf.deliverCount())
	}
	// and m.ID is passed to Deliver both times (the Service dedup key)
	if surf.delivers[0].MsgID == "" || surf.delivers[0].MsgID != surf.delivers[1].MsgID {
		t.Fatalf("Deliver must carry a stable m.ID as the dedup key: %+v", surf.delivers)
	}
}

func TestEgressPartialFailureRetriesOnlyFailed(t *testing.T) {
	surf := &fakeSurface{service: "linear", deliverErrFor: map[string]bool{"ACME-A": true}}
	dir := &fakeDirectory{resolve: map[string]Delivery{
		"human:a": {Service: "linear", Address: "ACME-A"},
		"human:b": {Service: "linear", Address: "ACME-B"},
	}}
	e, _ := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "a"}, msglog.Target{Kind: "human", Ref: "b"}))
	e.egressTick(context.Background()) // A fails, B succeeds
	surf.deliverErrFor = nil           // A now recovers
	e.egressTick(context.Background()) // retries only A
	var a, b int
	for _, d := range surf.delivers {
		if d.Address == "ACME-A" {
			a++
		}
		if d.Address == "ACME-B" {
			b++
		}
	}
	if a != 2 || b != 1 {
		t.Fatalf("partial failure: A delivered %d (want 2, retried), B %d (want 1, once)", a, b)
	}
}

func TestEgressPassesBodyPrefixToDeliver(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{
		"channel:ops": {Service: "linear", Address: "ACME-9", BodyPrefix: "@bob "},
	}}
	e, _ := newEgressEngine(t, surf, dir)
	// internal-authored, external target
	_, _ = e.lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, To: []msglog.Target{{Kind: "channel", Ref: "ops"}}, Body: "x", Project: "acme"})
	e.egressTick(context.Background())
	if len(surf.delivers) != 1 || surf.delivers[0].BodyPrefix != "@bob " {
		t.Fatalf("BodyPrefix not passed through: %+v", surf.delivers)
	}
}

func TestEgressBacklogDrainsAcrossTicks(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "linear", Address: "ACME-1"}}}
	e, mk := newEgressEngine(t, surf, dir)

	// Seed one message and fully drain it, establishing a non-empty starting
	// LastSeq — messages at/under this Seq must never be (re-)delivered.
	first, err := e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	e.egressTick(context.Background())
	if surf.deliverCount() != 1 {
		t.Fatalf("setup: expected 1 delivery, got %d", surf.deliverCount())
	}
	if got := mk.m[surf.service].LastSeq; got != first.Seq {
		t.Fatalf("setup: LastSeq = %d, want %d", got, first.Seq)
	}

	// Append a backlog bigger than one egressBatch.
	const backlogSize = egressBatch + 50
	var tail msglog.Message
	for i := 0; i < backlogSize; i++ {
		m, err := e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		tail = m
	}

	// Drain across enough ticks to cover the whole backlog.
	for i := 0; i < backlogSize/egressBatch+2; i++ {
		e.egressTick(context.Background())
	}

	if surf.deliverCount() != 1+backlogSize {
		t.Fatalf("backlog drain: delivered %d times, want %d", surf.deliverCount(), 1+backlogSize)
	}
	firstDeliveries := 0
	for _, d := range surf.delivers {
		if d.MsgID == first.ID {
			firstDeliveries++
		}
	}
	if firstDeliveries != 1 {
		t.Fatalf("message at/under starting LastSeq was (re-)delivered %d times, want 1", firstDeliveries)
	}
	if got := mk.m[surf.service].LastSeq; got != tail.Seq {
		t.Fatalf("LastSeq = %d after drain, want tail %d", got, tail.Seq)
	}
}

// TestEgressLowWaterUsesSeqNotID interleaves externally-authored ingress ids
// ("in:linear:...", echo-guarded and never egressed) with internal-authored
// messages carrying ordinary generated ids. The id namespaces are NOT
// mutually lexically ordered, so a low-water keyed on id would be unsound;
// this asserts the low-water tracks Seq (append order) instead, and that a
// re-tick after advancing delivers nothing new (ListSince(LastSeq) is empty).
func TestEgressLowWaterUsesSeqNotID(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "linear", Address: "ACME-1"}}}
	e, mk := newEgressEngine(t, surf, dir)

	_, _ = e.lg.Append(msglog.Message{ID: "in:linear:c1", From: msglog.Target{Kind: "human", Ref: "bob"}, To: []msglog.Target{{Kind: "actor", Ref: "cove-1"}}, Body: "hi"})
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	_, _ = e.lg.Append(msglog.Message{ID: "in:linear:c2", From: msglog.Target{Kind: "human", Ref: "bob"}, To: []msglog.Target{{Kind: "actor", Ref: "cove-1"}}, Body: "hi"})
	tail, err := e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	e.egressTick(context.Background())
	if surf.deliverCount() != 2 {
		t.Fatalf("expected 2 deliveries (echo-guard skips the in:* messages), got %d", surf.deliverCount())
	}
	if got := mk.m[surf.service].LastSeq; got != tail.Seq {
		t.Fatalf("LastSeq = %d, want tail Seq %d", got, tail.Seq)
	}

	// Re-tick: ListSince(LastSeq) returns nothing new → exactly-once holds.
	e.egressTick(context.Background())
	if surf.deliverCount() != 2 {
		t.Fatalf("re-tick delivered extra messages: %d", surf.deliverCount())
	}
}

func TestEgressSkipsOtherService(t *testing.T) {
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:alice": {Service: "discord", Address: "chan-1"}}}
	e, _ := newEgressEngine(t, surf, dir)
	_, _ = e.lg.Append(actorMsg(msglog.Target{Kind: "human", Ref: "alice"}))
	e.egressTick(context.Background())
	if surf.deliverCount() != 0 {
		t.Fatal("a target resolving to another Service must not be delivered by this engine")
	}
}
