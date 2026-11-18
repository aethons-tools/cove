package agentrun

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/studioagent"
)

// Each IdleTimeout and IdleDeadline push replaces that message's last push,
// and neither touches the other.
func TestControlRecordsIdlePushes(t *testing.T) {
	w := New(Config{}, nil)
	if to, dl := w.idle.get(); to != 0 || !dl.IsZero() {
		t.Fatalf("before any push: timeout %v deadline %v, want none", to, dl)
	}
	at := time.UnixMilli(1_790_000_000_000)
	w.Control(studioagent.Control{Kind: studioagent.IdleTimeout, IdleTimeout: 30 * time.Minute})
	w.Control(studioagent.Control{Kind: studioagent.IdleDeadline, IdleDeadline: at})
	w.Control(studioagent.Control{Kind: studioagent.IdleTimeout, IdleTimeout: 2 * time.Hour})
	if to, dl := w.idle.get(); to != 2*time.Hour || !dl.Equal(at) {
		t.Fatalf("timeout %v deadline %v, want 2h and %v", to, dl, at)
	}
	w.Control(studioagent.Control{Kind: studioagent.IdleDeadline})
	w.Control(studioagent.Control{Kind: studioagent.IdleTimeout})
	if to, dl := w.idle.get(); to != 0 || !dl.IsZero() {
		t.Fatalf("after the 0 pushes: timeout %v deadline %v, want none", to, dl)
	}
}
