package jam

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// Rooms as roster channels (intercom slice 2a): a project's roster channels
// are its live rooms. A project doc's stored Roster.Channels is empty once
// roster_schema 5 has run; the view fills it on the way out, and AddChannel /
// RemoveChannel write rooms. The view goes away with the log cutover (2b).

// rosterChannels is the roster view of a project's live rooms, sorted by
// name: Service is the bound connection's kind, Ref the binding's ref.
// Caller holds mu.
func (m *memState) rosterChannels(project ident.ID) []RosterChannel {
	if project == "" {
		return nil
	}
	var out []RosterChannel
	for _, c := range m.channels {
		if c.ProjectID != project || c.Kind != SourceRoom || c.Status != StatusLive {
			continue
		}
		rc := RosterChannel{Name: c.Key, Service: "linear"}
		if len(c.Bindings) > 0 {
			rc.Ref = c.Bindings[0].Ref
			if conn, ok := m.connections[c.Bindings[0].ConnectionID]; ok {
				rc.Service = conn.Kind
			}
		}
		out = append(out, rc)
	}
	slices.SortFunc(out, func(a, b RosterChannel) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// prepareAddRoom plans AddChannel: the project's room named rc.Name, created
// or rebound to rc.Ref on the connection of kind rc.Service (default linear;
// created when the store has none). Caller holds mu.
func (m *memState) prepareAddRoom(project string, rc RosterChannel) (humanPlan, error) {
	var plan humanPlan
	if rc.Name == "" {
		return plan, fmt.Errorf("channel name required")
	}
	p, created, err := m.requireProject(project)
	if err != nil {
		return plan, err
	}
	if created {
		plan.projects = append(plan.projects, p)
	}
	return plan, m.planRoom(&plan, p, rc, false)
}

// planRoom plans roster channel rc of project p as a room. A binding another
// channel already holds is ErrBindingTaken — or, when demote is set (the
// migration, where two projects may have shared a ref), an egress-only
// binding, noted in the report. Reads (and extends) the plan's connections
// and channels, so one plan may hold several rooms. Caller holds mu.
func (m *memState) planRoom(plan *humanPlan, p Project, rc RosterChannel, demote bool) error {
	service := cmp.Or(rc.Service, "linear")
	if !slices.Contains(ConnectionKinds, service) {
		return fmt.Errorf("channel %q: unknown service %q (want one of %v)", rc.Name, service, ConnectionKinds)
	}
	conn, ok := m.connectionOfKind(service)
	if !ok {
		i := slices.IndexFunc(plan.connections, func(c Connection) bool { return c.Kind == service && c.Status == StatusLive })
		if i >= 0 {
			conn = plan.connections[i]
		} else {
			conn = Connection{ID: ident.New(ident.Connection), Kind: service, Name: service, Status: StatusLive}
			plan.connections = append(plan.connections, conn)
		}
	}
	ch, ok := m.liveChannelByKey(p.ID, SourceRoom, rc.Name)
	if ok {
		ch = copyChannel(ch)
	} else {
		ch = Channel{ID: ident.New(ident.Channel), ProjectID: p.ID, Kind: SourceRoom, Key: rc.Name, Label: rc.Name, Status: StatusLive}
	}
	ch.Bindings = nil
	if rc.Ref != "" {
		b := Binding{ConnectionID: conn.ID, Ref: rc.Ref, Mode: BindBoth}
		holder, taken := m.ingressHolder(b)
		if i := slices.IndexFunc(plan.channels, func(c Channel) bool { return slices.Contains(c.Bindings, b) }); !taken && i >= 0 {
			holder, taken = plan.channels[i], true
		}
		if taken && holder.ID != ch.ID {
			if !demote {
				return fmt.Errorf("%w: %s %s is %s %q's", ErrBindingTaken, service, rc.Ref, holder.Kind, holder.Key)
			}
			b.Mode = BindEgress
			plan.report.Notes = append(plan.report.Notes, fmt.Sprintf(
				"project %s: channel %q shares %s %s with another channel; it now only posts there (replies go to the first)", p.Name, rc.Name, service, rc.Ref))
		}
		ch.Bindings = []Binding{b}
	}
	plan.channels = append(plan.channels, ch)
	return nil
}

// prepareRemoveRoom resolves RemoveChannel to the room it archives (none when
// the project has no such room: a no-op, as before). Caller holds mu.
func (m *memState) prepareRemoveRoom(project, name string) (Channel, bool, error) {
	p, ok := m.projects[project]
	if !ok {
		return Channel{}, false, fmt.Errorf("project %q not found", project)
	}
	ch, ok := m.liveChannelByKey(p.ID, SourceRoom, name)
	if !ok {
		return Channel{}, false, nil
	}
	ch = copyChannel(ch)
	ch.Status = StatusArchived
	return ch, true, nil
}

// planRooms is registry migration step 5: every project doc's roster
// channels become rooms, and the doc drops them. It reads the docs earlier
// steps planned. Caller holds mu.
func (m *memState) planRooms(plan *humanPlan) {
	for _, name := range slices.Sorted(mapsKeys(m.projects)) {
		i := slices.IndexFunc(plan.projects, func(p Project) bool { return p.Name == name })
		p := m.projects[name]
		if i >= 0 {
			p = plan.projects[i]
		}
		if len(p.Roster.Channels) == 0 || p.ID == "" {
			continue
		}
		for _, rc := range p.Roster.Channels {
			if err := m.planRoom(plan, p, rc, true); err != nil {
				plan.report.Notes = append(plan.report.Notes, fmt.Sprintf("project %s: roster channel %q not migrated: %v", name, rc.Name, err))
			}
		}
		p = copyProject(p)
		p.Roster.Channels = nil
		if i >= 0 {
			plan.projects[i] = p
		} else {
			plan.projects = append(plan.projects, p)
		}
	}
}
