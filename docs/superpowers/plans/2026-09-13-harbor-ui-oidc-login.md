# Harbor Browser OIDC Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator sign into the read-only harbor admin UI from a browser via OAuth 2.0 Authorization Code + PKCE when harbor is OIDC-gated, and make loopback always reach the UI (fixing the current localhost 403 under OIDC).

**Architecture:** A new `internal/harbor/browserauth` package owns the auth-code+PKCE mechanics, the `/ui/auth/*` login routes, the session cookie, and the UI gate middleware. The session cookie holds the **access token**, re-verified per request by the **existing** `harbor.OIDCAuthenticator` (so `require-scope` is enforced identically to the bearer API). The `/ui` subtree is mounted with its **own** gate (loopback-or-session), decoupled from the `/admin/*` authenticator. `browserauth` imports `harbor`; `harbor` never imports `browserauth`.

**Tech Stack:** Go stdlib (`net/http`, `crypto/rand`, `crypto/subtle`, `encoding/base64`), `github.com/coreos/go-oidc/v3/oidc` (already a dep, for ID-token verification), raw token-endpoint POSTs (mirroring `internal/harbor/deviceflow`). No new third-party dependency.

## Global Constraints

- **Module path:** `github.com/aethons-tools/cove`. New package: `.../internal/harbor/browserauth`.
- **No new third-party dependency.** Reuse `go-oidc` (already present) and stdlib.
- **Security boundary:** `/admin/*` keeps its existing `authMiddleware`/authenticator, unchanged. The UI gets its own gate. Loopback always reaches `/ui`; off-loopback reaches it only with a valid session (when browser login is configured) else is refused. Fail closed: any verification error denies.
- **Secrets never logged:** never log a token, authorization code, PKCE verifier, `state`, `nonce`, or cookie value — at any level. Log a reason, not the material.
- **Crypto:** `crypto/rand` for `state`/`nonce`/PKCE verifier; PKCE `S256` only; `crypto/subtle.ConstantTimeCompare` for `state`.
- **Cookies:** session + temp cookies are `HttpOnly`, `Secure`, `SameSite=Lax`. Session `Path=/ui`, name `harbor_session`; temp cookies `Path=/ui/auth`, short `Max-Age`, cleared at callback.
- **Open-redirect guard:** post-login `return_to` must be a path beginning `/ui` and reject `//`, `/\`, backslashes, and absolute/scheme-relative URLs; default `/ui/`.
- **Tests hermetic:** `httptest` + a fake IdP (RS256 issuer, mirroring `internal/harbor/oidc_test.go`'s `fakeOIDC`). No Docker, network, or live VM. No `integration` build tag.
- **TDD:** failing test first, watch it fail, implement minimally, watch it pass, commit.
- **Build/test:** `go test ./internal/harbor/... ./cmd/at-harbor/...`; `go build ./...`; `just lint`. (Note: some transitive deps fetch via the module proxy — if a build 403s on `golang.org/x/...`, prefix with `GOPROXY=https://proxy.golang.org GOSUMDB=off`.)
- **Commit trailer:** end every commit message with EXACTLY, verbatim regardless of the implementing model:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```
- **Docs rule:** not done until `docs/` is updated in the same branch (Task 7).

---

### Task 1: harbor seams — `OIDCAuthenticator.VerifyToken` + `IsLoopbackRequest`

**Files:**
- Modify: `internal/harbor/oidc.go` (extract `VerifyToken`; `Authenticate` calls it)
- Modify: `internal/harbor/operator.go` (extract `IsLoopbackRequest`; `LoopbackAuthenticator` uses it)
- Test: `internal/harbor/oidc_test.go`, `internal/harbor/operator_test.go`

**Interfaces:**
- Produces:
  - `func (a *OIDCAuthenticator) VerifyToken(ctx context.Context, raw string) (Operator, error)` — verify a raw JWT (signature/iss/aud/exp + `require-scope`) and return the `Operator`. Empty `raw` → error.
  - `func IsLoopbackRequest(r *http.Request) bool` — true iff `r.RemoteAddr`'s host is a loopback IP.

- [ ] **Step 1: Write the failing test**

Add to `internal/harbor/oidc_test.go` (the file already has `newFakeOIDC`, `mint`, `claims`, `testAud`):

```go
func TestVerifyTokenRawString(t *testing.T) {
	f := newFakeOIDC(t)
	auth, err := NewOIDCAuthenticator(context.Background(), f.url, testAud, "harbor:admin")
	if err != nil {
		t.Fatal(err)
	}
	tok := f.mint(t, "RS256", f.kid, f.claims(testAud, "auth0|alice", "harbor:admin", time.Now().Add(time.Hour)), true)
	op, err := auth.VerifyToken(context.Background(), tok)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if op.ID != "auth0|alice" {
		t.Errorf("op.ID = %q, want auth0|alice", op.ID)
	}
	if _, err := auth.VerifyToken(context.Background(), ""); err == nil {
		t.Error("empty token should error")
	}
}
```

Add to `internal/harbor/operator_test.go`:

```go
func TestIsLoopbackRequest(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"[::1]:5000", true},
		{"203.0.113.7:5555", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.addr
		if got := IsLoopbackRequest(r); got != c.want {
			t.Errorf("IsLoopbackRequest(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
```

(Add `net/http/httptest` to `operator_test.go` imports if absent.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/ -run 'TestVerifyTokenRawString|TestIsLoopbackRequest' -v`
Expected: FAIL — `VerifyToken`/`IsLoopbackRequest` undefined.

- [ ] **Step 3: Extract `VerifyToken` in `oidc.go`**

Replace the body of `Authenticate` and add `VerifyToken`:

```go
func (a *OIDCAuthenticator) Authenticate(r *http.Request) (Operator, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return Operator{}, fmt.Errorf("missing bearer token")
	}
	return a.VerifyToken(r.Context(), raw)
}

// VerifyToken verifies a raw JWT (signature, iss, aud, exp via go-oidc) and, when
// configured, the required scope. It is the single verification path shared by the
// bearer header (Authenticate) and the browser session cookie.
func (a *OIDCAuthenticator) VerifyToken(ctx context.Context, raw string) (Operator, error) {
	if raw == "" {
		return Operator{}, fmt.Errorf("missing token")
	}
	tok, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return Operator{}, fmt.Errorf("token verification failed: %w", err)
	}
	var claims struct {
		Scope       string   `json:"scope"`
		Permissions []string `json:"permissions"`
	}
	if err := tok.Claims(&claims); err != nil {
		return Operator{}, fmt.Errorf("claims: %w", err)
	}
	if a.requireScope != "" && !hasScope(a.requireScope, claims.Scope, claims.Permissions) {
		return Operator{}, fmt.Errorf("operator lacks required scope %q", a.requireScope)
	}
	return Operator{ID: tok.Subject}, nil
}
```

- [ ] **Step 4: Extract `IsLoopbackRequest` in `operator.go`**

```go
// IsLoopbackRequest reports whether r originates from a loopback IP.
func IsLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

Rewrite `LoopbackAuthenticator.Authenticate` to use it:

```go
func (LoopbackAuthenticator) Authenticate(r *http.Request) (Operator, error) {
	if !IsLoopbackRequest(r) {
		return Operator{}, fmt.Errorf("admin request from non-loopback address %q", r.RemoteAddr)
	}
	return Operator{ID: "local"}, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/ -run 'TestVerifyTokenRawString|TestIsLoopbackRequest|TestLoopback|TestOIDC' -v`
Expected: PASS (new tests + the existing OIDC/loopback tests, unchanged behavior).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/oidc.go internal/harbor/operator.go internal/harbor/oidc_test.go internal/harbor/operator_test.go
git commit -m "$(cat <<'EOF'
refactor(harbor): expose VerifyToken + IsLoopbackRequest for the UI gate

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 2: `browserauth` OAuth mechanics — PKCE, authorize URL, code exchange

**Files:**
- Create: `internal/harbor/browserauth/oauth.go`
- Test: `internal/harbor/browserauth/oauth_test.go`

**Interfaces:**
- Produces:
  - `type HTTPDoer interface{ Do(*http.Request) (*http.Response, error) }`
  - `func PKCE() (verifier, challenge string, err error)` — `crypto/rand` 32-byte verifier (base64url, ~43 chars), `challenge = base64url(sha256(verifier))` (S256).
  - `func AuthCodeURL(authorizeEndpoint, clientID, redirectURI, scope, audience, state, nonce, challenge string) string` — builds the authorize URL (`response_type=code`, `code_challenge_method=S256`).
  - `func ExchangeCode(ctx context.Context, doer HTTPDoer, tokenEndpoint, clientID, code, verifier, redirectURI string) (idToken, accessToken string, err error)` — raw `application/x-www-form-urlencoded` POST (`grant_type=authorization_code`, no client secret); returns `id_token` + `access_token`.

- [ ] **Step 1: Write the failing test**

`internal/harbor/browserauth/oauth_test.go`:

```go
package browserauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestPKCEChallengeIsS256OfVerifier(t *testing.T) {
	v, c, err := PKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 43 {
		t.Errorf("verifier too short: %d", len(v))
	}
	sum := sha256.Sum256([]byte(v))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); c != want {
		t.Errorf("challenge = %q, want S256(verifier) = %q", c, want)
	}
}

func TestAuthCodeURLParams(t *testing.T) {
	u := AuthCodeURL("https://idp/authorize", "cid", "https://h/ui/auth/callback",
		"openid profile", "https://h/api", "st8", "nonce9", "chal")
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": "cid",
		"redirect_uri": "https://h/ui/auth/callback", "scope": "openid profile",
		"audience": "https://h/api", "state": "st8", "nonce": "nonce9",
		"code_challenge": "chal", "code_challenge_method": "S256",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("param %s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(u, "https://idp/authorize?") {
		t.Errorf("URL should start at the authorize endpoint: %s", u)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestExchangeCodePostsFormAndParsesTokens(t *testing.T) {
	var gotBody string
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id_token":"idT","access_token":"acT"}`)),
		}, nil
	})
	id, ac, err := ExchangeCode(context.Background(), doer, "https://idp/token", "cid", "the-code", "the-verifier", "https://h/ui/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	if id != "idT" || ac != "acT" {
		t.Errorf("tokens = %q,%q want idT,acT", id, ac)
	}
	form, _ := url.ParseQuery(gotBody)
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code",
		"code_verifier": "the-verifier", "client_id": "cid",
		"redirect_uri": "https://h/ui/auth/callback",
	} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if form.Has("client_secret") {
		t.Error("public client must not send client_secret")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/browserauth/ -v`
Expected: FAIL — package does not compile (`PKCE`/`AuthCodeURL`/`ExchangeCode` undefined).

- [ ] **Step 3: Implement `oauth.go`**

```go
// Package browserauth implements OAuth 2.0 Authorization Code + PKCE browser
// login for the harbor admin UI: a public client (no secret), a session cookie
// holding the API access token (re-verified per request by harbor's existing
// OIDCAuthenticator), and the UI gate that lets loopback through and redirects
// off-loopback browsers to log in. It imports harbor; harbor never imports it.
package browserauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// HTTPDoer is the subset of *http.Client the flow needs (injected for tests).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// PKCE returns a fresh (verifier, S256 challenge) pair using crypto/rand.
func PKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// AuthCodeURL builds the IdP authorization request URL for the code+PKCE flow.
func AuthCodeURL(authorizeEndpoint, clientID, redirectURI, scope, audience, state, nonce, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {scope},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if audience != "" {
		q.Set("audience", audience)
	}
	return authorizeEndpoint + "?" + q.Encode()
}

// ExchangeCode redeems an authorization code for tokens at the token endpoint as
// a public client (PKCE verifier, no client secret).
func ExchangeCode(ctx context.Context, doer HTTPDoer, tokenEndpoint, clientID, code, verifier, redirectURI string) (idToken, accessToken string, err error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doer.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("token exchange: status %d", resp.StatusCode)
	}
	var out struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("token exchange decode: %w", err)
	}
	if out.AccessToken == "" {
		return "", "", fmt.Errorf("token exchange: no access_token in response")
	}
	return out.IDToken, out.AccessToken, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/harbor/browserauth/ -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/browserauth/
git commit -m "$(cat <<'EOF'
feat(harbor/browserauth): PKCE, authorize URL, and code exchange

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 3: `browserauth` login/callback/logout handlers + session cookie

**Files:**
- Create: `internal/harbor/browserauth/session.go` (cookie helpers)
- Create: `internal/harbor/browserauth/service.go` (`Service`, `New`, `Routes`, handlers)
- Test: `internal/harbor/browserauth/service_test.go` (with a local fake IdP)

**Interfaces:**
- Consumes: Task 2's `PKCE`, `AuthCodeURL`, `ExchangeCode`; `github.com/coreos/go-oidc/v3/oidc`.
- Produces:
  - `const SessionCookie = "harbor_session"`
  - `func setSession(w http.ResponseWriter, token string, secure bool)` / `func clearSession(w http.ResponseWriter)`
  - `type RawConfig struct{ Issuer, ClientID, Scope, Audience string }`
  - `func New(ctx context.Context, cfg RawConfig, doer HTTPDoer) (*Service, error)` — discovers endpoints + builds the ID-token verifier (`ClientID = cfg.ClientID`) via `oidc.NewProvider`.
  - `func (s *Service) Routes() http.Handler` — a mux serving `GET /ui/auth/login`, `GET /ui/auth/callback`, `GET /ui/auth/logout`.

- [ ] **Step 1: Write the failing test**

`internal/harbor/browserauth/service_test.go` — stand up a fake IdP (RS256, like `oidc_test.go`'s `fakeOIDC`) that serves discovery + JWKS + a `/token` endpoint minting an ID token (with the `nonce` echoed) and an access token. Then:

```go
package browserauth

// (test helpers: newFakeIdP(t) returning discovery+jwks+token endpoints and an
//  RS256 mint(claims) — mirror internal/harbor/oidc_test.go's fakeOIDC.)

func TestLoginRedirectsAndSetsTempCookies(t *testing.T) {
	idp := newFakeIdP(t)
	svc := mustService(t, idp) // New(ctx, RawConfig{Issuer: idp.url, ClientID:"cid", Scope:"openid", Audience:"aud"}, idp.client())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/auth/login", nil)
	req.Host = "harbor.test"
	req.TLS = &tls.ConnectionState{} // force https → Secure cookies
	svc.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, idp.url+"/authorize") || !strings.Contains(loc, "code_challenge_method=S256") {
		t.Errorf("bad authorize redirect: %s", loc)
	}
	// state, nonce, pkce temp cookies set, HttpOnly + Secure + Path=/ui/auth.
	names := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		names[c.Name] = true
		if !c.HttpOnly || !c.Secure || c.Path != "/ui/auth" {
			t.Errorf("temp cookie %s flags wrong: %+v", c.Name, c)
		}
	}
	for _, want := range []string{"harbor_oauth_state", "harbor_oauth_nonce", "harbor_oauth_pkce"} {
		if !names[want] {
			t.Errorf("missing temp cookie %s", want)
		}
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	idp := newFakeIdP(t)
	svc := mustService(t, idp)
	req := httptest.NewRequest("GET", "/ui/auth/callback?state=EVIL&code=x", nil)
	req.AddCookie(&http.Cookie{Name: "harbor_oauth_state", Value: "REAL"})
	rec := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch = %d, want 400", rec.Code)
	}
}

func TestCallbackHappyPathSetsSessionCookie(t *testing.T) {
	idp := newFakeIdP(t)
	svc := mustService(t, idp)
	// Drive login to capture the temp cookies + the nonce the IdP must echo.
	// Then hit callback with matching state + a code the fake /token honors.
	// Assert: 302 to /ui/, Set-Cookie harbor_session=<access token>,
	//         HttpOnly + Secure + SameSite=Lax + Path=/ui.
	// (Full body in implementation — exercises ExchangeCode + ID-token/nonce verify.)
}

func TestReturnToOpenRedirectGuard(t *testing.T) {
	for _, bad := range []string{"//evil.com", "https://evil.com", "/\\evil", "/admin/x"} {
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/browserauth/ -run 'TestLogin|TestCallback|TestReturnTo|TestLogout' -v`
Expected: FAIL — `Service`/`New`/`Routes`/`safeReturnTo`/`SessionCookie` undefined.

- [ ] **Step 3: Implement `session.go`**

```go
package browserauth

import "net/http"

// SessionCookie holds the API access token for a logged-in browser session.
const SessionCookie = "harbor_session"

const (
	stateCookie = "harbor_oauth_state"
	nonceCookie = "harbor_oauth_nonce"
	pkceCookie  = "harbor_oauth_pkce"
)

func setSession(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/ui",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/ui",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// tempCookie sets a short-lived HttpOnly cookie under /ui/auth used only across
// the authorize→callback round trip.
func tempCookie(w http.ResponseWriter, name, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/ui/auth",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
}

func clearTemp(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/ui/auth", HttpOnly: true, MaxAge: -1})
}
```

- [ ] **Step 4: Implement `service.go`**

```go
package browserauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// RawConfig is the browser-login configuration resolved from operator-auth.oidc.
type RawConfig struct {
	Issuer   string
	ClientID string
	Scope    string
	Audience string
}

// Service serves the /ui/auth/* login routes for the code+PKCE flow.
type Service struct {
	cfg           RawConfig
	authorizeURL  string
	tokenURL      string
	idVerifier    *oidc.IDTokenVerifier
	doer          HTTPDoer
	log           *slog.Logger
}

// New discovers the IdP endpoints and builds the ID-token verifier (aud = ClientID).
func New(ctx context.Context, cfg RawConfig, doer HTTPDoer, log *slog.Logger) (*Service, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for browser login: %w", err)
	}
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Service{
		cfg:          cfg,
		authorizeURL: provider.Endpoint().AuthURL,
		tokenURL:     provider.Endpoint().TokenURL,
		idVerifier:   provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		doer:         doer,
		log:          log,
	}, nil
}

func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/auth/login", s.login)
	mux.HandleFunc("GET /ui/auth/callback", s.callback)
	mux.HandleFunc("GET /ui/auth/logout", s.logout)
	return mux
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func isSecure(r *http.Request) bool { return r.TLS != nil }

func redirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/ui/auth/callback"
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	state, nonce := randToken(), randToken()
	verifier, challenge, err := PKCE()
	if err != nil {
		http.Error(w, "login setup failed", http.StatusInternalServerError)
		return
	}
	secure := isSecure(r)
	// Bind the post-login destination to the state cookie value form: "<state>|<return_to>".
	rt := safeReturnTo(r.URL.Query().Get("return_to"))
	tempCookie(w, stateCookie, state+"|"+rt, secure)
	tempCookie(w, nonceCookie, nonce, secure)
	tempCookie(w, pkceCookie, verifier, secure)
	http.Redirect(w, r, AuthCodeURL(s.authorizeURL, s.cfg.ClientID, redirectURI(r), s.cfg.Scope, s.cfg.Audience, state, nonce, challenge), http.StatusFound)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	stateCookieVal, err := r.Cookie(stateCookie)
	if err != nil {
		http.Error(w, "missing login state", http.StatusBadRequest)
		return
	}
	wantState, rt, _ := strings.Cut(stateCookieVal.Value, "|")
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(wantState)) != 1 {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	pkce, err := r.Cookie(pkceCookie)
	if err != nil {
		http.Error(w, "missing login state", http.StatusBadRequest)
		return
	}
	idTok, accessTok, err := ExchangeCode(r.Context(), s.doer, s.tokenURL, s.cfg.ClientID, r.URL.Query().Get("code"), pkce.Value, redirectURI(r))
	if err != nil {
		s.log.Warn("browser login: code exchange failed", "reason", err.Error())
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	verified, err := s.idVerifier.Verify(r.Context(), idTok)
	if err != nil {
		s.log.Warn("browser login: id token verify failed", "reason", err.Error())
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	nonceCk, err := r.Cookie(nonceCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(nonceCk.Value)) != 1 {
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}
	// Success: drop temp cookies, set the session, redirect to the validated target.
	for _, n := range []string{stateCookie, nonceCookie, pkceCookie} {
		clearTemp(w, n)
	}
	setSession(w, accessTok, isSecure(r))
	http.Redirect(w, r, safeReturnTo(rt), http.StatusFound)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	http.Redirect(w, r, "/ui/auth/login", http.StatusFound)
}

// safeReturnTo permits only same-site absolute paths under /ui, defeating open
// redirects. Anything else collapses to /ui/.
func safeReturnTo(p string) string {
	if p == "" || !strings.HasPrefix(p, "/ui") {
		return "/ui/"
	}
	// Reject protocol-relative ("//host") and backslash tricks.
	if strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return "/ui/"
	}
	return p
}
```

Note: `go-oidc`'s `IDToken.Nonce` field carries the `nonce` claim, so verification compares it to the cookie.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/browserauth/ -v`
Expected: PASS (all Task 2 + Task 3 tests; fill in the `TestCallbackHappyPathSetsSessionCookie` body to drive login→callback against the fake IdP).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/browserauth/
git commit -m "$(cat <<'EOF'
feat(harbor/browserauth): login/callback/logout routes + session cookie

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 4: `browserauth` UI gate (loopback-or-session, redirect-on-miss)

**Files:**
- Create: `internal/harbor/browserauth/gate.go`
- Test: `internal/harbor/browserauth/gate_test.go`

**Interfaces:**
- Consumes: `harbor.OIDCAuthenticator.VerifyToken` and `harbor.IsLoopbackRequest` (Task 1); `SessionCookie` (Task 3).
- Produces:
  - `type SessionVerifier struct{ Auth *harbor.OIDCAuthenticator }` with `func (v *SessionVerifier) verify(r *http.Request) (harbor.Operator, bool)` — reads the `harbor_session` cookie and verifies it via `Auth.VerifyToken`.
  - `type Gate struct{ Sess *SessionVerifier; LoginPath string; Log *slog.Logger }` (`Sess == nil` ⇒ loopback-only) with `func (g Gate) Wrap(next http.Handler) http.Handler`.

- [ ] **Step 1: Write the failing test**

`internal/harbor/browserauth/gate_test.go` (reuse the fake IdP from Task 3 to mint access tokens the `harbor.OIDCAuthenticator` will accept — build the authenticator with `harbor.NewOIDCAuthenticator(ctx, idp.url, aud, "")`):

```go
func TestGateLoopbackAlwaysAllowed(t *testing.T) {
	next := okHandler()
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login"} // loopback-only mode
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	g.Wrap(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback = %d, want 200", rec.Code)
	}
}

func TestGateOffLoopbackLoopbackOnlyRefused(t *testing.T) {
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-loopback loopback-only = %d, want 403", rec.Code)
	}
}

func TestGateOffLoopbackNoSessionRedirects(t *testing.T) {
	idp := newFakeIdP(t)
	auth, _ := harbor.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	g := Gate{Sess: &SessionVerifier{Auth: auth}, LoginPath: "/ui/auth/login"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/auth/login" {
		t.Fatalf("no-session off-loopback = %d %q, want 302 /ui/auth/login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestGateOffLoopbackValidSessionAllowed(t *testing.T) {
	idp := newFakeIdP(t)
	auth, _ := harbor.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	g := Gate{Sess: &SessionVerifier{Auth: auth}, LoginPath: "/ui/auth/login"}
	tok := idp.mintAccess(t, "aud", "auth0|bob", "", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid session = %d, want 200", rec.Code)
	}
}
```

(`okHandler()` returns a 200 handler; add it as a test helper.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/browserauth/ -run TestGate -v`
Expected: FAIL — `Gate`/`SessionVerifier` undefined.

- [ ] **Step 3: Implement `gate.go`**

```go
package browserauth

import (
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/harbor"
)

// SessionVerifier verifies the browser session cookie using harbor's own token
// verifier — the same verification (iss/aud/exp + require-scope) as the bearer API.
type SessionVerifier struct {
	Auth *harbor.OIDCAuthenticator
}

func (v *SessionVerifier) verify(r *http.Request) (harbor.Operator, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return harbor.Operator{}, false
	}
	op, err := v.Auth.VerifyToken(r.Context(), c.Value)
	if err != nil {
		return harbor.Operator{}, false
	}
	return op, true
}

// Gate guards the /ui subtree. Loopback requests are always allowed (the trusted
// local operator). Off-loopback requests require a valid session when Sess is
// set (missing/invalid → 302 to LoginPath); when Sess is nil the UI is
// loopback-only and off-loopback is refused.
type Gate struct {
	Sess      *SessionVerifier
	LoginPath string
	Log       *slog.Logger
}

func (g Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if harbor.IsLoopbackRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if g.Sess == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if _, ok := g.Sess.verify(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, g.LoginPath, http.StatusFound)
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/harbor/browserauth/ -v`
Expected: PASS (all browserauth tests).

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/browserauth/
git commit -m "$(cat <<'EOF'
feat(harbor/browserauth): UI gate — loopback-or-session with redirect

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 5: config — `browser-client-id` + `browser-scope`

**Files:**
- Modify: `cmd/at-harbor/config.go` (add fields to the `OperatorAuth.OIDC` struct; a `browserAuthConfig()` accessor)
- Test: `cmd/at-harbor/config_test.go`

**Interfaces:**
- Produces: `func (c serveConfig) browserAuthConfig() *browserauth.RawConfig` — returns a `*browserauth.RawConfig{Issuer, ClientID: browser-client-id, Scope, Audience}` when OIDC + `browser-client-id` are set, else `nil`. `browser-scope` defaults to `"openid profile email"`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-harbor/config_test.go`:

```go
func TestBrowserAuthConfig(t *testing.T) {
	cfg := serveConfig{}
	cfg.OperatorAuth.OIDC = &struct { // mirror the real anonymous struct shape in config.go
		Issuer         string `yaml:"issuer"`
		Audience       string `yaml:"audience"`
		RequireScope   string `yaml:"require-scope"`
		DeviceClientID string `yaml:"device-client-id"`
		DeviceScope    string `yaml:"device-scope"`
		BrowserClientID string `yaml:"browser-client-id"`
		BrowserScope    string `yaml:"browser-scope"`
	}{Issuer: "https://idp/", Audience: "aud", BrowserClientID: "bcid"}
	bc := cfg.browserAuthConfig()
	if bc == nil || bc.ClientID != "bcid" || bc.Audience != "aud" || bc.Scope != "openid profile email" {
		t.Fatalf("browserAuthConfig = %+v, want bcid/aud/default-scope", bc)
	}
	// No browser-client-id ⇒ nil.
	cfg.OperatorAuth.OIDC.BrowserClientID = ""
	if cfg.browserAuthConfig() != nil {
		t.Error("no browser-client-id should yield nil")
	}
}
```

(If mirroring the anonymous struct is awkward, the implementer may instead load a small YAML fixture through the existing config parser — whichever matches the file's conventions. The behavior asserted is what matters.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-harbor/ -run TestBrowserAuthConfig -v`
Expected: FAIL — fields / `browserAuthConfig` undefined.

- [ ] **Step 3: Add the fields + accessor**

In `cmd/at-harbor/config.go`, add to the `OperatorAuth.OIDC` anonymous struct:

```go
			BrowserClientID string `yaml:"browser-client-id"`
			BrowserScope    string `yaml:"browser-scope"`
```

Add the accessor (import the `browserauth` package):

```go
// browserAuthConfig returns the browser code+PKCE login config when OIDC and a
// browser-client-id are set; nil disables browser login (off-loopback UI is then
// refused, loopback still works).
func (c serveConfig) browserAuthConfig() *browserauth.RawConfig {
	o := c.OperatorAuth.OIDC
	if o == nil || o.BrowserClientID == "" {
		return nil
	}
	scope := o.BrowserScope
	if scope == "" {
		scope = "openid profile email"
	}
	return &browserauth.RawConfig{Issuer: o.Issuer, ClientID: o.BrowserClientID, Scope: scope, Audience: o.Audience}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-harbor/ -run TestBrowserAuthConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go
git commit -m "$(cat <<'EOF'
feat(at-harbor): browser-client-id / browser-scope serve config

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 6: mount the UI with its own gate + wire browser login in `main`

**Files:**
- Modify: `internal/harbor/admin.go` (mount `/ui` OUTSIDE `authMiddleware`; `/admin/*` stays guarded)
- Modify: `cmd/at-harbor/main.go` (build the `/ui` handler: login routes + Gate-wrapped adminui; pass to `NewAdminHandler`)
- Test: `internal/harbor/admin_test.go` (rework `TestAdminHandlerMountsUI` to the new contract)

**Interfaces:**
- Consumes: `browserauth.New`, `browserauth.Gate`, `browserauth.SessionVerifier`, `adminui.Handler`, `cfg.browserAuthConfig()`, the API `auth` (which is `*harbor.OIDCAuthenticator` under OIDC).
- Produces: `NewAdminHandler`'s passed `ui http.Handler` is mounted verbatim (self-gated); `NewAdminHandler` no longer wraps `/ui` with the API `authMiddleware`.

- [ ] **Step 1: Write the failing test**

Rework `TestAdminHandlerMountsUI` in `internal/harbor/admin_test.go` to assert the new contract — the passed `ui` handler is mounted as-is under the API gate no longer applying to it:

```go
func TestAdminHandlerMountsUI(t *testing.T) {
	store := newStore(t)
	// A stub UI that records it was reached — it owns its own auth now.
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("UI:" + r.URL.Path))
	})
	h := NewAdminHandler(store, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, discardLogger(), ui)

	// Root still redirects to /ui/.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("GET / = %d %q, want 302 /ui/", rec.Code, rec.Header().Get("Location"))
	}

	// /ui/* reaches the mounted handler WITHOUT the API auth gate applying.
	rec = httptest.NewRecorder()
	req := adminReq("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.9:1000" // off-loopback: API gate would have 403'd before
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "UI:/ui/coves" {
		t.Fatalf("GET /ui/coves = %d %q, want 200 UI:/ui/coves (ui owns its own auth)", rec.Code, rec.Body.String())
	}

	// /admin/* is still guarded by the API auth.
	rec = httptest.NewRecorder()
	badReq := adminReq("GET", "/admin/roster", nil)
	badReq.RemoteAddr = "203.0.113.9:1000"
	h.ServeHTTP(rec, badReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-loopback /admin/roster = %d, want 403", rec.Code)
	}
}
```

(Use whatever discard-logger / `adminReq` / `newStore` helpers the file already defines; `adminReq` sets a loopback RemoteAddr by default, overridden per-case above.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/ -run TestAdminHandlerMountsUI -v`
Expected: FAIL — currently `/ui/coves` from off-loopback is 403'd by the shared `authMiddleware`.

- [ ] **Step 3: Restructure the mount in `admin.go`**

Replace the `if ui != nil { … }` block + `return authMiddleware(auth, log, mux)` tail with:

```go
	guarded := authMiddleware(auth, log, mux) // guards every /admin/* route
	if ui == nil {
		return guarded
	}
	parent := http.NewServeMux()
	parent.Handle("/admin/", guarded)
	parent.Handle("/ui/", ui) // ui owns its own auth (loopback-or-session)
	parent.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	return parent
```

(The `/admin/healthz` and `/admin/login-config` routes remain inside `mux`, still wrapped by `authMiddleware`, so their behavior — including the login-config exemption — is unchanged.)

- [ ] **Step 4: Wire browser login + the gate in `main.go`**

Where `main.go` builds `ui` and calls `NewAdminHandler` (currently `ui := adminui.Handler(st)` then `NewAdminHandler(..., ui)`), replace with a composed, self-gated `/ui` handler:

```go
	uiMux := http.NewServeMux()

	// Gate the adminui views: loopback always; off-loopback needs a session when
	// browser login is configured, else refused.
	gate := browserauth.Gate{LoginPath: "/ui/auth/login", Log: log}
	if bc := cfg.browserAuthConfig(); bc != nil {
		svc, err := browserauth.New(context.Background(), *bc, nil, log)
		if err != nil {
			return fmt.Errorf("browser login setup: %w", err)
		}
		uiMux.Handle("/ui/auth/", svc.Routes()) // login/callback/logout — unauthenticated
		if oidcAuth, ok := auth.(*harbor.OIDCAuthenticator); ok {
			gate.Sess = &browserauth.SessionVerifier{Auth: oidcAuth}
		}
	}
	uiMux.Handle("/ui/", gate.Wrap(adminui.Handler(st)))

	admin := harbor.NewAdminHandler(st, sup, auth, credExists, cfg.operatorLoginConfig(), log, uiMux)
```

Add imports: `context` (if not present), `net/http`, `github.com/aethons-tools/cove/internal/harbor/browserauth`. (`/ui/auth/` is registered before `/ui/`, and Go 1.22 pattern precedence routes `/ui/auth/...` to the login handler and everything else `/ui/...` to the gated adminui.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/... ./cmd/at-harbor/...`
Expected: PASS (reworked `TestAdminHandlerMountsUI`; all existing admin/adminui/main tests; the JSON API and its off-loopback 403 for `/admin/*` unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go cmd/at-harbor/main.go
git commit -m "$(cat <<'EOF'
feat(harbor): self-gated /ui mount + wire browser OIDC login

Mount /ui outside the /admin auth gate; the UI handler owns its own gate
(loopback-or-session). main composes /ui/auth/* login routes with a
Gate-wrapped adminui. Loopback always reaches the UI; off-loopback needs
a session when browser-client-id is configured.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 7: Documentation

**Files:**
- Modify: `docs/usage/harbor/ui.md` (replace the "not reachable from a remote browser yet" note with the browser-login story)
- Modify: `docs/usage/harbor/operators.md` (add `browser-client-id` / `browser-scope` to the `operator-auth.oidc` block)
- Test: the docs-audit checker

**Interfaces:** none (docs only).

- [ ] **Step 1: Update `docs/usage/harbor/ui.md`**

Replace the "Exposure — loopback only (this cut)" section's off-loopback bullet with the browser-login behavior:

```markdown
## Reaching the UI

- **Loopback** (local host, or an SSH tunnel to the admin port): always reachable, no login — the local operator is trusted. This holds whether or not `operator-auth.oidc` is configured.
- **Off-loopback**, when `operator-auth.oidc` includes a **`browser-client-id`**: the browser is redirected through an OIDC **Authorization Code + PKCE** login (`/ui/auth/login` → your IdP → `/ui/auth/callback`); on success a session cookie (the API access token, HttpOnly + Secure + SameSite=Lax) lets you browse until it expires, then you re-login. `require-scope` is enforced on every request, exactly as for the admin API.
- **Off-loopback with no `browser-client-id`**: the UI is refused (the programmatic admin API is still reachable with a bearer token — see [operators.md](operators.md)).

Register `https://<your-harbor-host>/ui/auth/callback` in your IdP's Allowed Callback URLs. Browser login needs TLS (the session cookie is `Secure`). The UI remains **read-only**; the login routes never expose mutation.
```

- [ ] **Step 2: Update `docs/usage/harbor/operators.md`**

In the `operator-auth.oidc` YAML block, add the two fields with a one-line note:

```yaml
    browser-client-id: "…"                # optional; enables browser (Authorization Code + PKCE) login for /ui
    browser-scope: "openid profile email" # optional; default shown, must include openid
```

Add a sentence: browser login uses the same `issuer`/`audience`/`require-scope`; the session cookie holds the API access token and is re-verified per request like a bearer.

- [ ] **Step 3: Run the docs audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs`
Expected: no NEW findings touching `ui.md`/`operators.md` (pre-existing backlog elsewhere is out of scope; note it, don't fix).

- [ ] **Step 4: Commit**

```bash
git add docs/usage/harbor/ui.md docs/usage/harbor/operators.md
git commit -m "$(cat <<'EOF'
docs(harbor): document browser OIDC login for the admin UI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 8: Full verification pass

**Files:** none (verification only).

- [ ] **Step 1: Full hermetic tests** — Run: `GOPROXY=https://proxy.golang.org GOSUMDB=off go test ./...` — Expected: all packages PASS (a cold module cache may need the proxy prefix for `golang.org/x/*`).
- [ ] **Step 2: Build** — Run: `go build ./...` — Expected: clean.
- [ ] **Step 3: Vet + lint** — Run: `go vet ./internal/harbor/... ./cmd/at-harbor/...` then `just lint` — Expected: clean (gofmt/vet); shellcheck/hadolint skipped is fine (no shell/Dockerfile changes).
- [ ] **Step 4: Secret-leak grep** — Run: `grep -rnE 'log\.(Info|Warn|Error|Debug)' internal/harbor/browserauth/` and confirm no call logs a token, code, verifier, state, nonce, or cookie value (only reasons). Expected: no material logged.

---

## Self-Review

**1. Spec coverage:**
- Loopback always reaches `/ui` (fixes 403) → Task 4 gate + Task 6 mount; `TestGateLoopbackAlwaysAllowed`. ✓
- Auth Code + PKCE public client → Task 2 (`AuthCodeURL` S256, `ExchangeCode` no secret). ✓
- Session = access token in HttpOnly+Secure+SameSite=Lax cookie, re-verified by existing `OIDCAuthenticator` → Task 1 `VerifyToken` + Task 3 `setSession` + Task 4 `SessionVerifier`. ✓
- ID token verified once at callback (aud=client id, nonce) → Task 3 `callback`. ✓
- Host-derived redirect URI → Task 3 `redirectURI`. ✓
- `browser-client-id` enables; unset ⇒ off-loopback refused → Task 5 accessor + Task 6 wiring; `TestGateOffLoopbackLoopbackOnlyRefused`. ✓
- `/admin/*` unchanged → Task 6 restructure keeps `authMiddleware` on `/admin/*`; test asserts off-loopback `/admin/roster` still 403. ✓
- Security: crypto/rand, constant-time state, open-redirect guard, cookie flags, SSRF-safe (endpoints from discovery), fail-closed, no-secret-logging → Tasks 2/3 + Task 8 grep. ✓
- Config fields → Task 5. ✓ Docs → Task 7. ✓ Hermetic tests (fake IdP) → all tasks. ✓

**2. Placeholder scan:** Task 3's `TestCallbackHappyPathSetsSessionCookie` body is described rather than fully written — this is the one place the implementer must author the drive-login-then-callback sequence against the fake IdP (the surrounding tests give the exact cookie/route/token shapes it needs); every other step has complete code. No TBD/TODO elsewhere.

**3. Type consistency:**
- `VerifyToken(ctx, raw) (Operator, error)` — Task 1 defines, Task 4 `SessionVerifier.verify` consumes. ✓
- `browserauth.RawConfig{Issuer,ClientID,Scope,Audience}` — Task 3 defines, Task 5 accessor returns, Task 6 passes to `New`. ✓
- `Gate{Sess *SessionVerifier; LoginPath string; Log}` + `SessionVerifier{Auth *harbor.OIDCAuthenticator}` — Task 4 defines, Task 6 constructs. ✓
- `New(ctx, RawConfig, HTTPDoer, *slog.Logger) (*Service, error)` / `Service.Routes() http.Handler` — Task 3 defines, Task 6 calls. ✓
- `SessionCookie`, `setSession`, cookie names — Task 3 defines, Task 4 reads. ✓
- `NewAdminHandler(..., ui http.Handler)` trailing param — unchanged from the merged code; Task 6 only changes how `ui` is mounted internally. ✓

Note carried to execution: Task 6 relies on Go 1.22 `net/http` pattern precedence (`/ui/auth/` more specific than `/ui/`) so login routes are not swallowed by the gated handler — stated in the task.
