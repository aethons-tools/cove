package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ConfigSnapshotVersion is the on-the-wire/on-disk schema version of a config
// backup. Import rejects any other value rather than mis-loading.
const ConfigSnapshotVersion = 1

// ConfigSnapshot is a backup of the jam control-plane CONFIG aggregates only:
// actors (with their token hashes and grants), roles, kits (all versions + the
// pin), destinations, and projects (roster, escalation, chat service). It never
// carries runtime/studio state (instances) or intercom unread cursors — those
// aggregates simply have no field here.
type ConfigSnapshot struct {
	Version      int                        `json:"version"`
	ExportedAt   time.Time                  `json:"exported_at"`
	Actors       []Actor                    `json:"actors"`
	Roles        map[string]map[string]Role `json:"roles"` // project → name → Role
	Kits         []Kit                      `json:"kits"`  // full: Current + all Versions
	Destinations []Destination              `json:"destinations"`
	Projects     []Project                  `json:"projects"`
}

// ErrConfigNotEmpty is returned by ImportConfig when the target already holds
// config; import is fail-closed and writes nothing in that case.
var ErrConfigNotEmpty = errors.New("import refused: target config is not empty")

// ErrUnsupportedConfigVersion is returned by ImportConfig for a snapshot whose
// Version this build does not understand.
var ErrUnsupportedConfigVersion = errors.New("unsupported config snapshot version")

// ExportConfig returns a deep copy of the five config aggregates, sorted for a
// stable/diffable backup. Runtime state (instances, unread cursors) is never
// read. Safe under the read lock; the returned snapshot shares nothing with the
// live store.
func (m *memState) ExportConfig() ConfigSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := ConfigSnapshot{Version: ConfigSnapshotVersion, ExportedAt: time.Now().UTC()}
	for _, a := range m.actors {
		snap.Actors = append(snap.Actors, a)
	}
	sort.Slice(snap.Actors, func(i, j int) bool { return snap.Actors[i].ID < snap.Actors[j].ID })

	snap.Roles = map[string]map[string]Role{}
	for p, rs := range m.roles {
		if len(rs) == 0 {
			continue
		}
		inner := make(map[string]Role, len(rs))
		for n, r := range rs {
			inner[n] = r
		}
		snap.Roles[p] = inner
	}

	for _, k := range m.kits {
		snap.Kits = append(snap.Kits, k)
	}
	sort.Slice(snap.Kits, func(i, j int) bool { return snap.Kits[i].Name < snap.Kits[j].Name })

	for _, d := range m.dests {
		snap.Destinations = append(snap.Destinations, d)
	}
	sort.Slice(snap.Destinations, func(i, j int) bool { return snap.Destinations[i].Name < snap.Destinations[j].Name })

	for name, p := range m.projects {
		p.Name = name
		snap.Projects = append(snap.Projects, p)
	}
	sort.Slice(snap.Projects, func(i, j int) bool { return snap.Projects[i].Name < snap.Projects[j].Name })

	return deepCopySnapshot(snap)
}

// deepCopySnapshot returns a copy sharing no maps/slices/pointers with s, via a
// JSON round-trip. The config structs are exactly what the store persists as
// JSON docs, so marshaling cannot fail in practice; a failure yields the zero
// snapshot rather than an aliased one.
func deepCopySnapshot(s ConfigSnapshot) ConfigSnapshot {
	b, err := json.Marshal(s)
	if err != nil {
		return ConfigSnapshot{Version: s.Version}
	}
	var out ConfigSnapshot
	if err := json.Unmarshal(b, &out); err != nil {
		return ConfigSnapshot{Version: s.Version}
	}
	return out
}

// checkImport validates a snapshot against an empty target. Caller holds the
// write lock. Returns ErrUnsupportedConfigVersion for a bad version, or
// ErrConfigNotEmpty (naming the offending aggregates) if any config aggregate
// already has entries.
func checkImport(m *memState, s ConfigSnapshot) error {
	if s.Version != ConfigSnapshotVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedConfigVersion, s.Version, ConfigSnapshotVersion)
	}
	if names := nonEmptyConfigAggregates(m); len(names) > 0 {
		return fmt.Errorf("%w (non-empty: %s)", ErrConfigNotEmpty, strings.Join(names, ", "))
	}
	return nil
}

// nonEmptyConfigAggregates names the config aggregates that currently hold
// entries. Caller holds at least the read lock.
func nonEmptyConfigAggregates(m *memState) []string {
	var names []string
	if len(m.actors) > 0 {
		names = append(names, "actors")
	}
	roleCount := 0
	for _, rs := range m.roles {
		roleCount += len(rs)
	}
	if roleCount > 0 {
		names = append(names, "roles")
	}
	if len(m.kits) > 0 {
		names = append(names, "kits")
	}
	if len(m.dests) > 0 {
		names = append(names, "destinations")
	}
	if len(m.projects) > 0 {
		names = append(names, "projects")
	}
	return names
}

// applyImport overwrites the config maps from a (deep-copied) snapshot. Caller
// holds the write lock and has already validated with checkImport. State maps
// (instances, unread) are untouched.
func applyImport(m *memState, s ConfigSnapshot) {
	s = deepCopySnapshot(s)
	m.actors = map[string]Actor{}
	for _, a := range s.Actors {
		m.actors[a.TokenHash] = a
	}
	m.roles = map[string]map[string]Role{}
	for p, rs := range s.Roles {
		inner := map[string]Role{}
		for n, r := range rs {
			inner[n] = r
		}
		m.roles[p] = inner
	}
	m.kits = map[string]Kit{}
	for _, k := range s.Kits {
		m.kits[k.Name] = k
	}
	m.dests = map[string]Destination{}
	for _, d := range s.Destinations {
		m.dests[d.Name] = d
	}
	m.projects = map[string]Project{}
	for _, p := range s.Projects {
		m.projects[p.Name] = p
	}
}

// ImportConfig restores a snapshot into an empty FileStore (fail-closed) and
// persists it.
func (fs *FileStore) ImportConfig(s ConfigSnapshot) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := checkImport(fs.memState, s); err != nil {
		return err
	}
	applyImport(fs.memState, s)
	return fs.save()
}
