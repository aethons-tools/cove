package jam

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// memState is the in-memory representation of the control plane, shared by
// MemStore and PostgresStore. It owns the maps, all read methods (RLock-guarded
// and promoted to the embedding store), lock-free read/validation helpers, and
// pure apply* mutators. Persistence is the embedding store's job:
//
//   - MemStore does: Lock; validate; compute; apply (nothing persisted).
//   - PostgresStore does: Lock; validate; compute; SQL; apply (commit then cache).
//
// The apply* mutators and the compute helpers never lock and never persist; the
// caller holds mu.Lock(). Read methods take mu.RLock(). sync.RWMutex is not
// re-entrant, so a mutator that needs to read uses a lock-free helper (e.g.
// kitExists, actorByID), never a public read method.
type memState struct {
	mu        sync.RWMutex
	roles     map[string]map[string]Role
	actors    map[string]Actor       // keyed by TokenHash
	dests     map[string]Destination // keyed by Name
	kits      map[string]Kit         // keyed by Name
	instances map[string]Instance    // keyed by ActorID
	projects  map[string]Project     // keyed by Name
	// unread is the per-(participant, channel) intercom-UI unread cursor:
	// participant → channel id → last-seen append Seq. Monotonic forward-only
	// (applyCommitUnread). Free-form keys — no backing entity is required.
	unread map[string]map[string]int64
}

func newMemState() *memState {
	return &memState{
		roles:     map[string]map[string]Role{},
		actors:    map[string]Actor{},
		dests:     map[string]Destination{},
		kits:      map[string]Kit{},
		instances: map[string]Instance{},
		projects:  map[string]Project{},
		unread:    map[string]map[string]int64{},
	}
}

// ---- reads (RLock; promoted to the embedding Store) ----

func (m *memState) Lookup(tokenHash string) (Actor, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.actors[tokenHash]
	return a, ok
}

func (m *memState) ListActors() []Actor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Actor, 0, len(m.actors))
	for _, a := range m.actors {
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b Actor) int { return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.TokenHash, b.TokenHash)) })
	return out
}

func (m *memState) GetRole(project, name string) (Role, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if project == "" {
		project = DefaultProject
	}
	r, ok := m.roles[project][name]
	return r, ok
}

func (m *memState) ListRoles(project string) []Role {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if project == "" {
		project = DefaultProject
	}
	out := make([]Role, 0, len(m.roles[project]))
	for _, r := range m.roles[project] {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Role) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// ListProjects lists the project records, sorted. Roles and grants can only
// name a recorded project, so this is every project in use.
func (m *memState) ListProjects() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.projects))
	for p := range m.projects {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (m *memState) GetKit(name string) (Kit, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.kits[name]
	if !ok {
		return Kit{}, false
	}
	return copyKit(k), true
}

func (m *memState) KitConfig(name string, version int) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.kits[name]
	if !ok {
		return "", false
	}
	if version == 0 {
		version = k.Current
	}
	cfg, ok := k.Versions[version]
	return cfg, ok
}

func (m *memState) ListKits() []Kit {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Kit, 0, len(m.kits))
	for _, k := range m.kits {
		out = append(out, copyKit(k))
	}
	slices.SortFunc(out, func(a, b Kit) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (m *memState) RoleReferencingKit(name string) (string, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.roleReferencingKit(name)
}

func (m *memState) GetInstance(actorID string) (Instance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i, ok := m.instances[actorID]
	return i, ok // Instance has no reference fields; value copy is a full copy
}

func (m *memState) ListInstances() []Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Instance, 0, len(m.instances))
	for _, i := range m.instances {
		out = append(out, i)
	}
	slices.SortFunc(out, func(a, b Instance) int { return cmp.Compare(a.ActorID, b.ActorID) })
	return out
}

func (m *memState) ListDestinations() []Destination {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Destination, 0, len(m.dests))
	for _, d := range m.dests {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Destination) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (m *memState) Match(reqPath string) (Destination, bool) {
	return Config{Destinations: m.ListDestinations()}.Match(reqPath)
}

func (m *memState) GetProject(name string) (Project, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.projects[name]
	if !ok {
		return Project{}, false
	}
	return copyProject(p), true
}

func (m *memState) GetRoster(project string) (Roster, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.projects[project]
	if !ok {
		return Roster{}, false
	}
	return Roster{
		Humans:   copyHumans(p.Roster.Humans),
		Channels: append([]Channel(nil), p.Roster.Channels...),
	}, true
}

// UnreadCursor returns the participant's last-seen Seq on channel, and whether
// a cursor has been committed for that (participant, channel) pair.
func (m *memState) UnreadCursor(participant, channel string) (int64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seq, ok := m.unread[participant][channel]
	return seq, ok
}

// UnreadCursors returns a copy of all of the participant's channel cursors
// (channel id → last-seen Seq), the map ProjectChannels consumes. Never nil.
func (m *memState) UnreadCursors(participant string) map[string]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]int64, len(m.unread[participant]))
	for ch, seq := range m.unread[participant] {
		out[ch] = seq
	}
	return out
}

// ---- lock-free read/validation helpers (caller holds the lock) ----

// actorByID finds the actor with the given id and returns its map key
// (TokenHash), a copy of the record, and whether it was found.
func (m *memState) actorByID(id string) (tokenHash string, a Actor, ok bool) {
	for h, rec := range m.actors {
		if rec.ID == id {
			return h, rec, true
		}
	}
	return "", Actor{}, false
}

func (m *memState) actorIDExists(id string) bool {
	_, _, ok := m.actorByID(id)
	return ok
}

func (m *memState) kitExists(name string) bool {
	_, ok := m.kits[name]
	return ok
}

func (m *memState) roleReferencingKit(name string) (string, string, bool) {
	for project, roles := range m.roles {
		for _, r := range roles {
			if r.Kit == name {
				return project, r.Name, true
			}
		}
	}
	return "", "", false
}

// requireProject resolves the project a write targets ("" means
// DefaultProject): the stored record (no copy), or — for DefaultProject only — a
// fresh record the caller must persist along with its write (created reports
// that). Any other unknown name is ErrProjectNotFound.
func (m *memState) requireProject(name string) (p Project, created bool, err error) {
	if name == "" {
		name = DefaultProject
	}
	if p, ok := m.projects[name]; ok {
		return p, false, nil
	}
	if name == DefaultProject {
		return Project{Name: name}, true, nil
	}
	return Project{}, false, fmt.Errorf("%w: %q (create it with `at-jam project create %s`)", ErrProjectNotFound, name, name)
}

// grantProjects resolves every project a's grants name, returning the records
// the caller must persist first (at most the materialized DefaultProject).
func (m *memState) grantProjects(a Actor) ([]Project, error) {
	var created []Project
	for _, g := range a.Grants {
		p, isNew, err := m.requireProject(g.Project)
		if err != nil {
			return nil, err
		}
		if isNew && len(created) == 0 {
			created = append(created, p)
		}
	}
	return created, nil
}

// projectReference names a role or grant that still references project, for
// RemoveProject's in-use refusal.
func (m *memState) projectReference(project string) (string, bool) {
	for name := range m.roles[project] {
		return fmt.Sprintf("role %s/%s", project, name), true
	}
	for _, a := range m.actors {
		for _, g := range a.Grants {
			if orDefaultProject(g.Project) == project {
				return fmt.Sprintf("actor %q's grant of %s/%s", a.ID, project, g.Role), true
			}
		}
	}
	return "", false
}

// checkCreateProject validates a CreateProject. Caller holds the lock.
func (m *memState) checkCreateProject(name string) error {
	if name == "" {
		return fmt.Errorf("project name is required")
	}
	if _, ok := m.projects[name]; ok {
		return fmt.Errorf("%w: %q", ErrProjectExists, name)
	}
	return nil
}

// checkRemoveProject validates a RemoveProject. Caller holds the lock.
func (m *memState) checkRemoveProject(name string) error {
	if _, ok := m.projects[name]; !ok {
		return fmt.Errorf("%w: %q", ErrProjectNotFound, name)
	}
	if ref, ok := m.projectReference(name); ok {
		return fmt.Errorf("%w: %q is referenced by %s", ErrProjectInUse, name, ref)
	}
	return nil
}

// backfillProjects gives every project named by a role or grant a record, so a
// store written before projects were first-class loads with no dangling
// references. Construction time only (no lock).
func (m *memState) backfillProjects() {
	add := func(name string) {
		if name == "" {
			name = DefaultProject
		}
		if _, ok := m.projects[name]; !ok {
			m.projects[name] = Project{Name: name}
		}
	}
	for p := range m.roles {
		add(p)
	}
	for _, a := range m.actors {
		for _, g := range a.Grants {
			add(g.Project)
		}
	}
}

// ---- pure apply* mutators (caller holds mu.Lock(); no persistence) ----

func (m *memState) applyPutActor(a Actor) { m.actors[a.TokenHash] = a }

func (m *memState) applyRemoveActorByID(id string) bool {
	if h, _, ok := m.actorByID(id); ok {
		delete(m.actors, h)
		return true
	}
	return false
}

func (m *memState) applyPutRole(project string, r Role) {
	if project == "" {
		project = DefaultProject
	}
	if m.roles[project] == nil {
		m.roles[project] = map[string]Role{}
	}
	m.roles[project][r.Name] = r
}

func (m *memState) applyRemoveRole(project, name string) bool {
	if project == "" {
		project = DefaultProject
	}
	if _, ok := m.roles[project][name]; !ok {
		return false
	}
	delete(m.roles[project], name)
	return true
}

// nextKitVersion returns the next version number for a kit (max existing + 1).
func nextKitVersion(k Kit) int {
	next := 0
	for v := range k.Versions {
		if v > next {
			next = v
		}
	}
	return next + 1
}

// applyPushKit appends config as a new version (max existing + 1), advances
// Current, and returns the new version number.
func (m *memState) applyPushKit(name, config string) int {
	k, ok := m.kits[name]
	if !ok {
		k = Kit{Name: name, Versions: map[int]string{}}
	}
	next := nextKitVersion(k)
	k.Versions[next] = config
	k.Current = next
	m.kits[name] = k
	return next
}

func (m *memState) applyPinKit(name string, version int) {
	k := m.kits[name]
	k.Current = version
	m.kits[name] = k
}

func (m *memState) applyRemoveKit(name string) bool {
	if _, ok := m.kits[name]; !ok {
		return false
	}
	delete(m.kits, name)
	return true
}

func (m *memState) applyPutInstance(i Instance) { m.instances[i.ActorID] = i }

func (m *memState) applyRemoveInstance(actorID string) bool {
	if _, ok := m.instances[actorID]; !ok {
		return false
	}
	delete(m.instances, actorID)
	return true
}

// applyAdvanceCommitCursor moves the instance's CommitSeq (the ordering key)
// and CommitCursor (the id echo, kept in lockstep) to upToSeq/upToID iff
// upToSeq is forward of the current CommitSeq; returns the (possibly
// unchanged) instance and whether the actor exists. Caller holds the write
// lock.
func (m *memState) applyAdvanceCommitCursor(actorID, upToID string, upToSeq int64) (Instance, bool) {
	i, ok := m.instances[actorID]
	if !ok {
		return Instance{}, false
	}
	if upToSeq > i.CommitSeq {
		i.CommitSeq = upToSeq
		i.CommitCursor = upToID
		m.instances[actorID] = i
	}
	return i, true
}

// applyCommitUnread moves the (participant, channel) unread cursor forward to
// seq iff seq is beyond the current value; returns the (possibly unchanged)
// cursor and whether it moved. Forward-only (a backward/equal seq is a no-op).
// Caller holds the write lock.
func (m *memState) applyCommitUnread(participant, channel string, seq int64) (int64, bool) {
	cur := m.unread[participant][channel]
	if seq <= cur {
		return cur, false
	}
	if m.unread[participant] == nil {
		m.unread[participant] = map[string]int64{}
	}
	m.unread[participant][channel] = seq
	return seq, true
}

func (m *memState) applyPutDestination(d Destination) { m.dests[d.Name] = d }

func (m *memState) applyRemoveDestination(name string) bool {
	if _, ok := m.dests[name]; !ok {
		return false
	}
	delete(m.dests, name)
	return true
}

func (m *memState) applyPutProject(p Project) { m.projects[p.Name] = p }

func (m *memState) applyRemoveProject(name string) { delete(m.projects, name) }

// ---- pure compute helpers for aggregate (doc) edits ----

// upsertGrant returns a with g upserted by (project, role); project defaults.
func upsertGrant(a Actor, g Grant) Actor {
	if g.Project == "" {
		g.Project = DefaultProject
	}
	for i := range a.Grants {
		if a.Grants[i].Project == g.Project && a.Grants[i].Role == g.Role {
			a.Grants[i] = g
			return a
		}
	}
	a.Grants = append(a.Grants, g)
	return a
}

// removeGrantFrom returns a with the (project, role) grant removed, and whether
// it was present; project defaults.
func removeGrantFrom(a Actor, project, role string) (Actor, bool) {
	if project == "" {
		project = DefaultProject
	}
	kept := a.Grants[:0]
	found := false
	for _, g := range a.Grants {
		if g.Project == project && g.Role == role {
			found = true
			continue
		}
		kept = append(kept, g)
	}
	a.Grants = kept
	return a, found
}

func upsertHuman(p Project, h Human) Project {
	for i := range p.Roster.Humans {
		if p.Roster.Humans[i].Name == h.Name {
			p.Roster.Humans[i] = h
			return p
		}
	}
	p.Roster.Humans = append(p.Roster.Humans, h)
	return p
}

func upsertChannel(p Project, c Channel) Project {
	if c.Service == "" {
		c.Service = "linear"
	}
	for i := range p.Roster.Channels {
		if p.Roster.Channels[i].Name == c.Name {
			p.Roster.Channels[i] = c
			return p
		}
	}
	p.Roster.Channels = append(p.Roster.Channels, c)
	return p
}

func removeHumanFrom(p Project, name string) Project {
	out := p.Roster.Humans[:0]
	for _, h := range p.Roster.Humans {
		if h.Name != name {
			out = append(out, h)
		}
	}
	p.Roster.Humans = out
	return p
}

func removeChannelFrom(p Project, name string) Project {
	out := p.Roster.Channels[:0]
	for _, c := range p.Roster.Channels {
		if c.Name != name {
			out = append(out, c)
		}
	}
	p.Roster.Channels = out
	return p
}

func setEscalation(p Project, category string, tiers []EscalationTier) Project {
	if category == "" {
		p.Escalation = tiers
		return p
	}
	if p.EscalationByCategory == nil {
		p.EscalationByCategory = map[string][]EscalationTier{}
	}
	p.EscalationByCategory[category] = tiers
	return p
}

// setChatService returns p with its chat service set (or cleared when "").
func setChatService(p Project, service string) Project {
	p.ChatService = service
	return p
}

// setProjectContext returns p with its authored context and resources replaced.
func setProjectContext(p Project, l sessionctx.Layer, rs []sessionctx.Resource) Project {
	p.Context = sessionctx.Layer{Core: l.Core, Leaves: slices.Clone(l.Leaves)}
	p.Resources = slices.Clone(rs)
	return p
}

// ---- copy helpers (defensive copies for reads) ----

func copyKit(k Kit) Kit {
	vs := make(map[int]string, len(k.Versions))
	for v, c := range k.Versions {
		vs[v] = c
	}
	return Kit{Name: k.Name, Current: k.Current, Versions: vs}
}

// copyHumans returns a deep copy of hs: the slice plus each Human's Delivery
// and Identity sub-slices, so a returned Human's sub-slices can't alias the
// store's state.
func copyHumans(hs []Human) []Human {
	out := append([]Human(nil), hs...)
	for i := range out {
		out[i].Delivery = append([]DeliveryProfile(nil), out[i].Delivery...)
		out[i].Identity = append([]OIDCIdentity(nil), out[i].Identity...)
	}
	return out
}

func copyProject(p Project) Project {
	p.Context.Leaves = slices.Clone(p.Context.Leaves)
	p.Resources = slices.Clone(p.Resources)
	p.Roster.Humans = copyHumans(p.Roster.Humans)
	p.Roster.Channels = append([]Channel(nil), p.Roster.Channels...)
	p.Escalation = append([]EscalationTier(nil), p.Escalation...)
	for i := range p.Escalation {
		p.Escalation[i].Targets = append([]string(nil), p.Escalation[i].Targets...)
	}
	if p.EscalationByCategory != nil {
		mm := make(map[string][]EscalationTier, len(p.EscalationByCategory))
		for cat, tiers := range p.EscalationByCategory {
			cp := append([]EscalationTier(nil), tiers...)
			for i := range cp {
				cp[i].Targets = append([]string(nil), cp[i].Targets...)
			}
			mm[cat] = cp
		}
		p.EscalationByCategory = mm
	}
	return p
}

// grantNotFoundErr / actorNotFoundErr keep the MemStore/PostgresStore error
// wording identical (the conformance suite checks that mutators error, not the
// exact text, but keeping one source avoids drift).
func actorNotFoundErr(id string) error { return fmt.Errorf("actor %q not found", id) }
