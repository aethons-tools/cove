package jam

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// ShareGrant is ad-hoc, time-boxed access to one session for one user (studio
// collaboration, docs/usage/jam/collaboration.md). It is keyed by session id
// and user; granting again replaces it. Validation (rights, lifetime) is the
// caller's: see collab.ValidateGrant.
type ShareGrant struct {
	SessionID string    `json:"session_id"`
	UserID    ident.ID  `json:"user_id"`
	Rights    []string  `json:"rights"` // "preview" | "ssh"
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	GrantedBy string    `json:"granted_by"`
}

// ShareGrantStore is the Store's share-grant table.
type ShareGrantStore interface {
	// PutShareGrant upserts by (session, user).
	PutShareGrant(g ShareGrant) error
	// RemoveShareGrant drops one grant (no-op if absent).
	RemoveShareGrant(sessionID string, user ident.ID) error
	// ShareGrants lists a session's grants, sorted by user; expired ones are
	// included (callers compare ExpiresAt). The result is a copy.
	ShareGrants(sessionID string) []ShareGrant
	// RemoveShareGrantsOf drops every grant of a session (no-op if none).
	RemoveShareGrantsOf(sessionID string) error
}

func (g ShareGrant) clone() ShareGrant {
	g.Rights = slices.Clone(g.Rights)
	return g
}

func (m *memState) ShareGrants(sessionID string) []ShareGrant {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ShareGrant, 0, len(m.grants[sessionID]))
	for _, g := range m.grants[sessionID] {
		out = append(out, g.clone())
	}
	slices.SortFunc(out, func(a, b ShareGrant) int { return cmp.Compare(a.UserID, b.UserID) })
	return out
}

// prepareShareGrant validates a grant's shape and that its user exists (a
// removed user's tombstone still counts, as with the Postgres foreign key).
// Caller holds mu.
func (m *memState) prepareShareGrant(g ShareGrant) error {
	if g.SessionID == "" || g.UserID == "" {
		return errors.New("share grant: session id and user id are required")
	}
	if _, ok := m.users[g.UserID]; !ok {
		return fmt.Errorf("share grant: unknown user %s", g.UserID)
	}
	return nil
}

func (m *memState) applyPutShareGrant(g ShareGrant) {
	if m.grants[g.SessionID] == nil {
		m.grants[g.SessionID] = map[ident.ID]ShareGrant{}
	}
	m.grants[g.SessionID][g.UserID] = g.clone()
}

func (m *memState) applyRemoveShareGrant(sessionID string, user ident.ID) {
	delete(m.grants[sessionID], user)
	if len(m.grants[sessionID]) == 0 {
		delete(m.grants, sessionID)
	}
}

func (m *memState) applyRemoveShareGrantsOf(sessionID string) {
	delete(m.grants, sessionID)
}

func (fs *MemStore) PutShareGrant(g ShareGrant) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.prepareShareGrant(g); err != nil {
		return err
	}
	fs.applyPutShareGrant(g)
	return nil
}

func (fs *MemStore) RemoveShareGrant(sessionID string, user ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyRemoveShareGrant(sessionID, user)
	return nil
}

func (fs *MemStore) RemoveShareGrantsOf(sessionID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyRemoveShareGrantsOf(sessionID)
	return nil
}
