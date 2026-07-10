//go:build integration

package linear

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
)

//	TestLive hits real Linear. Run with: LINEAR_TOKEN=… LINEAR_TEAM=AET \
//	  go test -tags integration ./internal/dispatch/linear/ -run TestLive -v
func TestLive(t *testing.T) {
	token, team := os.Getenv("LINEAR_TOKEN"), os.Getenv("LINEAR_TEAM")
	if token == "" || team == "" {
		t.Skip("set LINEAR_TOKEN and LINEAR_TEAM to run the live smoke test")
	}
	cfg := kit.Config{Tracker: &kit.Tracker{Linear: &kit.LinearTracker{
		Team: team, ClassLabelPrefix: "class:",
		States: kit.StateMap{Ready: "Todo", InProgress: "In Progress", InReview: "In Review", Done: "Done", NeedsInput: "Needs Input", Blocked: "Backlog"},
	}}}
	c, err := New(cfg, token, http.DefaultClient)
	if err != nil {
		t.Fatalf("New (state map): %v", err)
	}
	if _, err := c.ListReady(context.Background()); err != nil {
		t.Fatalf("ListReady: %v", err)
	}
}
