package apptoken

import (
	"context"
	"sync"

	"github.com/aethons-tools/cove/internal/ident"
)

// Live tracks the contexts of in-flight bearer requests by app-token id, so
// revoking a token ends its open requests (an event stream returns when its
// request context is done).
type Live struct {
	mu sync.Mutex
	m  map[ident.ID]map[*liveReq]struct{}
}

type liveReq struct{ cancel context.CancelFunc }

// NewLive returns an empty registry.
func NewLive() *Live { return &Live{m: map[ident.ID]map[*liveReq]struct{}{}} }

// Track derives a cancellable ctx for one request of token id; call done when
// the request ends.
func (l *Live) Track(ctx context.Context, id ident.ID) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	e := &liveReq{cancel: cancel}
	l.mu.Lock()
	if l.m[id] == nil {
		l.m[id] = map[*liveReq]struct{}{}
	}
	l.m[id][e] = struct{}{}
	l.mu.Unlock()
	return ctx, func() {
		l.mu.Lock()
		if set := l.m[id]; set != nil {
			delete(set, e)
			if len(set) == 0 {
				delete(l.m, id)
			}
		}
		l.mu.Unlock()
		cancel()
	}
}

// Revoke cancels every tracked request of id.
func (l *Live) Revoke(id ident.ID) {
	l.mu.Lock()
	set := l.m[id]
	delete(l.m, id)
	l.mu.Unlock()
	for e := range set {
		e.cancel()
	}
}

// Sweep revokes every tracked id that valid rejects (gone, expired, or its
// user removed).
func (l *Live) Sweep(valid func(ident.ID) bool) {
	l.mu.Lock()
	ids := make([]ident.ID, 0, len(l.m))
	for id := range l.m {
		ids = append(ids, id)
	}
	l.mu.Unlock()
	for _, id := range ids {
		if !valid(id) {
			l.Revoke(id)
		}
	}
}

// count is the number of tracked requests (tests).
func (l *Live) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, set := range l.m {
		n += len(set)
	}
	return n
}
