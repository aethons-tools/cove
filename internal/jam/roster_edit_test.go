package jam

import (
	"testing"
	"time"
)

func TestDeliverySpecRoundTrip(t *testing.T) {
	for _, s := range []string{"discord:chan-1", "discord:chan-1:123456", "linear:alice"} {
		p, err := ParseDeliverySpec(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got := FormatDeliverySpec(p); got != s {
			t.Errorf("round trip %q → %q", s, got)
		}
	}
	for _, bad := range []string{"discord", ":x", "discord:", "discord:c:", "linear:x:123", "discord:c:notdigits"} {
		if _, err := ParseDeliverySpec(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOIDCSpecRoundTrip(t *testing.T) {
	id, err := ParseOIDCSpec("https://idp.example:443/realm:sub-1")
	if err != nil || id.Issuer != "https://idp.example:443/realm" || id.Subject != "sub-1" {
		t.Fatalf("parse = %+v, %v", id, err)
	}
	if FormatOIDCSpec(id) != "https://idp.example:443/realm:sub-1" {
		t.Errorf("format = %q", FormatOIDCSpec(id))
	}
	for _, bad := range []string{"nocolon", ":sub", "iss:"} {
		if _, err := ParseOIDCSpec(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEscalationTierSpecRoundTrip(t *testing.T) {
	tr, err := ParseEscalationTierSpec("human:alice, human:bob@15m")
	if err != nil || len(tr.Targets) != 2 || tr.Targets[1] != "human:bob" || tr.Timeout != 15*time.Minute {
		t.Fatalf("parse = %+v, %v", tr, err)
	}
	if got := FormatEscalationTierSpec(tr); got != "human:alice,human:bob@15m" {
		t.Errorf("format = %q", got)
	}
	for _, bad := range []string{"human:alice", "human:alice@", "@15m", "human:alice@soon", "human:alice@-1m", "human:alice@0s"} {
		if _, err := ParseEscalationTierSpec(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
