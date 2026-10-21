package jam

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// configKindOrder is the creates' kind order (spec §3: users, connections →
// destinations, model specs → kits → projects → roles → Jam context).
var configKindOrder = []string{ConfigKindUser, ConfigKindConnection, ConfigKindDestination, ConfigKindModelSpec,
	ConfigKindKit, ConfigKindProject, ConfigKindRole, ConfigKindJamContext}

// configDeleteOrder is the creates' order reversed, except the Jam context,
// which nothing references, goes last.
var configDeleteOrder = []string{ConfigKindRole, ConfigKindProject, ConfigKindKit, ConfigKindModelSpec,
	ConfigKindDestination, ConfigKindConnection, ConfigKindUser, ConfigKindJamContext}

// planDeletes plans the delete of every managed object the repo no longer
// holds, in configDeleteOrder, each behind its guard (deleteObject) and the
// drift check. A mark whose object Jam no longer has is dropped with no op.
func (p *planner) planDeletes() {
	for _, kind := range configDeleteOrder {
		for _, key := range p.deleteKeys(kind) {
			cur, exists := p.live[key]
			if !exists {
				p.w.RemoveMarks = append(p.w.RemoveMarks, key)
				continue
			}
			drift := cur.Digest() != p.marks[key].Digest
			if drift && !p.in.Force {
				p.refuseDrift(key)
				continue
			}
			if err := p.deleteObject(key); err != nil {
				p.refuse("cannot delete %s: %s", key, err)
				continue
			}
			p.op(key, ConfigDelete, drift, cur.Canon, nil)
			p.w.RemoveMarks = append(p.w.RemoveMarks, key)
		}
	}
}

// deleteKeys are kind's managed keys the repo does not hold: kits children
// before parents, the others by name.
func (p *planner) deleteKeys(kind string) []ConfigKey {
	var keys []ConfigKey
	for k := range p.marks {
		if k.Kind == kind && !p.repo[k] {
			keys = append(keys, k)
		}
	}
	depth := func(ConfigKey) int { return 0 }
	if kind == ConfigKindKit {
		depth = func(k ConfigKey) int { return p.kitDepth(k.Name) }
	}
	slices.SortFunc(keys, func(a, b ConfigKey) int {
		return cmp.Or(cmp.Compare(depth(b), depth(a)), strings.Compare(a.Name, b.Name))
	})
	return keys
}

// kitDepth is the number of live ancestors of kit name (its current
// version's base chain), stopping at a cycle or a missing parent.
func (p *planner) kitDepth(name string) int {
	seen := map[string]bool{name: true}
	n := 0
	for {
		k, ok := p.m.kits[name]
		if !ok {
			return n
		}
		parent, _, _ := strings.Cut(kitParentSpec(k.Versions[k.Current]), "@")
		if parent == "" || seen[parent] {
			return n
		}
		if _, ok := p.m.kits[parent]; !ok {
			return n
		}
		seen[parent] = true
		name = parent
		n++
	}
}

// deleteObject checks key's delete guard against the planned state and, when
// it passes, applies the delete to the planned state and the write set. The
// error is the guard's reason.
func (p *planner) deleteObject(key ConfigKey) error {
	m, name := p.m, key.Name
	switch key.Kind {
	case ConfigKindRole:
		return p.deleteRole(name)
	case ConfigKindProject:
		return p.deleteProject(name)
	case ConfigKindKit:
		if pr, r, ok := m.roleReferencingKit(name); ok {
			return fmt.Errorf("role %s/%s binds it", pr, r)
		}
		follow, pinned := KitsReferencing(p.store, name)
		if kids := slices.Sorted(slices.Values(append(follow, pinned...))); len(kids) > 0 {
			return fmt.Errorf("kit %s builds on it", kids[0])
		}
		p.w.RemoveKits = append(p.w.RemoveKits, name)
		m.applyRemoveKit(name)
	case ConfigKindModelSpec:
		if err := m.checkRemoveModelSpec(name); err != nil {
			return err
		}
		p.w.RemoveModelSpecs = append(p.w.RemoveModelSpecs, name)
		m.applyRemoveModelSpec(name)
	case ConfigKindDestination:
		p.w.RemoveDestinations = append(p.w.RemoveDestinations, name)
		m.applyRemoveDestination(name)
	case ConfigKindConnection:
		c, _ := m.liveConnectionNamed(name)
		tomb, err := m.prepareRemoveConnection(c.ID)
		if err != nil {
			return err
		}
		p.w.Connections = append(p.w.Connections, tomb)
		m.applyPutConnection(tomb)
	case ConfigKindUser:
		return p.deleteUser(name)
	case ConfigKindJamContext:
		p.w.JamContext = &sessionctx.Layer{}
		m.applySetJamContext(sessionctx.Layer{})
	default:
		return fmt.Errorf("unknown kind %s", key.Kind)
	}
	return nil
}

func (p *planner) deleteRole(name string) error {
	m := p.m
	pname, r, _ := strings.Cut(name, "/")
	proj, ok := m.projects[pname]
	if !ok {
		return fmt.Errorf("project %s not found", pname)
	}
	for _, id := range slices.Sorted(maps.Keys(m.instances)) {
		inst := m.instances[id]
		if inst.Phase != PhaseGone && m.canonicalProject(inst.Project) == string(proj.ID) && inst.Role == r {
			return fmt.Errorf("live session %s uses it", inst.ActorID)
		}
	}
	p.w.RemoveRoles = append(p.w.RemoveRoles, ConfigRoleWrite{ProjectID: proj.ID, Role: m.roles[proj.ID][r]})
	m.applyRemoveRole(proj.ID, r)
	return nil
}

// deleteProject removes the project with its members and rooms. The guard
// (checkRemoveProject) runs first, ignoring the memberships the delete itself
// ends, so a refused delete leaves the planned state untouched.
func (p *planner) deleteProject(name string) error {
	m := p.m
	proj, ok := m.projects[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrProjectNotFound, name)
	}
	members := m.members[proj.ID]
	delete(m.members, proj.ID)
	_, err := m.checkRemoveProject(name)
	if members != nil {
		m.members[proj.ID] = members
	}
	if err != nil {
		return err
	}
	for _, uid := range slices.Sorted(maps.Keys(members)) {
		p.w.RemoveMemberships = append(p.w.RemoveMemberships, Membership{ProjectID: proj.ID, UserID: uid})
		m.applyRemoveMember(proj.ID, uid)
	}
	chans := m.projectChannels(proj.ID)
	slices.SortFunc(chans, func(a, b Channel) int { return strings.Compare(string(a.ID), string(b.ID)) })
	for _, c := range chans {
		c.Status = StatusArchived
		m.applyPutChannel(c)
		p.w.Channels = append(p.w.Channels, c)
	}
	tomb := copyProject(proj)
	tomb.Status = StatusRemoved
	p.w.Projects = append(p.w.Projects, tomb)
	m.applyRemoveProject(name)
	return nil
}

// deleteUser tombstones the user unless they own a live personal session,
// are named in a live project's escalation, or are still a member of any
// live project (managed or not: RemoveUser would silently drop it).
func (p *planner) deleteUser(name string) error {
	m := p.m
	u, ok := m.liveUserNamed(name)
	if !ok {
		return fmt.Errorf("user %s not found", name)
	}
	for _, id := range slices.Sorted(maps.Keys(m.instances)) {
		inst := m.instances[id]
		owns := inst.OwnerID == u.ID || (inst.OwnerID == "" && inst.Owner != "" && inst.Owner == u.Name)
		if inst.Phase != PhaseGone && owns {
			return fmt.Errorf("they own the live personal session %s", inst.ActorID)
		}
	}
	target := "user:" + string(u.ID)
	names := func(tiers []EscalationTier) bool {
		return slices.ContainsFunc(tiers, func(t EscalationTier) bool { return slices.Contains(t.Targets, target) })
	}
	for _, pn := range slices.Sorted(maps.Keys(m.projects)) {
		proj := m.projects[pn]
		if proj.Status == StatusRemoved {
			continue
		}
		if names(proj.Escalation) || slices.ContainsFunc(slices.Collect(maps.Values(proj.EscalationByCategory)), names) {
			return fmt.Errorf("project %s's escalation names them", pn)
		}
	}
	for _, pn := range slices.Sorted(maps.Keys(m.projects)) {
		proj := m.projects[pn]
		if _, member := m.members[proj.ID][u.ID]; member && proj.Status != StatusRemoved && proj.ID != "" {
			return fmt.Errorf("project %s lists them as a member", pn)
		}
	}
	tomb, unlinked, err := m.prepareRemoveUser(u.ID)
	if err != nil {
		return err
	}
	p.w.Users = append(p.w.Users, tomb)
	p.w.Accounts = append(p.w.Accounts, unlinked...)
	m.applyPutUser(tomb)
	for _, a := range unlinked {
		m.applyPutAccount(a)
	}
	m.applyDropMemberships(u.ID)
	return nil
}

// planUnmanaged lists the live objects the repo neither holds nor manages,
// in kind order, then by name.
func (p *planner) planUnmanaged() {
	for k := range p.live {
		if _, managed := p.marks[k]; !managed && !p.repo[k] {
			p.plan.Unmanaged = append(p.plan.Unmanaged, k)
		}
	}
	slices.SortFunc(p.plan.Unmanaged, func(a, b ConfigKey) int {
		return cmp.Or(cmp.Compare(slices.Index(configKindOrder, a.Kind), slices.Index(configKindOrder, b.Kind)),
			strings.Compare(a.Name, b.Name))
	})
}
