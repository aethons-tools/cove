package main

import "github.com/aethons-tools/cove/internal/jam/condition"

// clearStaleCredConditions resolves every open cred.unavailable condition
// whose credential is no longer configured (removed or renamed while the
// condition was open, then reloaded from Postgres): nothing would resolve it.
func clearStaleCredConditions(t *condition.Tracker, names []string) {
	live := map[string]bool{}
	for _, n := range names {
		live[condition.Key("cred.unavailable", n)] = true
	}
	for _, k := range t.OpenKeys("cred.unavailable") {
		if !live[k] {
			t.Ok(k)
		}
	}
}

// removedDriftKey is the key of the config drift condition, which no checker
// raises any more (config changes only through git): one persisted from
// before the upgrade would be reloaded and stay open forever, naming verbs
// that no longer exist.
const removedDriftKey = "config.drift:jam"

// clearRemovedConditions resolves the reloaded conditions whose checker is
// gone.
func clearRemovedConditions(t *condition.Tracker) {
	t.Ok(removedDriftKey)
}
