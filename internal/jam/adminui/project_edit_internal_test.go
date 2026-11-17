package adminui

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// A tier names people only — the engine skips a room — so the editor flags a
// channel: target unknown and a member known.
func TestChainFlagsRoomsUnknown(t *testing.T) {
	known := knownTargets([]jam.Member{{User: jam.User{ID: "usr_alice", Name: "alice"}}})
	tiers := []jam.EscalationTier{{Targets: []string{"user:alice", "human:usr_alice", "channel:eng"}, Timeout: 15 * time.Minute}}
	got := chain("", tiers, known).Tiers[0].Targets
	want := map[string]bool{"user:alice": false, "human:usr_alice": false, "channel:eng": true}
	if len(got) != len(want) {
		t.Fatalf("targets = %+v", got)
	}
	for _, tg := range got {
		if tg.Unknown != want[tg.Text] {
			t.Errorf("%s: Unknown = %v, want %v", tg.Text, tg.Unknown, want[tg.Text])
		}
	}
}
