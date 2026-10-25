package collabhttp

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/collab"
)

func newTestFrameTickets() (*FrameTickets, *time.Time) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	return NewFrameTickets(func() time.Time { return now }), &now
}

func TestFrameTicketRedeemOnce(t *testing.T) {
	f, _ := newTestFrameTickets()
	p := collab.Principal{Operator: "op"}
	raw, err := f.Mint(p, "Ada", "a.example.com", collab.Right("preview"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 43 {
		t.Fatalf("ticket too short: %d", len(raw))
	}
	got, ok := f.Redeem(raw, "a.example.com")
	if !ok || got.principal != p || got.name != "Ada" || got.host != "a.example.com" || got.right != collab.Right("preview") {
		t.Fatalf("redeem = %+v, %v", got, ok)
	}
	if _, ok := f.Redeem(raw, "a.example.com"); ok {
		t.Fatal("second redeem succeeded")
	}
}

func TestFrameTicketWrongHostSpends(t *testing.T) {
	f, _ := newTestFrameTickets()
	raw, _ := f.Mint(collab.Principal{}, "n", "a.example.com", collab.Right("preview"))
	if _, ok := f.Redeem(raw, "b.example.com"); ok {
		t.Fatal("wrong host redeemed")
	}
	if _, ok := f.Redeem(raw, "a.example.com"); ok {
		t.Fatal("ticket survived a wrong-host probe")
	}
}

func TestFrameTicketExpires(t *testing.T) {
	f, now := newTestFrameTickets()
	raw, _ := f.Mint(collab.Principal{}, "n", "a.example.com", collab.Right("preview"))
	*now = now.Add(61 * time.Second)
	if _, ok := f.Redeem(raw, "a.example.com"); ok {
		t.Fatal("expired ticket redeemed")
	}
}

func TestFrameTicketPrune(t *testing.T) {
	f, now := newTestFrameTickets()
	_, _ = f.Mint(collab.Principal{}, "n", "a.example.com", collab.Right("preview"))
	*now = now.Add(30 * time.Second)
	_, _ = f.Mint(collab.Principal{}, "n", "a.example.com", collab.Right("preview"))
	*now = now.Add(31 * time.Second)
	f.Prune()
	if len(f.m) != 1 {
		t.Fatalf("len after prune = %d, want 1", len(f.m))
	}
}

func TestFrameTicketStoredByHash(t *testing.T) {
	f, _ := newTestFrameTickets()
	raw, _ := f.Mint(collab.Principal{}, "n", "a.example.com", collab.Right("preview"))
	if len(f.m) != 1 {
		t.Fatalf("len = %d", len(f.m))
	}
	for k := range f.m {
		if string(k[:]) == raw {
			t.Fatal("raw ticket used as key")
		}
	}
	if _, ok := f.m[sha256Of(raw)]; !ok {
		t.Fatal("not keyed by sha256 of raw ticket")
	}
}

func sha256Of(s string) [32]byte { return sha256.Sum256([]byte(s)) }
