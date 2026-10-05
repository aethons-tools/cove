package jam

import "testing"

func TestParseActivityAcceptsHolding(t *testing.T) {
	for _, s := range []string{"running", "waiting", "holding", "blocked", "done"} {
		if a, ok := parseActivity(s); !ok || string(a) != s {
			t.Errorf("parseActivity(%q) = %q, %v", s, a, ok)
		}
	}
	if _, ok := parseActivity("sleeping"); ok {
		t.Error("parseActivity accepted an unknown activity")
	}
}
