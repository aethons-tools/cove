# at-mint Plan 2 — the `at-mint` binary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `at-mint`, a third binary in the `at-cove` family that mints one short-lived token to stdout — `at-mint github` (a repo-scoped GitHub App installation token) and `at-mint anthropic` (an Anthropic `sk-ant-oat01` via an Auth0 client-credentials JWT exchanged through Anthropic federation) — replacing the ad-hoc `mint-github-token.sh` shell script with tested Go.

**Architecture:** `cmd/at-mint` is `package main` using the shared `internal/cli` `cli.App` (like `at-task`), with two subcommands. Each subcommand parses non-secret identifiers as **flags** and reads secret material from **env** (never argv), then calls a pure provider function (`mintGitHub`/`mintAnthropic`) that takes an injectable `*http.Client` and returns the token or an error. JWT signing is stdlib (`crypto/rsa`, RS256). The binary prints exactly the token to stdout on success and fails closed (non-zero exit, message to stderr, nothing on stdout) on any error. Hermetic tests inject a fake `http.RoundTripper`; no network.

**Tech Stack:** Go 1.22, standard library only (`crypto/rsa`, `crypto/sha256`, `crypto/x509`, `crypto/rand`, `encoding/pem`, `encoding/base64`, `encoding/json`, `net/http`), the in-repo `internal/cli`. No new dependencies.

## Global Constraints

- **No new dependencies.** Standard library + the existing `internal/cli` only. `go.mod`/`go.sum` must be unchanged after this plan. (The module's only third-party dep stays `gopkg.in/yaml.v3`, which `at-mint` does not import.)
- **Flags = non-secret, env = secret.** Non-secret identifiers (app id, install id, tenant, audience, client id, org, rule, service-account, workspace, and the *path* to a key file) are command-line flags. Secret material (the GitHub App private-key **content**, the Auth0 **client secret**) is read ONLY from environment variables — never a flag, never on argv.
- **One token to stdout; fail closed.** On success, `at-mint` writes exactly the token (plus a trailing newline) to stdout and exits 0. On ANY error it writes a diagnostic to stderr, writes NOTHING to stdout, and exits non-zero. No secret value is ever written to stderr, a log, or argv.
- **Scope to `COVE_RUN_REPO`.** `at-mint github` scopes the token to the repo named by the `COVE_RUN_REPO` env var (set by at-cove per run), with `contents:write` + `pull_requests:write` — exactly as the shell script did. Scope is fixed in code, not caller-widenable.
- **Hermetic tests.** Provider functions take an injectable `*http.Client`; tests use a fake `http.RoundTripper` (the repo idiom: `type rtFunc func(*http.Request)(*http.Response,error)` with a `RoundTrip` method). Real GitHub/Auth0/Anthropic round-trips are out of scope for unit tests (a maintainer-run `integration`-tagged test may come later).
- **`at-mint` does NOT wire into secret resolution here.** It is a standalone binary invoked as a `command:` (e.g. `command: ["at-mint","github",...]`). The `mint:` profile expander (`internal/mint`) that constructs the invocation from a `minters:` profile is Plan 3. This plan makes `command: ["at-mint",...]` fully usable.

---

## File Structure

- `cmd/at-mint/main.go` (new) — `var version`, `main()`, `run(args, env, httpc, stdout, stderr)`; the `cli.App` with `github`/`anthropic` subcommands; `doGitHub`/`doAnthropic` handlers (flag + env parsing, fail-closed output).
- `cmd/at-mint/jwt.go` (new) — `signRS256`, `parseRSAPrivateKey`, `b64url`.
- `cmd/at-mint/github.go` (new) — `githubInput`, `mintGitHub`.
- `cmd/at-mint/anthropic.go` (new) — `anthropicInput`, `mintAnthropic` (+ `auth0Token`, `anthropicExchange`).
- `cmd/at-mint/*_test.go` (new) — hermetic unit tests per file (shared `rtFunc`/`resp` test helpers live in one test file).
- `scripts/build.sh` (modify) — add `at-mint` to the `BINARIES` array.
- `justfile` (modify) — add `at-mint` to the `install` binary loop.
- `kits/reference-worker/mint-github-token.sh` (delete) — superseded by `at-mint github`.
- `kits/reference-worker/RUNBOOK.md` (modify) — the supply examples use `at-mint`, not the shell script.
- `docs/usage/at-mint.md` (new) + `docs/usage/INDEX.md` (modify) + `docs/OVERVIEW.md` (modify) — an `at-mint` usage leaf.

All of `cmd/at-mint` is `package main`; provider functions are package-main functions tested directly (the `at-task`/`at-cove` convention — `run()` and helpers tested in-package).

---

### Task 1: JWT / crypto helpers

**Files:**
- Create: `cmd/at-mint/jwt.go`
- Test: `cmd/at-mint/jwt_test.go`

**Interfaces:**
- Produces:
  - `func b64url(b []byte) string` — base64url, no padding.
  - `func signRS256(headerJSON, payloadJSON []byte, key *rsa.PrivateKey) (string, error)` — compact JWS `b64url(header).b64url(payload).b64url(sig)`, RS256.
  - `func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error)` — PKCS#1 or PKCS#8 PEM.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestSignRS256RoundTrips(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	header := []byte(`{"alg":"RS256","typ":"JWT"}`)
	payload := []byte(`{"iss":"123","iat":1,"exp":2}`)
	jwt, err := signRS256(header, payload, key)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}
	if parts[0] != b64url(header) || parts[1] != b64url(payload) {
		t.Fatal("header/payload segments not base64url of inputs")
	}
	// Verify the signature over "header.payload" with the public key.
	signingInput := parts[0] + "." + parts[1]
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := b64urlDecodeForTest(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}

func TestParseRSAPrivateKeyPKCS1(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	got, err := parseRSAPrivateKey(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got.N.Cmp(key.N) != 0 {
		t.Fatal("parsed key modulus differs")
	}
}

func TestParseRSAPrivateKeyRejectsGarbage(t *testing.T) {
	if _, err := parseRSAPrivateKey([]byte("not a pem")); err == nil {
		t.Fatal("want error for non-PEM input")
	}
}
```

Add this small test-only decode helper in the same file (tests need raw bytes back):

```go
func b64urlDecodeForTest(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-mint/ -run 'TestSign|TestParseRSA' -v`
Expected: FAIL — `undefined: signRS256` (the package does not exist yet). If `go test` complains the package has no non-test files, that is the same "not yet implemented" signal — proceed to Step 3.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
)

// b64url is base64url without padding, as JWT/JWS requires.
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signRS256 produces a compact JWS: b64url(header).b64url(payload).b64url(sig),
// signed RS256 (RSASSA-PKCS1-v1_5 over SHA-256).
func signRS256(headerJSON, payloadJSON []byte, key *rsa.PrivateKey) (string, error) {
	signingInput := b64url(headerJSON) + "." + b64url(payloadJSON)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signingInput + "." + b64url(sig), nil
}

// parseRSAPrivateKey parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8).
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse RSA private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PEM key is not an RSA private key")
	}
	return rk, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-mint/ -run 'TestSign|TestParseRSA' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-mint/jwt.go cmd/at-mint/jwt_test.go
git commit -m "feat(at-mint): RS256 JWT signing + RSA PEM key parsing (stdlib)"
```

---

### Task 2: GitHub provider

**Files:**
- Create: `cmd/at-mint/github.go`
- Test: `cmd/at-mint/github_test.go` (also defines the shared `rtFunc`/`resp` test helpers used by later tasks)

**Interfaces:**
- Consumes: `signRS256`, `parseRSAPrivateKey` (Task 1).
- Produces:
  - `type githubInput struct { AppID, InstallID string; KeyPEM []byte; Repo string }`
  - `func mintGitHub(ctx context.Context, httpc *http.Client, in githubInput, now time.Time) (string, error)` — builds an RS256 App JWT (iat=now-60s, exp=now+540s, iss=AppID), POSTs `{"repositories":["<name>"],"permissions":{"contents":"write","pull_requests":"write"}}` to `https://api.github.com/app/installations/<InstallID>/access_tokens` with `Authorization: Bearer <jwt>`, and returns the response's `.token`.
  - Shared test helpers: `type rtFunc func(*http.Request)(*http.Response,error)` with `RoundTrip`, and `func resp(code int, body string) *http.Response`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestMintGitHubHappyPath(t *testing.T) {
	var gotURL, gotAuth string
	var gotBody map[string]any
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		return resp(201, `{"token":"ghs_installationtoken"}`), nil
	})}
	in := githubInput{AppID: "123", InstallID: "456", KeyPEM: testKeyPEM(t), Repo: "acme/widgets"}
	tok, err := mintGitHub(context.Background(), c, in, time.Unix(1_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if tok != "ghs_installationtoken" {
		t.Fatalf("token = %q", tok)
	}
	if gotURL != "https://api.github.com/app/installations/456/access_tokens" {
		t.Fatalf("url = %q", gotURL)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") || strings.Count(gotAuth, ".") != 2 {
		t.Fatalf("auth header not a bearer JWT: %q", gotAuth)
	}
	repos, _ := gotBody["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "widgets" { // owner/name -> name
		t.Fatalf("repositories = %v, want [widgets]", gotBody["repositories"])
	}
	perms, _ := gotBody["permissions"].(map[string]any)
	if perms["contents"] != "write" || perms["pull_requests"] != "write" {
		t.Fatalf("permissions = %v", gotBody["permissions"])
	}
}

func TestMintGitHubNon2xxFailsClosed(t *testing.T) {
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(403, `{"message":"Resource not accessible by integration"}`), nil
	})}
	in := githubInput{AppID: "1", InstallID: "2", KeyPEM: testKeyPEM(t), Repo: "o/r"}
	if _, err := mintGitHub(context.Background(), c, in, time.Now()); err == nil {
		t.Fatal("non-2xx must be an error")
	}
}

func TestMintGitHubRequiresRepo(t *testing.T) {
	in := githubInput{AppID: "1", InstallID: "2", KeyPEM: testKeyPEM(t), Repo: ""}
	if _, err := mintGitHub(context.Background(), &http.Client{}, in, time.Now()); err == nil {
		t.Fatal("empty repo must be an error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-mint/ -run TestMintGitHub -v`
Expected: FAIL — `undefined: mintGitHub`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type githubInput struct {
	AppID     string
	InstallID string
	KeyPEM    []byte
	Repo      string // "owner/name"
}

// mintGitHub mints a short-lived GitHub App installation token scoped to in.Repo
// with contents+pull_requests write. It signs a ~9-minute App JWT (RS256) and
// exchanges it at the installation access-tokens endpoint.
func mintGitHub(ctx context.Context, httpc *http.Client, in githubInput, now time.Time) (string, error) {
	if in.AppID == "" || in.InstallID == "" {
		return "", fmt.Errorf("app-id and install-id are required")
	}
	if in.Repo == "" {
		return "", fmt.Errorf("COVE_RUN_REPO is not set (the repo to scope the token to)")
	}
	key, err := parseRSAPrivateKey(in.KeyPEM)
	if err != nil {
		return "", err
	}
	header := []byte(`{"alg":"RS256","typ":"JWT"}`)
	payload, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(540 * time.Second).Unix(),
		"iss": in.AppID,
	})
	if err != nil {
		return "", err
	}
	jwt, err := signRS256(header, payload, key)
	if err != nil {
		return "", err
	}
	repoName := in.Repo
	if i := strings.IndexByte(repoName, '/'); i >= 0 {
		repoName = repoName[i+1:] // installation is org-scoped; request by name
	}
	body, err := json.Marshal(map[string]any{
		"repositories": []string{repoName},
		"permissions":  map[string]string{"contents": "write", "pull_requests": "write"},
	})
	if err != nil {
		return "", err
	}
	url := "https://api.github.com/app/installations/" + in.InstallID + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	res, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("github installation token: HTTP %d: %s", res.StatusCode, strings.TrimSpace(buf.String()))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return "", fmt.Errorf("parse github response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("github response contained no token")
	}
	return out.Token, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-mint/ -run TestMintGitHub -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-mint/github.go cmd/at-mint/github_test.go
git commit -m "feat(at-mint): github provider — App JWT -> repo-scoped installation token"
```

---

### Task 3: Anthropic provider (Auth0 → federation)

**Files:**
- Create: `cmd/at-mint/anthropic.go`
- Test: `cmd/at-mint/anthropic_test.go`

**Interfaces:**
- Consumes: the `rtFunc`/`resp` helpers (Task 2).
- Produces:
  - `type anthropicInput struct { Tenant, ClientID, Audience, ClientSecret, Org, Rule, ServiceAccount, Workspace string }`
  - `func mintAnthropic(ctx context.Context, httpc *http.Client, in anthropicInput) (string, error)` — hop 1: `POST https://<Tenant>/oauth/token` grant `client_credentials` (`client_id`/`client_secret`/`audience`) → an upstream JWT (`access_token`); hop 2: `POST https://api.anthropic.com/v1/oauth/token` grant `urn:ietf:params:oauth:grant-type:jwt-bearer` with `{assertion, federation_rule_id, service_account_id, organization_id[, workspace_id]}` → the `sk-ant-oat01` (`access_token`).

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMintAnthropicTwoHops(t *testing.T) {
	var hop1Body, hop2Body map[string]any
	var hop2URL string
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.String(), "tenant.us.auth0.com/oauth/token"):
			_ = json.Unmarshal(b, &hop1Body)
			return resp(200, `{"access_token":"upstream.jwt.sig","token_type":"Bearer","expires_in":900}`), nil
		case strings.Contains(r.URL.String(), "api.anthropic.com/v1/oauth/token"):
			hop2URL = r.URL.String()
			_ = json.Unmarshal(b, &hop2Body)
			return resp(200, `{"access_token":"sk-ant-oat01-xyz","expires_in":600}`), nil
		default:
			t.Fatalf("unexpected URL %s", r.URL)
			return nil, nil
		}
	})}
	in := anthropicInput{
		Tenant: "tenant.us.auth0.com", ClientID: "cid", Audience: "urn:cove:wif", ClientSecret: "shh",
		Org: "org-uuid", Rule: "fdrl_1", ServiceAccount: "svac_1", Workspace: "wrkspc_1",
	}
	tok, err := mintAnthropic(context.Background(), c, in)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "sk-ant-oat01-xyz" {
		t.Fatalf("token = %q", tok)
	}
	// hop 1: client-credentials with audience + secret
	if hop1Body["grant_type"] != "client_credentials" || hop1Body["client_id"] != "cid" ||
		hop1Body["client_secret"] != "shh" || hop1Body["audience"] != "urn:cove:wif" {
		t.Fatalf("hop1 body = %v", hop1Body)
	}
	// hop 2: jwt-bearer with the upstream assertion + federation ids
	if hop2URL != "https://api.anthropic.com/v1/oauth/token" {
		t.Fatalf("hop2 url = %q", hop2URL)
	}
	if hop2Body["grant_type"] != "urn:ietf:params:oauth:grant-type:jwt-bearer" ||
		hop2Body["assertion"] != "upstream.jwt.sig" || hop2Body["federation_rule_id"] != "fdrl_1" ||
		hop2Body["service_account_id"] != "svac_1" || hop2Body["organization_id"] != "org-uuid" ||
		hop2Body["workspace_id"] != "wrkspc_1" {
		t.Fatalf("hop2 body = %v", hop2Body)
	}
}

func TestMintAnthropicHop1FailsClosed(t *testing.T) {
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(401, `{"error":"access_denied"}`), nil
	})}
	in := anthropicInput{Tenant: "t.auth0.com", ClientID: "c", Audience: "a", ClientSecret: "s", Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"}
	if _, err := mintAnthropic(context.Background(), c, in); err == nil {
		t.Fatal("hop1 non-2xx must be an error")
	}
}

func TestMintAnthropicOmitsEmptyWorkspace(t *testing.T) {
	var hop2Body map[string]any
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.String(), "auth0.com") {
			return resp(200, `{"access_token":"j"}`), nil
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &hop2Body)
		return resp(200, `{"access_token":"sk-ant-oat01-z"}`), nil
	})}
	in := anthropicInput{Tenant: "t.auth0.com", ClientID: "c", Audience: "a", ClientSecret: "s", Org: "o", Rule: "fdrl_1", ServiceAccount: "svac_1"} // no Workspace
	if _, err := mintAnthropic(context.Background(), c, in); err != nil {
		t.Fatal(err)
	}
	if _, present := hop2Body["workspace_id"]; present {
		t.Fatal("workspace_id must be omitted when Workspace is empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-mint/ -run TestMintAnthropic -v`
Expected: FAIL — `undefined: mintAnthropic`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type anthropicInput struct {
	Tenant         string
	ClientID       string
	Audience       string
	ClientSecret   string
	Org            string
	Rule           string
	ServiceAccount string
	Workspace      string
}

// mintAnthropic mints an sk-ant-oat01 via two hops: an Auth0 client-credentials
// JWT (hop 1) exchanged through Anthropic federation (hop 2).
func mintAnthropic(ctx context.Context, httpc *http.Client, in anthropicInput) (string, error) {
	if in.Tenant == "" || in.ClientID == "" || in.Audience == "" || in.ClientSecret == "" {
		return "", fmt.Errorf("auth0 tenant, client-id, audience and client secret are required")
	}
	if in.Org == "" || in.Rule == "" || in.ServiceAccount == "" {
		return "", fmt.Errorf("anthropic org, rule and service-account are required")
	}
	assertion, err := auth0Token(ctx, httpc, in)
	if err != nil {
		return "", err
	}
	return anthropicExchange(ctx, httpc, in, assertion)
}

func auth0Token(ctx context.Context, httpc *http.Client, in anthropicInput) (string, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     in.ClientID,
		"client_secret": in.ClientSecret,
		"audience":      in.Audience,
	})
	if err != nil {
		return "", err
	}
	url := "https://" + in.Tenant + "/oauth/token"
	tok, err := postForToken(ctx, httpc, url, nil, body)
	if err != nil {
		return "", fmt.Errorf("auth0 client-credentials: %w", err)
	}
	return tok, nil
}

func anthropicExchange(ctx context.Context, httpc *http.Client, in anthropicInput, assertion string) (string, error) {
	payload := map[string]string{
		"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
		"assertion":          assertion,
		"federation_rule_id": in.Rule,
		"service_account_id": in.ServiceAccount,
		"organization_id":    in.Org,
	}
	if in.Workspace != "" {
		payload["workspace_id"] = in.Workspace
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	tok, err := postForToken(ctx, httpc, "https://api.anthropic.com/v1/oauth/token",
		map[string]string{"anthropic-version": "2023-06-01"}, body)
	if err != nil {
		return "", fmt.Errorf("anthropic federation exchange: %w", err)
	}
	return tok, nil
}

// postForToken POSTs a JSON body and returns the response's access_token, failing
// closed on any non-2xx or a missing token.
func postForToken(ctx context.Context, httpc *http.Client, url string, extraHeaders map[string]string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	res, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(buf.String()))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("response contained no access_token")
	}
	return out.AccessToken, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-mint/ -run TestMintAnthropic -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-mint/anthropic.go cmd/at-mint/anthropic_test.go
git commit -m "feat(at-mint): anthropic provider — Auth0 client-credentials -> federation oat"
```

---

### Task 4: `main.go` — subcommand dispatch, flags/env, fail-closed

**Files:**
- Create: `cmd/at-mint/main.go`
- Test: `cmd/at-mint/main_test.go`

**Interfaces:**
- Consumes: `mintGitHub`, `mintAnthropic`; `internal/cli` (`cli.App`, `cli.Command`, `cli.Globals`, `cli.ParseInterspersed`).
- Produces:
  - `var version = "dev"`
  - `func main()`
  - `func run(args []string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int`
  - Non-secret flags per subcommand; secrets from env (`AT_MINT_GITHUB_APP_KEY`, `AT_MINT_AUTH0_CLIENT_SECRET`); `COVE_RUN_REPO` from env for `github`. Token → stdout (+`\n`), errors → stderr, fail-closed exit codes.

**Import note:** the module path is `github.com/aethons-tools/cove`; import the CLI as `"github.com/aethons-tools/cove/internal/cli"`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
)

// envMap builds an env lookup func from a map.
func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestRunVersion(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "9.9.9"
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, envMap(nil), http.DefaultClient, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "9.9.9") {
		t.Fatalf("version not printed: %q", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, envMap(nil), http.DefaultClient, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunGitHubPrintsOnlyToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	httpc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(201, `{"token":"ghs_ok"}`), nil
	})}
	env := envMap(map[string]string{
		"AT_MINT_GITHUB_APP_KEY": string(keyPEM),
		"COVE_RUN_REPO":          "acme/widgets",
	})
	var out, errOut bytes.Buffer
	code := run([]string{"github", "--app-id", "1", "--install-id", "2"}, env, httpc, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "ghs_ok" {
		t.Fatalf("stdout must be exactly the token, got %q", out.String())
	}
}

func TestRunGitHubFailsClosedNoStdout(t *testing.T) {
	// Missing key env -> error, non-zero, nothing on stdout.
	env := envMap(map[string]string{"COVE_RUN_REPO": "o/r"})
	var out, errOut bytes.Buffer
	code := run([]string{"github", "--app-id", "1", "--install-id", "2"}, env, http.DefaultClient, &out, &errOut)
	if code == 0 {
		t.Fatal("missing app key must fail closed")
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must be empty on failure, got %q", out.String())
	}
	if errOut.Len() == 0 {
		t.Fatal("expected a diagnostic on stderr")
	}
}

func TestRunAnthropicPrintsOnlyToken(t *testing.T) {
	httpc := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.String(), "auth0.com") {
			return resp(200, `{"access_token":"j"}`), nil
		}
		return resp(200, `{"access_token":"sk-ant-oat01-ok"}`), nil
	})}
	env := envMap(map[string]string{"AT_MINT_AUTH0_CLIENT_SECRET": "shh"})
	var out, errOut bytes.Buffer
	code := run([]string{"anthropic",
		"--auth0-tenant", "t.us.auth0.com", "--auth0-client-id", "c", "--auth0-audience", "a",
		"--anthropic-org", "o", "--anthropic-rule", "fdrl_1", "--anthropic-service-account", "svac_1",
	}, env, httpc, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "sk-ant-oat01-ok" {
		t.Fatalf("stdout = %q", out.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-mint/ -run TestRun -v`
Expected: FAIL — `undefined: run` / `undefined: version`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, http.DefaultClient, os.Stdout, os.Stderr))
}

// run dispatches the at-mint subcommands. env and httpc are injected for tests.
func run(args []string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "at-mint",
		Version: version,
		Commands: []cli.Command{
			{Name: "github", Brief: "mint a repo-scoped GitHub App installation token", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return doGitHub(a, env, httpc, out, errw)
			}},
			{Name: "anthropic", Brief: "mint an Anthropic oauth token via Auth0 WIF", Run: func(a []string, g cli.Globals, out, errw io.Writer) int {
				return doAnthropic(a, env, httpc, out, errw)
			}},
		},
	}
	return app.Run(args, stdout, stderr)
}

// getenv reads a var via the injected lookup.
func getenv(env func(string) (string, bool), k string) string { v, _ := env(k); return v }

func doGitHub(args []string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("github", flag.ContinueOnError)
	fs.SetOutput(stderr)
	appID := fs.String("app-id", "", "GitHub App id (non-secret)")
	installID := fs.String("install-id", "", "GitHub App installation id (non-secret)")
	appKeyFile := fs.String("app-key-file", "", "path to the App private-key PEM (a path is non-secret)")
	if _, err := cli.ParseInterspersed(fs, args); err != nil {
		return 2
	}
	keyPEM, err := readKeyPEM(*appKeyFile, getenv(env, "AT_MINT_GITHUB_APP_KEY"))
	if err != nil {
		fmt.Fprintf(stderr, "at-mint: %v\n", err)
		return 1
	}
	in := githubInput{AppID: *appID, InstallID: *installID, KeyPEM: keyPEM, Repo: getenv(env, "COVE_RUN_REPO")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := mintGitHub(ctx, httpc, in, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "at-mint: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, tok)
	return 0
}

// readKeyPEM prefers the file path (non-secret) and falls back to env content.
func readKeyPEM(path, envContent string) ([]byte, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading --app-key-file: %w", err)
		}
		return b, nil
	}
	if envContent != "" {
		return []byte(envContent), nil
	}
	return nil, fmt.Errorf("no App private key: set --app-key-file or AT_MINT_GITHUB_APP_KEY")
}

func doAnthropic(args []string, env func(string) (string, bool), httpc *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("anthropic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tenant := fs.String("auth0-tenant", "", "Auth0 tenant domain, e.g. tenant.us.auth0.com (non-secret)")
	clientID := fs.String("auth0-client-id", "", "Auth0 M2M client id (non-secret)")
	audience := fs.String("auth0-audience", "", "Auth0 API identifier / token aud (non-secret)")
	org := fs.String("anthropic-org", "", "Anthropic organization id (non-secret)")
	rule := fs.String("anthropic-rule", "", "Anthropic federation rule id fdrl_... (non-secret)")
	svc := fs.String("anthropic-service-account", "", "Anthropic service account id svac_... (non-secret)")
	workspace := fs.String("anthropic-workspace", "", "Anthropic workspace id (optional, non-secret)")
	if _, err := cli.ParseInterspersed(fs, args); err != nil {
		return 2
	}
	secret := getenv(env, "AT_MINT_AUTH0_CLIENT_SECRET")
	if secret == "" {
		fmt.Fprintln(stderr, "at-mint: AT_MINT_AUTH0_CLIENT_SECRET is not set")
		return 1
	}
	in := anthropicInput{
		Tenant: *tenant, ClientID: *clientID, Audience: *audience, ClientSecret: secret,
		Org: *org, Rule: *rule, ServiceAccount: *svc, Workspace: *workspace,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := mintAnthropic(ctx, httpc, in)
	if err != nil {
		fmt.Fprintf(stderr, "at-mint: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, tok)
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-mint/ -v`
Expected: PASS (all at-mint tests). Then `go build ./...` — clean. Then `go vet ./cmd/at-mint/` — clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-mint/main.go cmd/at-mint/main_test.go
git commit -m "feat(at-mint): subcommand dispatch — flags(non-secret)/env(secret), one token to stdout, fail-closed"
```

---

### Task 5: Build integration + retire the shell script + RUNBOOK

**Files:**
- Modify: `scripts/build.sh` (add `at-mint` to `BINARIES`)
- Modify: `justfile` (add `at-mint` to the `install` binary loop)
- Delete: `kits/reference-worker/mint-github-token.sh`
- Modify: `kits/reference-worker/RUNBOOK.md`

**Interfaces:** none (build + docs).

- [ ] **Step 1: Write the failing check**

There is no unit test for the build script; verify by building. First confirm the binary builds standalone and the script currently omits it:

Run: `go build -o /dev/null ./cmd/at-mint && grep -n 'BINARIES=' scripts/build.sh`
Expected: the binary builds; `BINARIES=(at-cove at-task)` — note `at-mint` is missing.

- [ ] **Step 2: Confirm the gap**

Run: `grep -rn 'mint-github-token.sh' kits/ docs/`
Expected: references in `RUNBOOK.md` (and possibly a comment) that must move to `at-mint`.

- [ ] **Step 3: Make the changes**

1. In `scripts/build.sh`, add `at-mint` to the binaries array:

```bash
BINARIES=(at-cove at-task at-mint)
```

2. In `justfile`, add `at-mint` to the `install` recipe's binary loop (match the existing loop's style — wherever it iterates binary names to `install -m 0755`, add `at-mint`). Do NOT change the unrelated set of binaries otherwise; just append `at-mint`.

3. Delete the shell script:

```bash
git rm kits/reference-worker/mint-github-token.sh
```

4. In `kits/reference-worker/RUNBOOK.md`, replace the `mint-github-token.sh` supply example with `at-mint`. The machine-side `~/.config/at-cove/secrets.yml` git-token supply becomes (the App key path is non-secret; `COVE_RUN_REPO` is provided by at-cove per run):

````markdown
```yaml
kits:
  reference-worker:
    AT_TASK_GIT_TOKEN:
      command: ["at-mint", "github", "--app-id", "123456", "--install-id", "7890",
                "--app-key-file", "/etc/cove/gh-app.pem"]
    ANTHROPIC_AUTH_TOKEN:
      command: ["at-mint", "anthropic",
                "--auth0-tenant", "your-tenant.us.auth0.com",
                "--auth0-client-id", "YOUR_CLIENT_ID",
                "--auth0-audience", "urn:cove:anthropic-wif",
                "--anthropic-org", "YOUR_ORG_UUID",
                "--anthropic-rule", "fdrl_...",
                "--anthropic-service-account", "svac_..."]
```

`at-mint github` needs the App private key — pass a path with `--app-key-file`
(non-secret), or set `AT_MINT_GITHUB_APP_KEY` (PEM content) in the host env.
`at-mint anthropic` reads the Auth0 client secret from `AT_MINT_AUTH0_CLIENT_SECRET`
in the host env (Plan 3's `mint:` profiles will source it from a manager instead).
`COVE_RUN_REPO` is set by at-cove per run; you do not pass it.
````

Remove any remaining prose that describes `mint-github-token.sh` as the kit's resolver.

- [ ] **Step 4: Verify**

Run: `bash -n scripts/build.sh && grep -n at-mint scripts/build.sh justfile && ! test -e kits/reference-worker/mint-github-token.sh && go test ./... 2>&1 | grep -E 'FAIL' || echo OK`
Expected: `at-mint` present in both build files; the script is gone; `OK` (no test failures — the RUNBOOK/build changes don't affect Go tests, but confirm the reference kit still parses via `go test ./internal/kit/`).

Also confirm the reference kit still parses:
Run: `go test ./internal/kit/ -run TestReferenceWorkerKitConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/build.sh justfile kits/reference-worker/RUNBOOK.md
git rm kits/reference-worker/mint-github-token.sh
git commit -m "build(at-mint): add to build/install; retire mint-github-token.sh; RUNBOOK uses at-mint"
```

---

### Task 6: `at-mint` usage doc

**Files:**
- Create: `docs/usage/at-mint.md`
- Modify: `docs/usage/INDEX.md` (add a row)
- Modify: `docs/OVERVIEW.md` (name the third binary where the binaries are introduced)

**Interfaces:** none (docs).

- [ ] **Step 1: Confirm the docs gap**

Run: `grep -n 'at-mint' docs/usage/INDEX.md docs/OVERVIEW.md || echo "no at-mint in docs yet"`
Expected: `no at-mint in docs yet`.

- [ ] **Step 2: Confirm INDEX/frontmatter conventions**

Run: `sed -n '1,12p' docs/usage/at-cove-secrets.md`
Expected: shows the frontmatter schema (`summary`/`read_when`/`owns`/`prereqs`/`tier`/`updated`) to mirror in the new leaf.

- [ ] **Step 3: Write the doc**

Create `docs/usage/at-mint.md` with frontmatter matching the sibling leaves, documenting: what `at-mint` is (a host-side minter invoked as a secrets `command:`); the `github` subcommand (flags `--app-id`/`--install-id`/`--app-key-file`, env `AT_MINT_GITHUB_APP_KEY`, reads `COVE_RUN_REPO`, mints a repo-scoped installation token with contents+PR write); the `anthropic` subcommand (the Auth0→federation two-hop flow, flags `--auth0-*`/`--anthropic-*`, env `AT_MINT_AUTH0_CLIENT_SECRET`); the contract (flags=non-secret, env=secret, one token to stdout, fail-closed); and a pointer to [at-cove-secrets.md](at-cove-secrets.md) for how a kit's demand is supplied by such a command. Keep it under the 200-line leaf budget. Frontmatter `read_when`: "You are configuring a machine-side secret supply that mints a token — a GitHub App installation token or an Anthropic WIF bearer." Set `updated: 2026-07-14`.

- [ ] **Step 4: Wire it into the map**

Add a row to `docs/usage/INDEX.md`'s table (mirroring the doc's frontmatter one-liner). In `docs/OVERVIEW.md`, where the binaries are introduced (currently `at-cove`, `at-task`), name `at-mint` as the host-side token minter and link to the new leaf. Keep OVERVIEW a brief map — one clause, not a duplicate of the leaf.

- [ ] **Step 5: Verify links + commit**

Run: `grep -n 'at-mint.md' docs/usage/INDEX.md && test -f docs/usage/at-mint.md && echo OK`
Expected: `OK`; the INDEX row links to the existing file. If a docs checker is available, run it scoped to `docs/usage` (do not run a repo-root audit needing `docs/INDEX.md`).

```bash
git add docs/usage/at-mint.md docs/usage/INDEX.md docs/OVERVIEW.md
git commit -m "docs(at-mint): usage leaf + INDEX row + OVERVIEW binary mention"
```

---

## Notes for the executor

- **Task 1's package-creation:** `cmd/at-mint` does not exist before Task 1; the first `go test ./cmd/at-mint/` creates/uses it. If the tooling errors with "no Go files" before Step 3, that is the RED (not-yet-implemented) state — proceed.
- **Test helpers** `rtFunc`/`resp`/`testKeyPEM` are defined once (Task 2's `github_test.go`) and reused by Tasks 3–4 (same package). Do not redefine them.
- **After Task 4**, `go build ./...` and `go test ./...` must be green. After Task 5, `bash -n scripts/build.sh` clean and the reference kit still parses.
- **No `mint:` wiring here.** at-mint is invoked as a bare `command:`; the `internal/mint` profile expander that builds the `at-mint` argv from a `minters:` profile is Plan 3. This plan already makes GitHub-token minting fully functional via `command: ["at-mint","github",...]` (the App-key path is non-secret and `COVE_RUN_REPO` is injected by at-cove); the Anthropic client secret comes from the host env in the interim.
- **Secrets discipline:** never add a flag that carries secret material. If you find yourself adding `--client-secret` or `--app-key` (content), stop — secrets come from env only.
