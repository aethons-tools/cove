package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/dispatch/githubissues"
	"github.com/aethons-tools/cove/internal/dispatch/linear"
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
	if d.Linear == nil {
		return nil, fmt.Errorf("linear: no linear tracker configured")
	}
	c, err := linear.New(*d.Linear, token, httpc)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Viewer retry backoff for a GitHub tracker whose lookup failed (not
// ErrNoViewer, not a rate limit): from viewerRetryMin, doubling, at most
// viewerRetryMax between attempts.
const (
	viewerRetryMin = 2 * time.Second
	viewerRetryMax = 5 * time.Minute
)

// trackerSelf resolves Jam's own identity on the tracker: the relay's
// self-post filter. Linear: one lookup, as always (an error is warned and
// leaves it unknown). GitHub: ErrNoViewer (an App token) is "" — the relay
// then relies on dropping Bot authors; any other error is warned once per
// attempt and retried (after a rate limit's RetryAfter, else a doubling
// backoff) until it succeeds, so the relay never reads the feed without a
// self filter. ok=false: ctx ended first. sleep waits d or until ctx ends
// (false).
func trackerSelf(ctx context.Context, kind string, viewer func(context.Context) (string, error), log *slog.Logger, sleep func(context.Context, time.Duration) bool) (self string, ok bool) {
	wait := viewerRetryMin
	for attempt := 1; ; attempt++ {
		self, err := viewer(ctx)
		switch {
		case err == nil:
			return self, true
		case errors.Is(err, tracker.ErrNoViewer):
			log.Info("Jam relay: the token has no viewer identity; dropping bot authors only", "kind", kind)
			return "", true
		case kind != tracker.KindGitHub:
			log.Warn("Jam relay: viewer lookup failed; self-post filter disabled", "kind", kind, "error", err.Error())
			return "", true
		}
		next := wait
		var rl *tracker.RateLimitError
		if errors.As(err, &rl) {
			next = rl.RetryAfter
		} else {
			wait = min(wait*2, viewerRetryMax)
		}
		log.Warn("Jam relay: viewer lookup failed; the relay starts once it succeeds", "kind", kind, "attempt", attempt, "retry-in", next.String(), "error", err.Error())
		if !sleep(ctx, next) {
			return "", false
		}
	}
}

// sleepCtx waits d, or until ctx ends (false).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
