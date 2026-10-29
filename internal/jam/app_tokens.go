package jam

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aethons-tools/cove/internal/ident"
)

// AppTokenTTL is an app token's lifetime: from use for a member token
// (sliding), from issue for one with operator standing (fixed).
const AppTokenTTL = 30 * 24 * time.Hour

// AppTokenTouchEvery is how often, at most, a token's last use is written.
const AppTokenTouchEvery = 5 * time.Minute

// AppTokenLabelMax caps an app token's label, in runes.
const AppTokenLabelMax = 64

// ErrAppTokenInvalid is every app-token resolution failure: unknown, expired,
// or the user is no longer live.
var ErrAppTokenInvalid = errors.New("invalid or expired app token")

// MaxAppTokensPerUser caps a user's stored app tokens; adding more evicts the
// least recently used.
const MaxAppTokensPerUser = 20

// AppToken is one app token (docs/usage/jam/app-tokens.md): an opaque bearer
// credential a user granted to a client. Only the hash of the raw token is
// kept.
type AppToken struct {
	ID     ident.ID
	UserID ident.ID
	// Hash is HashToken(raw).
	Hash  string
	Label string
	// OperatorID is the operator standing recorded at consent; "" = member.
	OperatorID string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// AppTokenStore is the Store's app-token table.
type AppTokenStore interface {
	// PutAppToken inserts t for a live user. A duplicate id or hash is
	// refused. It evicts the user's least recently used tokens beyond
	// MaxAppTokensPerUser and returns their ids.
	PutAppToken(t AppToken) (evicted []ident.ID, err error)
	AppTokenByHash(hash string) (AppToken, bool)
	AppTokenByID(id ident.ID) (AppToken, bool)
	// AppTokens lists a user's tokens, oldest CreatedAt first.
	AppTokens(user ident.ID) []AppToken
	// DeleteUserAppToken drops the token only if user owns it, reporting
	// whether it did.
	DeleteUserAppToken(user, id ident.ID) (bool, error)
	// TouchAppToken sets LastUsedAt and ExpiresAt (no error when absent).
	TouchAppToken(id ident.ID, lastUsed, expires time.Time) error
	// DeleteExpiredAppTokens drops every token whose ExpiresAt is not after
	// now and returns their ids.
	DeleteExpiredAppTokens(now time.Time) ([]ident.ID, error)
}

// prepareAppToken validates t and returns the ids its insertion evicts.
// Caller holds mu.
func (m *memState) prepareAppToken(t AppToken) ([]ident.ID, error) {
	if t.ID == "" || t.UserID == "" || t.Hash == "" {
		return nil, errors.New("app token: id, user id and hash are required")
	}
	if _, err := m.liveUser(t.UserID); err != nil {
		return nil, fmt.Errorf("app token: %w", err)
	}
	if _, ok := m.appTokens[t.ID]; ok {
		return nil, fmt.Errorf("app token: id %s already exists", t.ID)
	}
	if _, ok := m.appTokenByHash[t.Hash]; ok {
		return nil, errors.New("app token: hash already exists")
	}
	var mine []AppToken
	for _, a := range m.appTokens {
		if a.UserID == t.UserID {
			mine = append(mine, a)
		}
	}
	over := len(mine) + 1 - MaxAppTokensPerUser
	if over <= 0 {
		return nil, nil
	}
	slices.SortFunc(mine, func(a, b AppToken) int {
		return cmp.Or(a.LastUsedAt.Compare(b.LastUsedAt), cmp.Compare(a.ID, b.ID))
	})
	out := make([]ident.ID, over)
	for i := range over {
		out[i] = mine[i].ID
	}
	return out, nil
}

func (m *memState) applyPutAppToken(t AppToken, evicted []ident.ID) {
	for _, id := range evicted {
		m.applyDeleteAppToken(id)
	}
	m.appTokens[t.ID] = t
	m.appTokenByHash[t.Hash] = t.ID
}

func (m *memState) applyDeleteAppToken(id ident.ID) {
	if t, ok := m.appTokens[id]; ok {
		delete(m.appTokenByHash, t.Hash)
		delete(m.appTokens, id)
	}
}

// applyDropAppTokens deletes every app token of user (user removal).
func (m *memState) applyDropAppTokens(user ident.ID) {
	for id, t := range m.appTokens {
		if t.UserID == user {
			m.applyDeleteAppToken(id)
		}
	}
}

// expiredAppTokens lists the ids of tokens expired at now. Caller holds mu.
func (m *memState) expiredAppTokens(now time.Time) []ident.ID {
	var out []ident.ID
	for id, t := range m.appTokens {
		if !now.Before(t.ExpiresAt) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func (m *memState) AppTokenByHash(hash string) (AppToken, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.appTokenByHash[hash]
	if !ok {
		return AppToken{}, false
	}
	return m.appTokens[id], true
}

func (m *memState) AppTokenByID(id ident.ID) (AppToken, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.appTokens[id]
	return t, ok
}

func (m *memState) AppTokens(user ident.ID) []AppToken {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []AppToken
	for _, t := range m.appTokens {
		if t.UserID == user {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b AppToken) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}

func (fs *MemStore) PutAppToken(t AppToken) ([]ident.ID, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	evicted, err := fs.prepareAppToken(t)
	if err != nil {
		return nil, err
	}
	fs.applyPutAppToken(t, evicted)
	return evicted, nil
}

func (fs *MemStore) DeleteUserAppToken(user, id ident.ID) (bool, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if t, ok := fs.appTokens[id]; !ok || t.UserID != user {
		return false, nil
	}
	fs.applyDeleteAppToken(id)
	return true, nil
}

func (fs *MemStore) TouchAppToken(id ident.ID, lastUsed, expires time.Time) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if t, ok := fs.appTokens[id]; ok {
		t.LastUsedAt, t.ExpiresAt = lastUsed, expires
		fs.appTokens[id] = t
	}
	return nil
}

func (fs *MemStore) DeleteExpiredAppTokens(now time.Time) ([]ident.ID, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	gone := fs.expiredAppTokens(now)
	for _, id := range gone {
		fs.applyDeleteAppToken(id)
	}
	return gone, nil
}

// NewAppToken mints a token for user, returning the row to store (hash only)
// and the raw token to hand the client once.
func NewAppToken(user ident.ID, label, operatorID string, now time.Time) (AppToken, string, error) {
	raw, err := MintToken()
	if err != nil {
		return AppToken{}, "", err
	}
	now = now.UTC()
	return AppToken{
		ID: ident.New(ident.AppToken), UserID: user, Hash: HashToken(raw), Label: CleanAppTokenLabel(label),
		OperatorID: operatorID, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(AppTokenTTL),
	}, raw, nil
}

// Expired reports whether t has expired at now.
func (t AppToken) Expired(now time.Time) bool { return !now.Before(t.ExpiresAt) }

// Operator reports whether t carries operator standing.
func (t AppToken) Operator() bool { return t.OperatorID != "" }

// CleanAppTokenLabel trims s, drops control and format runes (escapes,
// newlines, bidi overrides), and caps it at AppTokenLabelMax runes; an empty
// result is "VS Code".
func CleanAppTokenLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > AppTokenLabelMax {
		s = strings.TrimSpace(string(r[:AppTokenLabelMax]))
	}
	if s == "" {
		return "VS Code"
	}
	return s
}

// ResolveAppToken looks up the raw bearer token raw and its live user. It
// records the use at most once per AppTokenTouchEvery, sliding a member
// token's expiry; a touch failure is logged and does not fail the request.
func ResolveAppToken(store Store, raw string, now time.Time, log *slog.Logger) (AppToken, User, error) {
	if raw == "" {
		return AppToken{}, User{}, ErrAppTokenInvalid
	}
	t, ok := store.AppTokenByHash(HashToken(raw))
	if !ok || t.Expired(now) {
		return AppToken{}, User{}, ErrAppTokenInvalid
	}
	u, ok := store.GetUser(t.UserID)
	if !ok || u.Status != StatusLive {
		return AppToken{}, User{}, ErrAppTokenInvalid
	}
	if now.Sub(t.LastUsedAt) >= AppTokenTouchEvery {
		t.LastUsedAt = now.UTC()
		if !t.Operator() {
			t.ExpiresAt = t.LastUsedAt.Add(AppTokenTTL)
		}
		if err := store.TouchAppToken(t.ID, t.LastUsedAt, t.ExpiresAt); err != nil && log != nil {
			log.Warn("app token touch failed", "token", t.ID, "err", err)
		}
	}
	return t, u, nil
}
