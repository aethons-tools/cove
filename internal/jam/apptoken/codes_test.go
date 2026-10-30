package apptoken

import (
	"errors"
	"testing"
	"time"
)

func TestCodesSingleUse(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := newCodes(func() time.Time { return now })
	raw, err := c.issue(grant{Challenge: "ch", UserID: "usr_a"})
	if err != nil || raw == "" {
		t.Fatal(raw, err)
	}
	if _, ok := c.m[raw]; ok {
		t.Fatal("raw code used as a key")
	}
	g, ok := c.redeem(raw)
	if !ok || g.Challenge != "ch" || g.UserID != "usr_a" {
		t.Fatalf("redeem: %+v %v", g, ok)
	}
	if _, ok := c.redeem(raw); ok {
		t.Fatal("second redeem succeeded")
	}
	if _, ok := c.redeem(""); ok {
		t.Fatal("empty code redeemed")
	}
}

func TestCodesExpire(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := newCodes(func() time.Time { return now })
	raw, _ := c.issue(grant{})
	keep, _ := c.issue(grant{})
	now = now.Add(CodeTTL)
	if _, ok := c.redeem(raw); ok {
		t.Fatal("expired code redeemed")
	}
	c.prune()
	if len(c.m) != 0 {
		t.Fatalf("prune left %d", len(c.m))
	}
	_ = keep
}

func TestCodesCap(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := newCodes(func() time.Time { return now })
	for range maxCodes {
		if _, err := c.issue(grant{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.issue(grant{}); !errors.Is(err, errTooManyCodes) {
		t.Fatalf("over cap: %v", err)
	}
	now = now.Add(CodeTTL)
	if _, err := c.issue(grant{}); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}
