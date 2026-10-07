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
	name := k
	for i := 2; m.checkConnectionName(name, "") != nil; i++ { // another kind's connection may hold the name
		name = fmt.Sprintf("%s-%d", k, i)
	}
	c := Connection{ID: ident.New(ident.Connection), Kind: k, Name: name, Status: StatusLive}
	plan.connections = append(plan.connections, c)
	return c
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
