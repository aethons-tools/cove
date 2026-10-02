package jam

import (
	"net/http"
	"strings"
	"sync"
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

func TestPutRosterHuman(t *testing.T) {
	st := newRoleStore(t)
	must(t, st.CreateProject("acme"))
	alice := Human{Name: "alice", Handle: "a", Login: "sub-a", Delivery: []DeliveryProfile{{Service: "discord", Address: "c", UserID: "111"}}}
	must(t, PutRosterHuman(st, "acme", alice))
	alice.Handle = "a2" // re-put under the same name updates
	must(t, PutRosterHuman(st, "acme", alice))
	if r, _ := st.GetRoster("acme"); len(r.Humans) != 1 || r.Humans[0].Handle != "a2" {
		t.Fatalf("roster = %+v", r.Humans)
	}
	for name, h := range map[string]Human{
		"no name":         {Handle: "x"},
		"login taken":     {Name: "bob", Login: "sub-a"},
		"discord id used": {Name: "bob", Delivery: []DeliveryProfile{{Service: "discord", Address: "d", UserID: "111"}}},
		"bad delivery":    {Name: "bob", Delivery: []DeliveryProfile{{Service: "linear", Address: "d", UserID: "1"}}},
		"bad identity":    {Name: "bob", Identity: []OIDCIdentity{{Issuer: "", Subject: "s"}}},
	} {
		if err := PutRosterHuman(st, "acme", h); WriteStatus(err, 0) != http.StatusBadRequest {
			t.Errorf("%s: err = %v, want 400", name, err)
		}
	}
	if err := PutRosterHuman(st, "ghost", Human{Name: "x"}); WriteStatus(err, 0) != http.StatusNotFound {
		t.Errorf("unknown project = %v, want 404", err)
	}
}

// Two humans racing for one login: exactly one wins.
func TestPutRosterHumanLoginRace(t *testing.T) {
	st := newRoleStore(t)
	must(t, st.CreateProject("acme"))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- PutRosterHuman(st, "acme", Human{Name: "h" + strings.Repeat("x", i), Login: "same"})
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d humans got the same login, want 1", ok)
	}
}
