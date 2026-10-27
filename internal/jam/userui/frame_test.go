package userui

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestMintLimiterSlides(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newMintLimiter(func() time.Time { return now })
	alice, bob := ident.New(ident.User), ident.New(ident.User)
	for range maxFrameMints {
		if !l.allow(alice) {
			t.Fatal("refused under the cap")
		}
		now = now.Add(time.Second)
	}
	if l.allow(alice) {
		t.Fatal("allowed over the cap")
	}
	if !l.allow(bob) {
		t.Fatal("one user's mints count against another")
	}
	// The oldest falls out of the window: one more is allowed, then not.
	now = time.Unix(1000, 0).Add(frameMintWindow)
	if !l.allow(alice) {
		t.Fatal("still refused once the oldest left the window")
	}
	if l.allow(alice) {
		t.Fatal("allowed two after one left the window")
	}
}
