package jam

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ConfigMark records that the config repo manages one object: what was last
// applied to it and from where.
type ConfigMark struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Commit    string    `json:"commit"` // source commit; "dir" for a --dir apply
	Digest    string    `json:"digest"` // configObject.Digest() of the form applied
	AppliedAt time.Time `json:"applied_at"`
}

// Key is the mark's object.
func (m ConfigMark) Key() ConfigKey { return ConfigKey{Kind: m.Kind, Name: m.Name} }

// ConfigSource is the Jam's one tracked config source and its last apply
// from it. It lives in jam_settings ('config_source'), not in a table of
// its own, and is not part of at-jam export (like the marks).
type ConfigSource struct {
	Repo      string    `json:"repo"`
	Dir       string    `json:"dir,omitempty"`
	Ref       string    `json:"ref"`
	Commit    string    `json:"commit,omitempty"` // last commit applied from it; "" = none since tracked
	AppliedAt time.Time `json:"applied_at,omitzero"`
}

// Source is c as a fetchable source.
func (c ConfigSource) Source() gitsource.Source {
	return gitsource.Source{Repo: c.Repo, Dir: c.Dir, Ref: c.Ref}
}

// checkConfigSource refuses a tracked source without a repo or a ref; nil
// (clear / untouched) is fine.
func checkConfigSource(src *ConfigSource) error {
	if src != nil && (src.Repo == "" || src.Ref == "") {
		return errors.New("config source: repo and ref are required")
	}
	return nil
}

// GetConfigSource returns the tracked config source; ok=false when none.
func (m *memState) GetConfigSource() (ConfigSource, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.configSource == nil {
		return ConfigSource{}, false
	}
	return *m.configSource, true
}

// applySetConfigSource replaces the tracked source (nil clears). Caller holds mu.
func (m *memState) applySetConfigSource(src *ConfigSource) {
	if src == nil {
		m.configSource = nil
		return
	}
	c := *src
	m.configSource = &c
}

// ConfigRoleWrite is a role in a project, by project id.
type ConfigRoleWrite struct {
	ProjectID ident.ID
	Role      Role
}

// ConfigWrite is one config apply's whole write set, in apply order. Removals
// of registry entities are tombstones in Connections/Users/Projects.
type ConfigWrite struct {
	Connections        []Connection
	Users              []User
	Accounts           []Account // accounts a user removal unlinks
	Destinations       []Destination
	RemoveDestinations []string
	ModelSpecs         []ModelSpec
	RemoveModelSpecs   []string
	Kits               []Kit // whole docs, as PutKitDocs
	RemoveKits         []string
	Projects           []Project    // upserts; StatusRemoved = a tombstone
	RemoveMemberships  []Membership // by ProjectID + UserID
	Memberships        []Membership
	Channels           []Channel         // rooms: creates, rebinds, archives
	RemoveRoles        []ConfigRoleWrite // by ProjectID + Role.Name
	Roles              []ConfigRoleWrite
	JamContext         *sessionctx.Layer // nil = untouched; empty = cleared
	Marks              []ConfigMark
	RemoveMarks        []ConfigKey
	ConfigSource       *ConfigSource // the tracked source's new record; nil = untouched
}

// Empty reports whether the write changes nothing.
func (w ConfigWrite) Empty() bool {
	return len(w.Connections)+len(w.Users)+len(w.Accounts)+len(w.Destinations)+len(w.RemoveDestinations)+
		len(w.ModelSpecs)+len(w.RemoveModelSpecs)+len(w.Kits)+len(w.RemoveKits)+len(w.Projects)+
		len(w.RemoveMemberships)+len(w.Memberships)+len(w.Channels)+len(w.RemoveRoles)+len(w.Roles)+
		len(w.Marks)+len(w.RemoveMarks) == 0 && w.JamContext == nil && w.ConfigSource == nil
}

// ListConfigMarks returns the managed marks, sorted by kind, then name.
func (m *memState) ListConfigMarks() []ConfigMark {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ConfigMark, 0, len(m.marks))
	for _, mk := range m.marks {
		out = append(out, mk)
	}
	slices.SortFunc(out, func(a, b ConfigMark) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// checkConfigWrite refuses a write that cannot apply whole. Caller holds mu.
func checkConfigWrite(m *memState, w ConfigWrite) error {
	if err := checkKitDocs(w.Kits); err != nil {
		return fmt.Errorf("config write: %w", err)
	}
	if err := checkConfigSource(w.ConfigSource); err != nil {
		return fmt.Errorf("config write: %w", err)
	}
	empty := func(kind string, n int, name string) error {
		if name == "" {
			return fmt.Errorf("config write: %s name is required (record %d)", kind, n)
		}
		return nil
	}
	for i, d := range w.Destinations {
		if err := empty("destination", i, d.Name); err != nil {
			return err
		}
	}
	for i, s := range w.ModelSpecs {
		if err := empty("model-spec", i, s.Name); err != nil {
			return err
		}
		if _, err := prepareModelSpec(s); err != nil {
			return fmt.Errorf("config write: %w", err)
		}
	}
	for i, p := range w.Projects {
		if err := empty("project", i, p.Name); err != nil {
			return err
		}
	}
	for i, r := range w.Roles {
		if err := empty("role", i, r.Role.Name); err != nil {
			return err
		}
	}
	for i, u := range w.Users {
		if err := empty("user", i, u.Name); err != nil {
			return err
		}
	}
	for i, c := range w.Connections {
		if err := empty("connection", i, c.Name); err != nil {
			return err
		}
	}
	for i, mk := range w.Marks {
		if err := empty("mark", i, mk.Name); err != nil {
			return err
		}
	}
	inWrite := map[ident.ID]bool{}
	leaving := map[ident.ID]bool{}
	for _, p := range w.Projects {
		inWrite[p.ID] = true
		if p.Status == StatusRemoved {
			leaving[p.ID] = true
		}
	}
	for _, p := range w.Projects {
		if live, ok := m.projects[p.Name]; ok && p.Status != StatusRemoved && live.ID != p.ID && !leaving[live.ID] {
			return fmt.Errorf("config write: project name %q is held by another live project", p.Name)
		}
	}
	knownProject := func(id ident.ID) bool {
		if inWrite[id] {
			return true
		}
		_, ok := m.projectByID(id)
		return ok
	}
	for _, r := range w.Roles {
		if !knownProject(r.ProjectID) {
			return fmt.Errorf("config write: role %q is in unknown project %s", r.Role.Name, r.ProjectID)
		}
	}
	for _, ms := range w.Memberships {
		if !knownProject(ms.ProjectID) {
			return fmt.Errorf("config write: membership of %s is in unknown project %s", ms.UserID, ms.ProjectID)
		}
	}
	for _, c := range w.Channels {
		if !knownProject(c.ProjectID) {
			return fmt.Errorf("config write: channel %q is in unknown project %s", c.Key, c.ProjectID)
		}
	}
	userInWrite := map[ident.ID]bool{}
	for _, u := range w.Users {
		userInWrite[u.ID] = true
	}
	for _, ms := range w.Memberships {
		if _, ok := m.users[ms.UserID]; !ok && !userInWrite[ms.UserID] {
			return fmt.Errorf("config write: membership names unknown user %s", ms.UserID)
		}
	}
	return nil
}

// prepareConfigWrite returns w ready to persist and apply identically in both
// stores: model specs are deep-copied, and a tombstone of a live project
// becomes the live doc with Status removed plus archived copies of all its
// live channels (as RemoveProject does). Caller holds mu and has run
// checkConfigWrite.
func prepareConfigWrite(m *memState, w ConfigWrite) ConfigWrite {
	out := w
	out.ModelSpecs = nil
	for _, s := range w.ModelSpecs {
		c, _ := prepareModelSpec(s) // checked by checkConfigWrite
		out.ModelSpecs = append(out.ModelSpecs, c)
	}
	out.Projects = slices.Clone(w.Projects)
	var archived []Channel
	explicit := map[ident.ID]bool{}
	for _, c := range w.Channels {
		explicit[c.ID] = true
	}
	for i, p := range out.Projects {
		if p.Status != StatusRemoved {
			continue
		}
		live, ok := m.projects[p.Name]
		if !ok || live.ID != p.ID {
			continue
		}
		tomb := copyProject(live)
		tomb.Status = StatusRemoved
		out.Projects[i] = tomb
		for _, c := range m.projectChannels(p.ID) {
			if !explicit[c.ID] {
				c.Status = StatusArchived
				archived = append(archived, c)
			}
		}
	}
	out.Channels = append(archived, w.Channels...)
	return out
}

// applyConfigWrite applies w to the cache in struct field order. Caller holds
// mu.Lock() and has run checkConfigWrite.
func applyConfigWrite(m *memState, w ConfigWrite) {
	for _, c := range w.Connections {
		m.applyPutConnection(c)
	}
	for _, u := range w.Users {
		m.applyPutUser(u)
	}
	for _, a := range w.Accounts {
		m.applyPutAccount(a)
	}
	for _, d := range w.Destinations {
		m.applyPutDestination(d)
	}
	for _, n := range w.RemoveDestinations {
		m.applyRemoveDestination(n)
	}
	for _, s := range w.ModelSpecs {
		m.applyPutModelSpec(s) // prepared by prepareConfigWrite
	}
	for _, n := range w.RemoveModelSpecs {
		m.applyRemoveModelSpec(n)
	}
	for _, k := range w.Kits {
		m.kits[k.Name] = copyKit(k)
	}
	for _, n := range w.RemoveKits {
		m.applyRemoveKit(n)
	}
	for _, p := range w.Projects {
		if p.Status == StatusRemoved {
			if live, ok := m.projects[p.Name]; ok && live.ID == p.ID {
				m.applyRemoveProject(p.Name)
			} else {
				m.removedProjects[p.ID] = p
			}
			continue
		}
		m.applyPutProject(p)
	}
	for _, ms := range w.RemoveMemberships {
		m.applyRemoveMember(ms.ProjectID, ms.UserID)
	}
	for _, ms := range w.Memberships {
		m.applyPutMembership(ms)
	}
	for _, c := range w.Channels {
		m.applyPutChannel(c)
	}
	for _, r := range w.RemoveRoles {
		m.applyRemoveRole(r.ProjectID, r.Role.Name)
	}
	for _, r := range w.Roles {
		m.applyPutRole(r.ProjectID, r.Role)
	}
	if w.JamContext != nil {
		m.applySetJamContext(*w.JamContext)
	}
	for _, mk := range w.Marks {
		m.marks[mk.Key()] = mk
	}
	for _, k := range w.RemoveMarks {
		delete(m.marks, k)
	}
	if w.ConfigSource != nil {
		m.applySetConfigSource(w.ConfigSource)
	}
}
