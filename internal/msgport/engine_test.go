package msgport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

func openLog(t *testing.T) *msglog.Log {
	t.Helper()
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return lg
}

func TestNewRebuildsSeenForItsService(t *testing.T) {
	lg := openLog(t)
	// two inbound-linear ids, one inbound-discord id, one normal outbound id
	_, _ = lg.Append(msglog.Message{ID: "in:linear:c1", From: msglog.Target{Kind: "human", Ref: "a"}, To: []msglog.Target{{Kind: "actor", Ref: "x"}}, Body: "b"})
	_, _ = lg.Append(msglog.Message{ID: "in:linear:c2", From: msglog.Target{Kind: "human", Ref: "a"}, To: []msglog.Target{{Kind: "actor", Ref: "x"}}, Body: "b"})
	_, _ = lg.Append(msglog.Message{ID: "in:discord:c3", From: msglog.Target{Kind: "human", Ref: "a"}, To: []msglog.Target{{Kind: "actor", Ref: "x"}}, Body: "b"})
	_, _ = lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "x"}, To: []msglog.Target{{Kind: "human", Ref: "a"}}, Body: "b"})
	e := New(&fakeSurface{service: "linear"}, lg, &fakeMarkers{}, &fakeCursors{}, &fakeDirectory{}, Config{}, nil)
	if !e.seen["in:linear:c1"] || !e.seen["in:linear:c2"] {
		t.Fatal("New must seed seen with this Service's inbound ids")
	}
	if e.seen["in:discord:c3"] {
		t.Fatal("New must NOT seed another Service's inbound ids")
	}
	if len(e.seen) != 2 {
		t.Fatalf("seen has %d ids, want 2", len(e.seen))
	}
}

func TestRunEgressDisabledOnlyIngress(t *testing.T) {
	lg := openLog(t)
	// an outbound (internal-authored, external target) message that egress WOULD deliver if enabled
	_, _ = lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-1"}, To: []msglog.Target{{Kind: "human", Ref: "a"}}, Body: "x", Project: "acme"})
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{resolve: map[string]Delivery{"human:a": {Service: "linear", Address: "ACME-1"}}}
	e := New(surf, lg, &fakeMarkers{}, &fakeCursors{}, dir, Config{EgressEnabled: false, EgressPoll: time.Millisecond, IngressPoll: time.Millisecond}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	time.Sleep(30 * time.Millisecond) // a few ticks would fire if egress ran
	cancel()
	if surf.deliverCount() != 0 {
		t.Fatalf("EgressEnabled=false must not deliver; got %d", surf.deliverCount())
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	e := New(&fakeSurface{service: "linear"}, openLog(t), &fakeMarkers{}, &fakeCursors{}, &fakeDirectory{}, Config{EgressPoll: time.Hour, IngressPoll: time.Hour}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
