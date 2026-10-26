package main

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/tracker"
)

// openTracker builds the Requisitioner's tracker from whichever block is
// set (validateRequisitioner guarantees exactly one). The same value
// serves the Requisitioner, ticket reports, wake-on close and the relay.
// A failure is a nil interface, never a typed nil.
func openTracker(d *requisitionerConfig, token string, httpc *http.Client) (tracker.Tracker, error) {
	if d.GitHub != nil {
		c, err := githubissues.NewJam(*d.GitHub, token, httpc)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	// linear.New wants a full kit.Config; wrap the configured LinearTracker.
	c, err := linear.New(kit.Config{Tracker: &kit.Tracker{Linear: d.Linear}}, token, httpc)
	if err != nil {
		return nil, err
	}
	return c, nil
}
