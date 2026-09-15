package harbor

import (
	"fmt"
	"sort"
	"sync"
)

// memState is the in-memory representation of the control plane, shared by
// FileStore and PostgresStore. It owns the maps, all read methods (RLock-guarded
// and promoted to the embedding store), lock-free read/validation helpers, and
// pure apply* mutators. Persistence is the embedding store's job:
//
//   - FileStore does: Lock; validate; compute; apply; save() (mutate then rewrite).
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
}

func newMemState() *memState {
	return &memState{
		roles:     map[string]map[string]Role{},
		actors:    map[string]Actor{},
		dests:     map[string]Destination{},
		kits:      map[string]Kit{},
		instances: map[string]Instance{},
		projects:  map[string]Project{},
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
	return out
}

func (m *memState) ListProjects() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := map[string]struct{}{}
	for p := range m.roles {
		set[p] = struct{}{}
	}
	for _, a := range m.actors {
		for _, g := range a.Grants {
			set[g.Project] = struct{}{}
		}
	}
	for p := range m.projects {
		set[p] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for p := range set {
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
	return out
}

func (m *memState) ListDestinations() []Destination {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Destination, 0, len(m.dests))
	for _, d := range m.dests {
		out = append(out, d)
	}
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

// rawProject returns the stored Project (no copy) with Name defaulted, for a
// mutator that is about to modify and re-store it.
func (m *memState) rawProject(name string) Project {
	p := m.projects[name]
	p.Name = name
	return p
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

func (m *memState) applyPutDestination(d Destination) { m.dests[d.Name] = d }

func (m *memState) applyRemoveDestination(name string) bool {
	if _, ok := m.dests[name]; !ok {
		return false
	}
	delete(m.dests, name)
	return true
}

func (m *memState) applyPutProject(p Project) { m.projects[p.Name] = p }

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

// ---- copy helpers (defensive copies for reads) ----

func copyKit(k Kit) Kit {
	vs := make(map[int]string, len(k.Versions))
	for v, c := range k.Versions {
		vs[v] = c
	}
	return Kit{Name: k.Name, Current: k.Current, Versions: vs}
}

// copyHumans returns a deep copy of hs: the slice plus each Human's Delivery
// sub-slice, so a returned Human's Delivery can't alias the store's state.
func copyHumans(hs []Human) []Human {
	out := append([]Human(nil), hs...)
	for i := range out {
		out[i].Delivery = append([]DeliveryProfile(nil), out[i].Delivery...)
	}
	return out
}

func copyProject(p Project) Project {
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

// grantNotFoundErr / actorNotFoundErr keep the FileStore/PostgresStore error
// wording identical (the conformance suite checks that mutators error, not the
// exact text, but keeping one source avoids drift).
func actorNotFoundErr(id string) error { return fmt.Errorf("actor %q not found", id) }
