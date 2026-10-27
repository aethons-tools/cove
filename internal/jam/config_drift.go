package jam

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// A managed object's state against its mark.
const (
	ConfigInSync  = "in-sync"
	ConfigDrifted = "drifted" // edited in Jam since the apply that set the mark
	ConfigMissing = "missing" // removed in Jam since
)

// ConfigDriftKey is the config.drift condition's key: one for the Jam.
const ConfigDriftKey = "config.drift:jam"

// ConfigDriftEvery is how often serve re-checks drift; applies, syncs and
// releases re-check at once.
const ConfigDriftEvery = time.Minute

// configDriftFix is the condition's fix: the three ways out.
const configDriftFix = "Review with jam config diff (or the Config page), then: config apply --force to restore the repo's version, " +
	"config write + commit + config apply to keep Jam's, or config release <kind>/<name> to stop managing it."

// configDriftLastErr is the error text the drift checker last warned about
// ("" after a check that worked), so a Jam that stays unrenderable warns
// once, not every minute. Guarded by configMu.
var configDriftLastErr string

// ConfigMarkState is a managed mark and how its live object compares.
type ConfigMarkState struct {
	ConfigMark
	State string `json:"state"` // ConfigInSync | ConfigDrifted | ConfigMissing
}

// ConfigMarkStates compares every managed mark with its live object's
// digest, in the canonical form the planner uses (exportObjects): in sync,
// drifted or missing. It renders every live object once (nothing without
// marks). Marks are read before the snapshot; hold configMu for a reading
// no apply lands inside.
func ConfigMarkStates(store Store) ([]ConfigMarkState, error) {
	marks := store.ListConfigMarks()
	if len(marks) == 0 {
		return nil, nil
	}
	live, err := exportObjects(stateFromSnapshot(store.configSnapshot(), nil), nil)
	if err != nil {
		return nil, fmt.Errorf("config drift: %w", err)
	}
	out := make([]ConfigMarkState, 0, len(marks))
	for _, mk := range marks {
		state := ConfigInSync
		switch o, ok := live[mk.Key()]; {
		case !ok:
			state = ConfigMissing
		case o.Digest() != mk.Digest:
			state = ConfigDrifted
		}
		out = append(out, ConfigMarkState{ConfigMark: mk, State: state})
	}
	return out, nil
}

// CheckConfigDrift raises config.drift (info) while any managed object has
// drifted or gone missing, and clears it otherwise. It holds configMu, so
// it never compares marks and objects from either side of an apply. t nil:
// a no-op.
func CheckConfigDrift(store Store, t *condition.Tracker, log *slog.Logger) {
	if t == nil {
		return
	}
	configMu.Lock()
	defer configMu.Unlock()
	checkConfigDriftLocked(store, t, log)
}

// checkConfigDriftLocked is CheckConfigDrift for a caller holding configMu.
// A Jam it cannot render leaves the condition as it was, and warns only
// when the error differs from the last one warned.
func checkConfigDriftLocked(store Store, t *condition.Tracker, log *slog.Logger) {
	if t == nil {
		return
	}
	states, err := ConfigMarkStates(store)
	if err != nil {
		if msg := err.Error(); msg != configDriftLastErr {
			configDriftLastErr = msg
			if log != nil {
				log.Warn("config drift: check failed; config.drift unchanged", "err", msg)
			}
		}
		return
	}
	configDriftLastErr = ""
	var lines []string
	for _, s := range states {
		if s.State != ConfigInSync {
			lines = append(lines, s.Key().String()+" ("+s.State+")")
		}
	}
	if len(lines) == 0 {
		t.Clear(ConfigDriftKey)
		return
	}
	t.Raise(condition.Condition{Key: ConfigDriftKey, Severity: condition.Info,
		Summary: fmt.Sprintf("%d managed object(s) differ from the config repo", len(lines)),
		Detail:  strings.Join(lines, "\n"), // names only; the tracker clips it
		Fix:     configDriftFix})
}

// RunConfigDriftCheck checks drift now and every `every` until ctx ends. Each
// round also re-evaluates the kit.migrate conditions, so a push, pin or
// removal that leaves a kit's current version not broken clears its
// condition within a round, and a pin onto a broken version opens it.
func RunConfigDriftCheck(ctx context.Context, store Store, t *condition.Tracker, every time.Duration, log *slog.Logger) {
	if t == nil {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		kitMigrateConditions(store, t)
		ReservedDestinationEnvConditions(store, t)
		CheckConfigDrift(store, t, log)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
