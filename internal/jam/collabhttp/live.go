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

// Live is the registry of open SSH connections, shared by both planes so an
// unshare on one closes connections opened on the other.
type Live struct {
	mu    sync.Mutex
	conns map[*liveEntry]struct{}
}

type liveEntry struct {
	collabwire.LiveConn
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

// Sweep re-checks every open non-operator connection with collab.Decide and
// closes those no longer allowed ssh — the user was removed, the role's policy
// or the user's membership changed, the grant expired or went, or the studio
// left the registry — logging "ssh tunnel revoked" for each. It returns how
// many it closed.
func (l *Live) Sweep(store jam.Store, now time.Time, log *slog.Logger) int {
	n := 0
	for _, e := range l.users(func(*liveEntry) bool { return true }) {
		inst, ok := store.GetInstance(e.Session)
		reason := "studio gone"
		if ok {
			if _, allowed := collab.Decide(store, now, collab.Principal{UserID: e.userID}, inst, collab.SSH); allowed {
				continue
			}
			reason = revokeReason(store, now, e)
		}
		e.close()
		n++
		log.Info("ssh tunnel revoked", "user", e.User, "session", e.Session, "right", "ssh", "reason", reason)
	}
	return n
}

// revokeReason names why e's user lost ssh, as far as the store tells.
func revokeReason(store jam.Store, now time.Time, e *liveEntry) string {
	if u, ok := store.GetUser(e.userID); !ok || u.Status != jam.StatusLive {
		return "user removed"
	}
	for _, g := range store.ShareGrants(e.Session) {
		if g.UserID == e.userID && slices.Contains(g.Rights, string(collab.SSH)) && !now.Before(g.ExpiresAt) {
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
