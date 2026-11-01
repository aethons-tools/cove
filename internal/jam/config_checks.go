package jam

import (
	"context"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// ConfigChecksEvery is how often serve re-evaluates the periodic conditions.
const ConfigChecksEvery = time.Minute

// RunConfigChecks evaluates the periodic conditions now and every `every`
// until ctx ends: the kit.migrate conditions, so a push, pin or removal that
// leaves a kit's current version not broken clears its condition within a
// round and a pin onto a broken version opens it, and the reserved
// destination env conditions.
func RunConfigChecks(ctx context.Context, store Store, t *condition.Tracker, every time.Duration, _ *slog.Logger) {
	if t == nil {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		kitMigrateConditions(store, t)
		ReservedDestinationEnvConditions(store, t)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
