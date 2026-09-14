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
