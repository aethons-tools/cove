package jam

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runOAuthBetaCase builds a broker whose sole destination is an anthropic bearer
// destination (OAuthBeta = oauthBeta), sends one request carrying betaIn as its
// anthropic-beta header, and returns the anthropic-beta the upstream received.
func runOAuthBetaCase(t *testing.T, oauthBeta bool, betaIn string) string {
	t.Helper()
	var gotBeta string
	seen := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta, seen = r.Header.Get("anthropic-beta"), true
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	store := NewMemStore()
	tok, _ := MintToken()
	mustCreateProject(t, store, "ACME")
	if err := store.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(Actor{ID: "spider", TokenHash: HashToken(tok), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDestination(Destination{
		Name: "anthropic", Route: "/anthropic/", Upstream: up.URL,
		IdentityIn: ApplyBearer, CredName: "anthropic-sub", Apply: ApplyBearer, OAuthBeta: oauthBeta,
	}); err != nil {
		t.Fatal(err)
	}
	b := NewBroker(store, fakeCreds{"anthropic-sub": "REAL"}, testLogger())

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	if betaIn != "" {
		req.Header.Set("anthropic-beta", betaIn)
	}
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != 200 || !seen {
		t.Fatalf("request did not reach upstream (code=%d seen=%v)", rec.Code, seen)
	}
	return gotBeta
}

func TestOAuthBetaAppendedWhenFlagSet(t *testing.T) {
	got := runOAuthBetaCase(t, true, "claude-code-20250219,context-1m-2025-08-07")
	if !strings.Contains(got, "oauth-2025-04-20") {
		t.Fatalf("anthropic-beta must gain oauth-2025-04-20, got %q", got)
	}
	if !strings.Contains(got, "claude-code-20250219") {
		t.Fatalf("existing betas must be preserved, got %q", got)
	}
}

func TestOAuthBetaNotDuplicated(t *testing.T) {
	got := runOAuthBetaCase(t, true, "claude-code-20250219,oauth-2025-04-20")
	if n := strings.Count(got, "oauth-2025-04-20"); n != 1 {
		t.Fatalf("oauth beta duplicated (%d): %q", n, got)
	}
}

func TestOAuthBetaAddedWhenNoBetaHeader(t *testing.T) {
	got := runOAuthBetaCase(t, true, "")
	if got != "oauth-2025-04-20" {
		t.Fatalf("with no inbound beta, want just the oauth beta, got %q", got)
	}
}

func TestOAuthBetaUntouchedWhenFlagUnset(t *testing.T) {
	got := runOAuthBetaCase(t, false, "claude-code-20250219")
	if strings.Contains(got, "oauth-2025-04-20") {
		t.Fatalf("without the flag the broker must not add the oauth beta, got %q", got)
	}
}
