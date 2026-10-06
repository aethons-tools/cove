package relay

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

const (
	chOps   = ident.ID("chn_01j9q3aaaaaaaaaaaaaaaaaaaa")
	chOther = ident.ID("chn_01j9q3bbbbbbbbbbbbbbbbbbbb")
	ses     = ident.ID("ses_01j9q3aaaaaaaaaaaaaaaaaaaa")
	reader  = ident.ID("ses_01j9q3bbbbbbbbbbbbbbbbbbbb")
	alice   = ident.ID("usr_01j9q3aaaaaaaaaaaaaaaaaaaa")
)

func openLog(t *testing.T) *intercom.Log {
	t.Helper()
	return intercom.NewMemLog(nil)
}

func post(t *testing.T, lg intercom.Store, m intercom.Squawk) intercom.Squawk {
	t.Helper()
	if m.Channel == "" {
		m.Channel = chOps
	}
	if m.From == "" {
		m.From = ses
	}
	if m.Body == "" {
		m.Body = "hi"
	}
	got, err := lg.Append(m, nil)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return got
}

func TestNewRebuildsSeenForItsService(t *testing.T) {
	lg := openLog(t)
	for _, id := range []string{"in:linear:c1", "in:linear:c2", "in:discord:c3", ""} {
		post(t, lg, intercom.Squawk{ID: id})
	}
	e := New(&fakeSurface{service: "linear"}, lg, &fakeMarkers{}, &fakeCursors{}, &fakeDirectory{}, Config{}, nil)
	if !e.seen["in:linear:c1"] || !e.seen["in:linear:c2"] || e.seen["in:discord:c3"] || len(e.seen) != 2 {
		t.Fatalf("seen = %v, want this Service's two inbound ids", e.seen)
	}
}

func TestRunEgressDisabledOnlyIngress(t *testing.T) {
	lg := openLog(t)
	post(t, lg, intercom.Squawk{})
	surf := &fakeSurface{service: "linear"}
	dir := &fakeDirectory{surfaces: map[ident.ID][]Delivery{chOps: {{Service: "linear", Address: "ACME-1"}}}}
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
