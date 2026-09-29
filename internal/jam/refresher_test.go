package jam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshDueRotatesTokenWithinMargin(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var gotGrant, gotRefresh, gotClient, gotScope, gotCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		var body struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
			ClientID     string `json:"client_id"`
			Scope        string `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotGrant, gotRefresh, gotClient, gotScope = body.GrantType, body.RefreshToken, body.ClientID, body.Scope
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600,
		})
	}))
	defer ts.Close()

	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatal(err)
	}
	// expires in 5m; margin 15m ⇒ due.
	if err := st.SetAccount(PoolAccount{Name: "a", AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}

	r := NewRefresher(st, RefresherOptions{
		HTTPClient: ts.Client(), TokenURL: ts.URL, ClientID: "test-client", Scope: "s1 s2",
		Now: func() time.Time { return now }, Margin: 15 * time.Minute, Log: testLogger(),
	})
	if err := r.RefreshDue(context.Background()); err != nil {
		t.Fatalf("RefreshDue: %v", err)
	}
	if gotGrant != "refresh_token" || gotRefresh != "old-refresh" || gotClient != "test-client" || gotScope != "s1 s2" {
		t.Fatalf("request shape: grant=%q refresh=%q client=%q scope=%q", gotGrant, gotRefresh, gotClient, gotScope)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotCT)
	}
	accts, _ := st.Accounts()
	if accts[0].AccessToken != "new-access" || accts[0].RefreshToken != "new-refresh" {
		t.Fatalf("tokens not rotated: %+v", accts[0])
	}
	if !accts[0].ExpiresAt.Equal(now.Add(3600 * time.Second)) {
		t.Fatalf("expiry not advanced: %v", accts[0].ExpiresAt)
	}
}

func TestRefreshDueSkipsFarFromExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer ts.Close()
	st, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccount(PoolAccount{Name: "a", AccessToken: "old", RefreshToken: "r", ExpiresAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r := NewRefresher(st, RefresherOptions{HTTPClient: ts.Client(), TokenURL: ts.URL, ClientID: "c", Now: func() time.Time { return now }, Margin: 15 * time.Minute, Log: testLogger()})
	if err := r.RefreshDue(context.Background()); err != nil {
		t.Fatalf("RefreshDue: %v", err)
	}
	if called {
		t.Fatal("account far from expiry must not be refreshed")
	}
}

func TestRefresherDefaultsToProbedConstants(t *testing.T) {
	r := NewRefresher(nil, RefresherOptions{Log: testLogger()})
	if r.opt.TokenURL != defaultTokenURL || r.opt.ClientID != defaultClientID || r.opt.Scope != defaultScope {
		t.Fatalf("defaults not applied: url=%q client=%q scope=%q", r.opt.TokenURL, r.opt.ClientID, r.opt.Scope)
	}
}
