# Harbor Broker + Enrollment (Guest MVP) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `at-harbor`, a Go service that brokers Anthropic and git credentials to an enrolled cove over real client-addressed TLS, so downstream secrets live in harbor and never in the cove.

**Architecture:** One general credential-injecting reverse proxy driven by a per-destination policy table. Each request runs a three-question pipeline — (1) may this identity reach the destination? (2) does it need a credential? (3) which credential and how to apply it? — then swaps the caller's identity token for harbor's real credential and proxies upstream. Anthropic and git are config rows, not bespoke handlers. Pure decision/rewrite logic is unit-tested; the HTTP transport is tested with `httptest`; a real-TLS + real-cove round-trip lives behind the `integration` build tag.

**Tech Stack:** Go (stdlib: `net/http`, `net/http/httputil`, `crypto/tls`, `crypto/rand`, `crypto/sha256`, `log/slog`), `gopkg.in/yaml.v3`, the repo's `internal/{cli,secret,runner,logging}`.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **Go 1.22**; **stdlib + `gopkg.in/yaml.v3` only — no new dependencies.**
- Tests are **hermetic** (drive `runner.Fake` / `httptest`, use `t.TempDir()`); real-TLS/real-cove tests go behind `//go:build integration` and get their own `just integration-harbor` recipe.
- **Secrets never hit disk, argv, or logs.** The identity store persists only the **SHA-256 hash** of a token; resolved credential values and identity tokens are never logged at any level.
- New binary follows the repo command pattern: `run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int` + `func main() { os.Exit(run(...)) }`, using `internal/cli.App` for subcommand dispatch.
- **Fail closed:** any auth/authz/resolve failure returns a secret-free error and a 4xx/5xx — never proxies.
- `at-harbor` is a **host service**, not embedded in the sandbox image. Add it to the `just build` output; do **not** add it to the hardening `embed.FS`.
- Package doc comment on the first file of a new package; match the terse house comment style.

---

### Task 1: Identity tokens + file-backed store

**Files:**
- Create: `internal/harbor/identity.go`
- Create: `internal/harbor/identity_test.go`
- Create: `internal/harbor/filestore.go`
- Create: `internal/harbor/filestore_test.go`

**Interfaces:**
- Consumes: nothing (foundation).
- Produces:
  - `type Identity struct { ID, TokenHash, Project, Role string; Destinations, Repos []string; Expiry time.Time }`
  - `func MintToken() (string, error)` — URL-safe 256-bit token.
  - `func HashToken(tok string) string` — hex SHA-256.
  - `type Store interface { Add(Identity) error; Lookup(tokenHash string) (Identity, bool); Remove(id string) error }`
  - `func NewFileStore(path string) (*FileStore, error)` — JSON-file `Store`.

- [ ] **Step 1: Write the failing test for token primitives**

Create `internal/harbor/identity_test.go`:

```go
package harbor

import "testing"

func TestMintTokenIsUniqueAndHashable(t *testing.T) {
	a, err := MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	b, _ := MintToken()
	if a == b {
		t.Fatal("two mints returned the same token")
	}
	if len(a) < 40 {
		t.Fatalf("token too short: %d chars", len(a))
	}
	if HashToken(a) == a {
		t.Fatal("hash equals raw token")
	}
	if HashToken(a) != HashToken(a) {
		t.Fatal("hash not stable for same input")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run TestMintToken -v`
Expected: FAIL — `undefined: MintToken` / `undefined: HashToken`.

- [ ] **Step 3: Implement `identity.go`**

Create `internal/harbor/identity.go`:

```go
// Package harbor is the credential broker: it authenticates an enrolled identity,
// decides whether that identity may reach a configured destination and which
// credential to inject, and reverse-proxies the request with harbor's real
// credential swapped in. Downstream secrets live in harbor, never in the caller.
package harbor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// Identity is one enrolled actor's record. The raw token is never stored — only
// its hash — so a leaked store yields no usable credentials.
type Identity struct {
	ID           string    `json:"id"`
	TokenHash    string    `json:"token_hash"`
	Project      string    `json:"project"`
	Role         string    `json:"role"`
	Destinations []string  `json:"destinations"` // allow-listed destination names, e.g. ["anthropic","git"]
	Repos        []string  `json:"repos"`        // allowed "owner/repo" globs for git, e.g. ["aethons-tools/*"]
	Expiry       time.Time `json:"expiry"`       // zero = no expiry
}

// MintToken returns a new high-entropy bearer token (URL-safe, no padding).
func MintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex SHA-256 of a token — the stored/looked-up key.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/harbor/ -run TestMintToken -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for the file store**

Create `internal/harbor/filestore_test.go`:

```go
package harbor

import (
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	id := Identity{ID: "spider-18", TokenHash: HashToken("tok"), Project: "ACME", Role: "guest", Destinations: []string{"anthropic"}}
	if err := s.Add(id); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reload from disk to prove persistence.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := s2.Lookup(HashToken("tok"))
	if !ok || got.ID != "spider-18" {
		t.Fatalf("Lookup after reload = %+v, %v", got, ok)
	}
	if err := s2.Remove("spider-18"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := s2.Lookup(HashToken("tok")); ok {
		t.Fatal("identity still present after Remove")
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run TestFileStore -v`
Expected: FAIL — `undefined: NewFileStore`.

- [ ] **Step 7: Implement `filestore.go`**

Create `internal/harbor/filestore.go`:

```go
package harbor

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Store records enrolled identities keyed by token hash.
type Store interface {
	Add(id Identity) error
	Lookup(tokenHash string) (Identity, bool)
	Remove(id string) error
}

// FileStore is a JSON-file-backed Store. Single-node MVP; a real DB is slice #2.
type FileStore struct {
	path string
	mu   sync.Mutex
	ids  map[string]Identity // keyed by TokenHash
}

// NewFileStore loads (or initializes) the store at path.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, ids: map[string]Identity{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fs, nil
		}
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &fs.ids); err != nil {
			return nil, fmt.Errorf("load identity store %s: %w", path, err)
		}
	}
	return fs, nil
}

func (fs *FileStore) save() error {
	data, err := json.MarshalIndent(fs.ids, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fs.path, data, 0o600)
}

func (fs *FileStore) Add(id Identity) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.ids[id.TokenHash] = id
	return fs.save()
}

func (fs *FileStore) Lookup(tokenHash string) (Identity, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	id, ok := fs.ids[tokenHash]
	return id, ok
}

func (fs *FileStore) Remove(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for h, rec := range fs.ids {
		if rec.ID == id {
			delete(fs.ids, h)
			return fs.save()
		}
	}
	return fmt.Errorf("identity %q not found", id)
}
```

- [ ] **Step 8: Run it to verify it passes**

Run: `go test ./internal/harbor/ -v`
Expected: PASS (both tests).

- [ ] **Step 9: Commit**

```bash
git add internal/harbor/identity.go internal/harbor/identity_test.go internal/harbor/filestore.go internal/harbor/filestore_test.go
git commit -m "feat(harbor): identity tokens + file-backed identity store"
```

---

### Task 2: Destination policy + the three-question decision

**Files:**
- Create: `internal/harbor/policy.go`
- Create: `internal/harbor/policy_test.go`
- Create: `internal/harbor/decide.go`
- Create: `internal/harbor/decide_test.go`

**Interfaces:**
- Consumes: `Identity` (Task 1).
- Produces:
  - `type ApplyMethod string` with `ApplyBearer`, `ApplyBasicPassword`.
  - `type Destination struct { Name, Route, Upstream string; IdentityIn ApplyMethod; CredName string; Apply ApplyMethod; RepoScoped bool }`
  - `type Config struct { Destinations []Destination }` + `func (Config) Match(path string) (Destination, bool)`
  - `func RepoFromPath(route, reqPath string) (string, bool)`
  - `type Decision struct { Dest Destination; NeedCred bool; CredName string; Apply ApplyMethod }`
  - `func Decide(id Identity, dest Destination, ownerRepo string, now time.Time) (Decision, error)`

- [ ] **Step 1: Write the failing test for `Config.Match` and `RepoFromPath`**

Create `internal/harbor/policy_test.go`:

```go
package harbor

import "testing"

func testConfig() Config {
	return Config{Destinations: []Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyBearer, CredName: "anthropic-bearer", Apply: ApplyBearer},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword, RepoScoped: true},
	}}
}

func TestConfigMatch(t *testing.T) {
	c := testConfig()
	d, ok := c.Match("/anthropic/v1/messages")
	if !ok || d.Name != "anthropic" {
		t.Fatalf("anthropic match = %+v, %v", d, ok)
	}
	if _, ok := c.Match("/nope/x"); ok {
		t.Fatal("unexpected match for /nope/x")
	}
}

func TestRepoFromPath(t *testing.T) {
	got, ok := RepoFromPath("/git/", "/git/acme/api.git/info/refs")
	if !ok || got != "acme/api" {
		t.Fatalf("RepoFromPath = %q, %v", got, ok)
	}
	if _, ok := RepoFromPath("/git/", "/git/acme"); ok {
		t.Fatal("expected failure for missing repo segment")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestConfigMatch|TestRepoFromPath' -v`
Expected: FAIL — `undefined: Config` etc.

- [ ] **Step 3: Implement `policy.go`**

Create `internal/harbor/policy.go`:

```go
package harbor

import (
	"path"
	"strings"
)

// ApplyMethod is how a credential (the inbound identity token, or the outbound
// real credential) is carried on an HTTP request.
type ApplyMethod string

const (
	ApplyBearer        ApplyMethod = "bearer"         // Authorization: Bearer <value>
	ApplyBasicPassword ApplyMethod = "basic-password" // HTTP basic auth, <value> as the password
)

// Destination is one configured upstream the broker will proxy to. Anthropic and
// git are simply two Destinations; the engine has no service-specific branches.
type Destination struct {
	Name       string      `yaml:"name"`        // "anthropic", "git"
	Route      string      `yaml:"route"`       // inbound path prefix, e.g. "/anthropic/" or "/git/"
	Upstream   string      `yaml:"upstream"`    // "https://api.anthropic.com", "https://github.com"
	IdentityIn ApplyMethod `yaml:"identity_in"` // how the caller presents its identity token
	CredName   string      `yaml:"cred_name"`   // harbor credential to inject; "" = no credential
	Apply      ApplyMethod `yaml:"apply"`       // how to apply the real credential upstream
	RepoScoped bool        `yaml:"repo_scoped"` // path is <route><owner>/<repo>/...; checked against Identity.Repos
}

// Config is the broker's destination table.
type Config struct {
	Destinations []Destination `yaml:"destinations"`
}

// Match returns the destination whose Route prefixes reqPath (longest wins).
func (c Config) Match(reqPath string) (Destination, bool) {
	best := -1
	var bestDest Destination
	for _, d := range c.Destinations {
		if strings.HasPrefix(reqPath, d.Route) && len(d.Route) > best {
			best = len(d.Route)
			bestDest = d
		}
	}
	return bestDest, best >= 0
}

// RepoFromPath extracts "owner/repo" from a repo-scoped path after the route:
// route "/git/", path "/git/acme/api.git/info/refs" → "acme/api".
func RepoFromPath(route, reqPath string) (string, bool) {
	rest := strings.TrimPrefix(reqPath, route)
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), true
}

// repoAllowed reports whether ownerRepo matches any glob in allowed.
func repoAllowed(ownerRepo string, allowed []string) bool {
	for _, g := range allowed {
		if ok, _ := path.Match(g, ownerRepo); ok {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/harbor/ -run 'TestConfigMatch|TestRepoFromPath' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for `Decide`**

Create `internal/harbor/decide_test.go`:

```go
package harbor

import (
	"testing"
	"time"
)

func TestDecideAllowsListedDestination(t *testing.T) {
	id := Identity{ID: "spider-18", Destinations: []string{"anthropic"}}
	dest := testConfig().Destinations[0] // anthropic
	dec, err := Decide(id, dest, "", time.Now())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.NeedCred || dec.CredName != "anthropic-bearer" || dec.Apply != ApplyBearer {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestDecideDeniesUnlistedDestination(t *testing.T) {
	id := Identity{ID: "spider-18", Destinations: []string{"anthropic"}}
	git := testConfig().Destinations[1]
	if _, err := Decide(id, git, "acme/api", time.Now()); err == nil {
		t.Fatal("expected denial for unlisted destination")
	}
}

func TestDecideEnforcesRepoScope(t *testing.T) {
	id := Identity{ID: "spider-18", Destinations: []string{"git"}, Repos: []string{"aethons-tools/*"}}
	git := testConfig().Destinations[1]
	if _, err := Decide(id, git, "aethons-tools/cove", time.Now()); err != nil {
		t.Fatalf("allowed repo denied: %v", err)
	}
	if _, err := Decide(id, git, "someone-else/secret", time.Now()); err == nil {
		t.Fatal("expected denial for out-of-scope repo")
	}
}

func TestDecideRejectsExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	id := Identity{ID: "x", Destinations: []string{"anthropic"}, Expiry: past}
	if _, err := Decide(id, testConfig().Destinations[0], "", time.Now()); err == nil {
		t.Fatal("expected expired identity to be rejected")
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run TestDecide -v`
Expected: FAIL — `undefined: Decide`.

- [ ] **Step 7: Implement `decide.go`**

Create `internal/harbor/decide.go`:

```go
package harbor

import (
	"fmt"
	"slices"
	"time"
)

// Decision is the outcome of the three-question pipeline for one request.
type Decision struct {
	Dest     Destination
	NeedCred bool
	CredName string
	Apply    ApplyMethod
}

// Decide answers, for an authenticated identity and a matched destination:
//  1. may this identity reach the destination (and, if repo-scoped, this repo)?
//  2. does the call require a credential?
//  3. which credential, and how to apply it?
//
// It fails closed: any policy violation returns an error and the zero Decision.
// now is injected so expiry is testable.
func Decide(id Identity, dest Destination, ownerRepo string, now time.Time) (Decision, error) {
	if !id.Expiry.IsZero() && now.After(id.Expiry) {
		return Decision{}, fmt.Errorf("identity %q expired", id.ID)
	}
	if !slices.Contains(id.Destinations, dest.Name) {
		return Decision{}, fmt.Errorf("identity %q not allowed destination %q", id.ID, dest.Name)
	}
	if dest.RepoScoped {
		if ownerRepo == "" {
			return Decision{}, fmt.Errorf("destination %q requires owner/repo", dest.Name)
		}
		if !repoAllowed(ownerRepo, id.Repos) {
			return Decision{}, fmt.Errorf("identity %q not allowed repo %q", id.ID, ownerRepo)
		}
	}
	return Decision{Dest: dest, NeedCred: dest.CredName != "", CredName: dest.CredName, Apply: dest.Apply}, nil
}
```

- [ ] **Step 8: Run it to verify it passes**

Run: `go test ./internal/harbor/ -v`
Expected: PASS (all harbor tests).

- [ ] **Step 9: Commit**

```bash
git add internal/harbor/policy.go internal/harbor/policy_test.go internal/harbor/decide.go internal/harbor/decide_test.go
git commit -m "feat(harbor): destination policy + three-question decision"
```

---

### Task 3: Credential resolver over internal/secret

**Files:**
- Create: `internal/harbor/creds.go`
- Create: `internal/harbor/creds_test.go`

**Interfaces:**
- Consumes: `internal/secret.Spec`, `internal/secret.Resolve`, `internal/runner.Runner`.
- Produces:
  - `type CredResolver interface { Resolve(name string) (string, error) }`
  - `func NewSecretResolver(r runner.Runner, specs map[string]secret.Spec) *SecretResolver`

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/creds_test.go`:

```go
package harbor

import (
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

func TestSecretResolverRunsConfiguredCommand(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Out: "REAL-PAT\n"}}}
	r := NewSecretResolver(f, map[string]secret.Spec{
		"git-pat": {Command: []string{"at-mint", "github"}},
	})
	got, err := r.Resolve("git-pat")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "REAL-PAT" { // secret.Resolve trims the trailing newline
		t.Fatalf("Resolve = %q", got)
	}
}

func TestSecretResolverUnknownName(t *testing.T) {
	r := NewSecretResolver(&runner.Fake{}, nil)
	if _, err := r.Resolve("nope"); err == nil {
		t.Fatal("expected error for unknown credential")
	}
}
```

> If `runner.FakeResult`'s field is not `Out`, match the actual field name in `internal/runner/runner.go` (around line 151).

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run TestSecretResolver -v`
Expected: FAIL — `undefined: NewSecretResolver`.

- [ ] **Step 3: Implement `creds.go`**

Create `internal/harbor/creds.go`:

```go
package harbor

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

// CredResolver returns harbor's real downstream credential value by name,
// in-memory. Implementations must never log or persist the value.
type CredResolver interface {
	Resolve(name string) (string, error)
}

// SecretResolver resolves credentials via internal/secret specs (a resolver
// command or a literal per credential), run through a runner.Runner.
type SecretResolver struct {
	r     runner.Runner
	specs map[string]secret.Spec
}

// NewSecretResolver builds a resolver from credential name → secret.Spec.
func NewSecretResolver(r runner.Runner, specs map[string]secret.Spec) *SecretResolver {
	return &SecretResolver{r: r, specs: specs}
}

// Resolve runs the spec for name and returns its value. Fails closed.
func (s *SecretResolver) Resolve(name string) (string, error) {
	spec, ok := s.specs[name]
	if !ok {
		return "", fmt.Errorf("no credential configured for %q", name)
	}
	spec.Name = name
	vals, err := secret.Resolve(s.r, nil, []secret.Spec{spec})
	if err != nil {
		return "", err
	}
	return vals[name], nil
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/harbor/ -run TestSecretResolver -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/creds.go internal/harbor/creds_test.go
git commit -m "feat(harbor): credential resolver over internal/secret"
```

---

### Task 4: The broker proxy handler

**Files:**
- Create: `internal/harbor/proxy.go`
- Create: `internal/harbor/proxy_test.go`

**Interfaces:**
- Consumes: `Store` (Task 1), `Config`/`Destination`/`Decision`/`Decide`/`RepoFromPath`/`ApplyMethod` (Task 2), `CredResolver` (Task 3).
- Produces: `func NewBroker(store Store, cfg Config, creds CredResolver, log *slog.Logger) *Broker` implementing `http.Handler`.

- [ ] **Step 1: Write the failing test (identity→cred swap, deny, no secret in logs)**

Create `internal/harbor/proxy_test.go`:

```go
package harbor

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCreds returns canned real credentials.
type fakeCreds map[string]string

func (f fakeCreds) Resolve(name string) (string, error) { return f[name], nil }

func newTestBroker(t *testing.T, upstreamAnthropic, upstreamGit string) (*Broker, *bytes.Buffer, string) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := MintToken()
	if err := store.Add(Identity{
		ID: "spider-18", TokenHash: HashToken(tok), Project: "ACME", Role: "guest",
		Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Destinations: []Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: upstreamAnthropic, IdentityIn: ApplyBearer, CredName: "anthropic-bearer", Apply: ApplyBearer},
		{Name: "git", Route: "/git/", Upstream: upstreamGit, IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword, RepoScoped: true},
	}}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewBroker(store, cfg, fakeCreds{"anthropic-bearer": "REAL-ANTHROPIC", "git-pat": "REAL-PAT"}, log), &logbuf, tok
}

func TestBrokerSwapsAnthropicBearer(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	b, logbuf, tok := newTestBroker(t, up.URL, "http://unused")
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotAuth != "Bearer REAL-ANTHROPIC" {
		t.Fatalf("upstream Authorization = %q, want swapped real cred", gotAuth)
	}
	if strings.Contains(logbuf.String(), tok) || strings.Contains(logbuf.String(), "REAL-ANTHROPIC") {
		t.Fatal("secret material leaked into logs")
	}
}

func TestBrokerSwapsGitBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	b, _, tok := newTestBroker(t, "http://unused", up.URL)
	req := httptest.NewRequest("GET", "/git/acme/api/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("x-access-token", tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotPass != "REAL-PAT" {
		t.Fatalf("upstream git password = %q, want REAL-PAT", gotPass)
	}
	_ = gotUser
}

func TestBrokerDeniesOutOfScopeRepo(t *testing.T) {
	b, _, tok := newTestBroker(t, "http://unused", "http://unused")
	req := httptest.NewRequest("GET", "/git/someone/secret/info/refs", nil)
	req.SetBasicAuth("x-access-token", tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestBrokerRejectsUnknownIdentity(t *testing.T) {
	b, _, _ := newTestBroker(t, "http://unused", "http://unused")
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run TestBroker -v`
Expected: FAIL — `undefined: NewBroker`.

- [ ] **Step 3: Implement `proxy.go`**

Create `internal/harbor/proxy.go`:

```go
package harbor

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Broker is the credential-injecting reverse proxy: authenticate the identity,
// run the three-question decision, resolve harbor's real credential, rewrite the
// request with it, and proxy to the upstream. Implements http.Handler.
type Broker struct {
	store Store
	cfg   Config
	creds CredResolver
	now   func() time.Time
	log   *slog.Logger
}

// NewBroker constructs a Broker.
func NewBroker(store Store, cfg Config, creds CredResolver, log *slog.Logger) *Broker {
	return &Broker{store: store, cfg: cfg, creds: creds, now: time.Now, log: log}
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dest, ok := b.cfg.Match(r.URL.Path)
	if !ok {
		http.Error(w, "no such destination", http.StatusNotFound)
		return
	}
	tok, ok := presentedToken(r, dest.IdentityIn)
	if !ok {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	id, ok := b.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	var ownerRepo string
	if dest.RepoScoped {
		ownerRepo, _ = RepoFromPath(dest.Route, r.URL.Path)
	}
	dec, err := Decide(id, dest, ownerRepo, b.now())
	if err != nil {
		b.log.Warn("broker denied", "identity", id.ID, "destination", dest.Name, "reason", err.Error())
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var cred string
	if dec.NeedCred {
		if cred, err = b.creds.Resolve(dec.CredName); err != nil {
			b.log.Error("credential resolve failed", "destination", dest.Name)
			http.Error(w, "credential unavailable", http.StatusBadGateway)
			return
		}
	}
	up, err := url.Parse(dest.Upstream)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusInternalServerError)
		return
	}
	trimmed := strings.TrimSuffix(dest.Route, "/") // "/anthropic/" -> "/anthropic"
	rp := &httputil.ReverseProxy{Director: func(out *http.Request) {
		out.URL.Scheme = up.Scheme
		out.URL.Host = up.Host
		out.Host = up.Host
		out.URL.Path = strings.TrimPrefix(r.URL.Path, trimmed) // strip the route prefix
		out.Header.Del("Authorization")                        // drop the inbound identity credential
		if dec.NeedCred {
			applyCred(out, dec.Apply, cred)
		}
	}}
	b.log.Info("broker proxy", "identity", id.ID, "destination", dest.Name, "path", r.URL.Path)
	rp.ServeHTTP(w, r)
}

// presentedToken extracts the caller's identity token from the request per how.
func presentedToken(r *http.Request, how ApplyMethod) (string, bool) {
	switch how {
	case ApplyBearer:
		if s, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s != "" {
			return s, true
		}
	case ApplyBasicPassword:
		if _, pass, ok := r.BasicAuth(); ok && pass != "" {
			return pass, true
		}
	}
	return "", false
}

// applyCred sets harbor's real credential on the upstream request.
func applyCred(r *http.Request, how ApplyMethod, cred string) {
	switch how {
	case ApplyBearer:
		r.Header.Set("Authorization", "Bearer "+cred)
	case ApplyBasicPassword:
		r.SetBasicAuth("x-access-token", cred) // git smart-HTTP: any username, PAT as password
	}
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/harbor/ -run TestBroker -v`
Expected: PASS (all four broker tests).

- [ ] **Step 5: Run the full package + vet**

Run: `go test ./internal/harbor/ && go vet ./internal/harbor/`
Expected: PASS, no vet complaints.

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/proxy.go internal/harbor/proxy_test.go
git commit -m "feat(harbor): credential-injecting reverse-proxy broker"
```

---

### Task 5: Enrollment (mint + record + render Guest snippet)

**Files:**
- Create: `internal/harbor/enroll.go`
- Create: `internal/harbor/enroll_test.go`

**Interfaces:**
- Consumes: `Store`, `Identity`, `MintToken`, `HashToken` (Task 1).
- Produces:
  - `func Enroll(store Store, id string, project, role string, dests, repos []string, ttl time.Duration, now time.Time) (token string, err error)`
  - `func RenderEnrollSnippet(baseURL, token string) string`

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/enroll_test.go`:

```go
package harbor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnrollStoresHashedIdentity(t *testing.T) {
	store, _ := NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	tok, err := Enroll(store, "spider-18", "ACME", "guest", []string{"anthropic", "git"}, []string{"acme/*"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	id, ok := store.Lookup(HashToken(tok))
	if !ok || id.ID != "spider-18" || id.TokenHash == tok {
		t.Fatalf("stored identity = %+v, ok=%v (hash must not equal raw token)", id, ok)
	}
}

func TestRenderEnrollSnippetIncludesEndpointsNotSecrets(t *testing.T) {
	out := RenderEnrollSnippet("https://harbor.local.aethons.tools", "TOK123")
	for _, want := range []string{
		"ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic",
		"ANTHROPIC_AUTH_TOKEN=TOK123",
		`url."https://harbor.local.aethons.tools/git/".insteadOf`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snippet missing %q\n---\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestEnroll|TestRenderEnroll' -v`
Expected: FAIL — `undefined: Enroll`.

- [ ] **Step 3: Implement `enroll.go`**

Create `internal/harbor/enroll.go`:

```go
package harbor

import (
	"fmt"
	"strings"
	"time"
)

// Enroll mints a token, records the identity (storing only the token hash), and
// returns the raw token once for the caller to hand to the Guest. ttl == 0 means
// no expiry. now is injected for testability.
func Enroll(store Store, id, project, role string, dests, repos []string, ttl time.Duration, now time.Time) (string, error) {
	if id == "" {
		return "", fmt.Errorf("identity id is required")
	}
	tok, err := MintToken()
	if err != nil {
		return "", err
	}
	rec := Identity{
		ID: id, TokenHash: HashToken(tok), Project: project, Role: role,
		Destinations: dests, Repos: repos,
	}
	if ttl > 0 {
		rec.Expiry = now.Add(ttl)
	}
	if err := store.Add(rec); err != nil {
		return "", err
	}
	return tok, nil
}

// RenderEnrollSnippet renders the env + gitconfig a Guest cove sources to route
// Anthropic and git through harbor. The token is the caller's identity for both
// connectors; harbor swaps it for the real credentials.
func RenderEnrollSnippet(baseURL, token string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%s/anthropic\n", baseURL)
	fmt.Fprintf(&b, "export ANTHROPIC_AUTH_TOKEN=%s\n", token)
	fmt.Fprintf(&b, "git config --global url.%q.insteadOf https://github.com/\n", baseURL+"/git/")
	fmt.Fprintf(&b, "# git credential: username 'x-access-token', password = ANTHROPIC_AUTH_TOKEN (your harbor identity)\n")
	return b.String()
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/harbor/ -run 'TestEnroll|TestRenderEnroll' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/enroll.go internal/harbor/enroll_test.go
git commit -m "feat(harbor): enrollment — mint, record hashed identity, render Guest snippet"
```

---

### Task 6: `at-harbor` binary — config, TLS server, `serve`/`enroll`/`revoke`

**Files:**
- Create: `cmd/at-harbor/main.go`
- Create: `cmd/at-harbor/main_test.go`
- Create: `cmd/at-harbor/config.go`
- Create: `cmd/at-harbor/config_test.go`
- Create: `cmd/at-harbor/serve_integration_test.go`
- Modify: `justfile` (add `at-harbor` to `build`; add `integration-harbor` recipe)

**Interfaces:**
- Consumes: everything in `internal/harbor`; `internal/cli`, `internal/secret`, `internal/runner`.
- Produces: `func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int`; `type serveConfig` (YAML).

- [ ] **Step 1: Write the failing test for config parsing**

Create `cmd/at-harbor/config_test.go`:

```go
package main

import "testing"

func TestParseServeConfig(t *testing.T) {
	yml := `
listen: ":8443"
tls: { cert: /c.pem, key: /k.pem }
store: /var/lib/harbor/ids.json
destinations:
  - { name: anthropic, route: /anthropic/, upstream: https://api.anthropic.com, identity_in: bearer, cred_name: anthropic-bearer, apply: bearer }
  - { name: git, route: /git/, upstream: https://github.com, identity_in: basic-password, cred_name: git-pat, apply: basic-password, repo_scoped: true }
credentials:
  anthropic-bearer: { command: [at-mint, anthropic] }
  git-pat: { value: literal-dev-pat }
`
	cfg, err := parseServeConfig([]byte(yml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Listen != ":8443" || cfg.Store != "/var/lib/harbor/ids.json" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Broker.Destinations) != 2 || cfg.Broker.Destinations[1].Name != "git" {
		t.Fatalf("destinations = %+v", cfg.Broker.Destinations)
	}
	if s := cfg.credSpecs()["git-pat"]; !s.Literal || s.Value != "literal-dev-pat" {
		t.Fatalf("git-pat spec = %+v", s)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/at-harbor/ -run TestParseServeConfig -v`
Expected: FAIL — `undefined: parseServeConfig`.

- [ ] **Step 3: Implement `config.go`**

Create `cmd/at-harbor/config.go`:

```go
package main

import (
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/secret"
	"gopkg.in/yaml.v3"
)

// credSpec is the YAML shape for one harbor credential: either a resolver
// command or a literal value (dev only).
type credSpec struct {
	Command []string `yaml:"command"`
	Value   string   `yaml:"value"`
}

// serveConfig is the on-disk config for `at-harbor serve`.
type serveConfig struct {
	Listen string `yaml:"listen"`
	TLS    struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	Store       string              `yaml:"store"`
	Broker      harbor.Config       `yaml:",inline"`
	Credentials map[string]credSpec `yaml:"credentials"`
}

// parseServeConfig parses the serve config YAML.
func parseServeConfig(data []byte) (serveConfig, error) {
	var c serveConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return serveConfig{}, err
	}
	return c, nil
}

// credSpecs maps each configured credential to a secret.Spec (literal or command).
func (c serveConfig) credSpecs() map[string]secret.Spec {
	out := make(map[string]secret.Spec, len(c.Credentials))
	for name, cs := range c.Credentials {
		if cs.Value != "" {
			out[name] = secret.Spec{Value: cs.Value, Literal: true}
		} else {
			out[name] = secret.Spec{Command: cs.Command}
		}
	}
	return out
}
```

> Note: `harbor.Config` has `Destinations []Destination` with yaml tag `destinations`, and `yaml:",inline"` folds those keys into the top level, so `destinations:` sits alongside `listen:`.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./cmd/at-harbor/ -run TestParseServeConfig -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for the `enroll` command**

Create `cmd/at-harbor/main_test.go`:

```go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollCommandPrintsSnippet(t *testing.T) {
	store := filepath.Join(t.TempDir(), "ids.json")
	var out, errb bytes.Buffer
	code := run([]string{
		"enroll", "--store", store, "--id", "spider-18", "--project", "ACME",
		"--role", "guest", "--destinations", "anthropic,git", "--repos", "acme/*",
		"--base-url", "https://harbor.local.aethons.tools",
	}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic") {
		t.Fatalf("stdout missing snippet:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_AUTH_TOKEN=") {
		t.Fatal("stdout missing minted token line")
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./cmd/at-harbor/ -run 'TestEnrollCommand|TestUnknownCommand' -v`
Expected: FAIL — `undefined: run`.

- [ ] **Step 7: Implement `main.go`**

Create `cmd/at-harbor/main.go`:

```go
// Command at-harbor is the central credential broker. `serve` runs the
// credential-injecting reverse proxy; `enroll`/`revoke` manage identities.
// See docs/superpowers/specs/2026-09-10-harbor-broker-guest-mvp-design.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/runner"
)

var version = "dev"

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "at-harbor",
		Version: version,
		Commands: []cli.Command{
			{Name: "serve", Brief: "run the credential broker", Run: cmdServe},
			{Name: "enroll", Brief: "mint an identity and print its Guest snippet", Run: cmdEnroll},
			{Name: "revoke", Brief: "remove an identity", Run: cmdRevoke},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdEnroll(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	store := fs.String("store", "", "path to the identity store")
	id := fs.String("id", "", "identity id (e.g. spider-18)")
	project := fs.String("project", "", "project name")
	role := fs.String("role", "guest", "role name")
	dests := fs.String("destinations", "", "comma-separated destination names")
	repos := fs.String("repos", "", "comma-separated owner/repo globs")
	baseURL := fs.String("base-url", "", "harbor base URL for the printed snippet")
	ttl := fs.Duration("ttl", 0, "identity lifetime (0 = no expiry)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-harbor enroll: takes no positional arguments")
		return 2
	}
	if *store == "" || *id == "" || *baseURL == "" {
		fmt.Fprintln(stderr, "at-harbor enroll: --store, --id and --base-url are required")
		return 2
	}
	st, err := harbor.NewFileStore(*store)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	tok, err := harbor.Enroll(st, *id, *project, *role, splitCSV(*dests), splitCSV(*repos), *ttl, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprint(stdout, harbor.RenderEnrollSnippet(*baseURL, tok))
	return 0
}

func cmdRevoke(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	store := fs.String("store", "", "path to the identity store")
	id := fs.String("id", "", "identity id to remove")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *store == "" || *id == "" {
		fmt.Fprintln(stderr, "at-harbor revoke: --store and --id are required")
		return 2
	}
	st, err := harbor.NewFileStore(*store)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	if err := st.Remove(*id); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, "revoked", *id)
	return 0
}

func cmdServe(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to the serve config YAML")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *cfgPath == "" {
		fmt.Fprintln(stderr, "at-harbor serve: --config is required")
		return 2
	}
	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	cfg, err := parseServeConfig(data)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	st, err := harbor.NewFileStore(cfg.Store)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	creds := harbor.NewSecretResolver(runner.OS{}, cfg.credSpecs())
	broker := harbor.NewBroker(st, cfg.Broker, creds, log)
	srv := &http.Server{Addr: cfg.Listen, Handler: broker}
	log.Info("harbor listening", "addr", cfg.Listen)
	if err := srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }
```

- [ ] **Step 8: Run it to verify it passes**

Run: `go test ./cmd/at-harbor/ -v`
Expected: PASS (config + enroll + unknown-command tests).

- [ ] **Step 9: Write the integration test (real TLS round-trip) behind the tag**

Create `cmd/at-harbor/serve_integration_test.go`:

```go
//go:build integration

package main

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
)

// TestServeBrokersOverTLS starts the broker on a TLS listener with a self-signed
// cert, enrolls an identity, and proves an Anthropic request is credential-swapped
// end-to-end over HTTPS. Run: `go test -tags integration ./cmd/at-harbor/`.
func TestServeBrokersOverTLS(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "ids.json"))
	tok, _ := harbor.Enroll(store, "spider-18", "ACME", "guest", []string{"anthropic"}, nil, 0, timeNow())
	cfg := harbor.Config{Destinations: []harbor.Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: up.URL, IdentityIn: harbor.ApplyBearer, CredName: "anthropic-bearer", Apply: harbor.ApplyBearer},
	}}
	broker := harbor.NewBroker(store, cfg, fakeResolver{}, discardLogger())

	ts := httptest.NewTLSServer(broker)
	defer ts.Close()
	client := ts.Client() // trusts the test server's self-signed cert

	req, _ := http.NewRequest("POST", ts.URL+"/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if gotAuth != "Bearer REAL-ANTHROPIC" {
		t.Fatalf("upstream Authorization = %q", gotAuth)
	}
	_ = tls.VersionTLS12
}
```

> Provide the tiny `fakeResolver`, `discardLogger`, and `timeNow` helpers in this file (or reuse the test helpers from Task 4 by copying them under the `integration` tag). Keep them minimal: `fakeResolver` returns `"REAL-ANTHROPIC"` for any name; `discardLogger` is `slog.New(slog.NewTextHandler(io.Discard, nil))`; `timeNow` is `time.Now`.

- [ ] **Step 10: Run the integration test**

Run: `go test -tags integration ./cmd/at-harbor/ -run TestServeBrokersOverTLS -v`
Expected: PASS.

- [ ] **Step 11: Wire `just build` + add `integration-harbor`**

Edit `justfile`: add `at-harbor` to the binaries built by the `build` recipe (mirror how `at-cove`/`at-task` are built into `dist/<os>-<arch>/`), and add:

```
integration-harbor:
    go test -tags integration ./cmd/at-harbor/... ./internal/harbor/...
```

- [ ] **Step 12: Full build + hermetic suite + vet**

Run: `just build && go test ./... && go vet ./...`
Expected: builds `at-harbor`; all hermetic tests pass; no vet complaints.

- [ ] **Step 13: Update docs**

Per the repo's "no task done until docs updated" rule: add an `at-harbor` row/section to `docs/OVERVIEW.md`'s binary list and a one-line pointer in `docs/usage/INDEX.md`, linking the slice-#1 spec. Keep it terse (progressive disclosure).

- [ ] **Step 14: Commit**

```bash
git add cmd/at-harbor/ justfile docs/
git commit -m "feat(harbor): at-harbor binary — TLS broker + enroll/revoke (Guest MVP)"
```

---

## Manual verification (definition of done)

Not a unit test — the end-to-end acceptance from the spec, run once by hand:

1. Obtain a cert for `harbor.local.aethons.tools` (real DNS-01 `*.local.aethons.tools`, or a local dev CA), add `127.0.0.1 harbor.local.aethons.tools` to `/etc/hosts`, write a `serve` config with real Anthropic + git credentials, `at-harbor serve --config …`.
2. `at-harbor enroll --id spider-18 --project ACME --destinations anthropic,git --repos 'aethons-tools/*' --base-url https://harbor.local.aethons.tools …`; source the printed snippet inside a local Colima cove (mapping `harbor.local.aethons.tools` → the host).
3. Confirm, with **no Anthropic key and no git PAT in the cove**: `claude` works against the Anthropic connector, and `git clone`/`git push` of a **private** `aethons-tools/*` repo works through the git connector.
4. Confirm a revoked identity fails both, closed, and that no token/credential appears in harbor's logs.

---

## Self-review

**Spec coverage:** connector broker (Anthropic+git) → Tasks 2/4/6; identity+enroll+revoke → Tasks 1/5/6; the three-question pipeline → Tasks 2/4; identity-token-doubles-as-credential swap → Task 4 (`presentedToken`/`applyCred`); harbor holds real creds via secret model → Task 3/6; real TLS on the test domain → Task 6 + manual verification; hash-only storage & no-secret-in-logs → Tasks 1/4; fail-closed → Tasks 2/4; general (not bespoke) engine → policy-driven Tasks 2/4/6. Deferred items (messaging MCP, filtering forward-proxy, mTLS, DB) are correctly absent.

**Placeholders:** none — every code step is complete; the two "provide the tiny helper" notes name the exact minimal bodies.

**Type consistency:** `Identity`, `Store`, `Config`, `Destination`, `ApplyMethod`/`ApplyBearer`/`ApplyBasicPassword`, `Decision`, `Decide`, `CredResolver`, `NewSecretResolver`, `NewBroker`, `Enroll`, `RenderEnrollSnippet`, `parseServeConfig`/`serveConfig.credSpecs` are defined once and referenced with the same signatures throughout. `runner.Fake{Outputs: []FakeResult}` matches the repo (verify the `FakeResult` field name when writing Task 3's test).
