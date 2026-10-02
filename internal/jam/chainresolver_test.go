package jam

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChainResolverRoutesPoolCredToPoolByIdentity(t *testing.T) {
	far := time.Now().Add(time.Hour)
	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccount(PoolAccount{Name: "a", AccessToken: "real-token", ExpiresAt: far}); err != nil {
		t.Fatal(err)
	}
	cr := NewChainResolver(fakeCreds{"git-pat": "PAT"}, NewPool(st), "anthropic-sub")

	got, err := cr.ResolveFor("anthropic-sub", HashToken("cove-identity"))
	if err != nil || got != "real-token" {
		t.Fatalf("pool cred: got %q err %v, want real-token", got, err)
	}
	// Non-pool creds delegate to the base (identity ignored).
	if got, _ := cr.ResolveFor("git-pat", "whatever"); got != "PAT" {
		t.Fatalf("git cred should delegate to base, got %q", got)
	}
}

func TestChainResolverBarePoolResolveFailsClosed(t *testing.T) {
	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatal(err)
	}
	cr := NewChainResolver(fakeCreds{}, NewPool(st), "anthropic-sub")
	if _, err := cr.Resolve("anthropic-sub"); err == nil {
		t.Fatal("bare Resolve of a pool cred must fail (identity required)")
	}
	// A non-pool cred still resolves through the base.
	if _, err := cr.Resolve("git-pat"); err != nil {
		// fakeCreds returns "" and nil for unknown; ensure no spurious error path.
		t.Fatalf("non-pool Resolve should delegate: %v", err)
	}
}

func TestBrokerInjectsPoolTokenAndPreservesBeta(t *testing.T) {
	var gotAuth, gotBeta string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	store := NewMemStore()
	tok, _ := MintToken()
	mustCreateProject(t, store, "ACME")
	if err := store.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(Actor{ID: "spider-18", TokenHash: HashToken(tok), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDestination(Destination{
		Name: "anthropic", Route: "/anthropic/", Upstream: up.URL,
		IdentityIn: ApplyBearer, CredName: "anthropic-sub", Apply: ApplyBearer,
	}); err != nil {
		t.Fatal(err)
	}

	ps, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.SetAccount(PoolAccount{Name: "a", AccessToken: "real-token", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	creds := NewChainResolver(fakeCreds{}, NewPool(ps), "anthropic-sub")

	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b := NewBroker(store, creds, log)

	req := httptest.NewRequest("POST", "/anthropic/v1/messages?beta=true", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer real-token" {
		t.Fatalf("upstream Authorization = %q, want Bearer real-token", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta not preserved: %q", gotBeta)
	}
	if strings.Contains(logbuf.String(), "real-token") || strings.Contains(logbuf.String(), tok) {
		t.Fatal("secret material leaked into logs")
	}
}
