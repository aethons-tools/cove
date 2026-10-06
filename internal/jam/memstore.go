package jam

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// MemStore is an in-memory Store for tests and tooling. Not for production:
// nothing is persisted. It shares the in-memory core (memState) with
// PostgresStore, so read and mutation semantics are identical.
type MemStore struct {
	*memState
}

var _ Store = (*MemStore)(nil)

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{memState: newMemState()} }

// ---- mutators: Lock; validate/compute via memState; apply ----

func (fs *MemStore) AddActor(a Actor) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.actorIDExists(a.ID) {
		return fmt.Errorf("actor %q already exists", a.ID)
	}
	created, err := fs.grantProjects(a)
	if err != nil {
		return err
	}
	for _, p := range created {
		fs.applyPutProject(p)
	}
	fs.applyPutActor(a)
	return nil
}

func (fs *MemStore) RemoveActor(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveActorByID(id) {
		return actorNotFoundErr(id)
	}
	return nil
}

func (fs *MemStore) AddGrant(actorID string, g Grant) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	_, a, ok := fs.actorByID(actorID)
	if !ok {
		return actorNotFoundErr(actorID)
	}
	p, created, err := fs.requireProject(g.Project)
	if err != nil {
		return err
	}
	if created {
		fs.applyPutProject(p)
	}
	fs.applyPutActor(upsertGrant(a, g))
	return nil
}

func (fs *MemStore) RemoveGrant(actorID, project, role string) error {
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
	return nil
}

func (fs *MemStore) PutRole(project string, r Role) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if r.Kit != "" && !fs.kitExists(r.Kit) {
		return fmt.Errorf("kit %q not found", r.Kit)
	}
	p, created, err := fs.requireProject(project)
	if err != nil {
		return err
	}
	if created {
		fs.applyPutProject(p)
	}
	fs.applyPutRole(project, r)
	return nil
}

func (fs *MemStore) CreateProject(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.checkCreateProject(name); err != nil {
		return err
	}
	fs.applyPutProject(newProject(name))
	return nil
}

func (fs *MemStore) RemoveProject(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.checkRemoveProject(name); err != nil {
		return err
	}
	fs.applyRemoveProject(name)
	return nil
}

func (fs *MemStore) RemoveRole(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	if !fs.applyRemoveRole(project, name) {
		return fmt.Errorf("role %q not found in project %q", name, project)
	}
	return nil
}

func (fs *MemStore) PushKit(name, config string) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if name == "" || config == "" {
		return 0, fmt.Errorf("kit name and config are required")
	}
	v := fs.applyPushKit(name, config)
	return v, nil
}

func (fs *MemStore) PinKit(name string, version int) error {
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
	return nil
}

func (fs *MemStore) RemoveKit(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveKit(name) {
		return fmt.Errorf("kit %q not found", name)
	}
	return nil
}

func (fs *MemStore) PutInstance(i Instance) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if i.ActorID == "" {
		return fmt.Errorf("instance actor id is required")
	}
	fs.applyPutInstance(i)
	return nil
}

func (fs *MemStore) RemoveInstance(actorID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveInstance(actorID) {
		return fmt.Errorf("instance %q not found", actorID)
	}
	return nil
}

func (fs *MemStore) AdvanceCommitCursor(actorID, upToID string, upToSeq int64) (Instance, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	before, ok := fs.instances[actorID]
	if !ok {
		return Instance{}, fmt.Errorf("instance %q not found", actorID)
	}
	i, _ := fs.applyAdvanceCommitCursor(actorID, upToID, upToSeq)
	if i.CommitSeq == before.CommitSeq {
		// No-op advance (backward/equal upToSeq): nothing changed.
		return i, nil
	}
	return i, nil
}

func (fs *MemStore) AddDestination(d Destination) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutDestination(d)
	return nil
}

func (fs *MemStore) RemoveDestination(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.applyRemoveDestination(name) {
		return fmt.Errorf("destination %q not found", name)
	}
	return nil
}

func (fs *MemStore) PutModelSpec(ms ModelSpec) error {
	c, err := prepareModelSpec(ms)
	if err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyPutModelSpec(c)
	return nil
}

func (fs *MemStore) RemoveModelSpec(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.checkRemoveModelSpec(name); err != nil {
		return err
	}
	fs.applyRemoveModelSpec(name)
	return nil
}

func (fs *MemStore) SetEscalationPolicy(project, category string, tiers []EscalationTier) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, _, err := fs.requireProject(project)
	if err != nil {
		return err
	}
	fs.applyPutProject(setEscalation(p, category, tiers))
	return nil
}

func (fs *MemStore) SetProjectContext(project string, l sessionctx.Layer, rs []sessionctx.Resource) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, _, err := fs.requireProject(project)
	if err != nil {
		return err
	}
	fs.applyPutProject(setProjectContext(p, l, rs))
	return nil
}

func (fs *MemStore) SetModelSpecSchema(v int) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.specSchema = v
	return nil
}

func (fs *MemStore) SetJamContext(l sessionctx.Layer) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applySetJamContext(l)
	return nil
}

func (fs *MemStore) SetChatService(project, ref string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p, created, err := fs.prepareSetChatService(project, ref)
	if err != nil {
		return err
	}
	for _, c := range created {
		fs.applyPutConnection(c)
	}
	fs.applyPutProject(p)
	return nil
}
