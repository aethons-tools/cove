package jam

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// MaxPushSubscriptionsPerUser caps a user's stored subscriptions; adding more
// evicts the oldest by CreatedAt.
const MaxPushSubscriptionsPerUser = 20

// PushSubscription is one browser's Web Push endpoint for one user.
type PushSubscription struct {
	Endpoint  string
	UserID    ident.ID
	P256dh    string
	Auth      string
	UserAgent string
	CreatedAt time.Time
	LastOKAt  time.Time
}

// PushSubscriptionStore is the Store's push-subscription table.
type PushSubscriptionStore interface {
	// PutPushSubscription upserts by Endpoint (a different user takes the
	// endpoint over) and evicts the user's oldest beyond the cap.
	PutPushSubscription(s PushSubscription) error
	// DeletePushSubscription drops one endpoint (no error when absent).
	DeletePushSubscription(endpoint string) error
	// DeleteUserPushSubscription drops the endpoint only if user owns it,
	// reporting whether it did.
	DeleteUserPushSubscription(user ident.ID, endpoint string) (bool, error)
	// PushSubscriptions lists a user's subscriptions, oldest first.
	PushSubscriptions(user ident.ID) []PushSubscription
	// TouchPushSubscription sets LastOKAt (no error when absent).
	TouchPushSubscription(endpoint string, at time.Time) error
}

// preparePushSubscription validates s and returns the endpoints its
// insertion evicts. Caller holds mu.
func (m *memState) preparePushSubscription(s PushSubscription) ([]string, error) {
	if s.Endpoint == "" || s.UserID == "" {
		return nil, errors.New("push subscription: endpoint and user id are required")
	}
	if _, err := m.liveUser(s.UserID); err != nil {
		return nil, fmt.Errorf("push subscription: %w", err)
	}
	var mine []PushSubscription
	for _, p := range m.pushSubs {
		if p.UserID == s.UserID && p.Endpoint != s.Endpoint {
			mine = append(mine, p)
		}
	}
	over := len(mine) + 1 - MaxPushSubscriptionsPerUser
	if over <= 0 {
		return nil, nil
	}
	slices.SortFunc(mine, cmpPushSub)
	out := make([]string, over)
	for i := range over {
		out[i] = mine[i].Endpoint
	}
	return out, nil
}

func cmpPushSub(a, b PushSubscription) int {
	return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.Endpoint, b.Endpoint))
}

func (m *memState) applyPutPushSubscription(s PushSubscription, evicted []string) {
	m.pushSubs[s.Endpoint] = s
	for _, e := range evicted {
		delete(m.pushSubs, e)
	}
}

func (m *memState) applyDeletePushSubscription(endpoint string) {
	delete(m.pushSubs, endpoint)
}

// applyDropPushSubscriptions deletes every subscription of user (RemoveUser).
func (m *memState) applyDropPushSubscriptions(user ident.ID) {
	for e, p := range m.pushSubs {
		if p.UserID == user {
			delete(m.pushSubs, e)
		}
	}
}

func (m *memState) PushSubscriptions(user ident.ID) []PushSubscription {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []PushSubscription
	for _, p := range m.pushSubs {
		if p.UserID == user {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, cmpPushSub)
	return out
}

func (fs *MemStore) PutPushSubscription(s PushSubscription) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	evicted, err := fs.preparePushSubscription(s)
	if err != nil {
		return err
	}
	fs.applyPutPushSubscription(s, evicted)
	return nil
}

func (fs *MemStore) DeletePushSubscription(endpoint string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.applyDeletePushSubscription(endpoint)
	return nil
}

func (fs *MemStore) DeleteUserPushSubscription(user ident.ID, endpoint string) (bool, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if p, ok := fs.pushSubs[endpoint]; !ok || p.UserID != user {
		return false, nil
	}
	fs.applyDeletePushSubscription(endpoint)
	return true, nil
}

func (fs *MemStore) TouchPushSubscription(endpoint string, at time.Time) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if p, ok := fs.pushSubs[endpoint]; ok {
		p.LastOKAt = at
		fs.pushSubs[endpoint] = p
	}
	return nil
}
