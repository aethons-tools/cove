package jam

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/studio"
)

// stateFromSnapshot is a private memState holding snap's config and registry
// (users, connections, accounts, memberships, rooms, standing sessions) and
// instances: the planner's and the exporter's scratch copy of a Jam.
func stateFromSnapshot(snap ConfigSnapshot, instances []Instance) *memState {
	m := newMemState()
	applyImport(m, snap)
	for _, u := range snap.Users {
		m.applyPutUser(u)
	}
	for _, c := range snap.Connections {
		m.applyPutConnection(c)
	}
	for _, a := range snap.Accounts {
		m.applyPutAccount(a)
	}
	for _, ms := range snap.Memberships {
		m.applyPutMembership(ms)
	}
	for _, c := range snap.Channels {
		m.applyPutChannel(c)
	}
	for _, ss := range snap.StandingSessions {
		m.applyPutStandingSession(ss.ProjectID, ss.Role, ss.Name, ss.SessionID)
	}
	for _, i := range instances {
		m.applyPutInstance(i)
	}
	return m
}

// userNameOf is the name of a user id, live or not.
func (m *memState) userNameOf(id ident.ID) (string, bool) {
	u, ok := m.users[id]
	return u.Name, ok
}

// exportObjects renders every live config object of m in file form (ids →
// names; tombstones, archived rooms and empty context layers skipped). A
// kit's name@vN parent ref to a kit inTree reports renders as ../<name>.
// A non-nil inTree also limits the kits to those it reports (the ones an
// export writes); nil (the planner) renders every kit.
func exportObjects(m *memState, inTree func(kit string) bool) (map[ConfigKey]configObject, error) {
	out := map[ConfigKey]configObject{}
	add := func(f configFile) { o := newConfigObject(f); out[o.Key] = o }

	for _, d := range m.dests {
		add(DestinationFile{Kind: ConfigKindDestination, Version: ConfigFileVersion, Destination: copyDestination(d)})
	}
	for _, ms := range m.specs {
		add(modelSpecFileOf(ms))
	}
	for _, u := range m.users {
		if u.Status == StatusRemoved {
			continue
		}
		add(UserFile{Kind: ConfigKindUser, Version: ConfigFileVersion, Name: u.Name, Logins: slices.Clone(u.Logins)})
	}
	for _, c := range m.connections {
		if c.Status == StatusRemoved {
			continue
		}
		add(ConnectionFile{Kind: ConfigKindConnection, Version: ConfigFileVersion, Name: c.Name, Service: c.Kind, Cred: c.CredName})
	}
	for name, p := range m.projects {
		p.Name = name
		pf := projectFileOf(m, p)
		add(pf)
		for rn, r := range m.roles[p.ID] {
			add(roleFileOf(name, rn, r, m.userNameOf))
		}
	}
	if !m.jamContext.Empty() {
		add(JamContextFile{Layer: m.jamContext})
	}
	for _, n := range slices.Sorted(maps.Keys(m.kits)) { // sorted: the first refusal is deterministic
		if inTree != nil && !inTree(n) {
			continue // an export leaves this kit out: neither rendered nor pin-checked
		}
		k := m.kits[n]
		cfg, ok := k.Versions[k.Current]
		if !ok {
			continue
		}
		b, err := renderKitFile(m, n, cfg, inTree)
		if errors.Is(err, ErrConfigKitPinBehind) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("kit %s: %w", n, err)
		}
		key := ConfigKey{ConfigKindKit, n}
		out[key] = configObject{Key: key, Files: map[string][]byte{key.Path(): b}, Canon: []byte(cfg)}
	}
	return out, nil
}

func tierFiles(m *memState, tiers []EscalationTier) []EscalationTierFile {
	var out []EscalationTierFile
	for _, t := range tiers {
		tf := EscalationTierFile{Timeout: formatConfigDuration(t.Timeout)}
		for _, tg := range t.Targets {
			if rest, ok := strings.CutPrefix(tg, "user:"); ok {
				if id, err := ident.Parse(rest); err == nil && id.Kind() == ident.User {
					if n, ok := m.userNameOf(id); ok {
						tg = "user:" + n
					}
				}
			}
			tf.Targets = append(tf.Targets, tg)
		}
		out = append(out, tf)
	}
	return out
}

// projectFileOf renders a live project in canonical file form (ids → names).
func projectFileOf(m *memState, p Project) ProjectFile {
	connName := func(id ident.ID) string {
		if c, ok := m.connections[id]; ok {
			return c.Name
		}
		return string(id)
	}
	f := ProjectFile{
		Kind: ConfigKindProject, Version: ConfigFileVersion, Name: p.Name,
		Escalation: tierFiles(m, p.Escalation),
		Resources:  slices.Clone(p.Resources), Context: p.Context,
	}
	if p.ChatService != "" {
		f.ChatService = connName(ident.ID(p.ChatService))
	}
	if len(p.EscalationByCategory) > 0 {
		f.EscalationByCategory = map[string][]EscalationTierFile{}
		for c, tiers := range p.EscalationByCategory {
			f.EscalationByCategory[c] = tierFiles(m, tiers)
		}
	}
	for _, uid := range slices.Sorted(maps.Keys(m.members[p.ID])) {
		ms := m.members[p.ID][uid]
		n, _ := m.userNameOf(uid)
		mf := MemberFile{User: n}
		for _, d := range ms.Delivery {
			mf.Delivery = append(mf.Delivery, DeliveryFile{Service: d.Service, Address: d.Address})
		}
		f.Members = append(f.Members, mf)
	}
	for _, c := range m.ListChannels(p.ID, SourceRoom) {
		rf := RoomFile{Key: c.Key, Label: c.Label}
		for _, b := range c.Bindings {
			rf.Bindings = append(rf.Bindings, RoomBindingFile{Connection: connName(b.ConnectionID), Ref: b.Ref, Mode: string(b.Mode)})
		}
		f.Rooms = append(f.Rooms, rf)
	}
	_ = f.normalize() // durations are formatted above, so they are canonical already
	return f
}

// ErrConfigKitPinBehind is a config write of a kit pinned behind its in-tree
// parent's current version: a path parent re-pins to the current version at
// apply, and versions are per-Jam, so the repo cannot express the lag.
var ErrConfigKitPinBehind = errors.New("kit pins its parent behind the parent's current version")

type kitPinBehindError struct {
	kit, parent string
	pin, cur    int
}

func (e *kitPinBehindError) Error() string {
	return fmt.Sprintf("kit %s pins %s@v%d but %s is at v%d; versions are per-Jam — re-push %s against v%d (or pin %s to v%d) before config write",
		e.kit, e.parent, e.pin, e.parent, e.cur, e.kit, e.cur, e.parent, e.pin)
}

func (e *kitPinBehindError) Unwrap() error { return ErrConfigKitPinBehind }

// renderKitFile renders kit name's stored config as kits/<n>/kit.yml. A
// parent in the tree is written as a path parent, which apply re-pins to the
// parent's current version — so only a pin equal to it can be; an older pin
// is ErrConfigKitPinBehind.
func renderKitFile(m *memState, name, config string, inTree func(string) bool) ([]byte, error) {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		return nil, err
	}
	if parent, v, err := sk.Base.ParentRef(); err == nil && v > 0 && inTree != nil && inTree(parent) {
		if cur := m.kits[parent].Current; v != cur {
			return nil, &kitPinBehindError{kit: name, parent: parent, pin: v, cur: cur}
		}
		sk.Base.Kit = "../" + parent // a sibling in the tree: a path parent, re-pinned at apply
	}
	return renderYAML(sk), nil
}

// ConfigTreeExport is a rendered config tree.
type ConfigTreeExport struct {
	Files  map[string]string `json:"files"`            // repo path → content
	Remove []string          `json:"remove,omitempty"` // managed paths Jam no longer has
}

// ExportConfigTree renders the managed objects (every live one with all) and,
// when serve is non-nil, jam.yml.
func ExportConfigTree(snap ConfigSnapshot, marks []ConfigMark, serve map[string]any, all bool) (ConfigTreeExport, error) {
	m := stateFromSnapshot(snap, nil)
	managed := map[ConfigKey]bool{}
	for _, mk := range marks {
		managed[mk.Key()] = true
	}
	want := func(k ConfigKey) bool { return all || managed[k] }
	inTree := func(kit string) bool { return want(ConfigKey{ConfigKindKit, kit}) && m.kitExists(kit) }
	objs, err := exportObjects(m, inTree)
	if err != nil {
		return ConfigTreeExport{}, err
	}
	exp := ConfigTreeExport{Files: map[string]string{}}
	for k, o := range objs {
		if !want(k) {
			continue
		}
		for p, b := range o.Files {
			exp.Files[p] = string(b)
		}
	}
	remove := map[string]bool{}
	for name, p := range m.projects {
		if want(ConfigKey{ConfigKindProject, name}) && p.Context.Empty() {
			remove["projects/"+name+"/context.md"] = true
		}
	}
	for k := range managed {
		if _, ok := objs[k]; ok {
			continue
		}
		remove[k.Path()] = true
		if k.Kind == ConfigKindProject {
			remove["projects/"+k.Name+"/context.md"] = true
		}
	}
	for p := range remove {
		if _, ok := exp.Files[p]; !ok {
			exp.Remove = append(exp.Remove, p)
		}
	}
	slices.Sort(exp.Remove)
	if serve != nil {
		exp.Files["jam.yml"] = string(renderYAML(serve))
	}
	return exp, nil
}
