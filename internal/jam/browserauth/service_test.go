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
	"net/url"
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
			"iss": f.url, "aud": "aud", "sub": "auth0|alice", "scope": "jam:admin",
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
	svc, err := New(context.Background(), RawConfig{Issuer: idp.url, ClientID: "cid", Scope: "openid", Audience: "aud"}, OperatorUIMount(), nil, discard())
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
	req.Host = "jam.test"
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
	loginReq.Host, loginReq.TLS = "jam.test", &tls.ConnectionState{}
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
	cbReq.Host, cbReq.TLS = "jam.test", &tls.ConnectionState{}
	for _, c := range cookies {
		cbReq.AddCookie(c)
	}
	svc.Routes().ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound || cbRec.Header().Get("Location") != "/ui/" {
		t.Fatalf("callback = %d %q, want 302 /ui/", cbRec.Code, cbRec.Header().Get("Location"))
	}
	var sess *http.Cookie
	for _, c := range cbRec.Result().Cookies() {
		if c.Name == OperatorUIMount().SessionCookie {
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
		if got := safeReturnTo(OperatorUIMount(), bad); got != "/ui/" {
			t.Errorf("safeReturnTo(%q) = %q, want /ui/", bad, got)
		}
	}
	if got := safeReturnTo(OperatorUIMount(), "/ui/coves"); got != "/ui/coves" {
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
		if c.Name == OperatorUIMount().SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout must clear the session cookie (MaxAge<0)")
	}
}

// startLogin drives svc's login (with return_to) on host and returns the
// cookies it set, the state value and the return_to the state cookie bound;
// it points the fake IdP at the login's nonce.
func startLogin(t *testing.T, idp *fakeIdP, svc *Service, host, returnTo string) (cookies []*http.Cookie, state, bound string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", svc.mount.Prefix+"/auth/login?return_to="+url.QueryEscape(returnTo), nil)
	req.Host, req.TLS = host, &tls.ConnectionState{}
	svc.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d", rec.Code)
	}
	cookies = rec.Result().Cookies()
	for _, c := range cookies {
		switch c.Name {
		case svc.mount.tempName(stateCookie):
			state, bound, _ = strings.Cut(c.Value, "|")
		case svc.mount.tempName(nonceCookie):
			idp.idNonce = c.Value
		}
	}
	return cookies, state, bound
}

func callbackWith(svc *Service, host, state string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", svc.mount.Prefix+"/auth/callback?state="+url.QueryEscape(state)+"&code=good", nil)
	req.Host, req.TLS = host, &tls.ConnectionState{}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	svc.Routes().ServeHTTP(rec, req)
	return rec
}

// loginAndCallback drives svc's login and then its callback on host,
// returning the callback's recorder and the return_to the state bound.
func loginAndCallback(t *testing.T, idp *fakeIdP, svc *Service, host, returnTo string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	cookies, state, bound := startLogin(t, idp, svc, host, returnTo)
	return callbackWith(svc, host, state, cookies), bound
}

// previewSvc is a Service over PreviewMount whose Complete counts its calls.
func previewSvc(t *testing.T, idp *fakeIdP, calls *int) *Service {
	t.Helper()
	m := PreviewMount(func(w http.ResponseWriter, r *http.Request, _, _, _ string) {
		*calls++
		w.WriteHeader(http.StatusTeapot)
	}, func(string) bool { return true })
	svc, err := New(context.Background(), RawConfig{Issuer: idp.url, ClientID: "cid", Scope: "openid", Audience: "aud"}, m, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestPreviewMountCookiesAreHostPrefixed(t *testing.T) {
	idp := newFakeIdP(t)
	var calls int
	svc := previewSvc(t, idp, &calls)
	cookies, state, _ := startLogin(t, idp, svc, "jam.preview.example", "https://x/")
	names := map[string]bool{}
	for _, c := range cookies {
		names[c.Name] = true
		if !strings.HasPrefix(c.Name, "__Host-") || c.Path != "/" || !c.Secure || !c.HttpOnly || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("login cookie %+v, want __Host-, Path=/, Secure, HttpOnly, no Domain, Lax", c)
		}
	}
	for _, n := range []string{"__Host-jam_oauth_state", "__Host-jam_oauth_nonce", "__Host-jam_oauth_pkce"} {
		if !names[n] {
			t.Errorf("missing %s (got %v)", n, names)
		}
	}
	rec := callbackWith(svc, "jam.preview.example", state, cookies)
	if rec.Code != http.StatusTeapot || calls != 1 {
		t.Fatalf("callback = %d, Complete calls %d", rec.Code, calls)
	}
	for _, c := range rec.Result().Cookies() {
		if !strings.HasPrefix(c.Name, "__Host-") || c.Path != "/" || !c.Secure || c.Domain != "" || c.MaxAge >= 0 {
			t.Errorf("callback cookie %+v, want a cleared __Host- temp cookie", c)
		}
	}
	// A plain (tossable) temp cookie is not read on a HostPrefixed mount.
	var plain []*http.Cookie
	for _, c := range cookies {
		plain = append(plain, &http.Cookie{Name: strings.TrimPrefix(c.Name, "__Host-"), Value: c.Value})
	}
	if rec := callbackWith(svc, "jam.preview.example", state, plain); rec.Code != http.StatusBadRequest || calls != 1 {
		t.Errorf("unprefixed temp cookies accepted: %d", rec.Code)
	}
}

func TestExistingMountsTempCookiesUnprefixed(t *testing.T) {
	idp := newFakeIdP(t)
	for _, m := range []Mount{OperatorUIMount(), ParticipantMount()} {
		svc, err := New(context.Background(), RawConfig{Issuer: idp.url, ClientID: "cid", Scope: "openid", Audience: "aud"}, m, nil, discard())
		if err != nil {
			t.Fatal(err)
		}
		cookies, _, _ := startLogin(t, idp, svc, "jam.test", "")
		for _, c := range cookies {
			if strings.HasPrefix(c.Name, "__Host-") || !strings.HasPrefix(c.Name, "jam_oauth_") || c.Path != m.Prefix+"/auth" {
				t.Errorf("%s temp cookie %+v changed", m.Prefix, c)
			}
		}
	}
}

func TestCompleteMountStillChecksStateAndNonce(t *testing.T) {
	idp := newFakeIdP(t)
	var calls int
	svc := previewSvc(t, idp, &calls)
	cookies, state, _ := startLogin(t, idp, svc, "jam.preview.example", "https://x/")
	if rec := callbackWith(svc, "jam.preview.example", state+"x", cookies); rec.Code != http.StatusBadRequest {
		t.Errorf("bad state = %d, want 400", rec.Code)
	}
	idp.idNonce = "wrong"
	if rec := callbackWith(svc, "jam.preview.example", state, cookies); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad nonce = %d, want 401", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("Complete called %d times on a refused callback", calls)
	}
}

func TestMountCompleteReplacesSession(t *testing.T) {
	idp := newFakeIdP(t)
	var gotID, gotAccess, gotRT, gotPath string
	complete := func(w http.ResponseWriter, r *http.Request, idToken, accessToken, returnTo string) {
		gotID, gotAccess, gotRT, gotPath = idToken, accessToken, returnTo, r.URL.Path
		w.WriteHeader(http.StatusTeapot)
	}
	allow := func(u string) bool { return u == "https://app.example/x" }
	m := PreviewMount(complete, allow)
	if m.Prefix != "/__jam" {
		t.Fatalf("PreviewMount prefix = %q", m.Prefix)
	}
	svc, err := New(context.Background(), RawConfig{Issuer: idp.url, ClientID: "cid", Scope: "openid", Audience: "aud"}, m, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	rec, bound := loginAndCallback(t, idp, svc, "jam.preview.example", "https://app.example/x")
	if bound != "https://app.example/x" {
		t.Errorf("state bound return_to %q, want the ReturnTo-accepted URL", bound)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("callback = %d, want Complete's response", rec.Code)
	}
	if gotID == "" || gotAccess == "" || gotID == gotAccess {
		t.Errorf("Complete tokens id=%q access=%q, want both", gotID, gotAccess)
	}
	if gotRT != "https://app.example/x" || gotPath != "/__jam/auth/callback" {
		t.Errorf("Complete returnTo=%q path=%q", gotRT, gotPath)
	}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge >= 0 && c.Value != "" {
			t.Errorf("callback set cookie %s; Complete owns the session", c.Name)
		}
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("callback redirected to %q itself", loc)
	}
	// The preview mount has no session cookie of its own, so no logout route.
	lo := httptest.NewRecorder()
	svc.Routes().ServeHTTP(lo, httptest.NewRequest("GET", "/__jam/auth/logout", nil))
	if lo.Code != http.StatusNotFound {
		t.Errorf("preview mount logout = %d, want 404", lo.Code)
	}
}

func TestMountReturnToReplacesPrefixCheck(t *testing.T) {
	allow := func(u string) bool { return strings.HasPrefix(u, "https://ok.example/") }
	m := PreviewMount(nil, allow)
	if got := safeReturnTo(m, "https://ok.example/a?b=1"); got != "https://ok.example/a?b=1" {
		t.Errorf("accepted URL collapsed to %q", got)
	}
	for _, bad := range []string{"/__jam/x", "https://evil.example/", "//ok.example/", ""} {
		if got := safeReturnTo(m, bad); got != "/__jam/" {
			t.Errorf("safeReturnTo(%q) = %q, want /__jam/", bad, got)
		}
	}
	// Existing mounts keep the prefix check.
	if got := safeReturnTo(OperatorUIMount(), "https://ok.example/a"); got != "/ui/" {
		t.Errorf("operator mount accepted %q", got)
	}
}
