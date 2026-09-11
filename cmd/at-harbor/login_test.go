package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// fakeAuth0 serves discovery + device-code + a token endpoint that immediately
// returns a JWT carrying the given sub.
func fakeAuth0(t *testing.T, sub string) *httptest.Server {
	t.Helper()
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	jwt := b64(`{"alg":"RS256"}`) + "." + b64(`{"sub":"`+sub+`","exp":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+`}`) + ".sig"
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": base, "device_authorization_endpoint": base + "/dev", "token_endpoint": base + "/tok",
		})
	})
	mux.HandleFunc("/dev", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "D", "user_code": "USER-CODE-1", "verification_uri_complete": base + "/act?c=1", "interval": 1, "expires_in": 600,
		})
	})
	mux.HandleFunc("/tok", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": jwt, "expires_in": 3600})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	return srv
}

// harborWithLogin stands up a harbor admin API whose login-config points at the
// given issuer.
func harborWithLogin(t *testing.T, issuer string) *httptest.Server {
	t.Helper()
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	lc := &harbor.OperatorLoginConfig{Issuer: issuer, Audience: "https://harbor.test/api", ClientID: "cid", Scope: "openid"}
	h := harbor.NewAdminHandler(store, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestLoginCachesToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	idp := fakeAuth0(t, "auth0|alice")
	hb := harborWithLogin(t, idp.URL)

	var out, errb bytes.Buffer
	code := run([]string{"login", "--admin-url", hb.URL}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("login exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "USER-CODE-1") {
		t.Fatalf("login output missing user code:\n%s", out.String())
	}
	got, ok := loadToken()
	if !ok || got.Sub != "auth0|alice" {
		t.Fatalf("cached token = %+v ok=%v", got, ok)
	}

	// whoami prints the sub
	out.Reset()
	if code := run([]string{"whoami"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("whoami exit = %d", code)
	}
	if !strings.Contains(out.String(), "auth0|alice") {
		t.Fatalf("whoami output = %q", out.String())
	}

	// logout clears it
	if code := run([]string{"logout"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("logout exit = %d", code)
	}
	if _, ok := loadToken(); ok {
		t.Fatal("token still cached after logout")
	}
}

func TestCommandUsesCachedTokenAsBearer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveToken(cachedToken{AccessToken: "CACHED-JWT", Sub: "s", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("[]"))
	}))
	defer ts.Close()

	var out, errb bytes.Buffer
	// no --token, no env → must fall back to the cached token
	code := run([]string{"destination", "list", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("destination list exit = %d, stderr=%s", code, errb.String())
	}
	if gotAuth != "Bearer CACHED-JWT" {
		t.Fatalf("Authorization = %q, want Bearer CACHED-JWT", gotAuth)
	}
}
