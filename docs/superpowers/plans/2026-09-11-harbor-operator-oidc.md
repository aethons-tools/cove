# Harbor Operator OIDC (server-side validation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Validate an Auth0 (any OIDC) operator bearer token on harbor's admin API, behind the existing `OperatorAuthenticator` seam, config-selected; add the CLI `--token` path and the `AT_HARBOR_` env convention.

**Architecture:** A new `OIDCAuthenticator` (using `github.com/coreos/go-oidc/v3`) drops in wherever `LoopbackAuthenticator` is used today. `serve` picks OIDC vs loopback from an `operator-auth.oidc` config block. The admin client gains an optional bearer token; the CLI supplies it via `--token` / `AT_HARBOR_ADMIN_TOKEN`.

**Tech Stack:** Go (stdlib) + `gopkg.in/yaml.v3` + **`github.com/coreos/go-oidc/v3`** (new — the first non-stdlib/yaml dependency; deliberate, security-scoped).

## ⛔ Prerequisite — sandbox egress (do this before Task 1)

go-oidc pulls `golang.org/x/oauth2` (and `go-jose` pulls `golang.org/x/crypto`), which fetch from `golang.org` / `proxy.golang.org` — **both are blocked by the sandbox egress lock** (github-only). The module can't be added until the kit allows a Go module proxy. Fix (human-gated, per SANDBOX.md):

- Add **`proxy.golang.org`** to the kit's `image.allowed-domains` in `.at-cove/config.yml`, then `at-cove recreate`.
- Builds then use `GOPROXY=https://proxy.golang.org GOSUMDB=off` (the repo already sets `GOSUMDB=off`).

Until that lands, `go get github.com/coreos/go-oidc/v3` fails with a `Forbidden` proxy fetch and no task can build.

## Global Constraints

- Module `github.com/aethons-tools/cove`; stdlib + `gopkg.in/yaml.v3` + **`coreos/go-oidc/v3`** only — no *other* new dependency. Adding go-oidc will bump the `go` directive in `go.mod` (go-oidc requires a recent Go); accept it (CI's toolchain is ≥ that). Commit the resulting `go.mod` **and** `go.sum`.
- Tests hermetic — a **fake OIDC provider** (`httptest` serving discovery + JWKS, minting RS256 tokens with a test key); no network. Real Auth0 round-trip behind `//go:build integration` / manual.
- No secret in logs; the admin API logs the operator `sub` (not tokens).
- Env-var convention is **`AT_HARBOR_`**. New: `AT_HARBOR_ADMIN_TOKEN`. Rename the enroll snippet's `HARBOR_IDENTITY_TOKEN` → `AT_HARBOR_IDENTITY_TOKEN`.
- Before committing each task: `go vet ./...` and `gofmt -l internal/ cmd/` must be clean (the CI gate fails on unformatted code — run `gofmt -w`).
- Slice-1/#2a behavior (broker, loopback admin auth, enroll/revoke/destination via the admin API) stays green.

---

### Task 1: OIDCAuthenticator (go-oidc) + fake-provider tests

**Files:**
- Modify: `go.mod`, `go.sum` (add go-oidc)
- Create: `internal/harbor/oidc.go`
- Create: `internal/harbor/oidc_test.go`

**Interfaces:**
- Consumes: `Operator`, `OperatorAuthenticator` (slice #2a).
- Produces: `func NewOIDCAuthenticator(ctx context.Context, issuer, audience, requireScope string) (*OIDCAuthenticator, error)` implementing `OperatorAuthenticator`.

- [ ] **Step 1: Add the dependency**

Run (with the egress prerequisite in place):
```bash
GOPROXY=https://proxy.golang.org GOSUMDB=off go get github.com/coreos/go-oidc/v3@latest
```
Expected: `go.mod`/`go.sum` gain `github.com/coreos/go-oidc/v3` (+ indirect `go-jose`, `golang.org/x/oauth2`, `golang.org/x/crypto`).

- [ ] **Step 2: Write the failing test (fake OIDC provider + accept/reject cases)**

Create `internal/harbor/oidc_test.go`:

```go
package harbor

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeOIDC stands up a minimal OIDC provider: discovery + JWKS, and mints RS256 tokens.
type fakeOIDC struct {
	url string
	key *rsa.PrivateKey
	kid string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{key: key, kid: "test-1"}
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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// mint signs a JWT with the given header kid + claims (RS256). alg="none" or a bad
// kid can be forced by the caller for the reject cases.
func (f *fakeOIDC) mint(t *testing.T, alg, kid string, claims map[string]any, sign bool) string {
	t.Helper()
	hdr := map[string]any{"alg": alg, "typ": "JWT", "kid": kid}
	enc := func(v any) string { b, _ := json.Marshal(v); return b64(b) }
	signing := enc(hdr) + "." + enc(claims)
	if !sign {
		return signing + "." // no signature (alg=none case)
	}
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func (f *fakeOIDC) claims(aud, sub, scope string, exp time.Time) map[string]any {
	return map[string]any{"iss": f.url, "aud": aud, "sub": sub, "scope": scope,
		"iat": time.Now().Unix(), "exp": exp.Unix()}
}

const testAud = "https://harbor.test/api"

func req(token string) *http.Request {
	r := httptest.NewRequest("GET", "/admin/healthz", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestOIDCAuthenticatorAcceptsValidToken(t *testing.T) {
	f := newFakeOIDC(t)
	a, err := NewOIDCAuthenticator(context.Background(), f.url, testAud, "")
	if err != nil {
		t.Fatalf("NewOIDCAuthenticator: %v", err)
	}
	tok := f.mint(t, "RS256", f.kid, f.claims(testAud, "auth0|abc", "read write", time.Now().Add(time.Hour)), true)
	op, err := a.Authenticate(req(tok))
	if err != nil || op.ID != "auth0|abc" {
		t.Fatalf("accept: op=%+v err=%v", op, err)
	}
}

func TestOIDCAuthenticatorRejects(t *testing.T) {
	f := newFakeOIDC(t)
	a, err := NewOIDCAuthenticator(context.Background(), f.url, testAud, "")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing":     "",
		"expired":     f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "", time.Now().Add(-time.Hour)), true),
		"wrong-aud":   f.mint(t, "RS256", f.kid, f.claims("someone-else", "s", "", time.Now().Add(time.Hour)), true),
		"bad-sig":     f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "", time.Now().Add(time.Hour)), true) + "tampered",
		"alg-none":    f.mint(t, "none", f.kid, f.claims(testAud, "s", "", time.Now().Add(time.Hour)), false),
		"unknown-kid": f.mint(t, "RS256", "nope", f.claims(testAud, "s", "", time.Now().Add(time.Hour)), true),
	}
	for name, tok := range cases {
		if _, err := a.Authenticate(req(tok)); err == nil {
			t.Errorf("%s: expected rejection, got nil error", name)
		}
	}
}

func TestOIDCAuthenticatorRequireScope(t *testing.T) {
	f := newFakeOIDC(t)
	a, _ := NewOIDCAuthenticator(context.Background(), f.url, testAud, "harbor:admin")
	// scope claim carries it
	ok := f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "openid harbor:admin", time.Now().Add(time.Hour)), true)
	if _, err := a.Authenticate(req(ok)); err != nil {
		t.Fatalf("in-scope token rejected: %v", err)
	}
	no := f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "openid", time.Now().Add(time.Hour)), true)
	if _, err := a.Authenticate(req(no)); err == nil {
		t.Fatal("token lacking required scope was accepted")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/harbor/ -run TestOIDC -v`
Expected: FAIL — `undefined: NewOIDCAuthenticator`.

- [ ] **Step 4: Implement `internal/harbor/oidc.go`**

```go
package harbor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// OIDCAuthenticator validates an operator's OIDC/Auth0 bearer token (signature +
// iss + aud + exp via go-oidc), and optionally requires a scope/permission.
type OIDCAuthenticator struct {
	verifier     *oidc.IDTokenVerifier
	requireScope string
}

// NewOIDCAuthenticator does OIDC discovery against issuer (fetching the JWKS) and
// builds a verifier bound to audience. Fails closed if the issuer is unreachable.
func NewOIDCAuthenticator(ctx context.Context, issuer, audience, requireScope string) (*OIDCAuthenticator, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", issuer, err)
	}
	return &OIDCAuthenticator{
		verifier:     provider.Verifier(&oidc.Config{ClientID: audience}),
		requireScope: requireScope,
	}, nil
}

func (a *OIDCAuthenticator) Authenticate(r *http.Request) (Operator, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return Operator{}, fmt.Errorf("missing bearer token")
	}
	tok, err := a.verifier.Verify(r.Context(), raw)
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

// hasScope reports whether want is in the space-delimited scope string or the
// permissions array (Auth0 puts scopes in either, depending on RBAC config).
func hasScope(want, scope string, perms []string) bool {
	for _, s := range strings.Fields(scope) {
		if s == want {
			return true
		}
	}
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/harbor/ -run TestOIDC -count=1 -v`
Expected: PASS (accept, all reject cases, require-scope).

- [ ] **Step 6: Full package + vet + gofmt**

Run: `go test ./internal/harbor/ -count=1 && go vet ./internal/harbor/ && gofmt -l internal/harbor`
Expected: PASS, no vet output, gofmt lists nothing.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/harbor/oidc.go internal/harbor/oidc_test.go
git commit -m "feat(harbor): OIDCAuthenticator — go-oidc operator token validation"
```

---

### Task 2: Config `operator-auth.oidc` + serve authenticator selection + docs

**Files:**
- Modify: `cmd/at-harbor/config.go`
- Modify: `cmd/at-harbor/config_test.go`
- Modify: `cmd/at-harbor/main.go` (`cmdServe`)
- Modify: `docs/OVERVIEW.md`, `docs/usage/INDEX.md`

**Interfaces:**
- Consumes: `harbor.NewOIDCAuthenticator` (Task 1), `harbor.LoopbackAuthenticator`, `harbor.NewAdminHandler`.
- Produces: `serveConfig.OperatorAuth` with an `oidc` block.

- [ ] **Step 1: Add the config block**

In `cmd/at-harbor/config.go`, add to `serveConfig` (after `Credentials`):

```go
	OperatorAuth struct {
		OIDC *struct {
			Issuer       string `yaml:"issuer"`
			Audience     string `yaml:"audience"`
			RequireScope string `yaml:"require-scope"`
		} `yaml:"oidc"`
	} `yaml:"operator-auth"`
```

- [ ] **Step 2: Write the failing config test**

In `cmd/at-harbor/config_test.go`, add:

```go
func TestParseServeConfigOIDC(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
listen: ":8443"
admin-listen: "127.0.0.1:8081"
store: /s.json
operator-auth:
  oidc:
    issuer: https://acme.us.auth0.com/
    audience: https://harbor.acme/api
    require-scope: harbor:admin
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.OperatorAuth.OIDC == nil || cfg.OperatorAuth.OIDC.Issuer != "https://acme.us.auth0.com/" ||
		cfg.OperatorAuth.OIDC.Audience != "https://harbor.acme/api" || cfg.OperatorAuth.OIDC.RequireScope != "harbor:admin" {
		t.Fatalf("oidc = %+v", cfg.OperatorAuth.OIDC)
	}
}
```

Run: `go test ./cmd/at-harbor/ -run TestParseServeConfigOIDC -v` → FAIL (field undefined). Then Step 1 makes it pass.

- [ ] **Step 3: Wire the authenticator selection in `cmdServe`**

In `cmd/at-harbor/main.go` `cmdServe`, replace the admin-API block (`if cfg.AdminListen != "" { … LoopbackAuthenticator{} … }`) with authenticator selection:

```go
	if cfg.AdminListen != "" {
		var auth harbor.OperatorAuthenticator = harbor.LoopbackAuthenticator{}
		if o := cfg.OperatorAuth.OIDC; o != nil {
			oidcAuth, err := harbor.NewOIDCAuthenticator(context.Background(), o.Issuer, o.Audience, o.RequireScope)
			if err != nil {
				fmt.Fprintln(stderr, "at-harbor: operator OIDC:", err)
				return 1
			}
			auth = oidcAuth
			log.Info("harbor admin auth: OIDC", "issuer", o.Issuer, "audience", o.Audience)
		} else {
			log.Info("harbor admin auth: loopback")
		}
		credExists := func(n string) bool { _, ok := specs[n]; return ok }
		admin := harbor.NewAdminHandler(st, auth, credExists, log)
		go func() {
			log.Info("harbor admin API listening", "addr", cfg.AdminListen)
			if err := http.ListenAndServe(cfg.AdminListen, admin); err != nil {
				log.Error("admin API stopped", "err", err.Error())
			}
		}()
	}
```

Add `"context"` to `cmd/at-harbor/main.go` imports.

- [ ] **Step 4: Run tests + build**

Run: `go build ./... && go test ./cmd/at-harbor/ -count=1 && go vet ./... && gofmt -l internal/ cmd/`
Expected: builds; config + serve tests pass; vet clean; gofmt lists nothing.

- [ ] **Step 5: Docs**

`docs/OVERVIEW.md`: in the at-harbor section, note the admin API now supports OIDC operator auth (`operator-auth.oidc`: issuer/audience/require-scope), loopback default; **safety** — the admin listener is plain-HTTP-loopback, so OIDC is validated but off-loopback exposure awaits TLS (next cut). `docs/usage/INDEX.md`: point to this spec. Keep terse.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go cmd/at-harbor/main.go docs/
git commit -m "feat(harbor): serve selects OIDC vs loopback operator auth from config"
```

---

### Task 3: adminclient bearer token + CLI `--token` / `AT_HARBOR_ADMIN_TOKEN`

**Files:**
- Modify: `internal/harbor/adminclient/adminclient.go`
- Modify: `internal/harbor/adminclient/adminclient_test.go`
- Modify: `cmd/at-harbor/main.go` (enroll/revoke/destination)

**Interfaces:**
- Produces: `func New(baseURL, token string) *Client` (token added); the client sends `Authorization: Bearer <token>` when non-empty.

- [ ] **Step 1: Update the failing client test**

In `internal/harbor/adminclient/adminclient_test.go`, change `New(ts.URL)` calls to `New(ts.URL, "")`, and add a bearer test:

```go
func TestClientSendsBearer(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte("[]"))
	}))
	defer ts.Close()
	if _, err := New(ts.URL, "tok-123").ListDestinations(); err != nil {
		t.Fatalf("ListDestinations: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
}
```

(Add `"net/http"` to the test imports if not present.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/adminclient/ -run TestClientSendsBearer -v`
Expected: FAIL — `too many arguments in call to New` (and the bearer assertion).

- [ ] **Step 3: Add the token to the client**

In `internal/harbor/adminclient/adminclient.go`, add a `token` field, take it in `New`, and set the header in `do`:

```go
type Client struct {
	base  string
	token string
	httpc *http.Client
}

// New returns a Client for the admin base URL. token (may be "") is sent as a
// bearer on every request — required against an OIDC-gated harbor, ignored by a
// loopback-gated one.
func New(baseURL, token string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, httpc: &http.Client{Timeout: 10 * time.Second}}
}
```

In `do`, after `req.Header.Set("Content-Type", …)` (or right after building `req`):

```go
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
```

- [ ] **Step 4: Thread the CLI flag**

In `cmd/at-harbor/main.go`, in `cmdEnroll`, `cmdRevoke`, and `cmdDestination`, add a token flag defaulting to the env var and pass it to `New`:

```go
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token for an OIDC-gated admin API (env: AT_HARBOR_ADMIN_TOKEN)")
```

and change each `adminclient.New(*adminURL)` to `adminclient.New(*adminURL, *token)`.

- [ ] **Step 5: Run tests + build + vet + gofmt**

Run: `go build ./... && go test ./... -count=1 && go vet ./... && gofmt -l internal/ cmd/`
Expected: builds; all pass; clean.

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminclient/ cmd/at-harbor/main.go
git commit -m "feat(harbor): admin client bearer token + CLI --token/AT_HARBOR_ADMIN_TOKEN"
```

---

### Task 4: Enroll snippet env rename → `AT_HARBOR_IDENTITY_TOKEN`

**Files:**
- Modify: `internal/harbor/enroll.go`
- Modify: `internal/harbor/enroll_test.go`

- [ ] **Step 1: Update the test to expect the new name**

In `internal/harbor/enroll_test.go` `TestRenderEnrollSnippetIncludesEndpointsNotSecrets`, replace `HARBOR_IDENTITY_TOKEN` with `AT_HARBOR_IDENTITY_TOKEN` in every `want` string (`export AT_HARBOR_IDENTITY_TOKEN=TOK123`, `ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN`, and the helper's `password=$AT_HARBOR_IDENTITY_TOKEN`). The "raw token appears exactly once" assertion is unchanged.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/ -run TestRenderEnrollSnippet -v`
Expected: FAIL (still emits `HARBOR_IDENTITY_TOKEN`).

- [ ] **Step 3: Rename in `RenderEnrollSnippet`**

In `internal/harbor/enroll.go`, change the three `HARBOR_IDENTITY_TOKEN` occurrences to `AT_HARBOR_IDENTITY_TOKEN` — the `const helper` string (`password=$AT_HARBOR_IDENTITY_TOKEN`), the `export AT_HARBOR_IDENTITY_TOKEN=%s` line, and the `ANTHROPIC_API_KEY=$AT_HARBOR_IDENTITY_TOKEN` line.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/harbor/ -run TestRenderEnrollSnippet -count=1 && gofmt -l internal/harbor`
Expected: PASS; gofmt clean.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/enroll.go internal/harbor/enroll_test.go
git commit -m "feat(harbor): rename identity env to AT_HARBOR_IDENTITY_TOKEN"
```

---

## Manual verification (definition of done)

1. Configure `operator-auth.oidc` (your Auth0 tenant issuer + a harbor API audience); `at-harbor serve` starts and logs `admin auth: OIDC`.
2. Obtain an access token for that audience (client-credentials or the Auth0 CLI). `at-harbor destination list --token <jwt>` (or `AT_HARBOR_ADMIN_TOKEN=<jwt>`) succeeds; a request with no/expired/wrong-aud token → 401/403.
3. With `require-scope: harbor:admin`, a token lacking that scope/permission → 403.
4. Remove `operator-auth` → loopback auth unchanged.
5. `at-harbor enroll` prints `AT_HARBOR_IDENTITY_TOKEN`.

## Self-review

**Spec coverage:** OIDCAuthenticator (go-oidc, iss/aud/exp/sig) → Task 1; config-selected authenticator → Task 2; authz default-any + require-scope → Task 1 (`hasScope`) + Task 2 config; CLI token + `AT_HARBOR_ADMIN_TOKEN` → Task 3; `AT_HARBOR_IDENTITY_TOKEN` rename → Task 4; audit (operator sub logged) → already in `admin.go` (logs `id` from the returned Operator); safety note (loopback-plain-HTTP) → Task 2 docs. Deferred (login flow, TLS/off-loopback, at-harborctl split) correctly absent.

**Placeholder scan:** none — complete code or exact edits in every step; the fake-provider harness is fully written.

**Type consistency:** `NewOIDCAuthenticator(ctx, issuer, audience, requireScope)` (Task 1) matches the serve call (Task 2); `New(baseURL, token)` (Task 3) matches the CLI callers (Task 3); `serveConfig.OperatorAuth.OIDC` (Task 2) matches its test + wiring; `Operator{ID}` unchanged from slice #2a.
