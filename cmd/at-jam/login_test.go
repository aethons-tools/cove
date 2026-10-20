package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// fakeAuth0 serves discovery + device-code + a token endpoint that immediately
// returns a JWT carrying the given sub, and an ID token for it.
func fakeAuth0(t *testing.T, sub string) *httptest.Server {
	return fakeIdP(t, sub, testIDToken(sub))
}

// testIDToken is the (unsigned) ID token fakeAuth0 issues for sub.
func testIDToken(sub string) string {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return b64(`{"alg":"RS256"}`) + "." + b64(`{"sub":"`+sub+`","aud":"cid","exp":`+strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10)+`}`) + ".idsig"
}

// fakeIdP is fakeAuth0 with the token response's id_token set to idToken
// (omitted when empty).
func fakeIdP(t *testing.T, sub, idToken string) *httptest.Server {
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
		resp := map[string]any{"access_token": jwt, "expires_in": 3600}
		if idToken != "" {
			resp["id_token"] = idToken
		}
		json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	return srv
}

// jamWithLogin stands up a Jam admin API whose login-config points at the
// given issuer.
func jamWithLogin(t *testing.T, issuer string) *httptest.Server {
	return jamWithLoginScope(t, issuer, "openid")
}

// jamWithLoginScope is jamWithLogin advertising the given device scope.
func jamWithLoginScope(t *testing.T, issuer, scope string) *httptest.Server {
	t.Helper()
	store := jam.NewMemStore()
	lc := &jam.OperatorLoginConfig{Issuer: issuer, Audience: "https://jam.test/api", ClientID: "cid", Scope: scope}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestLoginCachesToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	idp := fakeAuth0(t, "auth0|alice")
	hb := jamWithLogin(t, idp.URL)

	var out, errb bytes.Buffer
	code := run([]string{"login", "--admin-url", hb.URL}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("login exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "USER-CODE-1") {
		t.Fatalf("login output missing user code:\n%s", out.String())
	}
	got, ok := loadToken("default")
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
	if _, ok := loadToken("default"); ok {
		t.Fatal("token still cached after logout")
	}
}

func TestLoginPersistsAdminURLAndScopesByApp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	idp := fakeAuth0(t, "auth0|bob")
	hb := jamWithLogin(t, idp.URL)

	var out, errb bytes.Buffer
	if code := run([]string{"login", "--app", "prod", "--admin-url", hb.URL}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("login exit = %d, stderr=%s", code, errb.String())
	}
	// --admin-url is persisted into the prod app's settings
	if s := loadSettings("prod"); s.AdminURL != hb.URL {
		t.Fatalf("login did not persist admin-url: %+v", s)
	}
	// token is cached under the prod app, not default
	if _, ok := loadToken("prod"); !ok {
		t.Fatal("prod token not cached")
	}
	if _, ok := loadToken("default"); ok {
		t.Fatal("default app should have no token")
	}
}

func TestCommandUsesCachedTokenAsBearer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("[]"))
	}))
	defer ts.Close()

	// the default app's cached token is used for default-app commands …
	if err := saveToken("default", cachedToken{AccessToken: "CACHED-JWT", Sub: "s", Expiry: time.Now().Add(time.Hour), AdminURL: ts.URL}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{"destination", "list", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("destination list exit = %d, stderr=%s", code, errb.String())
	}
	if gotAuth != "Bearer CACHED-JWT" {
		t.Fatalf("Authorization = %q, want Bearer CACHED-JWT", gotAuth)
	}

	// … but a DIFFERENT app (no token cached for it) sends nothing — per-app token
	// files are the scoping boundary, so one Jam's token never leaks to another.
	gotAuth = "sentinel"
	if code := run([]string{"destination", "list", "--app", "other", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("destination list exit = %d, stderr=%s", code, errb.String())
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty (the 'other' app has no cached token)", gotAuth)
	}
}

func TestEnvTokenShadowWarning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AT_JAM_ADMIN_TOKEN", "ENV-TOK")
	if err := saveToken("default", cachedToken{AccessToken: "CACHED", Expiry: time.Now().Add(time.Hour), AdminURL: "http://x"}); err != nil {
		t.Fatal(err)
	}
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("[]"))
	}))
	defer ts.Close()

	var out, errb bytes.Buffer
	if code := run([]string{"destination", "list", "--admin-url", ts.URL}, os.Getenv, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	// env wins over cache (documented precedence) …
	if gotAuth != "Bearer ENV-TOK" {
		t.Fatalf("Authorization = %q, want Bearer ENV-TOK (env overrides cache)", gotAuth)
	}
	// … but the shadowing is called out, so a stale env var isn't a silent footgun.
	if !strings.Contains(errb.String(), "AT_JAM_ADMIN_TOKEN is set") {
		t.Fatalf("expected a shadow warning on stderr, got: %q", errb.String())
	}
}

func TestLoginNotOIDCGated(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := jam.NewMemStore()
	// nil login config → /admin/login-config 404
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	code := run([]string{"login", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("login against non-OIDC Jam exit = %d, want 0; stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "not OIDC-gated") {
		t.Fatalf("output = %q, want a 'not OIDC-gated' message", out.String())
	}
}

func TestLoginParticipantCachesIDTokenSeparately(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	idp := fakeAuth0(t, "auth0|alice")
	hb := jamWithLogin(t, idp.URL)

	var out, errb bytes.Buffer
	if code := run([]string{"login", "--participant", "--admin-url", hb.URL}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("login --participant exit = %d, stderr=%s", code, errb.String())
	}
	if strings.Contains(out.String()+errb.String(), ".idsig") || strings.Contains(out.String()+errb.String(), ".sig") {
		t.Fatalf("login printed a token:\n%s%s", out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "auth0|alice") {
		t.Fatalf("output = %q, want the signed-in subject", out.String())
	}
	// The operator profile is untouched: a participant login is not an operator login.
	if _, ok := loadToken("default"); ok {
		t.Fatal("participant login cached an operator token")
	}
	got, ok := loadTokenAt(participantTokenPath("default"))
	if !ok || got.IDToken == "" || got.AccessToken != "" || got.Sub != "auth0|alice" || got.Expiry.IsZero() || got.AdminURL != hb.URL {
		t.Fatalf("participant token = %+v ok=%v", got, ok)
	}
	tok, err := resolveParticipantToken("default")
	if err != nil || tok != got.IDToken {
		t.Fatalf("resolveParticipantToken = %q, %v", tok, err)
	}

	// logout --participant clears only the participant profile.
	if err := saveToken("default", cachedToken{AccessToken: "OP", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"logout", "--participant"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("logout --participant exit = %d", code)
	}
	if _, err := resolveParticipantToken("default"); err == nil {
		t.Fatal("participant token still cached after logout --participant")
	}
	if _, ok := loadToken("default"); !ok {
		t.Fatal("logout --participant cleared the operator token")
	}
}

func TestLoginParticipantRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		idToken, scope, want string
	}{
		"no id token":        {"", "openid", "no ID token"},
		"scope lacks openid": {testIDToken("auth0|alice"), "offline_access", "openid"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			idp := fakeIdP(t, "auth0|alice", tc.idToken)
			hb := jamWithLoginScope(t, idp.URL, tc.scope)
			var out, errb bytes.Buffer
			if code := run([]string{"login", "--participant", "--admin-url", hb.URL}, func(string) string { return "" }, &out, &errb); code == 0 {
				t.Fatalf("login --participant exit = 0, want failure")
			}
			if !strings.Contains(errb.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", errb.String(), tc.want)
			}
			if _, ok := loadTokenAt(participantTokenPath("default")); ok {
				t.Fatal("a refused participant login cached a token")
			}
		})
	}
}
