package jam

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestPool(t *testing.T, accts ...PoolAccount) *Pool {
	t.Helper()
	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatalf("NewFilePoolStore: %v", err)
	}
	for _, a := range accts {
		if err := st.SetAccount(a); err != nil {
			t.Fatalf("SetAccount: %v", err)
		}
	}
	return NewPool(st)
}

func poolTokenFor(t *testing.T, p *Pool, id string) string {
	t.Helper()
	tok, err := p.TokenFor(id)
	if err != nil {
		t.Fatalf("TokenFor(%q): %v", id, err)
	}
	return tok
}

func TestTokenForBindsOnceAndIsStable(t *testing.T) {
	far := time.Now().Add(time.Hour)
	p := newTestPool(t, PoolAccount{Name: "a", AccessToken: "tok-a", ExpiresAt: far})
	if got1, got2 := poolTokenFor(t, p, "id-hash-1"), poolTokenFor(t, p, "id-hash-1"); got1 != "tok-a" || got2 != "tok-a" {
		t.Fatalf("want tok-a twice, got %q then %q", got1, got2)
	}
}

func TestTokenForSpreadsAcrossAccounts(t *testing.T) {
	far := time.Now().Add(time.Hour)
	p := newTestPool(t,
		PoolAccount{Name: "a", AccessToken: "tok-a", ExpiresAt: far},
		PoolAccount{Name: "b", AccessToken: "tok-b", ExpiresAt: far},
	)
	seen := map[string]bool{}
	seen[poolTokenFor(t, p, "id-1")] = true
	seen[poolTokenFor(t, p, "id-2")] = true
	if !seen["tok-a"] || !seen["tok-b"] {
		t.Fatalf("two identities should spread across both accounts, saw %v", seen)
	}
}

func TestTokenForReflectsRotatedToken(t *testing.T) {
	far := time.Now().Add(time.Hour)
	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.SetAccount(PoolAccount{Name: "a", AccessToken: "old", ExpiresAt: far}); err != nil {
		t.Fatalf("SetAccount: %v", err)
	}
	p := NewPool(st)
	poolTokenFor(t, p, "id-1") // bind to "a"
	if err := st.SetAccount(PoolAccount{Name: "a", AccessToken: "new", ExpiresAt: far}); err != nil {
		t.Fatalf("SetAccount rotate: %v", err)
	}
	if got := poolTokenFor(t, p, "id-1"); got != "new" {
		t.Fatalf("want rotated token new, got %q", got)
	}
}

func TestTokenForNoAccounts(t *testing.T) {
	p := newTestPool(t)
	if _, err := p.TokenFor("id-1"); err == nil {
		t.Fatal("want error when no accounts configured")
	}
}

func TestFilePoolStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	st, err := NewFilePoolStore(path)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.SetAccount(PoolAccount{Name: "a", AccessToken: "tok-a", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SetAccount: %v", err)
	}
	if err := st.Bind("id-1", "a"); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	st2, err := NewFilePoolStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	b, err := st2.Bindings()
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if b["id-1"] != "a" {
		t.Fatalf("binding not persisted: %v", b)
	}
}
