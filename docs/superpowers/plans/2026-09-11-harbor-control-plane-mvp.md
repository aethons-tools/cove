# Harbor Control-Plane MVP (self-config admin API) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator manage harbor's **destinations** and **enrollments** at runtime through a minimal, loopback-only admin API backed by a live store — no YAML edits, no restarts — with credentials still config-sourced.

**Architecture:** The `serve` process is the sole writer. It runs the existing cove-facing broker (TLS) plus a new operator-facing admin API on a loopback listener. Both share one file-backed store holding two collections (identities + destinations); the broker matches destinations from the **live** store per request, so admin changes take effect immediately. The CLI (`enroll`/`revoke`/`destination …`) becomes an admin-API client. Every admin request passes through an `OperatorAuthenticator` seam (loopback impl now; Auth0 behind the same seam next slice).

**Tech Stack:** Go 1.22 (stdlib: `net/http` incl. 1.22 method+wildcard `ServeMux` patterns, `encoding/json`, `log/slog`), `gopkg.in/yaml.v3`, and the slice-1 `internal/harbor` package.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **Go 1.22**; **stdlib + `gopkg.in/yaml.v3` only — no new dependencies.**
- Tests hermetic (`httptest`, `t.TempDir()`); real two-listener round-trip behind `//go:build integration`.
- **Credentials are never in the store or the API** — resolved from config / `at-mint` as in slice #1. No credential value or identity token ever appears in an API response or a log; the store persists identity **hashes** only.
- The admin API is **credential-routing-sensitive** (a destination row points a `cred_name` at an `upstream`), so: it is **loopback-only**, and `POST /admin/destinations` **rejects a destination whose `cred_name` doesn't resolve**.
- Follow the slice-1 CLI pattern (`cli.App`, `cli.ParseFlags`, `run(argv,getenv,stdout,stderr) int`).
- Slice-1 behavior (Anthropic x-api-key, git basic-auth + `WWW-Authenticate` challenge) must stay green.

---

## File Structure

- `internal/harbor/filestore.go` (modify) — `Store` interface + `FileStore` gain a **destinations** collection, `ListIdentities`, and `Match`; on-disk format becomes `{identities, destinations}` with v1 migration.
- `internal/harbor/policy.go` (modify) — add `json` tags to `Destination` (it's now persisted + wire-serialized).
- `internal/harbor/proxy.go` (modify) — `NewBroker(store, creds, log)` (drop the static `Config`); `ServeHTTP` matches via `store.Match`.
- `internal/harbor/operator.go` (create) — `Operator`, `OperatorAuthenticator`, `LoopbackAuthenticator`.
- `internal/harbor/admin.go` (create) — `NewAdminHandler(...)` — the admin `http.Handler` (destinations + enrollments + healthz), auth-gated.
- `internal/harbor/adminclient/adminclient.go` (create) — typed HTTP client + request/response DTOs (the future `at-harborctl` seam).
- `cmd/at-harbor/config.go` (modify) — `serveConfig` gains `admin-listen`, drops the inline destinations block.
- `cmd/at-harbor/main.go` (modify) — `serve` starts both listeners; `enroll`/`revoke` become admin-client calls; add `destination add|list|rm|import`.
- Test files alongside each.

---

### Task 1: Store v2 — destinations collection, list, match, v1 migration

**Files:**
- Modify: `internal/harbor/policy.go` (add json tags to `Destination`)
- Modify: `internal/harbor/filestore.go`
- Modify: `internal/harbor/filestore_test.go`

**Interfaces:**
- Consumes: `Identity`, `Destination`, `Config` + `Config.Match` (slice #1).
- Produces: extended `Store` interface —
  `Add(Identity) error`, `Lookup(string)(Identity,bool)`, `Remove(string) error`, `ListIdentities() []Identity`,
  `AddDestination(Destination) error`, `RemoveDestination(string) error`, `ListDestinations() []Destination`,
  `Match(string)(Destination,bool)`; `NewFileStore(path)(*FileStore,error)` implementing all of it.

- [ ] **Step 1: Add json tags to `Destination`** (it's now stored + sent as JSON, not just YAML)

In `internal/harbor/policy.go`, replace the `Destination` struct with:

```go
type Destination struct {
	Name       string      `json:"name"        yaml:"name"`
	Route      string      `json:"route"       yaml:"route"`
	Upstream   string      `json:"upstream"    yaml:"upstream"`
	IdentityIn ApplyMethod `json:"identity_in" yaml:"identity_in"`
	CredName   string      `json:"cred_name"   yaml:"cred_name"`
	Apply      ApplyMethod `json:"apply"       yaml:"apply"`
	RepoScoped bool        `json:"repo_scoped" yaml:"repo_scoped"`
}
```

- [ ] **Step 2: Write the failing test for the extended store**

Replace the body of `internal/harbor/filestore_test.go`'s existing `TestFileStoreRoundTrip` is fine to keep; ADD this test:

```go
func TestFileStoreDestinationsAndMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	d := Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword, RepoScoped: true}
	if err := s.AddDestination(d); err != nil {
		t.Fatalf("AddDestination: %v", err)
	}
	if err := s.Add(Identity{ID: "spider-18", TokenHash: HashToken("tok"), Destinations: []string{"git"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reload from disk: both collections persist.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, ok := s2.Match("/git/acme/api.git/info/refs"); !ok || got.Name != "git" {
		t.Fatalf("Match after reload = %+v, %v", got, ok)
	}
	if len(s2.ListDestinations()) != 1 || len(s2.ListIdentities()) != 1 {
		t.Fatalf("lists: dests=%d ids=%d", len(s2.ListDestinations()), len(s2.ListIdentities()))
	}
	if err := s2.RemoveDestination("git"); err != nil {
		t.Fatalf("RemoveDestination: %v", err)
	}
	if _, ok := s2.Match("/git/acme/api.git/info/refs"); ok {
		t.Fatal("destination still matched after removal")
	}
}

func TestFileStoreMigratesLegacyIdentityMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	// v1 on-disk format: a bare map[tokenHash]Identity
	legacy := `{"` + HashToken("tok") + `":{"id":"old-one","token_hash":"` + HashToken("tok") + `"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore (legacy): %v", err)
	}
	if id, ok := s.Lookup(HashToken("tok")); !ok || id.ID != "old-one" {
		t.Fatalf("legacy identity not migrated: %+v, %v", id, ok)
	}
}
```

Add `"os"` to the test imports if not present.

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/harbor/ -run 'TestFileStoreDestinations|TestFileStoreMigrates' -v`
Expected: FAIL — `s.AddDestination undefined` (and friends).

- [ ] **Step 4: Rewrite `internal/harbor/filestore.go`**

```go
package harbor

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Store records enrolled identities (by token hash) and the destination table.
type Store interface {
	Add(id Identity) error
	Lookup(tokenHash string) (Identity, bool)
	Remove(id string) error
	ListIdentities() []Identity

	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)
}

// storeFile is the on-disk JSON shape (format v2).
type storeFile struct {
	Identities   map[string]Identity    `json:"identities"`   // keyed by TokenHash
	Destinations map[string]Destination `json:"destinations"` // keyed by Name
}

// FileStore is a JSON-file-backed Store. Single-node MVP; the serve process is the
// sole writer, so there is no cross-process contention.
type FileStore struct {
	path  string
	mu    sync.Mutex
	ids   map[string]Identity
	dests map[string]Destination
}

// NewFileStore loads (or initializes) the store at path. A legacy v1 file (a bare
// map[tokenHash]Identity) is migrated into the identities collection.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, ids: map[string]Identity{}, dests: map[string]Destination{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fs, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return fs, nil
	}
	var v2 storeFile
	if err := json.Unmarshal(data, &v2); err != nil {
		return nil, fmt.Errorf("load store %s: %w", path, err)
	}
	if v2.Identities == nil && v2.Destinations == nil {
		// v1 migration: the whole file is a map[tokenHash]Identity.
		var legacy map[string]Identity
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, fmt.Errorf("load store %s (legacy): %w", path, err)
		}
		fs.ids = legacy
		return fs, nil
	}
	if v2.Identities != nil {
		fs.ids = v2.Identities
	}
	if v2.Destinations != nil {
		fs.dests = v2.Destinations
	}
	return fs, nil
}

// save persists both collections (v2). Caller holds fs.mu.
func (fs *FileStore) save() error {
	data, err := json.MarshalIndent(storeFile{Identities: fs.ids, Destinations: fs.dests}, "", "  ")
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

func (fs *FileStore) ListIdentities() []Identity {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Identity, 0, len(fs.ids))
	for _, id := range fs.ids {
		out = append(out, id)
	}
	return out
}

func (fs *FileStore) AddDestination(d Destination) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.dests[d.Name] = d
	return fs.save()
}

func (fs *FileStore) RemoveDestination(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.dests[name]; !ok {
		return fmt.Errorf("destination %q not found", name)
	}
	delete(fs.dests, name)
	return fs.save()
}

func (fs *FileStore) ListDestinations() []Destination {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Destination, 0, len(fs.dests))
	for _, d := range fs.dests {
		out = append(out, d)
	}
	return out
}

// Match resolves the destination whose Route prefixes reqPath (longest wins),
// reusing Config.Match over a snapshot of the current table.
func (fs *FileStore) Match(reqPath string) (Destination, bool) {
	return Config{Destinations: fs.ListDestinations()}.Match(reqPath)
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/harbor/ -run 'TestFileStore' -v`
Expected: PASS (round-trip, destinations+match, legacy migration).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/policy.go internal/harbor/filestore.go internal/harbor/filestore_test.go
git commit -m "feat(harbor): store v2 — destinations collection, list/match, v1 migration"
```

---

### Task 2: Broker reads the live store (drop the static Config)

**Files:**
- Modify: `internal/harbor/proxy.go`
- Modify: `internal/harbor/proxy_test.go`

**Interfaces:**
- Consumes: `Store.Match` (Task 1), `CredResolver`, `Decide`, `RepoFromPath` (slice #1).
- Produces: `func NewBroker(store Store, creds CredResolver, log *slog.Logger) *Broker` (the `Config` parameter is removed).

- [ ] **Step 1: Update the broker to match via the store**

In `internal/harbor/proxy.go`: remove the `cfg Config` field and use the store for matching.

Replace the struct + constructor:

```go
type Broker struct {
	store Store
	creds CredResolver
	now   func() time.Time
	log   *slog.Logger
}

// NewBroker constructs a Broker that matches destinations from the live store.
func NewBroker(store Store, creds CredResolver, log *slog.Logger) *Broker {
	return &Broker{store: store, creds: creds, now: time.Now, log: log}
}
```

Change the first line of `ServeHTTP` from `dest, ok := b.cfg.Match(r.URL.Path)` to:

```go
	dest, ok := b.store.Match(r.URL.Path)
```

(Everything else in `ServeHTTP` is unchanged.)

- [ ] **Step 2: Update the test helper + the two config-building tests**

In `internal/harbor/proxy_test.go`, `newTestBroker` currently builds a `Config`. Change it to seed the two destinations into the store and call the new `NewBroker`. Replace the `cfg := Config{...}` block and the `return NewBroker(store, cfg, ...)` line with:

```go
	for _, d := range []Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: upstreamAnthropic, IdentityIn: ApplyBearer, CredName: "anthropic-bearer", Apply: ApplyBearer},
		{Name: "git", Route: "/git/", Upstream: upstreamGit, IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword, RepoScoped: true},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewBroker(store, fakeCreds{"anthropic-bearer": "REAL-ANTHROPIC", "git-pat": "REAL-PAT"}, log), &logbuf, tok
```

In `TestBrokerSwapsXAPIKey`, replace the `cfg := Config{...}` + `b := NewBroker(store, cfg, ...)` lines with:

```go
	if err := store.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: up.URL, IdentityIn: ApplyXAPIKey, CredName: "anthropic-key", Apply: ApplyXAPIKey}); err != nil {
		t.Fatal(err)
	}
	b := NewBroker(store, fakeCreds{"anthropic-key": "REAL-ANTHROPIC"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
```

- [ ] **Step 3: Run the harbor package tests**

Run: `go test ./internal/harbor/ -count=1`
Expected: PASS — all slice-1 broker tests (swap, challenge, deny, x-api-key) still green against the store-backed broker.

- [ ] **Step 4: Commit**

```bash
git add internal/harbor/proxy.go internal/harbor/proxy_test.go
git commit -m "refactor(harbor): broker matches destinations from the live store"
```

---

### Task 3: Operator-auth seam (loopback impl)

**Files:**
- Create: `internal/harbor/operator.go`
- Create: `internal/harbor/operator_test.go`

**Interfaces:**
- Produces: `type Operator struct { ID string }`; `type OperatorAuthenticator interface { Authenticate(*http.Request) (Operator, error) }`; `type LoopbackAuthenticator struct{}` implementing it.

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/operator_test.go`:

```go
package harbor

import (
	"net/http/httptest"
	"testing"
)

func TestLoopbackAuthenticator(t *testing.T) {
	a := LoopbackAuthenticator{}

	r := httptest.NewRequest("GET", "/admin/healthz", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	if op, err := a.Authenticate(r); err != nil || op.ID != "local" {
		t.Fatalf("loopback: op=%+v err=%v", op, err)
	}

	r2 := httptest.NewRequest("GET", "/admin/healthz", nil)
	r2.RemoteAddr = "10.0.0.5:9999"
	if _, err := a.Authenticate(r2); err == nil {
		t.Fatal("non-loopback request should be rejected")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/ -run TestLoopbackAuthenticator -v`
Expected: FAIL — `undefined: LoopbackAuthenticator`.

- [ ] **Step 3: Implement `internal/harbor/operator.go`**

```go
package harbor

import (
	"fmt"
	"net"
	"net/http"
)

// Operator is the identity behind an admin-API request. Operator identity (humans
// managing harbor) is a separate plane from actor identity (enrollment tokens).
type Operator struct{ ID string }

// OperatorAuthenticator resolves the operator behind an admin request, or rejects it.
// The MVP impl trusts loopback; the Auth0/OIDC impl (next slice) drops in here.
type OperatorAuthenticator interface {
	Authenticate(r *http.Request) (Operator, error)
}

// LoopbackAuthenticator accepts only requests from the loopback interface and
// attributes them to the single local operator. Defence in depth for the
// loopback-bound admin listener.
type LoopbackAuthenticator struct{}

func (LoopbackAuthenticator) Authenticate(r *http.Request) (Operator, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return Operator{}, fmt.Errorf("admin request from non-loopback address %q", r.RemoteAddr)
	}
	return Operator{ID: "local"}, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/harbor/ -run TestLoopbackAuthenticator -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/operator.go internal/harbor/operator_test.go
git commit -m "feat(harbor): operator-auth seam + loopback authenticator"
```

---

### Task 4: Admin API handler

**Files:**
- Create: `internal/harbor/admin.go`
- Create: `internal/harbor/admin_test.go`

**Interfaces:**
- Consumes: `Store` (Task 1), `OperatorAuthenticator` (Task 3), `Enroll`/`Identity`/`Destination` (slice #1 + Task 1).
- Produces:
  - `func NewAdminHandler(store Store, auth OperatorAuthenticator, credExists func(string) bool, log *slog.Logger) http.Handler`
  - wire shapes (also used by Task 5): request `EnrollBody{ID,Project,Role string; Destinations,Repos []string; TTLSeconds int64}`, response `EnrollResult{ID,Token string}`, and `IdentitySummary{ID,Project,Role string; Destinations,Repos []string; Expiry time.Time}` (JSON).

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/admin_test.go`:

```go
package harbor

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestAdmin(t *testing.T) (http.Handler, Store) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	credExists := func(n string) bool { return n == "git-pat" || n == "anthropic-key" }
	h := NewAdminHandler(store, LoopbackAuthenticator{}, credExists, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h, store
}

// loopback requests carry a loopback RemoteAddr; httptest.NewRequest defaults to
// 192.0.2.1, so set it explicitly.
func adminReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5000"
	return r
}

func TestAdminAddAndListDestination(t *testing.T) {
	h, store := newTestAdmin(t)
	body := `{"name":"git","route":"/git/","upstream":"https://github.com","identity_in":"basic-password","cred_name":"git-pat","apply":"basic-password","repo_scoped":true}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/destinations", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(store.ListDestinations()) != 1 {
		t.Fatalf("store has %d destinations", len(store.ListDestinations()))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/destinations", ""))
	var got []Destination
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Name != "git" {
		t.Fatalf("GET destinations = %+v", got)
	}
}

func TestAdminRejectsUnresolvableCredName(t *testing.T) {
	h, _ := newTestAdmin(t)
	body := `{"name":"bad","route":"/bad/","upstream":"https://x","identity_in":"bearer","cred_name":"nope","apply":"bearer"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/destinations", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unresolvable cred_name", rec.Code)
	}
}

func TestAdminEnrollThenRevoke(t *testing.T) {
	h, store := newTestAdmin(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/enrollments", `{"id":"spider-18","project":"ACME","role":"guest","destinations":["git"],"repos":["acme/*"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var res EnrollResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Token == "" || res.ID != "spider-18" {
		t.Fatalf("enroll result = %+v", res)
	}
	if _, ok := store.Lookup(HashToken(res.Token)); !ok {
		t.Fatal("enrolled identity not in store")
	}
	// GET must not leak tokens or hashes.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/enrollments", ""))
	if bytes.Contains(rec.Body.Bytes(), []byte(res.Token)) || bytes.Contains(rec.Body.Bytes(), []byte(HashToken(res.Token))) {
		t.Fatal("enrollment list leaked token or hash")
	}
	// revoke
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("DELETE", "/admin/enrollments/spider-18", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", rec.Code)
	}
	if _, ok := store.Lookup(HashToken(res.Token)); ok {
		t.Fatal("identity still present after revoke")
	}
}

func TestAdminRejectsNonLoopback(t *testing.T) {
	h, _ := newTestAdmin(t)
	r := httptest.NewRequest("GET", "/admin/destinations", nil)
	r.RemoteAddr = "10.0.0.9:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for non-loopback", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/ -run TestAdmin -v`
Expected: FAIL — `undefined: NewAdminHandler` / `EnrollResult`.

- [ ] **Step 3: Implement `internal/harbor/admin.go`**

```go
package harbor

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// EnrollBody is the POST /admin/enrollments request.
type EnrollBody struct {
	ID           string   `json:"id"`
	Project      string   `json:"project"`
	Role         string   `json:"role"`
	Destinations []string `json:"destinations"`
	Repos        []string `json:"repos"`
	TTLSeconds   int64    `json:"ttl_seconds"`
}

// EnrollResult is the POST /admin/enrollments response — the token is returned once.
type EnrollResult struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// IdentitySummary is a GET /admin/enrollments item: never a token or hash.
type IdentitySummary struct {
	ID           string    `json:"id"`
	Project      string    `json:"project"`
	Role         string    `json:"role"`
	Destinations []string  `json:"destinations"`
	Repos        []string  `json:"repos"`
	Expiry       time.Time `json:"expiry"`
}

// NewAdminHandler builds the loopback admin API. credExists validates that a
// destination's cred_name resolves before the destination is accepted.
func NewAdminHandler(store Store, auth OperatorAuthenticator, credExists func(string) bool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /admin/destinations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.ListDestinations())
	})
	mux.HandleFunc("POST /admin/destinations", func(w http.ResponseWriter, r *http.Request) {
		var d Destination
		if !decode(w, r, &d) {
			return
		}
		if d.Name == "" || d.Route == "" || d.Upstream == "" {
			http.Error(w, "name, route and upstream are required", http.StatusBadRequest)
			return
		}
		if d.CredName != "" && !credExists(d.CredName) {
			http.Error(w, "cred_name does not resolve to a configured credential", http.StatusBadRequest)
			return
		}
		if err := store.AddDestination(d); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin destination added", "name", d.Name, "route", d.Route, "upstream", d.Upstream)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveDestination(r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/enrollments", func(w http.ResponseWriter, r *http.Request) {
		var out []IdentitySummary
		for _, id := range store.ListIdentities() {
			out = append(out, IdentitySummary{ID: id.ID, Project: id.Project, Role: id.Role, Destinations: id.Destinations, Repos: id.Repos, Expiry: id.Expiry})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/enrollments", func(w http.ResponseWriter, r *http.Request) {
		var b EnrollBody
		if !decode(w, r, &b) {
			return
		}
		if b.ID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		tok, err := Enroll(store, b.ID, b.Project, b.Role, b.Destinations, b.Repos, time.Duration(b.TTLSeconds)*time.Second, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin enrolled", "id", b.ID, "project", b.Project, "role", b.Role)
		writeJSON(w, http.StatusCreated, EnrollResult{ID: b.ID, Token: tok})
	})
	mux.HandleFunc("DELETE /admin/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Remove(r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Auth gate wraps every route.
	return authMiddleware(auth, log, mux)
}

func authMiddleware(auth OperatorAuthenticator, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := auth.Authenticate(r); err != nil {
			log.Warn("admin request rejected", "reason", err.Error(), "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return false
	}
	return true
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/harbor/ -run TestAdmin -count=1 -v`
Expected: PASS (add/list, cred validation, enroll+revoke+no-leak, non-loopback 403).

- [ ] **Step 5: Full package + vet**

Run: `go test ./internal/harbor/ -count=1 && go vet ./internal/harbor/`
Expected: PASS, clean.

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go
git commit -m "feat(harbor): loopback admin API — destinations + enrollments"
```

---

### Task 5: Admin client + wire DTOs

**Files:**
- Create: `internal/harbor/adminclient/adminclient.go`
- Create: `internal/harbor/adminclient/adminclient_test.go`

**Interfaces:**
- Consumes: the admin API (Task 4); `harbor.Destination`, `harbor.EnrollBody`, `harbor.EnrollResult` for wire shapes.
- Produces: `func New(baseURL string) *Client`; `Client.Enroll(EnrollParams)(harbor.EnrollResult,error)`, `Client.Revoke(id string) error`, `Client.AddDestination(harbor.Destination) error`, `Client.ListDestinations()([]harbor.Destination,error)`, `Client.RemoveDestination(name string) error`; `type EnrollParams struct { ID,Project,Role string; Destinations,Repos []string; TTL time.Duration }`.

> Note: for the MVP this client imports `internal/harbor` for the wire types (harbor is stdlib-only, so no heavy deps leak in). When harbor gains server-only deps (Fly/OIDC/DB in later slices), extract the shared wire types into a `harborapi` package so `at-harborctl` stays light — a mechanical move, tracked as the split trigger.

- [ ] **Step 1: Write the failing test (client against the real handler)**

Create `internal/harbor/adminclient/adminclient_test.go`:

```go
package adminclient

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
)

func newServer(t *testing.T) (*httptest.Server, harbor.Store) {
	t.Helper()
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := harbor.NewAdminHandler(store, harbor.LoopbackAuthenticator{}, func(n string) bool { return n == "git-pat" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h) // listens on 127.0.0.1 → passes the loopback authenticator
	t.Cleanup(ts.Close)
	return ts, store
}

func TestClientRoundTrip(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL)

	if err := c.AddDestination(harbor.Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: harbor.ApplyBasicPassword, CredName: "git-pat", Apply: harbor.ApplyBasicPassword, RepoScoped: true}); err != nil {
		t.Fatalf("AddDestination: %v", err)
	}
	ds, err := c.ListDestinations()
	if err != nil || len(ds) != 1 || ds[0].Name != "git" {
		t.Fatalf("ListDestinations = %+v, %v", ds, err)
	}
	res, err := c.Enroll(EnrollParams{ID: "spider-18", Project: "ACME", Role: "guest", Destinations: []string{"git"}, Repos: []string{"acme/*"}})
	if err != nil || res.Token == "" {
		t.Fatalf("Enroll = %+v, %v", res, err)
	}
	if _, ok := store.Lookup(harbor.HashToken(res.Token)); !ok {
		t.Fatal("identity not stored")
	}
	if err := c.Revoke("spider-18"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := store.Lookup(harbor.HashToken(res.Token)); ok {
		t.Fatal("identity present after Revoke")
	}
}

func TestClientAddDestinationRejected(t *testing.T) {
	ts, _ := newServer(t)
	c := New(ts.URL)
	err := c.AddDestination(harbor.Destination{Name: "bad", Route: "/bad/", Upstream: "https://x", IdentityIn: harbor.ApplyBearer, CredName: "nope", Apply: harbor.ApplyBearer})
	if err == nil {
		t.Fatal("expected error for unresolvable cred_name")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/harbor/adminclient/ -v`
Expected: FAIL — `undefined: New` / `EnrollParams`.

- [ ] **Step 3: Implement `internal/harbor/adminclient/adminclient.go`**

```go
// Package adminclient is a typed HTTP client for harbor's loopback admin API.
// Kept dependency-light so a future at-harborctl can reuse it; today it imports
// internal/harbor only for the wire types (harbor is stdlib-only).
package adminclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// Client talks to a running harbor's admin API (e.g. http://127.0.0.1:8081).
type Client struct {
	base  string
	httpc *http.Client
}

// New returns a Client for the admin base URL (no trailing slash needed).
func New(baseURL string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), httpc: &http.Client{Timeout: 10 * time.Second}}
}

// EnrollParams are the inputs to an enrollment.
type EnrollParams struct {
	ID           string
	Project      string
	Role         string
	Destinations []string
	Repos        []string
	TTL          time.Duration
}

func (c *Client) do(method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("admin API unreachable at %s (is `at-harbor serve` running?): %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("admin API %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Enroll(p EnrollParams) (harbor.EnrollResult, error) {
	var res harbor.EnrollResult
	err := c.do("POST", "/admin/enrollments", harbor.EnrollBody{
		ID: p.ID, Project: p.Project, Role: p.Role,
		Destinations: p.Destinations, Repos: p.Repos, TTLSeconds: int64(p.TTL / time.Second),
	}, &res)
	return res, err
}

func (c *Client) Revoke(id string) error {
	return c.do("DELETE", "/admin/enrollments/"+id, nil, nil)
}

func (c *Client) AddDestination(d harbor.Destination) error {
	return c.do("POST", "/admin/destinations", d, nil)
}

func (c *Client) ListDestinations() ([]harbor.Destination, error) {
	var out []harbor.Destination
	err := c.do("GET", "/admin/destinations", nil, &out)
	return out, err
}

func (c *Client) RemoveDestination(name string) error {
	return c.do("DELETE", "/admin/destinations/"+name, nil, nil)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/harbor/adminclient/ -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/adminclient/
git commit -m "feat(harbor): typed admin-API client (at-harborctl seam)"
```

---

### Task 6: CLI as admin client + serve two listeners + bootstrap config

**Files:**
- Modify: `cmd/at-harbor/config.go`
- Modify: `cmd/at-harbor/config_test.go`
- Modify: `cmd/at-harbor/main.go`
- Modify: `cmd/at-harbor/main_test.go`
- Modify: `cmd/at-harbor/serve_integration_test.go`
- Modify: `docs/OVERVIEW.md`, `docs/usage/INDEX.md`

**Interfaces:**
- Consumes: `adminclient` (Task 5); `harbor.NewAdminHandler`, `harbor.LoopbackAuthenticator`, `harbor.NewBroker`, `harbor.NewFileStore`, `harbor.NewSecretResolver`, `harbor.RenderEnrollSnippet`, `harbor.Config` (import parse).
- Produces: `serveConfig` with `AdminListen`, no inline destinations; `run()` with `serve`/`enroll`/`revoke`/`destination` verbs.

- [ ] **Step 1: Update `serveConfig` (bootstrap: add admin-listen, drop destinations)**

In `cmd/at-harbor/config.go`, replace the `serveConfig` struct with:

```go
type serveConfig struct {
	Listen      string `yaml:"listen"`
	AdminListen string `yaml:"admin-listen"`
	TLS         struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	Store       string              `yaml:"store"`
	Credentials map[string]credSpec `yaml:"credentials"`
}
```

(Remove the `Broker harbor.Config` field and the now-unused `harbor` import if `credSpecs` no longer needs it — it uses `secret`, keep that. Remove `harbor` from config.go imports.)

- [ ] **Step 2: Update `config_test.go`**

In `cmd/at-harbor/config_test.go`, change the YAML in `TestParseServeConfig` to drop `destinations:` and add `admin-listen:`, and replace the destinations assertion with an admin-listen assertion:

```go
	yml := `
listen: ":8443"
admin-listen: "127.0.0.1:8081"
tls: { cert: /c.pem, key: /k.pem }
store: /var/lib/harbor/store.json
credentials:
  anthropic-key: { command: [at-mint, anthropic] }
  git-pat: { value: literal-dev-pat }
`
	cfg, err := parseServeConfig([]byte(yml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Listen != ":8443" || cfg.AdminListen != "127.0.0.1:8081" || cfg.Store != "/var/lib/harbor/store.json" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if s := cfg.credSpecs()["git-pat"]; !s.Literal || s.Value != "literal-dev-pat" {
		t.Fatalf("git-pat spec = %+v", s)
	}
```

- [ ] **Step 3: Rewrite `cmd/at-harbor/main.go`**

```go
// Command at-harbor is the central credential broker + control plane. `serve`
// runs the credential-injecting reverse proxy and a loopback admin API;
// `enroll`/`revoke`/`destination` are admin-API clients. See the harbor specs
// under docs/superpowers/specs/.
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
	"github.com/aethons-tools/cove/internal/harbor/adminclient"
	"github.com/aethons-tools/cove/internal/runner"
	"gopkg.in/yaml.v3"
)

var version = "dev"

const defaultAdminURL = "http://127.0.0.1:8081"

func run(argv []string, getenv func(string) string, stdout, stderr io.Writer) int {
	app := cli.App{
		Name:    "at-harbor",
		Version: version,
		Commands: []cli.Command{
			{Name: "serve", Brief: "run the broker + loopback admin API", Run: cmdServe},
			{Name: "enroll", Brief: "enroll an identity (via the admin API) and print its snippet", Run: cmdEnroll},
			{Name: "revoke", Brief: "revoke an identity (via the admin API)", Run: cmdRevoke},
			{Name: "destination", Brief: "manage destinations (add|list|rm|import) via the admin API", Run: cmdDestination},
		},
	}
	return app.Run(argv, stdout, stderr)
}

func cmdEnroll(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	adminURL := fs.String("admin-url", defaultAdminURL, "harbor admin API URL")
	id := fs.String("id", "", "identity id (e.g. spider-18)")
	project := fs.String("project", "", "project name")
	role := fs.String("role", "guest", "role name")
	dests := fs.String("destinations", "", "comma-separated destination names")
	repos := fs.String("repos", "", "comma-separated owner/repo globs")
	baseURL := fs.String("base-url", "", "harbor broker base URL for the printed snippet")
	ttl := fs.Duration("ttl", 0, "identity lifetime (0 = no expiry)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *id == "" || *baseURL == "" {
		fmt.Fprintln(stderr, "at-harbor enroll: --id and --base-url are required")
		return 2
	}
	res, err := adminclient.New(*adminURL).Enroll(adminclient.EnrollParams{
		ID: *id, Project: *project, Role: *role,
		Destinations: splitCSV(*dests), Repos: splitCSV(*repos), TTL: *ttl,
	})
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprint(stdout, harbor.RenderEnrollSnippet(*baseURL, res.Token))
	return 0
}

func cmdRevoke(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	adminURL := fs.String("admin-url", defaultAdminURL, "harbor admin API URL")
	id := fs.String("id", "", "identity id to remove")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 || *id == "" {
		fmt.Fprintln(stderr, "at-harbor revoke: --id is required")
		return 2
	}
	if err := adminclient.New(*adminURL).Revoke(*id); err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, "revoked", *id)
	return 0
}

func cmdDestination(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor destination: expected add|list|rm|import")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("destination "+sub, flag.ContinueOnError)
	adminURL := fs.String("admin-url", defaultAdminURL, "harbor admin API URL")
	// add flags
	var d harbor.Destination
	fs.StringVar(&d.Name, "name", "", "destination name")
	fs.StringVar(&d.Route, "route", "", "inbound path prefix, e.g. /git/")
	fs.StringVar(&d.Upstream, "upstream", "", "upstream base URL")
	var identityIn, apply string
	fs.StringVar(&identityIn, "identity-in", "", "bearer|basic-password|x-api-key")
	fs.StringVar(&d.CredName, "cred-name", "", "credential name to inject")
	fs.StringVar(&apply, "apply", "", "bearer|basic-password|x-api-key")
	fs.BoolVar(&d.RepoScoped, "repo-scoped", false, "path is <route>/<owner>/<repo>/…")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	c := adminclient.New(*adminURL)
	switch sub {
	case "add":
		d.IdentityIn, d.Apply = harbor.ApplyMethod(identityIn), harbor.ApplyMethod(apply)
		if err := c.AddDestination(d); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added destination", d.Name)
	case "list":
		ds, err := c.ListDestinations()
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, dd := range ds {
			fmt.Fprintf(stdout, "%s\t%s\t-> %s\t(cred %q, %s)\n", dd.Name, dd.Route, dd.Upstream, dd.CredName, dd.Apply)
		}
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor destination rm: expected one destination name")
			return 2
		}
		if err := c.RemoveDestination(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed destination", pos[0])
	case "import":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor destination import: expected one YAML file path")
			return 2
		}
		data, err := os.ReadFile(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		var conf harbor.Config
		if err := yaml.Unmarshal(data, &conf); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, dd := range conf.Destinations {
			if err := c.AddDestination(dd); err != nil {
				fmt.Fprintln(stderr, "at-harbor:", err)
				return 1
			}
			fmt.Fprintln(stdout, "imported destination", dd.Name)
		}
	default:
		fmt.Fprintln(stderr, "at-harbor destination: unknown subcommand", sub)
		return 2
	}
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
	specs := cfg.credSpecs()
	creds := harbor.NewSecretResolver(runner.OS{}, specs)
	broker := harbor.NewBroker(st, creds, log)

	// Admin API on the loopback listener (operator surface).
	if cfg.AdminListen != "" {
		credExists := func(n string) bool { _, ok := specs[n]; return ok }
		admin := harbor.NewAdminHandler(st, harbor.LoopbackAuthenticator{}, credExists, log)
		go func() {
			log.Info("harbor admin API listening", "addr", cfg.AdminListen)
			if err := http.ListenAndServe(cfg.AdminListen, admin); err != nil {
				log.Error("admin API stopped", "err", err.Error())
			}
		}()
	}

	srv := &http.Server{Addr: cfg.Listen, Handler: broker}
	log.Info("harbor broker listening", "addr", cfg.Listen)
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

- [ ] **Step 4: Update `main_test.go` (enroll/revoke now hit an admin server)**

Replace `cmd/at-harbor/main_test.go`'s `TestEnrollCommandPrintsSnippet` with a test that stands up a real admin server and points the CLI at it:

```go
package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
)

func TestEnrollCommandPrintsSnippet(t *testing.T) {
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := harbor.NewAdminHandler(store, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	code := run([]string{
		"enroll", "--admin-url", ts.URL, "--id", "spider-18", "--project", "ACME",
		"--role", "guest", "--destinations", "anthropic,git", "--repos", "acme/*",
		"--base-url", "https://harbor.local.aethons.tools",
	}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic") {
		t.Fatalf("stdout missing snippet:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "HARBOR_IDENTITY_TOKEN=") {
		t.Fatal("stdout missing minted token line")
	}
	if len(store.ListIdentities()) != 1 {
		t.Fatal("identity was not created via the admin API")
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
```

- [ ] **Step 5: Run the cmd tests**

Run: `go test ./cmd/at-harbor/ -count=1 -v`
Expected: PASS (config parse with admin-listen; enroll-via-API prints the snippet + creates the identity; unknown command exits 2).

- [ ] **Step 6: Update the integration test for two listeners**

In `cmd/at-harbor/serve_integration_test.go` (behind `//go:build integration`), the broker no longer takes a `Config`. Update the broker construction to seed the destination into the store and call the new `NewBroker`:

```go
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.AddDestination(harbor.Destination{Name: "anthropic", Route: "/anthropic/", Upstream: up.URL, IdentityIn: harbor.ApplyXAPIKey, CredName: "anthropic-key", Apply: harbor.ApplyXAPIKey}); err != nil {
		t.Fatal(err)
	}
	tok, _ := harbor.Enroll(store, "spider-18", "ACME", "guest", []string{"anthropic"}, nil, 0, timeNow())
	broker := harbor.NewBroker(store, fakeResolver{}, discardLogger())
```

And change the request auth from `Authorization: Bearer` to `x-api-key` to match the x-api-key destination:

```go
	req.Header.Set("x-api-key", tok)
```

(assert the upstream saw `X-Api-Key: REAL-ANTHROPIC`; adjust the fakeResolver/assertion names to whatever the file already defines).

- [ ] **Step 7: Full build + suites + vet + integration**

Run: `go build ./... && go test ./... -count=1 && go vet ./... && go test -tags integration ./cmd/at-harbor/... -count=1`
Expected: everything builds; all hermetic tests pass; vet clean; integration passes.

- [ ] **Step 8: Docs**

Update `docs/OVERVIEW.md` (the `at-harbor` description: now broker + loopback admin API; `harbor.yaml` is bootstrap-only, destinations/enrollments managed via API/CLI) and add/adjust the `docs/usage/INDEX.md` pointer to reference this control-plane spec. Keep it terse.

- [ ] **Step 9: Commit**

```bash
git add cmd/at-harbor/ docs/
git commit -m "feat(harbor): CLI as admin client + serve two listeners + bootstrap config"
```

---

## Manual verification (definition of done)

Run once by hand against a locally running harbor:

1. Write a bootstrap `harbor.yaml` (`listen`, `admin-listen: 127.0.0.1:8081`, `tls`, `store`, `credentials`) with **no `destinations:`**. `at-harbor serve --config harbor.yaml`.
2. `at-harbor destination add --name git --route /git/ --upstream https://github.com --identity-in basic-password --cred-name git-pat --apply basic-password --repo-scoped` → then a `git clone` through harbor works **without restarting serve**.
3. `at-harbor destination add … --cred-name nope` → rejected (unresolvable cred).
4. `at-harbor enroll …` then `at-harbor revoke --id …` → the identity is accepted then rejected on the next brokered request, **no restart**.
5. `at-harbor destination import old-harbor.yaml` migrates a slice-1 destinations block.
6. Confirm no credential value or identity token appears in harbor's logs or any `GET /admin/*` response.

Then **close COV-139** referencing this slice (live reload folded in).

---

## Self-review

**Spec coverage:** admin API destinations+enrollments → Tasks 4/6; live store + broker-reads-live → Tasks 1/2; credentials-stay-config-sourced (validated, never stored) → Tasks 4/6 (`credExists`, no cred in store); loopback admin listener → Tasks 3/6; operator-auth seam (loopback now, Auth0 later) → Task 3; CLI-as-client + `destination` verbs + `import` → Task 6; `harbor.yaml`→bootstrap → Task 6; file-store (no new dep) → Task 1; subsumes COV-139 → single-writer model (Tasks 1/6) + manual-verify close. Deferred items (Auth0, object model, DB, UI, at-harborctl split) correctly absent.

**Placeholder scan:** none — every step has complete code or an exact edit; the one "adjust to whatever the file defines" note (integration test helpers) refers to symbols already in that file from slice #1.

**Type consistency:** `Store` (Task 1) is used with the same method set in Tasks 2/4/5/6; `NewBroker(store, creds, log)` (Task 2) matches its callers in Task 6 + the integration test; `Destination` json tags (Task 1) are what the admin handler (Task 4) and client (Task 5) marshal; `EnrollBody`/`EnrollResult`/`IdentitySummary` (Task 4) match the client's use (Task 5); `LoopbackAuthenticator`/`OperatorAuthenticator` (Task 3) match `NewAdminHandler`'s signature (Task 4) and serve wiring (Task 6); `adminclient.New`/`EnrollParams` (Task 5) match the CLI (Task 6).
