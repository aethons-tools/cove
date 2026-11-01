package jam

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// DestinationEnvKind is the condition kind of a destination whose stored env
// has a key under Jam's reserved prefix (destination.env:<name>): stored
// before JAM_* was reserved (the jam rename), it is dropped from every
// connector until renamed.
const DestinationEnvKind = "destination.env"

// reservedEnvKeys is d's env keys under the reserved prefix, sorted.
func reservedEnvKeys(d Destination) []string {
	var keys []string
	for k := range d.Env {
		if modelspec.ReservedEnvKey(k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// withoutReservedEnv is d with its reserved env keys dropped (Env stays
// non-nil, so a declared-env destination keeps its meaning).
func withoutReservedEnv(d Destination) Destination {
	if len(reservedEnvKeys(d)) == 0 {
		return d
	}
	d.Env = maps.Clone(d.Env)
	maps.DeleteFunc(d.Env, func(k, _ string) bool { return modelspec.ReservedEnvKey(k) })
	return d
}

// ReservedDestinationEnvConditions opens destination.env:<name> for each
// destination with a stored reserved env key, and clears every other open
// one. Fixed texts: the destination's name and the key names, never a value.
// Serve start and every RunConfigChecks round call it.
func ReservedDestinationEnvConditions(store Store, t *condition.Tracker) {
	open := map[string]bool{}
	for _, d := range store.ListDestinations() {
		keys := reservedEnvKeys(d)
		if len(keys) == 0 {
			continue
		}
		key := condition.Key(DestinationEnvKind, d.Name)
		open[key] = true
		list := strings.Join(keys, ", ")
		summary := fmt.Sprintf("env key %s is reserved (JAM_*); rename it", list)
		detail := fmt.Sprintf("destination %s sets %s; Jam drops it from every connector, as JAM_* names are Jam's own", d.Name, list)
		if len(keys) > 1 {
			summary = fmt.Sprintf("env keys %s are reserved (JAM_*); rename them", list)
			detail = fmt.Sprintf("destination %s sets %s; Jam drops them from every connector, as JAM_* names are Jam's own", d.Name, list)
		}
		t.Raise(condition.Condition{Key: key, Severity: condition.Warning,
			Summary: summary,
			Detail:  detail,
			Fix:     "rename the key in the destination's env (" + renamedFromAtDoc + ")"})
	}
	for _, key := range t.OpenKeys(DestinationEnvKind) {
		if !open[key] {
			t.Clear(key)
		}
	}
}
