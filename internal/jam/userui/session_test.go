package userui

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func TestKTokens(t *testing.T) {
	for n, want := range map[int64]string{950: "950", 84300: "84k", 199600: "200k", 1_000_000: "1M", 1_250_000: "1.2M"} {
		if got := kTokens(n); got != want {
			t.Errorf("kTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestContextUse(t *testing.T) {
	at := time.Unix(1000, 0)
	for _, c := range []struct {
		t    sessionevents.Totals
		want string
	}{
		{sessionevents.Totals{}, ""},
		{sessionevents.Totals{LastAt: at, Context: 950, ContextModel: "m"}, "ctx 950"},
		{sessionevents.Totals{LastAt: at, Context: 950, ContextWindow: 1_000_000, ContextModel: "m"}, "ctx 950 / 1M · 0%"},
		{sessionevents.Totals{LastAt: at, ContextModel: "m"}, "ctx —"}, // reset since the last call
	} {
		if got, _ := contextUse(c.t); got != c.want {
			t.Errorf("contextUse(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}
