package browserauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeIdP is a minimal RS256 OIDC provider: discovery + JWKS + a token endpoint
// that mints an id_token (with a caller-set nonce) and an access token.
type fakeIdP struct {
	url     string
	key     *rsa.PrivateKey
	kid     string
	idNonce string // nonce embedded into the next minted id_token
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, kid: "test-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.url, "jwks_uri": f.url + "/jwks",
			"authorization_endpoint": f.url + "/authorize", "token_endpoint": f.url + "/token",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kid,
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		exp := time.Now().Add(time.Hour)
		idClaims := map[string]any{
			"iss": f.url, "aud": "cid", "sub": "auth0|alice", "nonce": f.idNonce,
			"iat": time.Now().Unix(), "exp": exp.Unix(),
		}
		acClaims := map[string]any{
			"iss": f.url, "aud": "aud", "sub": "auth0|alice", "scope": "harbor:admin",
			"iat": time.Now().Unix(), "exp": exp.Unix(),
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": f.mint(t, idClaims), "access_token": f.mint(t, acClaims),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *fakeIdP) mint(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr := map[string]any{"alg": "RS256", "typ": "JWT", "kid": f.kid}
	enc := func(v any) string { b, _ := json.Marshal(v); return b64(b) }
	signing := enc(hdr) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mustService(t *testing.T, idp *fakeIdP) *Service {
	t.Helper()
	svc, err := New(context.Background(), RawConfig{Issuer: idp.url, ClientID: "cid", Scope: "openid", Audience: "aud"}, nil, discard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func TestLoginRedirectsAndSetsTempCookies(t *testing.T) {
	idp := newFakeIdP(t)
	svc := mustService(t, idp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/auth/login", nil)
	req.Host = "harbor.test"
	req.TLS = &tls.ConnectionState{}
	svc.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, idp.url+"/authorize") || !strings.Contains(loc, "code_challenge_method=S256") {
		t.Errorf("bad authorize redirect: %s", loc)
	}
	names := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		names[c.Name] = true
		if !c.HttpOnly || !c.Secure || c.Path != "/ui/auth" {
			t.Errorf("temp cookie %s flags wrong: %+v", c.Name, c)
		}
	}
	for _, want := range []string{stateCookie, nonceCookie, pkceCookie} {
		if !names[want] {
			t.Errorf("missing temp cookie %s", want)
		}
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	idp := newFakeIdP(t)
	svc := mustService(t, idp)
	req := httptest.NewRequest("GET", "/ui/auth/callback?state=EVIL&code=x", nil)
	req.AddCookie(&http.Cookie{Name: stateCookie, Value: "REAL|/ui/"})
	rec := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch = %d, want 400", rec.Code)
	}
}

func TestCallbackHappyPathSetsSessionCookie(t *testing.T) {
	idp := newFakeIdP(t)
	svc := mustService(t, idp)

	// Drive login to capture the temp cookies (and the nonce the IdP must echo).
	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest("GET", "/ui/auth/login", nil)
	loginReq.Host, loginReq.TLS = "harbor.test", &tls.ConnectionState{}
	svc.Routes().ServeHTTP(loginRec, loginReq)
	cookies := loginRec.Result().Cookies()
	var stateVal, nonce string
	for _, c := range cookies {
		switch c.Name {
		case stateCookie:
			stateVal = strings.SplitN(c.Value, "|", 2)[0]
		case nonceCookie:
			nonce = c.Value
		}
	}
	idp.idNonce = nonce // the fake /token embeds this nonce in the id_token

	cbRec := httptest.NewRecorder()
	cbReq := httptest.NewRequest("GET", "/ui/auth/callback?state="+stateVal+"&code=good", nil)
	cbReq.Host, cbReq.TLS = "harbor.test", &tls.ConnectionState{}
	for _, c := range cookies {
		cbReq.AddCookie(c)
	}
	svc.Routes().ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound || cbRec.Header().Get("Location") != "/ui/" {
		t.Fatalf("callback = %d %q, want 302 /ui/", cbRec.Code, cbRec.Header().Get("Location"))
	}
	var sess *http.Cookie
	for _, c := range cbRec.Result().Cookies() {
		if c.Name == SessionCookie {
			sess = c
		}
	}
	if sess == nil || sess.Value == "" {
		t.Fatal("no session cookie set")
	}
	if !sess.HttpOnly || !sess.Secure || sess.SameSite != http.SameSiteLaxMode || sess.Path != "/ui" {
		t.Errorf("session cookie flags wrong: %+v", sess)
	}
}

func TestReturnToOpenRedirectGuard(t *testing.T) {
	for _, bad := range []string{"//evil.com", "https://evil.com", "/\\evil", "/admin/x", ""} {
		if got := safeReturnTo(bad); got != "/ui/" {
			t.Errorf("safeReturnTo(%q) = %q, want /ui/", bad, got)
		}
	}
	if got := safeReturnTo("/ui/coves"); got != "/ui/coves" {
		t.Errorf("safeReturnTo(/ui/coves) = %q, want /ui/coves", got)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	idp := newFakeIdP(t)
	svc := mustService(t, idp)
	rec := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "/ui/auth/logout", nil))
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout must clear the session cookie (MaxAge<0)")
	}
}
