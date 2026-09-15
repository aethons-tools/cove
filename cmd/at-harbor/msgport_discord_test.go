package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msgport"
)

// fakeDiscordPoster is a fake discordPoster: recorded posts, and an
// injectable error, so discordSurface.Deliver is testable without a live
// Discord bot.
type fakeDiscordPoster struct {
	posts   []struct{ channel, content string }
	postErr error
}

func (f *fakeDiscordPoster) Post(_ context.Context, channel, content string) error {
	if f.postErr != nil {
		return f.postErr
	}
	f.posts = append(f.posts, struct{ channel, content string }{channel, content})
	return nil
}

func TestDiscordDeliver(t *testing.T) {
	p := &fakeDiscordPoster{}
	s := &discordSurface{poster: p}
	if s.Service() != "discord" {
		t.Fatalf("Service = %q", s.Service())
	}
	m := msglog.Message{Body: "hello"}
	if _, err := s.Deliver(context.Background(), msgport.Delivery{Service: "discord", Address: "chan-1", BodyPrefix: "cove-1: "}, m); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(p.posts) != 1 || p.posts[0].channel != "chan-1" || p.posts[0].content != "cove-1: hello" {
		t.Fatalf("posts = %+v", p.posts)
	}
}

func TestDiscordDeliverPropagatesError(t *testing.T) {
	s := &discordSurface{poster: &fakeDiscordPoster{postErr: fmt.Errorf("boom")}}
	if _, err := s.Deliver(context.Background(), msgport.Delivery{Address: "c"}, msglog.Message{Body: "x"}); err == nil {
		t.Fatal("expected post error")
	}
}

func TestDiscordPollIsInert(t *testing.T) {
	s := &discordSurface{}
	ev, next, err := s.Poll(context.Background(), "acme", "cursor-7")
	if err != nil || len(ev) != 0 || next != "cursor-7" {
		t.Fatalf("Poll = %v,%q,%v; want nil,cursor-7,nil", ev, next, err)
	}
}

func TestFileReceiptsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.json")
	r, err := newFileReceipts(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("D1"); ok {
		t.Fatal("empty lookup should miss")
	}
	if err := r.Record("D1", "cove-1"); err != nil {
		t.Fatal(err)
	}
	if a, ok := r.Lookup("D1"); !ok || a != "cove-1" {
		t.Fatalf("lookup = %q,%v", a, ok)
	}
	r2, err := newFileReceipts(p) // reload
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := r2.Lookup("D1"); !ok || a != "cove-1" {
		t.Fatalf("reload = %q,%v", a, ok)
	}
}

func TestFileReceiptsMissingFile(t *testing.T) {
	r, err := newFileReceipts(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("x"); ok {
		t.Fatal("missing file → empty")
	}
}
