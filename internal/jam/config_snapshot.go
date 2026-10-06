package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ConfigSnapshotVersion is the on-the-wire/on-disk schema version of a config
// backup this build writes. 2 carries the identity registry (users,
// connections, accounts, memberships, legacy aliases) with their ids; import
// also reads 1, whose roster humans and kind-named chat services it migrates.
// Import rejects any other value rather than mis-loading.
const ConfigSnapshotVersion = 3

// ConfigSnapshot is a backup of the jam control-plane CONFIG aggregates only:
// actors (with their token hashes and grants), roles, kits (all versions + the
// pin), destinations, model-specs, projects (roster, escalation, chat service, session context) and the
// Jam-wide session context. It never
// carries runtime/studio state (instances) or the intercom's read cursors and channel memberships — those
// aggregates simply have no field here.
type ConfigSnapshot struct {
	Version      int                        `json:"version"`
	ExportedAt   time.Time                  `json:"exported_at"`
	Actors       []Actor                    `json:"actors"`
	Roles        map[string]map[string]Role `json:"roles"` // project → name → Role
	Kits         []Kit                      `json:"kits"`  // full: Current + all Versions
	Destinations []Destination              `json:"destinations"`
	// ModelSpecs is omitted when empty, so pre-model-spec backups and readers
	// are unaffected (no version bump).
	ModelSpecs []ModelSpec `json:"model_specs,omitempty"`
	Projects   []Project   `json:"projects"`
	// JamContext is the Jam-wide authored session-context layer; nil = none.
	JamContext *sessionctx.Layer `json:"jam_context,omitempty"`
	// ModelSpecSchema is the exporter's model-spec schema marker
	// (ModelSpecSchemaVersion); 0 (absent) = a pre-COV-242 backup, whose specs
	// an import migrates (MigrateSnapshotModelSpecs).
	ModelSpecSchema int `json:"model_spec_schema,omitempty"`

	// The identity registry (v2): every entity, removed ones included (their
	// ids back history), so a restore keeps every stored reference valid.
	Users         []User        `json:"users,omitempty"`
	Connections   []Connection  `json:"connections,omitempty"`
	Accounts      []Account     `json:"accounts,omitempty"`
	Memberships   []Membership  `json:"memberships,omitempty"`
	LegacyAliases []LegacyAlias `json:"legacy_aliases,omitempty"`
	// StandingSessions maps declared standing sessions to their sessions, so
	// a restore keeps them on their state.
	StandingSessions []StandingSessionRef `json:"standing_sessions,omitempty"`
	// Channels (v3) is the channel registry — rooms, live and archived, with
	// their bindings — so a restore keeps channel ids. Membership is runtime
	// state and is not exported.
	Channels []Channel `json:"channels,omitempty"`
}

// ErrConfigNotEmpty is returned by ImportConfig when the target already holds
// config; import is fail-closed and writes nothing in that case.
var ErrConfigNotEmpty = errors.New("import refused: target config is not empty")

// ErrUnsupportedConfigVersion is returned by ImportConfig for a snapshot whose
// Version this build does not understand.
var ErrUnsupportedConfigVersion = errors.New("unsupported config snapshot version")

// ExportConfig returns a deep copy of the config aggregates, sorted for a
// stable/diffable backup. Runtime state (instances, read cursors) is never
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
		snap.Destinations = append(snap.Destinations, copyDestination(d))
	}
	sort.Slice(snap.Destinations, func(i, j int) bool { return snap.Destinations[i].Name < snap.Destinations[j].Name })

	for _, ms := range m.specs {
		snap.ModelSpecs = append(snap.ModelSpecs, ms)
	}
	sort.Slice(snap.ModelSpecs, func(i, j int) bool { return snap.ModelSpecs[i].Name < snap.ModelSpecs[j].Name })

	for name, p := range m.projects {
		p = copyProject(p) // the stored doc: people travel in the registry, not roster humans
		p.Name = name
		snap.Projects = append(snap.Projects, p)
	}
	sort.Slice(snap.Projects, func(i, j int) bool { return snap.Projects[i].Name < snap.Projects[j].Name })

	snap.ModelSpecSchema = m.specSchema
	m.exportRegistry(&snap)
	if !m.jamContext.Empty() {
		jc := m.jamContext
		snap.JamContext = &jc
	}
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
	if s.Version < 1 || s.Version > ConfigSnapshotVersion {
		return fmt.Errorf("%w: got %d, want 1 to %d", ErrUnsupportedConfigVersion, s.Version, ConfigSnapshotVersion)
	}
	if names := nonEmptyConfigAggregates(m); len(names) > 0 {
		return fmt.Errorf("%w (non-empty: %s)", ErrConfigNotEmpty, strings.Join(names, ", "))
	}
	if err := validateSnapshotProjectIDs(m, s); err != nil {
		return err
	}
	return validateSnapshotContext(s)
}

// validateSnapshotProjectIDs checks the ids a snapshot gives its projects: each
// must be a well-formed project id, unique in the snapshot and unused in the
// store. An empty id is allowed (withReferencedProjects mints one).
func validateSnapshotProjectIDs(m *memState, s ConfigSnapshot) error {
	seen := map[ident.ID]bool{}
	for _, p := range s.Projects {
		if p.ID == "" {
			continue
		}
		if _, err := ident.Parse(string(p.ID)); err != nil || p.ID.Kind() != ident.Project {
			return fmt.Errorf("%w: project %q has invalid id %q", ErrInvalidConfig, p.Name, p.ID)
		}
		if seen[p.ID] || m.idExists(p.ID) {
			return fmt.Errorf("%w: project %q reuses id %q", ErrInvalidConfig, p.Name, p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// ErrInvalidConfig is returned by ImportConfig for a snapshot whose authored
// session context (or a destination note) breaks the admin API's rules.
var ErrInvalidConfig = errors.New("import refused: invalid config")

// validateSnapshotContext applies the authoring rules the admin API enforces —
// layer budgets, leaf names, resources, destination notes — so a hand-edited
// backup can't store context sessions would receive truncated or broken.
func validateSnapshotContext(s ConfigSnapshot) error {
	bad := func(what string, err error) error { return fmt.Errorf("%w: %s: %v", ErrInvalidConfig, what, err) }
	if s.JamContext != nil {
		if err := sessionctx.ValidateLayer(*s.JamContext, sessionctx.BudgetJam); err != nil {
			return bad("jam context", err)
		}
	}
	for project, rs := range s.Roles {
		for name, r := range rs {
			if err := sessionctx.ValidateLayer(r.Context, sessionctx.BudgetRole); err != nil {
				return bad("role "+project+"/"+name+" context", err)
			}
		}
	}
	for _, p := range s.Projects {
		if err := validateProjectContext(p.Context, p.Resources); err != nil {
			return bad("project "+p.Name, err)
		}
	}
	for _, d := range s.Destinations {
		if len(d.Note) > MaxDestinationNote {
			return bad("destination "+d.Name, fmt.Errorf("note is %d bytes; at most %d", len(d.Note), MaxDestinationNote))
		}
		if err := d.validateHeaderSpecs(); err != nil {
			return bad("destination "+d.Name, err)
		}
		if err := d.ValidateAllowPaths(); err != nil {
			return bad("destination "+d.Name, err)
		}
	}
	sm := newSpecMigration(s.ModelSpecSchema, s.Destinations)
	for _, ms := range s.ModelSpecs {
		// Credentials are serve-config, not snapshot, state: check the structure
		// only (a restored Jam's credentials may differ from the exporter's).
		// A backup from before a migration step (COV-242, COV-245), or with
		// legacy oauth_beta flags (COV-241), is checked in the form the
		// migration leaves it (the admin import handler stores it migrated; a
		// direct store import is migrated at the next serve startup).
		ms, _ = sm.apply(ms)
		if err := ValidateModelSpec(ms, func(string) bool { return true }, true); err != nil {
			return bad("model-spec "+ms.Name, err)
		}
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
	if len(m.specs) > 0 {
		names = append(names, "model_specs")
	}
	if len(m.projects) > 0 {
		names = append(names, "projects")
	}
	// Connections alone don't count: a starting serve creates the one its
	// (deprecated) serve config names; import reconciles them.
	if len(m.users) > 0 || len(m.accounts) > 0 || len(m.channels) > 0 {
		names = append(names, "registry")
	}
	if !m.jamContext.Empty() {
		names = append(names, "jam_context")
	}
	return names
}

// withReferencedProjects returns s with an empty record added for every project
// a role or grant names but s.Projects lacks — a snapshot exported before
// projects were first-class — so an import never leaves a dangling reference.
// Every project without an id (a snapshot exported before projects had ids)
// gets a fresh one. It is idempotent: a second pass keeps the first's ids.
func withReferencedProjects(s ConfigSnapshot) ConfigSnapshot {
	projects := make([]Project, 0, len(s.Projects))
	have := map[string]bool{}
	for _, p := range s.Projects {
		if p.ID == "" {
			p.ID = newProject(p.Name).ID
		}
		have[p.Name] = true
		projects = append(projects, p)
	}
	add := func(name string) {
		if name == "" {
			name = DefaultProject
		}
		if !have[name] {
			have[name] = true
			projects = append(projects, newProject(name))
		}
	}
	for p := range s.Roles {
		add(p)
	}
	for _, a := range s.Actors {
		for _, g := range a.Grants {
			add(g.Project)
		}
	}
	s.Projects = projects
	return s
}

// applyImport overwrites the config maps from a (deep-copied) snapshot. Caller
// holds the write lock and has already validated with checkImport. State maps
// (instances, read cursors, channel members) are untouched.
func applyImport(m *memState, s ConfigSnapshot) {
	s = withReferencedProjects(deepCopySnapshot(s))
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
		m.dests[d.Name] = copyDestination(d)
	}
	m.specs = map[string]ModelSpec{}
	for _, ms := range s.ModelSpecs {
		m.specs[ms.Name] = ms
	}
	m.projects = map[string]Project{}
	for _, p := range s.Projects {
		m.projects[p.Name] = p
	}
	m.specSchema = s.ModelSpecSchema
	m.jamContext = sessionctx.Layer{}
	if s.JamContext != nil {
		m.jamContext = *s.JamContext
	}
}

// ImportConfig restores a snapshot into an empty MemStore (fail-closed).
func (fs *MemStore) ImportConfig(s ConfigSnapshot) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, plan, err := importPlan(fs.memState, s)
	if err != nil {
		return err
	}
	applyImport(fs.memState, s)
	fs.applyHumanPlan(plan)
	return nil
}

// exportRegistry adds the identity registry to a snapshot, sorted by id (or
// key) for a stable backup. Caller holds mu.
func (m *memState) exportRegistry(snap *ConfigSnapshot) {
	for _, id := range slices.Sorted(maps.Keys(m.users)) {
		snap.Users = append(snap.Users, copyUser(m.users[id]))
	}
	for _, id := range slices.Sorted(maps.Keys(m.connections)) {
		snap.Connections = append(snap.Connections, m.connections[id])
	}
	for _, id := range slices.Sorted(maps.Keys(m.accounts)) {
		snap.Accounts = append(snap.Accounts, m.accounts[id])
	}
	for _, pid := range slices.Sorted(maps.Keys(m.members)) {
		for _, uid := range slices.Sorted(maps.Keys(m.members[pid])) {
			snap.Memberships = append(snap.Memberships, copyMembership(m.members[pid][uid]))
		}
	}
	for _, project := range slices.Sorted(maps.Keys(m.aliases)) {
		for _, name := range slices.Sorted(maps.Keys(m.aliases[project])) {
			snap.LegacyAliases = append(snap.LegacyAliases, LegacyAlias{Project: project, Name: name, UserID: m.aliases[project][name]})
		}
	}
	for k, id := range m.standing {
		snap.StandingSessions = append(snap.StandingSessions, StandingSessionRef{ProjectID: k.project, Role: k.role, Name: k.name, SessionID: id})
	}
	slices.SortFunc(snap.StandingSessions, func(a, b StandingSessionRef) int { return strings.Compare(a.SessionID, b.SessionID) })
	for _, id := range slices.Sorted(maps.Keys(m.channels)) {
		snap.Channels = append(snap.Channels, copyChannel(m.channels[id]))
	}
}

// importPlan validates a snapshot for import into m (an empty store) and
// returns it normalized (withReferencedProjects) together with the registry
// writes the import makes: the snapshot's own registry (v2), then every
// registry migration step its config still needs (a v1 snapshot's roster
// humans and kind-named chat services). Caller holds the write lock.
func importPlan(m *memState, s ConfigSnapshot) (ConfigSnapshot, humanPlan, error) {
	if err := checkImport(m, s); err != nil {
		return ConfigSnapshot{}, humanPlan{}, err
	}
	s = withReferencedProjects(deepCopySnapshot(s))
	var existing []Connection
	for _, c := range m.connections {
		existing = append(existing, c)
	}
	reg, scratch, err := planSnapshotRegistry(s, existing)
	if err != nil {
		return ConfigSnapshot{}, humanPlan{}, err
	}
	applyImport(scratch, s)
	mig := scratch.planRegistryMigration(0)
	reg.connections = append(reg.connections, mig.connections...)
	reg.users = append(reg.users, mig.users...)
	reg.accounts = append(reg.accounts, mig.accounts...)
	reg.memberships = append(reg.memberships, mig.memberships...)
	reg.aliases = append(reg.aliases, mig.aliases...)
	reg.projects = append(reg.projects, mig.projects...)
	reg.roles = append(reg.roles, mig.roles...)
	reg.actors = append(reg.actors, mig.actors...)
	reg.standing = append(reg.standing, mig.standing...)
	reg.channels = append(reg.channels, mig.channels...)
	reg.report = mig.report
	return s, reg, nil
}

// planSnapshotRegistry replays a snapshot's registry into a scratch state
// through the store's own rules — unique live names, logins and OIDC
// bindings, unique service identities, references that resolve — and returns
// it as registry writes, plus the scratch state (the snapshot's projects and
// registry over the target's connections). Removed (tombstoned) users and
// connections are kept as they are. existing are the (otherwise empty)
// target's connections — those a starting serve created: one the snapshot
// also names is tombstoned so the snapshot's takes over its name; the rest
// stay. Any inconsistency is ErrInvalidConfig.
func planSnapshotRegistry(s ConfigSnapshot, existing []Connection) (humanPlan, *memState, error) {
	bad := func(format string, args ...any) (humanPlan, *memState, error) {
		return humanPlan{}, nil, fmt.Errorf("%w: registry: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
	}
	m := newMemState()
	for _, p := range s.Projects {
		m.projects[p.Name] = p
	}
	var plan humanPlan
	for _, c := range existing {
		if c.Status == StatusLive && slices.ContainsFunc(s.Connections, func(sc Connection) bool { return sc.Status == StatusLive && sc.Name == c.Name }) {
			c.Status = StatusRemoved
			plan.connections = append(plan.connections, c)
		}
		m.applyPutConnection(c)
	}
	checkID := func(id ident.ID, k ident.Kind) error {
		if _, err := ident.Parse(string(id)); err != nil || id.Kind() != k {
			return fmt.Errorf("%q is not a %s id", id, k)
		}
		if m.idExists(id) {
			return fmt.Errorf("id %q is used twice", id)
		}
		return nil
	}
	for _, c := range s.Connections {
		if c.Status == StatusRemoved {
			if err := checkID(c.ID, ident.Connection); err != nil {
				return bad("connection %q: %v", c.Name, err)
			}
		} else {
			created, err := m.prepareCreateConnection(c)
			if err != nil || created.ID != c.ID {
				return bad("connection %q: %v", c.Name, err)
			}
		}
		m.applyPutConnection(c)
		plan.connections = append(plan.connections, c)
	}
	for _, u := range s.Users {
		if u.Status == StatusRemoved {
			if err := checkID(u.ID, ident.User); err != nil {
				return bad("user %q: %v", u.Name, err)
			}
			u.Logins, u.OIDC = nil, nil // a tombstone holds no identity
		} else {
			created, err := m.prepareCreateUser(u)
			if err != nil || created.ID != u.ID {
				return bad("user %q: %v", u.Name, err)
			}
		}
		m.applyPutUser(u)
		plan.users = append(plan.users, copyUser(u))
	}
	for _, a := range s.Accounts {
		if err := checkID(a.ID, ident.Account); err != nil {
			return bad("account %s: %v", a.ID, err)
		}
		if _, ok := m.connections[a.ConnectionID]; !ok {
			return bad("account %s: no connection %s", a.ID, a.ConnectionID)
		}
		if a.ServiceUID == "" && a.Handle == "" && a.Label == "" {
			return bad("account %s has no service uid, handle or label", a.ID)
		}
		if a.UserID != "" {
			if _, err := m.liveUser(a.UserID); err != nil {
				return bad("account %s: %v", a.ID, err)
			}
		}
		for _, o := range m.accounts {
			if o.ConnectionID == a.ConnectionID && (a.ServiceUID != "" && o.ServiceUID == a.ServiceUID ||
				a.Status == StatusLive && o.Status == StatusLive && a.Handle != "" && o.Handle == a.Handle) {
				return bad("accounts %s and %s share a service identity", o.ID, a.ID)
			}
		}
		m.applyPutAccount(a)
		plan.accounts = append(plan.accounts, a)
	}
	for _, ms := range s.Memberships {
		ms, err := m.preparePutMembership(ms)
		if err != nil {
			return bad("membership %s in %s: %v", ms.UserID, ms.ProjectID, err)
		}
		m.applyPutMembership(ms)
		plan.memberships = append(plan.memberships, ms)
	}
	for _, al := range s.LegacyAliases {
		if _, ok := m.users[al.UserID]; !ok {
			return bad("legacy alias %s/%s: no user %s", al.Project, al.Name, al.UserID)
		}
		plan.aliases = append(plan.aliases, al)
	}
	for _, ss := range s.StandingSessions {
		if err := m.preparePutStandingSession(ss.ProjectID, ss.Role, ss.Name, ss.SessionID); err != nil {
			return bad("standing session %s/%s: %v", ss.Role, ss.Name, err)
		}
		m.applyPutStandingSession(ss.ProjectID, ss.Role, ss.Name, ss.SessionID)
		plan.standing = append(plan.standing, ss)
	}
	for _, c := range s.Channels {
		if c.Status != StatusLive && c.Status != StatusArchived {
			return bad("channel %q: status %q", c.Key, c.Status)
		}
		if c.Status == StatusArchived {
			if err := checkID(c.ID, ident.Channel); err != nil {
				return bad("channel %q: %v", c.Key, err)
			}
			if _, ok := m.projectByID(c.ProjectID); !ok || !slices.Contains(sourceKinds, c.Kind) {
				return bad("channel %q: unknown project %s or kind %q", c.Key, c.ProjectID, c.Kind)
			}
			for _, b := range c.Bindings {
				if _, ok := m.connections[b.ConnectionID]; !ok {
					return bad("channel %q: no connection %s", c.Key, b.ConnectionID)
				}
			}
		} else {
			created, err := m.prepareCreateChannel(c)
			if err != nil || created.ID != c.ID {
				return bad("channel %q: %v", c.Key, err)
			}
		}
		m.applyPutChannel(c)
		plan.channels = append(plan.channels, copyChannel(c))
	}
	return plan, m, nil
}
