package jam

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// Rooms as roster channels (intercom slice 2a): a project's roster channels
// are its live rooms. A project doc's stored Roster.Channels is empty once
// roster_schema 5 has run; the view fills it on the way out, and AddChannel /
// RemoveChannel write rooms. The view goes away with the log cutover (2b).

// rosterChannels is the roster view of a project's live rooms: Service is the
// bound connection's kind, Ref the binding's ref. A room that only posts to
// its ref (the migration demoted it: another channel receives that ref's
// replies) comes after the rest, so a consumer matching the first channel by
// ref finds the one replies belong to; otherwise sorted by name. Caller
// holds mu.
func (m *memState) rosterChannels(project ident.ID) []RosterChannel {
	if project == "" {
		return nil
	}
	type row struct {
		rc      RosterChannel
		demoted bool
	}
	var rows []row
	for _, c := range m.channels {
		if c.ProjectID != project || c.Kind != SourceRoom || c.Status != StatusLive {
			continue
		}
		r := row{rc: RosterChannel{Name: c.Key, Service: "linear"}}
		if len(c.Bindings) > 0 {
			b := c.Bindings[0]
			r.rc.Ref, r.demoted = b.Ref, b.Mode != BindBoth
			if conn, ok := m.connections[b.ConnectionID]; ok {
				r.rc.Service = conn.Kind
			}
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.demoted != b.demoted {
			if a.demoted {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.rc.Name, b.rc.Name)
	})
	out := make([]RosterChannel, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.rc)
	}
	if len(out) == 0 {
		return nil
	}
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
	if rc.Ref == "" {
		return plan, fmt.Errorf("channel %q: ref required", rc.Name)
	}
	if created {
		plan.projects = append(plan.projects, p)
	}
	return plan, m.planRoom(&plan, p, rc, false)
}

// planRoom plans roster channel rc of project p as a room (an upsert by
// name, against the store and the rooms the plan already holds). A binding
// another channel already holds is ErrBindingTaken — unless the room already
// binds it egress-only (kept as is), or demote is set (the migration, where
// two channels may have shared a ref): then the binding is egress-only, noted
// in the report. Reads (and extends) the plan's connections and channels, so
// one plan may hold several rooms. Caller holds mu.
func (m *memState) planRoom(plan *humanPlan, p Project, rc RosterChannel, demote bool) error {
	service := strings.ToLower(strings.TrimSpace(cmp.Or(rc.Service, "linear")))
	if !slices.Contains(ConnectionKinds, service) {
		return fmt.Errorf("channel %q: unknown service %q (want one of %v)", rc.Name, rc.Service, ConnectionKinds)
	}
	planned := slices.IndexFunc(plan.channels, func(c Channel) bool {
		return c.ProjectID == p.ID && c.Kind == SourceRoom && c.Key == rc.Name
	})
	var ch Channel
	if planned >= 0 {
		ch = plan.channels[planned]
	} else if live, ok := m.liveChannelByKey(p.ID, SourceRoom, rc.Name); ok {
		ch = copyChannel(live)
	} else {
		ch = Channel{ID: ident.New(ident.Channel), ProjectID: p.ID, Kind: SourceRoom, Key: rc.Name, Label: rc.Name, Status: StatusLive}
	}
	was := ch.Bindings
	ch.Bindings = nil
	if rc.Ref != "" {
		conn := m.planConnectionOfKind(plan, service)
		b := Binding{ConnectionID: conn.ID, Ref: rc.Ref, Mode: BindBoth}
		holder, taken := m.ingressHolder(b)
		if i := slices.IndexFunc(plan.channels, func(c Channel) bool { return slices.Contains(c.Bindings, b) }); i >= 0 {
			holder, taken = plan.channels[i], true
		}
		if taken && holder.ID != ch.ID {
			switch {
			case slices.Contains(was, Binding{ConnectionID: conn.ID, Ref: rc.Ref, Mode: BindEgress}):
				b.Mode = BindEgress // already demoted: re-saving it unchanged keeps it so
			case demote:
				b.Mode = BindEgress
				plan.report.Notes = append(plan.report.Notes, fmt.Sprintf(
					"project %s: channel %q shares %s %s with channel %q; it now only posts there (replies go to %q)", p.Name, rc.Name, service, rc.Ref, holder.Key, holder.Key))
			default:
				return fmt.Errorf("%w: %s %s is %s %q's", ErrBindingTaken, service, rc.Ref, holder.Kind, holder.Key)
			}
		}
		ch.Bindings = []Binding{b}
	}
	if planned >= 0 {
		plan.channels[planned] = ch
	} else {
		plan.channels = append(plan.channels, ch)
	}
	return nil
}

// planConnectionOfKind is the connection of kind k: the store's, else one
// the plan already creates, else a new one the plan creates. Caller holds mu.
func (m *memState) planConnectionOfKind(plan *humanPlan, k string) Connection {
	if c, ok := m.connectionOfKind(k); ok {
		return c
	}
	if i := slices.IndexFunc(plan.connections, func(c Connection) bool { return c.Kind == k && c.Status == StatusLive }); i >= 0 {
		return plan.connections[i]
	}
	c := Connection{ID: ident.New(ident.Connection), Kind: k, Name: k, Status: StatusLive}
	plan.connections = append(plan.connections, c)
	return c
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
// channels become rooms, and the doc drops them — except a channel it can't
// place (an unknown service), which stays in the doc, noted, rather than be
// lost. It reads the docs earlier steps planned. Caller holds mu.
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
		var kept []RosterChannel
		for _, rc := range p.Roster.Channels {
			if err := m.planRoom(plan, p, rc, true); err != nil {
				kept = append(kept, rc)
				plan.report.Notes = append(plan.report.Notes, fmt.Sprintf("project %s: roster channel %q not migrated (kept in the project doc): %v", name, rc.Name, err))
			}
		}
		p = copyProject(p)
		p.Roster.Channels = kept
		if i >= 0 {
			plan.projects[i] = p
		} else {
			plan.projects = append(plan.projects, p)
		}
	}
}
