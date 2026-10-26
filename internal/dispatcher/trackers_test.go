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
// ticket key; every other issue's state is untouched.
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
			// The issues not dispatched are left as they were: the unlabeled
			// one, and (GitHub) the pull request labelled like the claimed one.
			untouched := map[string]string{}
			if c.Linear != nil {
				untouched["ACME-2"] = c.Linear.IssueState("lin-2")
			}
			if c.GitHub != nil {
				untouched["#2"], untouched["#3 (pull request)"] = c.GitHub.State(2), c.GitHub.State(3)
			}
			for issue, state := range untouched {
				if state != "Ready" && state != "ready" {
					t.Errorf("%s state = %q; want untouched (ready)", issue, state)
				}
			}
		})
	}
}
