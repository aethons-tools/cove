package harbor

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Store records enrolled actors (by token hash), roles (by project+name), and the
// destination table.
type Store interface {
	AddActor(a Actor) error // error if the id already exists
	Lookup(tokenHash string) (Actor, bool)
	RemoveActor(id string) error
	ListActors() []Actor

	AddGrant(actorID string, g Grant) error // upsert by (project,role); error if actor absent
	RemoveGrant(actorID, project, role string) error

	PutRole(project string, r Role) error // upsert; auto-creates the project namespace
	GetRole(project, name string) (Role, bool)
	RemoveRole(project, name string) error
	ListRoles(project string) []Role
	ListProjects() []string

	PushKit(name, config string) (int, error)
	GetKit(name string) (Kit, bool)
	KitConfig(name string, version int) (string, bool)
	PinKit(name string, version int) error
	ListKits() []Kit
	RemoveKit(name string) error
	RoleReferencingKit(name string) (project, role string, ok bool)

	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)
}

// storeFile is the on-disk JSON shape (format v4).
type storeFile struct {
	Roles        map[string]map[string]Role `json:"roles"`        // project → roleName → Role
	Actors       map[string]Actor           `json:"actors"`       // keyed by TokenHash
	Destinations map[string]Destination     `json:"destinations"` // keyed by Name
	Kits         map[string]Kit             `json:"kits"`         // keyed by Kit.Name
}

// legacyIdentity is the pre-RBAC (v1/v2) per-identity record, read only during
// migration.
type legacyIdentity struct {
	ID           string    `json:"id"`
	TokenHash    string    `json:"token_hash"`
	Project      string    `json:"project"`
	Role         string    `json:"role"`
	Destinations []string  `json:"destinations"`
	Repos        []string  `json:"repos"`
	Expiry       time.Time `json:"expiry"`
}

// FileStore is a JSON-file-backed Store. Single-node MVP; the serve process is the
// sole writer, so there is no cross-process contention.
type FileStore struct {
	path   string
	mu     sync.Mutex
	roles  map[string]map[string]Role
	actors map[string]Actor
	dests  map[string]Destination
	kits   map[string]Kit
}

// NewFileStore loads (or initializes) the store at path, migrating a v1 (bare
// map[tokenHash]Identity) or v2 (identities+destinations) file into the v4 shape.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{
		path:   path,
		roles:  map[string]map[string]Role{},
		actors: map[string]Actor{},
		dests:  map[string]Destination{},
		kits:   map[string]Kit{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fs, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return fs, nil
	}

	// v3?
	var v3 storeFile
	if err := json.Unmarshal(data, &v3); err != nil {
		return nil, fmt.Errorf("load store %s: %w", path, err)
	}
	if v3.Actors != nil || v3.Roles != nil {
		if v3.Roles != nil {
			fs.roles = v3.Roles
		}
		if v3.Actors != nil {
			fs.actors = v3.Actors
		}
		if v3.Destinations != nil {
			fs.dests = v3.Destinations
		}
		if v3.Kits != nil {
			fs.kits = v3.Kits
		}
		return fs, nil
	}

	// v2? (identities + destinations)
	var v2 struct {
		Identities   map[string]legacyIdentity `json:"identities"`
		Destinations map[string]Destination    `json:"destinations"`
	}
	if err := json.Unmarshal(data, &v2); err != nil {
		return nil, fmt.Errorf("load store %s (v2): %w", path, err)
	}
	if v2.Identities != nil || v2.Destinations != nil {
		if v2.Destinations != nil {
			fs.dests = v2.Destinations
		}
		fs.migrateIdentities(v2.Identities)
		return fs, nil
	}

	// v1: the whole file is a map[tokenHash]legacyIdentity.
	var v1 map[string]legacyIdentity
	if err := json.Unmarshal(data, &v1); err != nil {
		return nil, fmt.Errorf("load store %s (v1): %w", path, err)
	}
	fs.migrateIdentities(v1)
	return fs, nil
}

// migrateIdentities converts legacy identities into actors + synthesized roles,
// preserving each actor's effective scope. Caller sets up fs maps. Not locked
// (construction time, single goroutine).
func (fs *FileStore) migrateIdentities(legacy map[string]legacyIdentity) {
	for _, li := range legacy {
		project := li.Project
		if project == "" {
			project = DefaultProject
		}
		role := li.Role
		if role == "" {
			role = "default"
		}
		if fs.roles[project] == nil {
			fs.roles[project] = map[string]Role{}
		}
		existing, ok := fs.roles[project][role]
		if !ok {
			existing = Role{Name: role, Scope: Scope{Destinations: li.Destinations, Repos: li.Repos}}
			fs.roles[project][role] = existing
		}
		g := Grant{Project: project, Role: role}
		if !sameStrings(existing.Scope.Destinations, li.Destinations) || !sameStrings(existing.Scope.Repos, li.Repos) {
			// EffectiveScope treats a nil override field as "inherit the role's
			// value" — but a legacy identity's nil/empty field means deny-all for
			// that field, not inherit. Coerce to a non-nil empty slice so the
			// override REPLACES rather than inherits, preserving the identity's
			// exact original scope regardless of map-iteration order.
			g.Overrides = &Override{Destinations: nonNilStrings(li.Destinations), Repos: nonNilStrings(li.Repos)}
		}
		fs.actors[li.TokenHash] = Actor{ID: li.ID, TokenHash: li.TokenHash, Expiry: li.Expiry, Grants: []Grant{g}}
	}
}

// nonNilStrings coerces a nil slice to a non-nil empty one, so that assigning it
// into an Override field makes EffectiveScope REPLACE the role's value (deny-all)
// instead of treating the nil field as "inherit the role".
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// save persists the v4 shape. Caller holds fs.mu.
func (fs *FileStore) save() error {
	data, err := json.MarshalIndent(storeFile{Roles: fs.roles, Actors: fs.actors, Destinations: fs.dests, Kits: fs.kits}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fs.path, data, 0o600)
}

func (fs *FileStore) AddActor(a Actor) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, rec := range fs.actors {
		if rec.ID == a.ID {
			return fmt.Errorf("actor %q already exists", a.ID)
		}
	}
	fs.actors[a.TokenHash] = a
	return fs.save()
}

func (fs *FileStore) Lookup(tokenHash string) (Actor, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	a, ok := fs.actors[tokenHash]
	return a, ok
}

func (fs *FileStore) RemoveActor(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for h, rec := range fs.actors {
		if rec.ID == id {
			delete(fs.actors, h)
			return fs.save()
		}
	}
	return fmt.Errorf("actor %q not found", id)
}

func (fs *FileStore) ListActors() []Actor {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Actor, 0, len(fs.actors))
	for _, a := range fs.actors {
		out = append(out, a)
	}
	return out
}

func (fs *FileStore) AddGrant(actorID string, g Grant) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if g.Project == "" {
		g.Project = DefaultProject
	}
	for h, rec := range fs.actors {
		if rec.ID != actorID {
			continue
		}
		// upsert by (project, role)
		replaced := false
		for i := range rec.Grants {
			if rec.Grants[i].Project == g.Project && rec.Grants[i].Role == g.Role {
				rec.Grants[i] = g
				replaced = true
				break
			}
		}
		if !replaced {
			rec.Grants = append(rec.Grants, g)
		}
		fs.actors[h] = rec
		return fs.save()
	}
	return fmt.Errorf("actor %q not found", actorID)
}

func (fs *FileStore) RemoveGrant(actorID, project, role string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	for h, rec := range fs.actors {
		if rec.ID != actorID {
			continue
		}
		kept := rec.Grants[:0]
		found := false
		for _, g := range rec.Grants {
			if g.Project == project && g.Role == role {
				found = true
				continue
			}
			kept = append(kept, g)
		}
		if !found {
			return fmt.Errorf("actor %q has no grant %s/%s", actorID, project, role)
		}
		rec.Grants = kept
		fs.actors[h] = rec
		return fs.save()
	}
	return fmt.Errorf("actor %q not found", actorID)
}

func (fs *FileStore) PutRole(project string, r Role) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	if r.Kit != "" {
		if _, ok := fs.kits[r.Kit]; !ok {
			return fmt.Errorf("kit %q not found", r.Kit)
		}
	}
	if fs.roles[project] == nil {
		fs.roles[project] = map[string]Role{}
	}
	fs.roles[project][r.Name] = r
	return fs.save()
}

func (fs *FileStore) GetRole(project, name string) (Role, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	r, ok := fs.roles[project][name]
	return r, ok
}

func (fs *FileStore) RemoveRole(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	if _, ok := fs.roles[project][name]; !ok {
		return fmt.Errorf("role %q not found in project %q", name, project)
	}
	delete(fs.roles[project], name)
	return fs.save()
}

func (fs *FileStore) ListRoles(project string) []Role {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	out := make([]Role, 0, len(fs.roles[project]))
	for _, r := range fs.roles[project] {
		out = append(out, r)
	}
	return out
}

func (fs *FileStore) ListProjects() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	set := map[string]struct{}{}
	for p := range fs.roles {
		set[p] = struct{}{}
	}
	for _, a := range fs.actors {
		for _, g := range a.Grants {
			set[g.Project] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	return out
}

func (fs *FileStore) PushKit(name, config string) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if name == "" || config == "" {
		return 0, fmt.Errorf("kit name and config are required")
	}
	k, ok := fs.kits[name]
	if !ok {
		k = Kit{Name: name, Versions: map[int]string{}}
	}
	next := 0
	for v := range k.Versions {
		if v > next {
			next = v
		}
	}
	next++
	k.Versions[next] = config
	k.Current = next
	fs.kits[name] = k
	return next, fs.save()
}

func (fs *FileStore) GetKit(name string) (Kit, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k, ok := fs.kits[name]
	if !ok {
		return Kit{}, false
	}
	// Copy the versions map so the caller can't observe (or race on) the
	// store's live map — see ListKits, which does the same.
	vs := make(map[int]string, len(k.Versions))
	for v, c := range k.Versions {
		vs[v] = c
	}
	return Kit{Name: k.Name, Current: k.Current, Versions: vs}, true
}

func (fs *FileStore) KitConfig(name string, version int) (string, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k, ok := fs.kits[name]
	if !ok {
		return "", false
	}
	if version == 0 {
		version = k.Current
	}
	cfg, ok := k.Versions[version]
	return cfg, ok
}

func (fs *FileStore) PinKit(name string, version int) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k, ok := fs.kits[name]
	if !ok {
		return fmt.Errorf("kit %q not found", name)
	}
	if _, ok := k.Versions[version]; !ok {
		return fmt.Errorf("kit %q has no version %d", name, version)
	}
	k.Current = version
	fs.kits[name] = k
	return fs.save()
}

func (fs *FileStore) ListKits() []Kit {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Kit, 0, len(fs.kits))
	for _, k := range fs.kits {
		// copy the versions map so callers can't mutate the store
		vs := make(map[int]string, len(k.Versions))
		for v, c := range k.Versions {
			vs[v] = c
		}
		out = append(out, Kit{Name: k.Name, Current: k.Current, Versions: vs})
	}
	return out
}

func (fs *FileStore) RemoveKit(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.kits[name]; !ok {
		return fmt.Errorf("kit %q not found", name)
	}
	delete(fs.kits, name)
	return fs.save()
}

func (fs *FileStore) RoleReferencingKit(name string) (string, string, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for project, roles := range fs.roles {
		for _, r := range roles {
			if r.Kit == name {
				return project, r.Name, true
			}
		}
	}
	return "", "", false
}

func (fs *FileStore) AddDestination(d Destination) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.dests[d.Name] = d
	return fs.save()
}

func (fs *FileStore) RemoveDestination(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.dests[name]; !ok {
		return fmt.Errorf("destination %q not found", name)
	}
	delete(fs.dests, name)
	return fs.save()
}

func (fs *FileStore) ListDestinations() []Destination {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Destination, 0, len(fs.dests))
	for _, d := range fs.dests {
		out = append(out, d)
	}
	return out
}

func (fs *FileStore) Match(reqPath string) (Destination, bool) {
	return Config{Destinations: fs.ListDestinations()}.Match(reqPath)
}
