# Harbor Operator Login (OIDC device flow) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `at-harbor login`/`logout`/`whoami` — an OIDC device-flow sign-in that self-configures from harbor and caches the operator token so the whole `at-harbor` CLI works without hand-carrying `AT_HARBOR_ADMIN_TOKEN`.

**Architecture:** Harbor advertises its public device-flow client params at a new auth-exempt `GET /admin/login-config`. A stdlib-only `internal/harbor/deviceflow` client runs RFC 8628 (discovery → device-code → poll). The CLI caches the minted token at `~/.config/at-harbor/token.json` (0600) and reads client endpoint defaults from `~/.config/at-harbor/settings.yml`. Verification is unchanged from cut 1 (go-oidc, server-side).

**Tech Stack:** Go (stdlib) + `gopkg.in/yaml.v3` + `github.com/coreos/go-oidc/v3` (server-side, from cut 1). **The device-flow client is stdlib-only — no new dependency.**

## Global Constraints

- Module `github.com/aethons-tools/cove`; `go` directive already `1.25.0` (cut 1). Allowed deps: stdlib + `yaml.v3` + `coreos/go-oidc/v3` only — **no new dependency** (`deviceflow` is stdlib).
- Tests hermetic: a fake OIDC/harbor provider via `httptest`; a temp `XDG_CONFIG_HOME` (`t.Setenv`) for anything touching `~/.config/at-harbor`. Real Auth0 device-flow round-trip behind `//go:build integration` / manual only.
- Secrets: `token.json` is mode `0600`; **never log tokens**; `settings.yml` is non-secret. `/admin/login-config` returns only public OAuth params.
- Env-var convention `AT_HARBOR_` (unchanged): `AT_HARBOR_ADMIN_TOKEN`.
- Before each commit: `go build ./...`, `go vet ./...`, and `gofmt -l internal/ cmd/` must be clean (CI gate runs `STRICT=1 ./scripts/lint.sh`; run `gofmt -w` on **your** files only — do **not** reformat the pre-existing `internal/switchboard/discord_integration_test.go`).
- Cut-1 behavior (OIDC/loopback auth, broker, admin verbs) stays green.

---

### Task 1: `internal/harbor/deviceflow` — stdlib device-flow client

**Files:**
- Create: `internal/harbor/deviceflow/deviceflow.go`
- Create: `internal/harbor/deviceflow/deviceflow_test.go`

**Interfaces:**
- Produces: `Config{Issuer,Audience,ClientID,Scope}`; `DeviceCode{DeviceCode,UserCode,VerificationURI,VerificationURIComplete,TokenEndpoint,Interval,ExpiresIn}`; `Token{AccessToken,ExpiresIn}`; `HTTPDoer` interface; `RequestDeviceCode(ctx, HTTPDoer, Config) (DeviceCode, error)`; `PollToken(ctx, HTTPDoer, sleep func(time.Duration), tokenEndpoint, clientID, deviceCode string, interval int) (Token, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/deviceflow/deviceflow_test.go`:

```go
package deviceflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeProvider serves discovery + device-auth + a token endpoint that returns
// authorization_pending a set number of times before issuing a token.
type fakeProvider struct {
	url            string
	pendingLeft    int
	slowDownOnce   bool
	tokenToReturn  string
	deviceCodeSeen string
}

func newFakeProvider(t *testing.T, pending int, token string) *fakeProvider {
	t.Helper()
	f := &fakeProvider{pendingLeft: pending, tokenToReturn: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                        f.url,
			"device_authorization_endpoint": f.url + "/oauth/device/code",
			"token_endpoint":                f.url + "/oauth/token",
		})
	})
	mux.HandleFunc("/oauth/device/code", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "DEV-123", "user_code": "WXYZ-1234",
			"verification_uri":          f.url + "/activate",
			"verification_uri_complete": f.url + "/activate?code=WXYZ-1234",
			"interval":                  1, "expires_in": 600,
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.deviceCodeSeen = r.Form.Get("device_code")
		if f.slowDownOnce {
			f.slowDownOnce = false
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"error": "slow_down"})
			return
		}
		if f.pendingLeft > 0 {
			f.pendingLeft--
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": f.tokenToReturn, "expires_in": 3600})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func TestRequestDeviceCode(t *testing.T) {
	f := newFakeProvider(t, 0, "TOK")
	dc, err := RequestDeviceCode(context.Background(), http.DefaultClient, Config{
		Issuer: f.url, Audience: "https://harbor.test/api", ClientID: "cid", Scope: "openid",
	})
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if dc.UserCode != "WXYZ-1234" || dc.DeviceCode != "DEV-123" || dc.Interval != 1 {
		t.Fatalf("device code = %+v", dc)
	}
	if dc.TokenEndpoint != f.url+"/oauth/token" {
		t.Fatalf("token endpoint = %q", dc.TokenEndpoint)
	}
}

func TestPollTokenPendingThenSuccess(t *testing.T) {
	f := newFakeProvider(t, 2, "ACCESS-TOK")
	var slept int
	sleep := func(time.Duration) { slept++ }
	tok, err := PollToken(context.Background(), http.DefaultClient, sleep, f.url+"/oauth/token", "cid", "DEV-123", 1)
	if err != nil {
		t.Fatalf("PollToken: %v", err)
	}
	if tok.AccessToken != "ACCESS-TOK" {
		t.Fatalf("token = %q", tok.AccessToken)
	}
	if slept < 2 {
		t.Fatalf("expected to sleep between polls, slept=%d", slept)
	}
	if f.deviceCodeSeen != "DEV-123" {
		t.Fatalf("device_code sent = %q", f.deviceCodeSeen)
	}
}

func TestPollTokenSlowDownWidensInterval(t *testing.T) {
	f := newFakeProvider(t, 0, "T")
	f.slowDownOnce = true
	var durations []time.Duration
	sleep := func(d time.Duration) { durations = append(durations, d) }
	if _, err := PollToken(context.Background(), http.DefaultClient, sleep, f.url+"/oauth/token", "cid", "DEV-123", 1); err != nil {
		t.Fatalf("PollToken: %v", err)
	}
	if len(durations) == 0 || durations[0] <= time.Second {
		t.Fatalf("slow_down should widen the interval beyond 1s, got %v", durations)
	}
}

func TestPollTokenDenied(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"error": "access_denied"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := PollToken(context.Background(), http.DefaultClient, func(time.Duration) {}, srv.URL+"/oauth/token", "cid", "DEV-123", 1); err == nil {
		t.Fatal("expected error on access_denied")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/deviceflow/ 2>&1 | head`
Expected: FAIL — `undefined: RequestDeviceCode` / `Config` / etc. (build failure).

- [ ] **Step 3: Implement `internal/harbor/deviceflow/deviceflow.go`**

```go
// Package deviceflow implements the OAuth 2.0 device authorization grant
// (RFC 8628) as a public client — no client secret, no redirect server. It is
// stdlib-only and does NOT import go-oidc (harbor's server-side token verifier);
// it merely obtains a token that harbor later verifies.
package deviceflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPDoer is the subset of *http.Client the flow needs (injected for tests).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Config is the public client configuration harbor advertises.
type Config struct {
	Issuer   string
	Audience string
	ClientID string
	Scope    string
}

// DeviceCode is the device authorization response (RFC 8628 §3.2) plus the
// token endpoint discovered alongside it, so the caller can poll without
// re-running discovery.
type DeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	TokenEndpoint           string
	Interval                int // seconds
	ExpiresIn               int // seconds
}

// Token is the minted access token (only the bearer + its lifetime are needed).
type Token struct {
	AccessToken string
	ExpiresIn   int
}

type discoveryDoc struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

func discover(ctx context.Context, doer HTTPDoer, issuer string) (discoveryDoc, error) {
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return discoveryDoc{}, err
	}
	resp, err := doer.Do(req)
	if err != nil {
		return discoveryDoc{}, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return discoveryDoc{}, fmt.Errorf("oidc discovery: %s", resp.Status)
	}
	var d discoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return discoveryDoc{}, fmt.Errorf("oidc discovery decode: %w", err)
	}
	if d.DeviceAuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return discoveryDoc{}, fmt.Errorf("issuer %q does not advertise device-flow endpoints", issuer)
	}
	return d, nil
}

// RequestDeviceCode starts the flow: discovery, then POST the device
// authorization endpoint with client_id/scope/audience.
func RequestDeviceCode(ctx context.Context, doer HTTPDoer, cfg Config) (DeviceCode, error) {
	d, err := discover(ctx, doer, cfg.Issuer)
	if err != nil {
		return DeviceCode{}, err
	}
	form := url.Values{"client_id": {cfg.ClientID}, "scope": {cfg.Scope}, "audience": {cfg.Audience}}
	var out struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
	}
	if status, err := postForm(ctx, doer, d.DeviceAuthorizationEndpoint, form, &out); err != nil {
		return DeviceCode{}, fmt.Errorf("device authorization: %w", err)
	} else if status != http.StatusOK {
		return DeviceCode{}, fmt.Errorf("device authorization: status %d", status)
	}
	interval := out.Interval
	if interval <= 0 {
		interval = 5 // RFC 8628 default
	}
	return DeviceCode{
		DeviceCode: out.DeviceCode, UserCode: out.UserCode,
		VerificationURI: out.VerificationURI, VerificationURIComplete: out.VerificationURIComplete,
		TokenEndpoint: d.TokenEndpoint, Interval: interval, ExpiresIn: out.ExpiresIn,
	}, nil
}

// PollToken polls the token endpoint until the user approves, honoring
// authorization_pending (keep waiting), slow_down (widen the interval), and
// treating access_denied/expired_token as terminal. sleep is injected for tests.
func PollToken(ctx context.Context, doer HTTPDoer, sleep func(time.Duration), tokenEndpoint, clientID, deviceCode string, interval int) (Token, error) {
	if interval <= 0 {
		interval = 5
	}
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode}, "client_id": {clientID},
	}
	for {
		if err := ctx.Err(); err != nil {
			return Token{}, err
		}
		var out struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			Error       string `json:"error"`
		}
		status, err := postForm(ctx, doer, tokenEndpoint, form, &out)
		if err != nil {
			return Token{}, err
		}
		switch {
		case status == http.StatusOK && out.AccessToken != "":
			return Token{AccessToken: out.AccessToken, ExpiresIn: out.ExpiresIn}, nil
		case out.Error == "authorization_pending":
			// keep polling
		case out.Error == "slow_down":
			interval += 5
		case out.Error == "access_denied":
			return Token{}, fmt.Errorf("login was denied")
		case out.Error == "expired_token":
			return Token{}, fmt.Errorf("login timed out")
		default:
			return Token{}, fmt.Errorf("device token poll failed: %q (status %d)", out.Error, status)
		}
		sleep(time.Duration(interval) * time.Second)
	}
}

// postForm POSTs a urlencoded form and JSON-decodes the body into out (when
// non-empty). It returns the HTTP status so callers can branch on it.
func postForm(ctx context.Context, doer HTTPDoer, endpoint string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doer.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if len(body) > 0 && out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/harbor/deviceflow/ -count=1 -v`
Expected: PASS (RequestDeviceCode, pending→success, slow_down, denied).

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./internal/harbor/deviceflow/ && gofmt -l internal/harbor/deviceflow/
git add internal/harbor/deviceflow/
git commit -m "feat(harbor): stdlib OIDC device-flow client (RFC 8628)"
```

---

### Task 2: Server — `OperatorLoginConfig` + `GET /admin/login-config` (auth-exempt) + `NewAdminHandler` param

**Files:**
- Modify: `internal/harbor/admin.go`
- Modify: `internal/harbor/admin_test.go`
- Modify: `internal/harbor/adminclient/adminclient_test.go` (caller signature)
- Modify: `cmd/at-harbor/main.go` (caller signature — pass `nil` for now; Task 3 fills it)

**Interfaces:**
- Consumes: `OperatorAuthenticator`, `Operator` (cut 1).
- Produces: `OperatorLoginConfig{Issuer,Audience,ClientID,Scope}` (json-tagged); `NewAdminHandler(store, auth, credExists, login *OperatorLoginConfig, log) http.Handler`.

- [ ] **Step 1: Write the failing test**

In `internal/harbor/admin_test.go`, add (near the other admin tests):

```go
// denyAll rejects every request — proves /admin/login-config bypasses operator auth.
type denyAll struct{}

func (denyAll) Authenticate(*http.Request) (Operator, error) {
	return Operator{}, errDeny
}

var errDeny = fmt.Errorf("denied")

func TestLoginConfigServedAndAuthExempt(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	lc := &OperatorLoginConfig{Issuer: "https://acme.auth0.com/", Audience: "https://harbor.acme/api", ClientID: "cid", Scope: "openid"}
	h := NewAdminHandler(store, denyAll{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// login-config is reachable with NO token even though the authenticator denies all.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/login-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("login-config status = %d, want 200 (auth-exempt)", rec.Code)
	}
	var got OperatorLoginConfig
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got != *lc {
		t.Fatalf("login-config = %+v, want %+v", got, *lc)
	}
	// a normal route is still gated.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/destinations", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("destinations status = %d, want 403", rec.Code)
	}
}

func TestLoginConfig404WhenNotConfigured(t *testing.T) {
	store, _ := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := NewAdminHandler(store, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/login-config", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login-config status = %d, want 404 when not OIDC-gated", rec.Code)
	}
}
```

Add `"fmt"` to `internal/harbor/admin_test.go` imports if not present.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/ -run TestLoginConfig 2>&1 | head`
Expected: FAIL — `too many arguments in call to NewAdminHandler` / `undefined: OperatorLoginConfig` (build failure).

- [ ] **Step 3: Add the type, param, route, and auth exemption**

In `internal/harbor/admin.go`, add the type (after the `IdentitySummary` block):

```go
// OperatorLoginConfig is the public device-flow client config harbor advertises
// at GET /admin/login-config so `at-harbor login` can self-configure. Every field
// is a public OAuth parameter — never a secret.
type OperatorLoginConfig struct {
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
}
```

Change the signature:

```go
func NewAdminHandler(store Store, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger) http.Handler {
```

Register the route (right after the `GET /admin/healthz` handler):

```go
	mux.HandleFunc("GET /admin/login-config", func(w http.ResponseWriter, r *http.Request) {
		if login == nil {
			http.Error(w, "harbor is not OIDC-gated; no login required", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, login)
	})
```

Exempt it in `authMiddleware` (add before `auth.Authenticate`):

```go
func authMiddleware(auth OperatorAuthenticator, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// login-config is the pre-auth bootstrap (you call it to obtain a token) and
		// carries only public OAuth params — exempt it from operator auth.
		if r.Method == http.MethodGet && r.URL.Path == "/admin/login-config" {
			next.ServeHTTP(w, r)
			return
		}
		op, err := auth.Authenticate(r)
		if err != nil {
			log.Warn("admin request rejected", "reason", err.Error(), "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, withOperator(r, op))
	})
}
```

- [ ] **Step 4: Update the other callers to compile**

In `internal/harbor/admin_test.go` `newTestAdmin`, and `TestAdminLogsOperatorOnMutations`, add the `nil` login arg:
- `NewAdminHandler(store, LoopbackAuthenticator{}, credExists, nil, slog.New(...))`
- `NewAdminHandler(store, fixedOperator{id: "auth0|alice"}, credExists, nil, log)`

In `internal/harbor/adminclient/adminclient_test.go` `newServer`:
- `harbor.NewAdminHandler(store, harbor.LoopbackAuthenticator{}, func(n string) bool { return n == "git-pat" }, nil, slog.New(...))`

In `cmd/at-harbor/main.go` `cmdServe`, change the call to pass `nil` for now:
- `admin := harbor.NewAdminHandler(st, auth, credExists, nil, log)`

- [ ] **Step 5: Run to verify it passes**

Run: `go build ./... && go test ./internal/harbor/ ./internal/harbor/adminclient/ -count=1`
Expected: builds; all pass including the two new login-config tests.

- [ ] **Step 6: vet + gofmt + commit**

```bash
go vet ./internal/harbor/... && gofmt -l internal/harbor/
git add internal/harbor/admin.go internal/harbor/admin_test.go internal/harbor/adminclient/adminclient_test.go cmd/at-harbor/main.go
git commit -m "feat(harbor): advertise public device-flow client params at /admin/login-config"
```

---

### Task 3: Config `device-client-id`/`device-scope` + `cmdServe` builds the login config

**Files:**
- Modify: `cmd/at-harbor/config.go`
- Modify: `cmd/at-harbor/config_test.go`
- Modify: `cmd/at-harbor/main.go` (`cmdServe`)

**Interfaces:**
- Consumes: `harbor.OperatorLoginConfig` (Task 2).
- Produces: `serveConfig.OperatorAuth.OIDC.{DeviceClientID,DeviceScope}`; `serveConfig.operatorLoginConfig() *harbor.OperatorLoginConfig`.

- [ ] **Step 1: Write the failing test**

In `cmd/at-harbor/config_test.go`, add:

```go
func TestOperatorLoginConfig(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
operator-auth:
  oidc:
    issuer: https://acme.us.auth0.com/
    audience: https://harbor.acme/api
    device-client-id: NativeClientId123
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	lc := cfg.operatorLoginConfig()
	if lc == nil || lc.ClientID != "NativeClientId123" || lc.Issuer != "https://acme.us.auth0.com/" ||
		lc.Audience != "https://harbor.acme/api" || lc.Scope != "openid" {
		t.Fatalf("login config = %+v (want default scope openid)", lc)
	}

	// no device-client-id → no login config (login disabled)
	none, _ := parseServeConfig([]byte("operator-auth:\n  oidc:\n    issuer: x\n    audience: y\n"))
	if none.operatorLoginConfig() != nil {
		t.Fatal("expected nil login config without device-client-id")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/at-harbor/ -run TestOperatorLoginConfig 2>&1 | head`
Expected: FAIL — `cfg.operatorLoginConfig undefined` and unknown YAML fields.

- [ ] **Step 3: Add the config fields + builder**

In `cmd/at-harbor/config.go`, add the two fields to the OIDC struct:

```go
	OperatorAuth struct {
		OIDC *struct {
			Issuer         string `yaml:"issuer"`
			Audience       string `yaml:"audience"`
			RequireScope   string `yaml:"require-scope"`
			DeviceClientID string `yaml:"device-client-id"`
			DeviceScope    string `yaml:"device-scope"`
		} `yaml:"oidc"`
	} `yaml:"operator-auth"`
```

Add the import and builder. Change the import block to include harbor:

```go
import (
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/secret"
	"gopkg.in/yaml.v3"
)
```

```go
// operatorLoginConfig builds the public device-flow client config harbor
// advertises at /admin/login-config, or nil when device login isn't configured
// (no device-client-id). Scope defaults to "openid".
func (c serveConfig) operatorLoginConfig() *harbor.OperatorLoginConfig {
	o := c.OperatorAuth.OIDC
	if o == nil || o.DeviceClientID == "" {
		return nil
	}
	scope := o.DeviceScope
	if scope == "" {
		scope = "openid"
	}
	return &harbor.OperatorLoginConfig{Issuer: o.Issuer, Audience: o.Audience, ClientID: o.DeviceClientID, Scope: scope}
}
```

- [ ] **Step 4: Wire it into `cmdServe`**

In `cmd/at-harbor/main.go` `cmdServe`, replace the Task-2 `nil`:

```go
		admin := harbor.NewAdminHandler(st, auth, credExists, cfg.operatorLoginConfig(), log)
```

- [ ] **Step 5: Run to verify it passes + build**

Run: `go test ./cmd/at-harbor/ -run TestOperatorLoginConfig -count=1 && go build ./...`
Expected: PASS; builds.

- [ ] **Step 6: vet + gofmt + commit**

```bash
go vet ./cmd/at-harbor/ && gofmt -l cmd/at-harbor/
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go cmd/at-harbor/main.go
git commit -m "feat(harbor): serve config device-client-id/device-scope + login-config wiring"
```

---

### Task 4: `adminclient.LoginConfig()`

**Files:**
- Modify: `internal/harbor/adminclient/adminclient.go`
- Modify: `internal/harbor/adminclient/adminclient_test.go`

**Interfaces:**
- Produces: `func (c *Client) LoginConfig() (harbor.OperatorLoginConfig, error)`.

- [ ] **Step 1: Write the failing test**

In `internal/harbor/adminclient/adminclient_test.go`, add:

```go
func TestClientLoginConfig(t *testing.T) {
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	lc := &harbor.OperatorLoginConfig{Issuer: "https://acme.auth0.com/", Audience: "https://harbor.acme/api", ClientID: "cid", Scope: "openid"}
	h := harbor.NewAdminHandler(store, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()

	got, err := New(ts.URL, "").LoginConfig()
	if err != nil {
		t.Fatalf("LoginConfig: %v", err)
	}
	if got != *lc {
		t.Fatalf("LoginConfig = %+v, want %+v", got, *lc)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/adminclient/ -run TestClientLoginConfig 2>&1 | head`
Expected: FAIL — `New(...).LoginConfig undefined`.

- [ ] **Step 3: Implement the method**

In `internal/harbor/adminclient/adminclient.go`, add:

```go
// LoginConfig fetches harbor's public device-flow client parameters from
// GET /admin/login-config. It needs no token (the endpoint is auth-exempt); a
// 404 means the harbor is not OIDC-gated.
func (c *Client) LoginConfig() (harbor.OperatorLoginConfig, error) {
	var lc harbor.OperatorLoginConfig
	err := c.do("GET", "/admin/login-config", nil, &lc)
	return lc, err
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/harbor/adminclient/ -count=1`
Expected: PASS.

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./internal/harbor/adminclient/ && gofmt -l internal/harbor/adminclient/
git add internal/harbor/adminclient/
git commit -m "feat(harbor): adminclient.LoginConfig fetches /admin/login-config"
```

---

### Task 5: Client settings + token cache (`cmd/at-harbor/clientconfig.go`)

**Files:**
- Create: `cmd/at-harbor/clientconfig.go`
- Create: `cmd/at-harbor/clientconfig_test.go`

**Interfaces:**
- Produces: `configDir() string`; `clientSettings{AdminURL,BaseURL}` + `loadSettings() clientSettings`; `cachedToken{AccessToken,Sub,Expiry}` + `saveToken(cachedToken) error` / `loadToken() (cachedToken, bool)` / `clearToken() error` / `tokenPath() string`; `parseJWTClaims(token string) (sub string, exp time.Time, err error)`; `firstNonEmpty(...string) string`.

- [ ] **Step 1: Write the failing test**

Create `cmd/at-harbor/clientconfig_test.go`:

```go
package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsAndTokenCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// settings.yml
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir(), "settings.yml"),
		[]byte("admin-url: http://harbor.local:8081\nbase-url: https://harbor.local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadSettings()
	if s.AdminURL != "http://harbor.local:8081" || s.BaseURL != "https://harbor.local" {
		t.Fatalf("settings = %+v", s)
	}

	// token round-trip + 0600
	tok := cachedToken{AccessToken: "AT", Sub: "auth0|op", Expiry: time.Now().Add(time.Hour)}
	if err := saveToken(tok); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tokenPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, err=%v (want 0600)", info.Mode().Perm(), err)
	}
	got, ok := loadToken()
	if !ok || got.AccessToken != "AT" || got.Sub != "auth0|op" {
		t.Fatalf("loadToken = %+v ok=%v", got, ok)
	}

	// expired cache loads as absent
	if err := saveToken(cachedToken{AccessToken: "OLD", Expiry: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadToken(); ok {
		t.Fatal("expired token must load as absent")
	}

	// clear
	if err := clearToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenPath()); !os.IsNotExist(err) {
		t.Fatal("token file should be gone after clearToken")
	}
}

func TestParseJWTClaims(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	exp := time.Now().Add(time.Hour).Unix()
	token := b64(`{"alg":"RS256"}`) + "." + b64(`{"sub":"auth0|abc","exp":`+itoa(exp)+`}`) + ".sig"
	sub, gotExp, err := parseJWTClaims(token)
	if err != nil || sub != "auth0|abc" || gotExp.Unix() != exp {
		t.Fatalf("parseJWTClaims: sub=%q exp=%v err=%v", sub, gotExp, err)
	}
}

func itoa(n int64) string { return time.Unix(n, 0).Format("") /* placeholder replaced below */ }
```

Replace the `itoa` helper stub with `strconv`: at the top of the test change to `import "strconv"` and use `strconv.FormatInt(exp, 10)` inline instead of `itoa(exp)`; delete the `itoa` stub. (Written this way so the test has no undefined helper.)

Concretely, the final test uses:
```go
token := b64(`{"alg":"RS256"}`) + "." + b64(`{"sub":"auth0|abc","exp":`+strconv.FormatInt(exp, 10)+`}`) + ".sig"
```
with `strconv` imported and no `itoa` function.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/at-harbor/ -run 'TestSettingsAndTokenCache|TestParseJWTClaims' 2>&1 | head`
Expected: FAIL — `undefined: configDir` / `loadSettings` / `cachedToken` / `parseJWTClaims` etc.

- [ ] **Step 3: Implement `cmd/at-harbor/clientconfig.go`**

```go
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// configDir is the host-side config directory for the at-harbor CLI, mirroring
// at-cove: $XDG_CONFIG_HOME/at-harbor, else ~/.config/at-harbor.
func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-harbor")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-harbor")
}

// clientSettings is the operator-authored ~/.config/at-harbor/settings.yml —
// non-secret client endpoint defaults. Command flags override these.
type clientSettings struct {
	AdminURL string `yaml:"admin-url"`
	BaseURL  string `yaml:"base-url"`
}

// loadSettings reads settings.yml; an absent or unparseable file yields zero
// values (defaults apply), never an error — settings are optional convenience.
func loadSettings() clientSettings {
	var s clientSettings
	data, err := os.ReadFile(filepath.Join(configDir(), "settings.yml"))
	if err != nil {
		return s
	}
	_ = yaml.Unmarshal(data, &s)
	return s
}

// cachedToken is the login-owned ~/.config/at-harbor/token.json (mode 0600).
type cachedToken struct {
	AccessToken string    `json:"access_token"`
	Sub         string    `json:"sub"`
	Expiry      time.Time `json:"expiry"`
}

func tokenPath() string { return filepath.Join(configDir(), "token.json") }

func saveToken(t cachedToken) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(tokenPath(), data, 0o600)
}

// loadToken returns the cached token if present and unexpired; ok=false otherwise.
func loadToken() (cachedToken, bool) {
	data, err := os.ReadFile(tokenPath())
	if err != nil {
		return cachedToken{}, false
	}
	var t cachedToken
	if err := json.Unmarshal(data, &t); err != nil {
		return cachedToken{}, false
	}
	if !t.Expiry.IsZero() && time.Now().After(t.Expiry) {
		return cachedToken{}, false
	}
	return t, true
}

func clearToken() error {
	if err := os.Remove(tokenPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// parseJWTClaims extracts sub + exp from a JWT payload WITHOUT verifying the
// signature — for local display/expiry only. harbor verifies tokens server-side.
func parseJWTClaims(token string) (sub string, exp time.Time, err error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", time.Time{}, fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", time.Time{}, err
	}
	var c struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", time.Time{}, err
	}
	if c.Exp > 0 {
		exp = time.Unix(c.Exp, 0)
	}
	return c.Sub, exp, nil
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./cmd/at-harbor/ -run 'TestSettingsAndTokenCache|TestParseJWTClaims' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: vet + gofmt + commit**

```bash
go vet ./cmd/at-harbor/ && gofmt -l cmd/at-harbor/
git add cmd/at-harbor/clientconfig.go cmd/at-harbor/clientconfig_test.go
git commit -m "feat(harbor): at-harbor client settings.yml + 0600 token cache"
```

---

### Task 6: CLI `login`/`logout`/`whoami` + token precedence + docs

**Files:**
- Modify: `cmd/at-harbor/main.go`
- Create: `cmd/at-harbor/login_test.go`
- Modify: `docs/OVERVIEW.md`, `docs/usage/INDEX.md`

**Interfaces:**
- Consumes: `deviceflow` (Task 1), `adminclient.LoginConfig` (Task 4), `configDir`/`loadSettings`/`saveToken`/`loadToken`/`clearToken`/`parseJWTClaims`/`firstNonEmpty` (Task 5).
- Produces: `cmdLogin`, `cmdLogout`, `cmdWhoami`; token-resolution helper `resolveToken`.

- [ ] **Step 1: Write the failing test**

Create `cmd/at-harbor/login_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/at-harbor/ -run 'TestLogin|TestCommandUsesCached' 2>&1 | head`
Expected: FAIL — unknown command `login` (exit code non-zero / no user code) and the cached-bearer assertion fails (commands don't yet consult the cache).

- [ ] **Step 3: Register the commands + add the handlers**

In `cmd/at-harbor/main.go`, add to the `Commands` slice in `run`:

```go
			{Name: "login", Brief: "sign in via OIDC device flow and cache the operator token", Run: cmdLogin},
			{Name: "logout", Brief: "clear the cached operator token", Run: cmdLogout},
			{Name: "whoami", Brief: "show the cached operator identity", Run: cmdWhoami},
```

Add `"context"` (already imported from cut 1), `"time"`, and the deviceflow import to `cmd/at-harbor/main.go`:

```go
	"time"

	"github.com/aethons-tools/cove/internal/harbor/deviceflow"
```

Add the handlers:

```go
func cmdLogin(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	settings := loadSettings()
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	adminURL := fs.String("admin-url", firstNonEmpty(settings.AdminURL, defaultAdminURL), "harbor admin API URL")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-harbor login: unexpected arguments")
		return 2
	}
	lc, err := adminclient.New(*adminURL, "").LoginConfig()
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		fmt.Fprintln(stderr, "(a loopback-only harbor needs no login)")
		return 1
	}
	ctx := context.Background()
	dc, err := deviceflow.RequestDeviceCode(ctx, http.DefaultClient, deviceflow.Config{
		Issuer: lc.Issuer, Audience: lc.Audience, ClientID: lc.ClientID, Scope: lc.Scope,
	})
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	target := firstNonEmpty(dc.VerificationURIComplete, dc.VerificationURI)
	fmt.Fprintf(stdout, "To sign in, open:\n  %s\nand confirm the code: %s\n", target, dc.UserCode)
	tok, err := deviceflow.PollToken(ctx, http.DefaultClient, time.Sleep, dc.TokenEndpoint, lc.ClientID, dc.DeviceCode, dc.Interval)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	sub, exp, _ := parseJWTClaims(tok.AccessToken)
	if exp.IsZero() && tok.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	if err := saveToken(cachedToken{AccessToken: tok.AccessToken, Sub: sub, Expiry: exp}); err != nil {
		fmt.Fprintln(stderr, "at-harbor login:", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as %s; token expires %s\n", sub, exp.Format(time.RFC3339))
	return 0
}

func cmdLogout(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if err := clearToken(); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, "logged out")
	return 0
}

func cmdWhoami(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if t, ok := loadToken(); ok {
		fmt.Fprintf(stdout, "%s (expires %s)\n", t.Sub, t.Expiry.Format(time.RFC3339))
		return 0
	}
	if _, err := os.Stat(tokenPath()); err == nil {
		fmt.Fprintln(stdout, "session expired; run `at-harbor login`")
	} else {
		fmt.Fprintln(stdout, "not logged in")
	}
	return 0
}

// resolveToken applies the operator-token precedence: an explicit flag/env value
// wins, else the cached login token (when present and unexpired), else "".
func resolveToken(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if ct, ok := loadToken(); ok {
		return ct.AccessToken
	}
	return ""
}
```

- [ ] **Step 4: Thread cached-token + settings defaults into the admin verbs**

In `cmd/at-harbor/main.go`, in `cmdEnroll`, `cmdRevoke`, and `cmdDestination`:

1. At the top of each (before defining flags), load settings:
   ```go
	settings := loadSettings()
   ```
2. Change each `admin-url` flag default to prefer settings:
   ```go
	adminURL := fs.String("admin-url", firstNonEmpty(settings.AdminURL, defaultAdminURL), "harbor admin API URL")
   ```
3. In `cmdEnroll`, change the `base-url` flag default to prefer settings:
   ```go
	baseURL := fs.String("base-url", settings.BaseURL, "harbor broker base URL for the printed snippet")
   ```
4. Change every `adminclient.New(*adminURL, *token)` to use the resolver:
   ```go
	adminclient.New(*adminURL, resolveToken(*token))
   ```
   (In `cmdDestination` the client is built once as `c := adminclient.New(*adminURL, resolveToken(*token))`.)

- [ ] **Step 5: Run to verify it passes**

Run: `go build ./... && go test ./cmd/at-harbor/ -count=1`
Expected: builds; login/whoami/logout + cached-bearer tests pass; cut-1 tests still green.

- [ ] **Step 6: Full suite + vet + gofmt**

Run: `go test ./... -count=1 && go vet ./... && gofmt -l internal/ cmd/`
Expected: all pass; vet clean; gofmt lists nothing except the pre-existing `internal/switchboard/discord_integration_test.go` (leave it untouched).

- [ ] **Step 7: Docs**

`docs/OVERVIEW.md` — in the at-harbor paragraph, note operators sign in with `at-harbor login` (OIDC device flow, self-configured from `GET /admin/login-config`), that `logout`/`whoami` manage the cached token at `~/.config/at-harbor/token.json` (0600), and that `~/.config/at-harbor/settings.yml` holds client endpoint defaults (`admin-url`/`base-url`); server config gains `operator-auth.oidc.device-client-id`. Keep terse.

`docs/usage/INDEX.md` — add a row after the operator-OIDC row:

```markdown
| [harbor operator login](../superpowers/specs/2026-09-11-harbor-operator-login-design.md) | `at-harbor login`/`logout`/`whoami` — OIDC device-flow sign-in that self-configures from harbor's auth-exempt `GET /admin/login-config` ({issuer,audience,client_id,scope}), caches the operator token at `~/.config/at-harbor/token.json` (0600), and reads endpoint defaults from `settings.yml`. Server config adds `operator-auth.oidc.device-client-id`/`device-scope`. | You are signing in an operator to an OIDC-gated harbor, or wiring the device-flow client id / client settings. |
```

- [ ] **Step 8: Commit**

```bash
git add cmd/at-harbor/main.go cmd/at-harbor/login_test.go docs/OVERVIEW.md docs/usage/INDEX.md
git commit -m "feat(harbor): at-harbor login/logout/whoami via OIDC device flow + cached-token fallback"
```

---

## Manual verification (definition of done)

1. Server: `operator-auth.oidc` has `device-client-id`; `at-harbor serve` starts.
2. `curl -s $ADMIN/admin/login-config` returns `{issuer,audience,client_id,scope}` **with no token**; a loopback-only harbor returns 404.
3. `at-harbor login` prints a code + URL; after browser approval it caches the token and prints `logged in as <sub>`.
4. `at-harbor destination list` (no `--token`, no env) succeeds via the cached token.
5. `at-harbor whoami` prints `<sub>` + expiry; `at-harbor logout` clears it; a follow-up command reports the missing/expired session.
6. A `settings.yml` with `admin-url` lets every verb run without `--admin-url`.

## Self-review

**Spec coverage:** `/admin/login-config` (auth-exempt, 404 when off) → Task 2; `device-client-id`/`device-scope` config → Task 3; device-flow client → Task 1; `adminclient.LoginConfig` → Task 4; `settings.yml` + `token.json` (0600) + JWT parse → Task 5; `login`/`logout`/`whoami` + token precedence (`--token`→env→cache) → Task 6; docs → Task 6. Deferred (refresh tokens, TLS/off-loopback, at-harborctl) correctly absent.

**Placeholder scan:** none. The one helper stub in the Task-5 test (`itoa`) is explicitly replaced with `strconv.FormatInt` in the same step, with the final line shown.

**Type consistency:** `NewAdminHandler(store, auth, credExists, *OperatorLoginConfig, log)` is used identically in Tasks 2/3/4/6; `deviceflow.RequestDeviceCode`/`PollToken` signatures match their calls in Task 6; `DeviceCode.TokenEndpoint` (Task 1) is consumed in Task 6; `resolveToken`/`firstNonEmpty`/cache helpers (Tasks 5/6) match usage; `operatorLoginConfig()` (Task 3) returns `*harbor.OperatorLoginConfig` (Task 2).
