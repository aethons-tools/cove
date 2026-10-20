package collabhttp

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabwire"
)

// RevokeEvery is how often Live.Run re-checks open connections by default.
const RevokeEvery = time.Minute

// Live is the registry of open studio connections — SSH tunnels and the
// preview proxy's pooled tunnel connections — shared by both planes and the
// preview proxy, so an unshare on any of them closes connections opened on
// the others. Each entry carries the right it rides on.
type Live struct {
	mu    sync.Mutex
	conns map[*liveEntry]struct{}
}

type liveEntry struct {
	collabwire.LiveConn
	userID   ident.ID // the roster user whose access the connection rides on; "" for an operator
	operator bool
	right    collab.Right // what the connection is for: collab.SSH or collab.Preview
	port     int          // the studio port it reaches (SSHPort, or the exposed port)
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

// List returns a session's open SSH connections, oldest first (preview
// connections are pooled per viewer, not a session a person holds open, so
// the Access card does not list them).
func (l *Live) List(session string) []collabwire.LiveConn {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []collabwire.LiveConn{}
	for e := range l.conns {
		if e.Session == session && e.right == collab.SSH {
			out = append(out, e.LiveConn)
		}
	}
	slices.SortFunc(out, func(a, b collabwire.LiveConn) int {
		return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.User, b.User))
	})
	return out
}

// users returns the open non-operator connections matching keep (an
// operator's access never rides on a rule that can be revoked).
func (l *Live) users(keep func(*liveEntry) bool) []*liveEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*liveEntry
	for e := range l.conns {
		if !e.operator && keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// CloseUser closes user's open non-operator connections on session and
// returns how many it closed.
func (l *Live) CloseUser(session string, user ident.ID) int {
	if user == "" {
		return 0
	}
	closing := l.users(func(e *liveEntry) bool { return e.Session == session && e.userID == user })
	for _, e := range closing {
		e.close() // splice ends and the handler removes the entry
	}
	return len(closing)
}

// CloseExposure closes every open preview connection (operators' included) to
// port on session — the agent unexposed it or moved its name to another port —
// and returns how many it closed. It only collects under the lock; the
// closes run on their own goroutine, so a caller holding a lock (the
// supervisor's exposure sink) never waits on a tunnel.
func (l *Live) CloseExposure(session string, port int) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	var closing []*liveEntry
	for e := range l.conns {
		if e.right == collab.Preview && e.Session == session && e.port == port {
			closing = append(closing, e)
		}
	}
	l.mu.Unlock()
	if len(closing) > 0 {
		go func() {
			for _, e := range closing {
				e.close() // the conn's own close removes the entry
			}
		}()
	}
	return len(closing)
}

// Sweep re-checks every open non-operator connection with collab.Decide for
// the right it rides on and closes those no longer allowed — the user was
// removed, the role's policy or the user's membership changed, the grant
// expired or went, or the studio left the registry — logging "ssh tunnel
// revoked" or "preview connection revoked" for each. It returns how many it
// closed.
func (l *Live) Sweep(store jam.Store, now time.Time, log *slog.Logger) int {
	n := 0
	for _, e := range l.users(func(*liveEntry) bool { return true }) {
		inst, ok := store.GetInstance(e.Session)
		reason := "studio gone"
		if ok {
			if _, allowed := collab.Decide(store, now, collab.Principal{UserID: e.userID}, inst, e.right); allowed {
				continue
			}
			reason = revokeReason(store, now, e)
		}
		e.close()
		n++
		msg := "ssh tunnel revoked"
		if e.right == collab.Preview {
			msg = "preview connection revoked"
		}
		log.Info(msg, "user", e.User, "session", e.Session, "right", string(e.right), "port", e.port, "reason", reason)
	}
	return n
}

// revokeReason names why e's user lost e's right, as far as the store tells.
func revokeReason(store jam.Store, now time.Time, e *liveEntry) string {
	if u, ok := store.GetUser(e.userID); !ok || u.Status != jam.StatusLive {
		return "user removed"
	}
	for _, g := range store.ShareGrants(e.Session) {
		if g.UserID == e.userID && slices.Contains(g.Rights, string(e.right)) && !now.Before(g.ExpiresAt) {
			return "grant expired"
		}
	}
	return "no longer allowed"
}

// Run sweeps every `every` (RevokeEvery when <= 0) on now's clock until ctx
// ends.
func (l *Live) Run(ctx context.Context, store jam.Store, log *slog.Logger, every time.Duration, now func() time.Time) {
	if every <= 0 {
		every = RevokeEvery
	}
	if now == nil {
		now = time.Now
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Sweep(store, now(), log)
		}
	}
}
