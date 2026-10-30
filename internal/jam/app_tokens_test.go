package jam

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/ident"
)

// touchCounter counts TouchAppToken calls on the wrapped Store.
type touchCounter struct {
	Store
	touches int
}

func (c *touchCounter) TouchAppToken(id ident.ID, lastUsed, expires time.Time) error {
	c.touches++
	return c.Store.TouchAppToken(id, lastUsed, expires)
}

var appTokenT0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// putTestAppToken stores a fresh token for a new user alice and returns it and
// its raw value.
func putTestAppToken(t *testing.T, s Store, operatorID string) (AppToken, string) {
	t.Helper()
	u, err := s.CreateUser(User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	tok, raw, err := NewAppToken(u.ID, "VS Code", operatorID, appTokenT0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAppToken(tok); err != nil {
		t.Fatal(err)
	}
	return tok, raw
}

func TestNewAppTokenHashOnly(t *testing.T) {
	tok, raw, err := NewAppToken("usr_x", "VS Code", "", appTokenT0)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || tok.Hash != HashToken(raw) {
		t.Fatalf("hash %q, raw %q", tok.Hash, raw)
	}
	for _, f := range []string{string(tok.ID), string(tok.UserID), tok.Hash, tok.Label, tok.OperatorID} {
		if strings.Contains(f, raw) {
			t.Fatalf("raw token stored in %q", f)
		}
	}
	if !tok.ExpiresAt.Equal(appTokenT0.Add(AppTokenTTL)) || !tok.CreatedAt.Equal(appTokenT0) || !tok.LastUsedAt.Equal(appTokenT0) {
		t.Fatalf("times %+v", tok)
	}
	if tok.ID.Kind() != ident.AppToken || tok.UserID != "usr_x" || tok.Operator() {
		t.Fatalf("token %+v", tok)
	}
	if !tok.Expired(tok.ExpiresAt) || tok.Expired(tok.ExpiresAt.Add(-time.Nanosecond)) {
		t.Fatal("Expired boundary")
	}
}

func TestResolveAppTokenMemberSlides(t *testing.T) {
	s := NewMemStore()
	tok, raw := putTestAppToken(t, s, "")
	use := appTokenT0.Add(6 * time.Minute)
	got, u, err := ResolveAppToken(s, raw, use, nil)
	if err != nil || got.ID != tok.ID || u.ID != tok.UserID {
		t.Fatalf("resolve: %+v %+v %v", got, u, err)
	}
	stored, _ := s.AppTokenByID(tok.ID)
	if !stored.ExpiresAt.Equal(use.Add(AppTokenTTL)) || !stored.LastUsedAt.Equal(use) {
		t.Fatalf("stored %+v", stored)
	}
}

func TestResolveAppTokenOperatorFixed(t *testing.T) {
	s := NewMemStore()
	tok, raw := putTestAppToken(t, s, "op-1")
	use := appTokenT0.Add(6 * time.Minute)
	got, _, err := ResolveAppToken(s, raw, use, nil)
	if err != nil || !got.Operator() || got.OperatorID != "op-1" {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	stored, _ := s.AppTokenByID(tok.ID)
	if !stored.ExpiresAt.Equal(tok.ExpiresAt) || !stored.LastUsedAt.Equal(use) {
		t.Fatalf("stored %+v", stored)
	}
}

func TestResolveAppTokenTouchThrottled(t *testing.T) {
	s := &touchCounter{Store: NewMemStore()}
	_, raw := putTestAppToken(t, s, "")
	for _, d := range []time.Duration{time.Minute, 4 * time.Minute} {
		if _, _, err := ResolveAppToken(s, raw, appTokenT0.Add(d), nil); err != nil {
			t.Fatal(err)
		}
	}
	if s.touches != 0 {
		t.Fatalf("touches = %d, want 0", s.touches)
	}
	if _, _, err := ResolveAppToken(s, raw, appTokenT0.Add(AppTokenTouchEvery), nil); err != nil {
		t.Fatal(err)
	}
	if s.touches != 1 {
		t.Fatalf("touches = %d, want 1", s.touches)
	}
}

func TestResolveAppTokenExpired(t *testing.T) {
	s := NewMemStore()
	tok, raw := putTestAppToken(t, s, "")
	if _, _, err := ResolveAppToken(s, raw, tok.ExpiresAt, nil); err != ErrAppTokenInvalid {
		t.Fatalf("expired: %v", err)
	}
	if _, _, err := ResolveAppToken(s, "not-a-token", appTokenT0, nil); err != ErrAppTokenInvalid {
		t.Fatalf("unknown: %v", err)
	}
}

func TestResolveAppTokenRemovedUser(t *testing.T) {
	s := &removedUserStore{Store: NewMemStore()}
	tok, raw := putTestAppToken(t, s, "")
	s.removed = tok.UserID // the token row outlives the user here
	if _, _, err := ResolveAppToken(s, raw, appTokenT0, nil); err != ErrAppTokenInvalid {
		t.Fatalf("removed user: %v", err)
	}
}

// removedUserStore reports user removed while keeping its app tokens, as a
// race between a lookup and RemoveUser could.
type removedUserStore struct {
	Store
	removed ident.ID
}

func (s *removedUserStore) GetUser(id ident.ID) (User, bool) {
	u, ok := s.Store.GetUser(id)
	if ok && id == s.removed {
		u.Status = StatusRemoved
	}
	return u, ok
}

func TestCleanAppTokenLabel(t *testing.T) {
	long := strings.Repeat("é", 100)
	for _, c := range []struct{ in, want string }{
		{"", "VS Code"},
		{"   ", "VS Code"},
		{"  VS Code on mac  ", "VS Code on mac"},
		{"\x1b[31mevil", "[31mevil"},
		{"a\nb\tc\u200bd\u202e\a", "abcd"},
		{long, strings.Repeat("é", AppTokenLabelMax)},
	} {
		got := CleanAppTokenLabel(c.in)
		if got != c.want {
			t.Errorf("CleanAppTokenLabel(%q) = %q, want %q", c.in, got, c.want)
		}
		if utf8.RuneCountInString(got) > AppTokenLabelMax {
			t.Errorf("too long: %d", utf8.RuneCountInString(got))
		}
	}
}
