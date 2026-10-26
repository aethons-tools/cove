package dispatcher

import (
	"context"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/tracker/trackertest"
)

// TestRequisitionerOnBothTrackers runs one real poll pass of the
// Requisitioner against each tracker's fake, through the shared interface:
// only the dispatch-labeled issue is claimed and raised, keyed by its
// ticket key.
func TestRequisitionerOnBothTrackers(t *testing.T) {
	for _, c := range trackertest.Both(t) {
		t.Run(c.Kind, func(t *testing.T) {
			raiser := &fakeRaiser{}
			d := New(c.Tracker, raiser, &fakeRegistry{}, &fakeAdmitter{grant: true}, Config{Role: "worker", Project: "acme"}, nil)
			d.tick(context.Background())
			if len(raiser.specs) != 1 {
				t.Fatalf("raised %d; want 1: %+v", len(raiser.specs), raiser.specs)
			}
			s := raiser.specs[0]
			if s.Unit != c.Key || s.Mission != "Fix the build" || !strings.Contains(s.Prompt, c.Key) || !strings.Contains(s.Prompt, "It is red.") {
				t.Fatalf("spec = %+v", s)
			}
			if got := c.State(); got != "in-progress" {
				t.Fatalf("claimed issue state = %q; want in-progress", got)
			}
		})
	}
}
