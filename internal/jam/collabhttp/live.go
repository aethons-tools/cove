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
	"github.com/aethons-tools/cove/internal/jam/preview"
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
	right    collab.Right // the right it rides on: collab.SSH or collab.Preview
	port     int          // the studio port it reaches (SSHPort, or the preview's port)
	// viaPreview: a preview proxy connection (right is collab.SSH for the
	// ide preview), not an SSH tunnel.
	viaPreview bool
	close      func()
}

// NewLive returns an empty registry.
func NewLive() *Live { return &Live{conns: map[*liveEntry]struct{}{}} }

// add registers a connection and returns its removal.
func (l *Live) add(e *liveEntry) (remove func()) {
	e.Right = string(e.right)
	l.mu.Lock()
	l.conns[e] = struct{}{}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		delete(l.conns, e)
		l.mu.Unlock()
	}
}

// List returns a session's open connections of every right (ssh, preview),
// oldest first.
func (l *Live) List(session string) []collabwire.LiveConn {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []collabwire.LiveConn{}
	for e := range l.conns {
		if e.Session == session {
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

// CloseUnexposed closes every open preview connection (operators' included)
// on session to a port not in exposed — the agent unexposed it, moved its
// name to another port, or the session went — and returns how many it
// closed. The built-in IDE (preview.IDEPort) is never exposed, so its
// connections are left alone; a kit's own ide exposure follows the rule. It
// judges the connections themselves, not a remembered previous set, so it is
// right on the first change after a Jam restart. It only
// collects under the lock; the closes run on their own goroutine, so a caller
// holding a lock (the supervisor's exposure sink) never waits on a tunnel.
func (l *Live) CloseUnexposed(session string, exposed []int32) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	var closing []*liveEntry
	for e := range l.conns {
		if e.viaPreview && e.Session == session && e.port != preview.IDEPort && !slices.Contains(exposed, int32(e.port)) {
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
		if e.viaPreview {
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
