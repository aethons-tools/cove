package collabhttp

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// LiveConn is one open SSH connection into a studio.
type LiveConn struct {
	Session string    `json:"session"`
	User    string    `json:"user"` // usr_… or op:<operator-id>, as in the audit lines
	Name    string    `json:"name"` // display name
	Since   time.Time `json:"since"`
}

// Live is the registry of open SSH connections, shared by both planes so an
// unshare on one closes connections opened on the other.
type Live struct {
	mu    sync.Mutex
	conns map[*liveEntry]struct{}
}

type liveEntry struct {
	LiveConn
	userID   ident.ID // the roster user whose access the connection rides on; "" for an operator
	operator bool
	close    func()
}

// NewLive returns an empty registry.
func NewLive() *Live { return &Live{conns: map[*liveEntry]struct{}{}} }

// add registers a connection and returns its removal.
func (l *Live) add(e *liveEntry) (remove func()) {
	l.mu.Lock()
	l.conns[e] = struct{}{}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		delete(l.conns, e)
		l.mu.Unlock()
	}
}

// List returns a session's open connections, oldest first.
func (l *Live) List(session string) []LiveConn {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []LiveConn{}
	for e := range l.conns {
		if e.Session == session {
			out = append(out, e.LiveConn)
		}
	}
	slices.SortFunc(out, func(a, b LiveConn) int { return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.User, b.User)) })
	return out
}

// CloseUser closes user's open non-operator connections on session (an
// operator's access never rides on a grant) and returns how many it closed.
func (l *Live) CloseUser(session string, user ident.ID) int {
	if l == nil || user == "" {
		return 0
	}
	l.mu.Lock()
	var closing []*liveEntry
	for e := range l.conns {
		if e.Session == session && !e.operator && e.userID == user {
			closing = append(closing, e)
		}
	}
	l.mu.Unlock()
	for _, e := range closing {
		e.close() // splice ends and the handler removes the entry
	}
	return len(closing)
}
