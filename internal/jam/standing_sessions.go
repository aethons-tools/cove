package jam

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// Standing sessions (intercom slice 1b): a declared standing session (project,
// role, name) maps to the session it currently is. A restart or upgrade sets
// the same session up in a new studio; a reset ends it, and the next raise
// starts a new one. Session ids are minted (ident.Session); sessions live when
// this map was seeded keep their pre-registry ids ("standing-…").

// StandingSessionRef is one entry of the standing-session map.
type StandingSessionRef struct {
	ProjectID ident.ID `json:"project_id"`
	Role      string   `json:"role"`
	Name      string   `json:"name"`
	SessionID string   `json:"session_id"`
}

type standingKey struct {
	project    ident.ID
	role, name string
}

// StandingSessionStore is the Store's standing-session map.
type StandingSessionStore interface {
	StandingSessionID(project ident.ID, role, name string) (string, bool)
	// PutStandingSession maps a declaration to a session (one session per
	// declaration, and a session id serves one declaration).
	PutStandingSession(project ident.ID, role, name, sessionID string) error
	// RemoveStandingSession drops a declaration's entry (no-op if absent).
	RemoveStandingSession(project ident.ID, role, name string) error
	ListStandingSessions() []StandingSessionRef
}

func (m *memState) StandingSessionID(project ident.ID, role, name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.standing[standingKey{project, role, name}]
	return id, ok
}

func (m *memState) ListStandingSessions() []StandingSessionRef {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StandingSessionRef, 0, len(m.standing))
	for k, id := range m.standing {
		out = append(out, StandingSessionRef{ProjectID: k.project, Role: k.role, Name: k.name, SessionID: id})
	}
	slices.SortFunc(out, func(a, b StandingSessionRef) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Role, b.Role), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// preparePutStandingSession validates an entry. Caller holds mu.
func (m *memState) preparePutStandingSession(project ident.ID, role, name, sessionID string) error {
	if _, ok := m.projectByID(project); !ok {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, project)
	}
	if role == "" || name == "" || sessionID == "" {
		return fmt.Errorf("standing session: role, name and session id are required")
	}
	key := standingKey{project, role, name}
	for k, id := range m.standing {
		if id == sessionID && k != key {
			return fmt.Errorf("session %s already serves standing session %s/%s", sessionID, k.role, k.name)
		}
	}
	return nil
}

func (m *memState) applyPutStandingSession(project ident.ID, role, name, sessionID string) {
	m.standing[standingKey{project, role, name}] = sessionID
}

func (m *memState) applyRemoveStandingSession(project ident.ID, role, name string) {
	delete(m.standing, standingKey{project, role, name})
}

func (fs *MemStore) PutStandingSession(project ident.ID, role, name, sessionID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.preparePutStandingSession(project, role, name, sessionID); err != nil {
		return err
	}
	fs.applyPutStandingSession(project, role, name, sessionID)
	return nil
}

func (fs *MemStore) RemoveStandingSession(project ident.ID, role, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyRemoveStandingSession(project, role, name)
	return nil
}
