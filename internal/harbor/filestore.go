package harbor

import (
	"encoding/json"
	"fmt"
	"os"
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

	PutInstance(i Instance) error
	GetInstance(actorID string) (Instance, bool)
	ListInstances() []Instance
	RemoveInstance(actorID string) error
	AdvanceCommitCursor(actorID, upTo string) (Instance, error) // monotonic forward; no-op if upTo <= current; error if actor absent

	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)

	AddHuman(project string, h Human) error // upsert by name
	AddChannel(project string, c Channel) error
	RemoveHuman(project, name string) error
	RemoveChannel(project, name string) error
	GetProject(name string) (Project, bool)
	GetRoster(project string) (Roster, bool)
	SetEscalationPolicy(project, category string, tiers []EscalationTier) error
	SetChatService(project, service string) error
}

// storeFile is the on-disk JSON shape, detected by field presence rather than a persisted version number; this shape adds Projects.
type storeFile struct {
	Roles        map[string]map[string]Role `json:"roles"`              // project → roleName → Role
	Actors       map[string]Actor           `json:"actors"`             // keyed by TokenHash
	Destinations map[string]Destination     `json:"destinations"`       // keyed by Name
	Kits         map[string]Kit             `json:"kits"`               // keyed by Kit.Name
	Instances    map[string]Instance        `json:"instances"`          // keyed by Instance.ActorID
	Projects     map[string]Project         `json:"projects,omitempty"` // keyed by Project.Name
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
// sole writer, so there is no cross-process contention. Reads and in-memory
// mutation live on the embedded *memState (shared with PostgresStore); FileStore
// adds whole-file persistence via save().
type FileStore struct {
	path string
	*memState
}

// NewFileStore loads (or initializes) the store at path, migrating a v1 (bare
// map[tokenHash]Identity) or v2 (identities+destinations) file into the v4 shape.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, memState: newMemState()}
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
	if v3.Actors != nil || v3.Roles != nil || v3.Projects != nil {
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
		if v3.Instances != nil {
			fs.instances = v3.Instances
		}
		if v3.Projects != nil {
			fs.projects = v3.Projects
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
	data, err := json.MarshalIndent(storeFile{Roles: fs.roles, Actors: fs.actors, Destinations: fs.dests, Kits: fs.kits, Instances: fs.instances, Projects: fs.projects}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fs.path, data, 0o600)
}

// ---- mutators: Lock; validate/compute via memState; apply; save() ----

func (fs *FileStore) AddActor(a Actor) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.actorIDExists(a.ID) {
		return fmt.Errorf("actor %q already exists", a.ID)
	}
	fs.applyPutActor(a)
	return fs.save()
}

func (fs *FileStore) RemoveActor(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveActorByID(id) {
		return actorNotFoundErr(id)
	}
	return fs.save()
}

func (fs *FileStore) AddGrant(actorID string, g Grant) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	_, a, ok := fs.actorByID(actorID)
	if !ok {
		return actorNotFoundErr(actorID)
	}
	fs.applyPutActor(upsertGrant(a, g))
	return fs.save()
}

func (fs *FileStore) RemoveGrant(actorID, project, role string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	_, a, ok := fs.actorByID(actorID)
	if !ok {
		return actorNotFoundErr(actorID)
	}
	updated, found := removeGrantFrom(a, project, role)
	if !found {
		return fmt.Errorf("actor %q has no grant %s/%s", actorID, project, role)
	}
	fs.applyPutActor(updated)
	return fs.save()
}

func (fs *FileStore) PutRole(project string, r Role) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if r.Kit != "" && !fs.kitExists(r.Kit) {
		return fmt.Errorf("kit %q not found", r.Kit)
	}
	fs.applyPutRole(project, r)
	return fs.save()
}

func (fs *FileStore) RemoveRole(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	if !fs.applyRemoveRole(project, name) {
		return fmt.Errorf("role %q not found in project %q", name, project)
	}
	return fs.save()
}

func (fs *FileStore) PushKit(name, config string) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if name == "" || config == "" {
		return 0, fmt.Errorf("kit name and config are required")
	}
	v := fs.applyPushKit(name, config)
	return v, fs.save()
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
	fs.applyPinKit(name, version)
	return fs.save()
}

func (fs *FileStore) RemoveKit(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveKit(name) {
		return fmt.Errorf("kit %q not found", name)
	}
	return fs.save()
}

func (fs *FileStore) PutInstance(i Instance) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if i.ActorID == "" {
		return fmt.Errorf("instance actor id is required")
	}
	fs.applyPutInstance(i)
	return fs.save()
}

func (fs *FileStore) RemoveInstance(actorID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveInstance(actorID) {
		return fmt.Errorf("instance %q not found", actorID)
	}
	return fs.save()
}

func (fs *FileStore) AdvanceCommitCursor(actorID, upTo string) (Instance, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	i, ok := fs.applyAdvanceCommitCursor(actorID, upTo)
	if !ok {
		return Instance{}, fmt.Errorf("instance %q not found", actorID)
	}
	return i, fs.save()
}

func (fs *FileStore) AddDestination(d Destination) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutDestination(d)
	return fs.save()
}

func (fs *FileStore) RemoveDestination(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveDestination(name) {
		return fmt.Errorf("destination %q not found", name)
	}
	return fs.save()
}

func (fs *FileStore) AddHuman(project string, h Human) error {
	if h.Name == "" {
		return fmt.Errorf("human name required")
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutProject(upsertHuman(fs.rawProject(project), h))
	return fs.save()
}

func (fs *FileStore) AddChannel(project string, c Channel) error {
	if c.Name == "" {
		return fmt.Errorf("channel name required")
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutProject(upsertChannel(fs.rawProject(project), c))
	return fs.save()
}

func (fs *FileStore) RemoveHuman(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, ok := fs.projects[project]
	if !ok {
		return fmt.Errorf("project %q not found", project)
	}
	fs.applyPutProject(removeHumanFrom(p, name))
	return fs.save()
}

func (fs *FileStore) RemoveChannel(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, ok := fs.projects[project]
	if !ok {
		return fmt.Errorf("project %q not found", project)
	}
	fs.applyPutProject(removeChannelFrom(p, name))
	return fs.save()
}

func (fs *FileStore) SetEscalationPolicy(project, category string, tiers []EscalationTier) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutProject(setEscalation(fs.rawProject(project), category, tiers))
	return fs.save()
}

func (fs *FileStore) SetChatService(project, service string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutProject(setChatService(fs.rawProject(project), service))
	return fs.save()
}
