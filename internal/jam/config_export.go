package jam

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/kittree"
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
// export writes); nil (the planner) renders every kit. A kit whose current
// version has no stored tree (broken) is an object with canon "# broken:
// <reason>" and no files: one bad kit never fails the export, plan or drift
// check.
func exportObjects(m *memState, inTree func(kit string) bool) (out map[ConfigKey]configObject, err error) {
	var at ConfigKey // the object being rendered, for a render that fails
	defer recoverRender(func() string { return at.String() }, &err)
	out = map[ConfigKey]configObject{}
	add := func(f configFile) {
		at = f.configKey()
		o := newConfigObject(f)
		out[o.Key] = o
	}

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
		v, ok := LoadKitVersion(m, n, 0)
		if !ok {
			continue
		}
		key := ConfigKey{ConfigKindKit, n}
		at = key
		if v.TreeHash == "" || v.Tree.Entries == nil {
			out[key] = configObject{Key: key, Canon: brokenKitCanon(brokenReason(k, v))}
			continue
		}
		blobs := kittree.MapBlobs(m.kitBlobs)
		if k.Source != nil { // a kit following its own source is written by reference
			out[key] = configObject{Key: key, Files: map[string][]byte{sourceKitPath(n): renderKitSourceFile(*k.Source)}, Canon: kitCanon(v.Tree, blobs, k.Source)}
			continue
		}
		files, err := renderKitDir(m, n, v, inTree)
		if errors.Is(err, ErrConfigKitPinBehind) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("kit %s: %w", n, err)
		}
		out[key] = configObject{Key: key, KitFiles: files, Canon: kitCanon(v.Tree, blobs, nil)}
	}
	return out, nil
}

// recoverRender, deferred, turns a render panic into *err naming what was
// rendered (what()), never its content, so a value the renderer cannot
// write fails the export, plan or drift check instead of panicking a serve
// goroutine or handler. The log has the panic's type and stack, not its
// value (which may quote a config value).
func recoverRender(what func() string, err *error) {
	r := recover()
	if r == nil {
		return
	}
	slog.Default().Error("config: render panicked", "object", what(), "panic_type", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
	*err = fmt.Errorf("%s cannot be rendered as a config file", what())
}

// brokenReason is why kit k's current version v has no tree to export, as
// fixed text: the migration's recorded reason, or that the tree is not stored.
func brokenReason(k Kit, v KitVersion) string {
	if r, ok := k.Broken[v.Version]; ok {
		return r
	}
	if v.TreeHash != "" {
		return "its tree is not stored"
	}
	return "it is not stored as a tree yet"
}

// brokenKitCanon is the canonical form of a kit whose current version has no
// tree (brokenReason): fixed text, no files.
func brokenKitCanon(reason string) []byte { return []byte("# broken: " + reason + "\n") }

// kitCanon is a kit's canonical form for plans and marks: its kit.yml, then
// "# files:" and one line per entry of its tree (mode, size, sha256, path),
// then, for a kit that tracks a source, that source (not its commit: a sync
// that only moves the commit is not a change). The listing identifies the
// tree exactly as its hash does, and a plan's line diff of two is readable.
func kitCanon(t kittree.Tree, blobs kittree.Blobs, tracked *KitSource) []byte {
	var b bytes.Buffer
	if e, ok := t.Lookup(kittree.KitFile); ok {
		yml, _ := blobs(e.SHA256)
		b.Write(yml)
		if len(yml) > 0 && yml[len(yml)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	b.WriteString("# files:\n")
	for _, e := range t.Entries {
		fmt.Fprintf(&b, "#   %04o %d %s %s\n", e.Mode, e.Size, e.SHA256, e.Path)
	}
	if tracked != nil {
		src := gitsource.Source{Repo: tracked.Repo, Dir: tracked.Dir, Ref: tracked.Ref}
		b.WriteString("# source: " + src.String() + "\n")
	}
	return b.Bytes()
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

// renderKitDir is kit name's current version v as its kits/<n>/ files: every
// file as stored (its bytes shared with m, never written to), kit.yml through
// treeKitYML. A blob that is missing (or not
// its entry's size) fails the render: a Jam that lost a kit's files.
func renderKitDir(m *memState, name string, v KitVersion, inTree func(string) bool) ([]ConfigKitFile, error) {
	out := make([]ConfigKitFile, 0, len(v.Tree.Entries))
	for _, e := range v.Tree.Entries {
		data, ok := m.kitBlobs[e.SHA256]
		if !ok || int64(len(data)) != e.Size {
			return nil, fmt.Errorf("%s has no stored content", e.Path)
		}
		if e.Path == kittree.KitFile {
			var err error
			if data, err = treeKitYML(m, name, data, inTree); err != nil {
				return nil, err
			}
		}
		out = append(out, ConfigKitFile{Path: e.Path, Data: data, Mode: uint32(e.Mode)}) // shared: blobs are immutable
	}
	return out, nil
}

// treeKitYML is kit.yml as a config tree holds it. A name@vN parent in the
// tree becomes the path parent ../<name>, which apply re-pins to the
// parent's current version, so only a pin equal to it can be written: an
// older pin is ErrConfigKitPinBehind, a pin of a parent written by reference
// too. Only the base.kit value changes (studio.SetBaseKit). A kit.yml that
// does not parse is written as it is.
func treeKitYML(m *memState, name string, yml []byte, inTree func(string) bool) ([]byte, error) {
	sk, err := studio.ParseStudioKit(yml)
	if err != nil {
		return yml, nil
	}
	parent, v, err := sk.Base.ParentRef()
	if err != nil || v == 0 || inTree == nil || !inTree(parent) {
		return yml, nil
	}
	if m.kits[parent].Source != nil {
		return nil, &kitPinSourceError{kit: name, parent: parent, pin: v}
	}
	if cur := m.kits[parent].Current; v != cur {
		return nil, &kitPinBehindError{kit: name, parent: parent, pin: v, cur: cur}
	}
	return studio.SetBaseKit(yml, "../"+parent)
}

// ConfigKitFile is one file of a kit directory a config write writes. An
// export's Data shares the stored (immutable) blob bytes with the state it
// was rendered from: read it, never modify it.
type ConfigKitFile struct {
	Path string `json:"path"` // relative to the kit directory
	Data []byte `json:"data"` // base64 in JSON
	Mode uint32 `json:"mode"` // 0644 or 0755
}

// ConfigTreeExport is a rendered config tree.
type ConfigTreeExport struct {
	Files  map[string]string `json:"files"`            // repo path → content
	Remove []string          `json:"remove,omitempty"` // managed paths Jam no longer has
	// Kits holds each kit directory the write owns, by its repo dir
	// (kits/<n>): its files at their modes, kit.yml included. A writer
	// mirrors the dir: it writes the files and removes every other file there
	// that the dir's .dockerignore (the exported one, else the dir's own)
	// does not exclude. An empty list mirrors the dir to nothing: a kit now
	// written by reference, or a managed kit gone from Jam.
	Kits map[string][]ConfigKitFile `json:"kits,omitempty"`
}

// kitDir is the repo dir of kit name.
func kitDir(name string) string { return "kits/" + name }

// ExportConfigTree renders the managed objects (every live one with all) and,
// when serve is non-nil, jam.yml. A kit whose current version is broken is
// left as the repo has it: neither written nor mirrored.
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
	exp := ConfigTreeExport{Files: map[string]string{}, Kits: map[string][]ConfigKitFile{}}
	remove := map[string]bool{}
	for k, o := range objs {
		if !want(k) {
			continue
		}
		for p, b := range o.Files {
			exp.Files[p] = string(b)
		}
		if k.Kind != ConfigKindKit {
			continue
		}
		switch _, bySource := o.Files[sourceKitPath(k.Name)]; {
		case o.KitFiles != nil:
			exp.Kits[kitDir(k.Name)] = o.KitFiles
			remove[sourceKitPath(k.Name)] = true
		case bySource:
			exp.Kits[kitDir(k.Name)] = []ConfigKitFile{} // written by reference: its directory goes
		}
	}
	if serve != nil {
		if exp.Files["jam.yml"], err = renderServeYAML(serve); err != nil {
			return ConfigTreeExport{}, err
		}
	}
	for name, p := range m.projects {
		if want(ConfigKey{ConfigKindProject, name}) && p.Context.Empty() {
			remove["projects/"+name+"/context.md"] = true
		}
	}
	for k := range managed {
		if _, ok := objs[k]; ok {
			continue
		}
		switch k.Kind {
		case ConfigKindKit:
			remove[sourceKitPath(k.Name)] = true
			exp.Kits[kitDir(k.Name)] = []ConfigKitFile{}
		case ConfigKindProject:
			remove[k.Path()] = true
			remove["projects/"+k.Name+"/context.md"] = true
		default:
			remove[k.Path()] = true
		}
	}
	for p := range remove {
		if _, ok := exp.Files[p]; !ok {
			exp.Remove = append(exp.Remove, p)
		}
	}
	slices.Sort(exp.Remove)
	if len(exp.Kits) == 0 {
		exp.Kits = nil
	}
	return exp, nil
}

// renderServeYAML is jam.yml, a render failure an error.
func renderServeYAML(serve map[string]any) (_ string, err error) {
	defer recoverRender(func() string { return "jam.yml" }, &err)
	return string(renderYAML(serve)), nil
}
