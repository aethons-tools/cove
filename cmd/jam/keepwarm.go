package main

import (
	"context"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/wakeon"
)

// compactDueBy is who the compaction Jam runs after an ignored compact-due
// wake is recorded as.
const compactDueBy = "Jam (compact-due)"

// keepWarmCompactor compacts a session whose compact-due turn didn't
// (wakeon.Compactor), through the session view's Compact.
type keepWarmCompactor struct{ ops sessionOps }

func (c keepWarmCompactor) Compact(ctx context.Context, inst jam.Instance) error {
	return c.ops.Command(ctx, inst, jam.CmdCompact, compactDueBy)
}

// contextSizes reads a session's context size from its events
// (wakeon.ContextSizes).
type contextSizes struct{ totals *sessionevents.TotalsCache }

func (c contextSizes) ContextSize(actorID string) (wakeon.ContextSize, error) {
	t, err := c.totals.Of(actorID)
	if err != nil {
		return wakeon.ContextSize{}, err
	}
	return wakeon.ContextSize{Tokens: t.LastInput, Resets: t.Resets}, nil
}
