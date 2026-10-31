package jam

// SessionColorStore is the Store's session_colors table: a session id's
// color (docs/usage/jam/session-colors.md), kept after the session ends.
// Rows are never deleted.
type SessionColorStore interface {
	SessionColor(actorID string) (string, bool)
	// PutSessionColor records actorID's color unless it already has one, and
	// returns the color it has after the call.
	PutSessionColor(actorID, color string) (string, error)
}

func (m *memState) SessionColor(actorID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.sessionColors[actorID]
	return c, ok
}

// putSessionColorLocked is PutSessionColor's state change; the caller holds mu.
func (m *memState) putSessionColorLocked(actorID, color string) string {
	if c, ok := m.sessionColors[actorID]; ok {
		return c
	}
	m.sessionColors[actorID] = color
	return color
}

func (fs *MemStore) PutSessionColor(actorID, color string) (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.putSessionColorLocked(actorID, color), nil
}
