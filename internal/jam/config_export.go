package jam

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/configtar"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
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
// export writes); nil (the planner) renders every kit. contextFiles renders
// a kit's packed context as context-dir files (renderKitFile).
func exportObjects(m *memState, inTree func(kit string) bool, contextFiles bool) (map[ConfigKey]configObject, error) {
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
		key := ConfigKey{ConfigKindKit, n}
		if k.Source != nil { // a kit following its own source is written by reference
			out[key] = configObject{Key: key, Files: map[string][]byte{sourceKitPath(n): renderKitSourceFile(*k.Source)},
				Canon: kitCanon(cfg, k.Source)}
			continue
		}
		b, ctx, inline, err := renderKitFile(m, n, cfg, inTree, contextFiles)
		if errors.Is(err, ErrConfigKitPinBehind) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("kit %s: %w", n, err)
		}
		out[key] = configObject{Key: key, Files: map[string][]byte{key.Path(): b}, Binary: ctx, inlineContext: inline, Canon: kitCanon(cfg, nil)}
	}
	return out, nil
}

// kitCanon is a kit's canonical bytes: its stored config, then, for a kit
// that tracks a source, that source (not its commit: a sync that only moves
// the commit is not a change).
func kitCanon(config string, tracked *KitSource) []byte {
	if tracked == nil {
		return []byte(config)
	}
	src := gitsource.Source{Repo: tracked.Repo, Dir: tracked.Dir, Ref: tracked.Ref}
	return []byte(config + "\n# source: " + src.String() + "\n")
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

// ErrConfigKitPinBehind is a config write of a kit whose parent pin the repo
// cannot express: behind its in-tree parent's current version (a path parent
// re-pins to current at apply), or to a parent written as
// kits/<parent>.source.yml (path parents cannot reach it). Versions are
// per-Jam.
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

type kitPinSourceError struct {
	kit, parent string
	pin         int
}

func (e *kitPinSourceError) Error() string {
	return fmt.Sprintf("kit %s pins %s@v%d, a kit from its own source (%s); versions are per-Jam — base %s on %s (unpinned) before config write",
		e.kit, e.parent, e.pin, sourceKitPath(e.parent), e.kit, e.parent)
}

func (e *kitPinSourceError) Unwrap() error { return ErrConfigKitPinBehind }

// renderKitFile renders kit name's stored config as kits/<n>/kit.yml. A
// parent in the tree is written as a path parent, which apply re-pins to the
// parent's current version — so only a pin equal to it can be; an older pin
// is ErrConfigKitPinBehind. With contextFiles, a packed base.context is rendered as files under
// kits/<n>/context/ (returned by repo path) that kit.yml names as its
// base.context-dir — loading the tree packs them back to the same bytes;
// one no context-dir reproduces (studio.ContextDirFiles) stays inline, as
// inline reports.
func renderKitFile(m *memState, name, config string, inTree func(string) bool, contextFiles bool) (yml []byte, ctx map[string]ConfigFileBlob, inline bool, err error) {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		return nil, nil, false, err
	}
	if parent, v, err := sk.Base.ParentRef(); err == nil && v > 0 && inTree != nil && inTree(parent) {
		if m.kits[parent].Source != nil {
			return nil, nil, false, &kitPinSourceError{kit: name, parent: parent, pin: v}
		}
		if cur := m.kits[parent].Current; v != cur {
			return nil, nil, false, &kitPinBehindError{kit: name, parent: parent, pin: v, cur: cur}
		}
		sk.Base.Kit = "../" + parent // a sibling in the tree: a path parent, re-pinned at apply
	}
	if contextFiles && sk.Base.Context != "" {
		if files, err := studio.ContextDirFiles(sk.Base.Context); err == nil {
			ctx = make(map[string]ConfigFileBlob, len(files))
			for _, f := range files {
				ctx[kitContextPath(name)+"/"+f.Path] = ConfigFileBlob{Data: f.Data, Mode: uint32(f.Mode)}
			}
			sk.Base.Context, sk.Base.ContextDir = "", kitContextDir
		}
	}
	return renderYAML(sk), ctx, sk.Base.Context != "", nil
}

// ConfigTreeExport is a rendered config tree.
type ConfigTreeExport struct {
	Files  map[string]string `json:"files"`            // repo path → content
	Remove []string          `json:"remove,omitempty"` // managed paths Jam no longer has
	// Binary holds files written byte for byte at a mode: a kit's rendered
	// context-dir (kits/<n>/context/…). A path is in Files or Binary, not both.
	Binary map[string]ConfigFileBlob `json:"binary,omitempty"`
	// Prune lists the kits/<n>/context dirs of the managed kits: a writer
	// removes every file under one that Binary does not hold.
	Prune []string `json:"prune,omitempty"`
	// Inline names the kits whose context stays inline in kit.yml: no
	// context-dir reproduces it, or its files would push the tree past the
	// config upload limits.
	Inline []string `json:"inline,omitempty"`
}

// ConfigFileBlob is one file of ConfigTreeExport.Binary.
type ConfigFileBlob struct {
	Data []byte `json:"data"` // base64 in JSON
	Mode uint32 `json:"mode"` // permission bits (0644 or 0755)
}

// ConfigExportOptions selects what ExportConfigTreeWith renders.
type ConfigExportOptions struct {
	All bool // every live object, not just the managed ones
	// ContextFiles renders kit contexts as kits/<n>/context/ files (Binary)
	// and lists Prune; off, a context stays inline in kit.yml, as a client
	// that knows neither field expects (GET export-tree?contexts=files opts in).
	ContextFiles bool
}

// ExportConfigTree renders the managed objects (every live one with all) and,
// when serve is non-nil, jam.yml, with kit contexts inline.
func ExportConfigTree(snap ConfigSnapshot, marks []ConfigMark, serve map[string]any, all bool) (ConfigTreeExport, error) {
	return ExportConfigTreeWith(snap, marks, serve, ConfigExportOptions{All: all})
}

// ExportConfigTreeWith is ExportConfigTree with options.
func ExportConfigTreeWith(snap ConfigSnapshot, marks []ConfigMark, serve map[string]any, opt ConfigExportOptions) (ConfigTreeExport, error) {
	all := opt.All
	m := stateFromSnapshot(snap, nil)
	managed := map[ConfigKey]bool{}
	for _, mk := range marks {
		managed[mk.Key()] = true
	}
	want := func(k ConfigKey) bool { return all || managed[k] }
	inTree := func(kit string) bool { return want(ConfigKey{ConfigKindKit, kit}) && m.kitExists(kit) }
	objs, err := exportObjects(m, inTree, opt.ContextFiles)
	if err != nil {
		return ConfigTreeExport{}, err
	}
	exp := ConfigTreeExport{Files: map[string]string{}}
	var rendered []ConfigKey // kits with context files, placed within the budget below
	for k, o := range objs {
		if !want(k) {
			continue
		}
		for p, b := range o.Files {
			exp.Files[p] = string(b)
		}
		switch {
		case k.Kind != ConfigKindKit || !opt.ContextFiles:
		case o.Binary != nil:
			rendered = append(rendered, k)
		case o.inlineContext:
			exp.Inline = append(exp.Inline, k.Name) // never pruned: its context/ files are not Jam's
		case m.kits[k.Name].Source == nil: // a tracked kit (.source.yml) has no context/ of Jam's
			exp.Prune = append(exp.Prune, kitContextPath(k.Name)) // no context: none left behind
		}
	}
	if serve != nil {
		exp.Files["jam.yml"] = string(renderYAML(serve))
	}
	budget := newTreeBudget(exp.Files)
	slices.SortFunc(rendered, func(a, b ConfigKey) int { return strings.Compare(a.Name, b.Name) })
	for _, k := range rendered {
		o := objs[k]
		if budget.take(o.Binary) {
			if exp.Binary == nil {
				exp.Binary = map[string]ConfigFileBlob{}
			}
			maps.Copy(exp.Binary, o.Binary)
			exp.Prune = append(exp.Prune, kitContextPath(k.Name))
			continue
		}
		// Over the upload limits as files: written inline instead.
		b, _, _, err := renderKitFile(m, k.Name, m.kits[k.Name].Versions[m.kits[k.Name].Current], inTree, false)
		if err != nil {
			return ConfigTreeExport{}, fmt.Errorf("kit %s: %w", k.Name, err)
		}
		budget.bytes += int64(len(b) - len(exp.Files[k.Path()]))
		exp.Files[k.Path()] = string(b)
		exp.Inline = append(exp.Inline, k.Name)
	}
	remove := map[string]bool{}
	for name, p := range m.projects {
		if want(ConfigKey{ConfigKindProject, name}) && p.Context.Empty() {
			remove["projects/"+name+"/context.md"] = true
		}
	}
	for k := range managed {
		if o, ok := objs[k]; ok {
			if k.Kind == ConfigKindKit { // the kit's other form, left from before it switched
				for _, p := range []string{k.Path(), sourceKitPath(k.Name)} {
					if _, ok := o.Files[p]; !ok {
						remove[p] = true
					}
				}
			}
			continue
		}
		remove[k.Path()] = true
		switch k.Kind {
		case ConfigKindProject:
			remove["projects/"+k.Name+"/context.md"] = true
		case ConfigKindKit:
			remove[sourceKitPath(k.Name)] = true
			if opt.ContextFiles {
				exp.Prune = append(exp.Prune, kitContextPath(k.Name))
			}
		}
	}
	for p := range remove {
		if _, ok := exp.Files[p]; !ok {
			exp.Remove = append(exp.Remove, p)
		}
	}
	slices.Sort(exp.Remove)
	slices.Sort(exp.Prune)
	slices.Sort(exp.Inline)
	return exp, nil
}

// The share of the config upload limits (configtar.MaxUnpacked bytes,
// configtar.MaxEntries files and directories) a config write leaves for the
// repo's own files (a README, CI config) beside the ones it writes.
const (
	configTreeHeadroomBytes   = 1 << 20
	configTreeHeadroomEntries = 200
)

// treeBudget tracks a tree's cost against the config upload limits: its
// file bytes and its entries (files plus every directory above them).
type treeBudget struct {
	bytes   int64
	entries int
	dirs    map[string]bool
}

func newTreeBudget(files map[string]string) *treeBudget {
	b := &treeBudget{dirs: map[string]bool{}}
	for p, body := range files {
		b.bytes += int64(len(body))
		b.entries++
		for d := path.Dir(p); d != "." && !b.dirs[d]; d = path.Dir(d) {
			b.dirs[d] = true
			b.entries++
		}
	}
	return b
}

// take adds files to the tree when they fit within the limits less the
// headroom, reporting whether they did.
func (b *treeBudget) take(files map[string]ConfigFileBlob) bool {
	var bytes int64
	entries := len(files)
	seen := map[string]bool{}
	for p, f := range files {
		bytes += int64(len(f.Data))
		for d := path.Dir(p); d != "." && !b.dirs[d] && !seen[d]; d = path.Dir(d) {
			seen[d] = true
			entries++
		}
	}
	if b.bytes+bytes > configtar.MaxUnpacked-configTreeHeadroomBytes || b.entries+entries > configtar.MaxEntries-configTreeHeadroomEntries {
		return false
	}
	b.bytes += bytes
	b.entries += entries
	maps.Copy(b.dirs, seen)
	return true
}
