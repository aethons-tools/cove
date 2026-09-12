# Harbor Actor Roster + Role Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace harbor's flat per-identity broker scope with a traditional RBAC spine — a top-level `Actor` (one durable identity + token) is granted `Role`s within `Project`s, a `Role` owns the security scope, and the broker authorizes each request additively across the Actor's grants.

**Architecture:** Evolve `internal/harbor` in place. The `Identity` type becomes `Actor` (top-level, holds `Grants`); `Role` owns `Scope{Destinations,Repos,TTL}`; `Grant{Project,Role,Overrides?}` is the many-to-many join embedded in the Actor. `Decide` stays pure but takes the list of effective scopes the Actor's grants resolve to and does a **per-grant existential** check (a request passes iff some single grant authorizes the whole `(destination, repo)` pair). The file store gains a v3 shape with a behavior-preserving migration. The admin API/CLI gain role + grant + roster management; enrollment becomes role-required. at-cove's auto-enroll drops its scope flags (staying go-oidc-free).

**Tech Stack:** Go 1.25, stdlib only in `internal/harbor` (no new deps); `net/http` ServeMux admin API; JSON file store; `runner.Fake` for hermetic tests.

## Global Constraints

- **Hermetic tests only** — no Docker/network/VM; `Decide`/`EffectiveScope` stay pure; store tests use temp files; at-cove tests drive `runner.Fake`. Real-ssh tests (none here) go behind the `integration` build tag.
- **go-oidc boundary** — `cmd/at-cove`, `internal/connect`, `internal/dispatchrun` must never import `internal/harbor` or `internal/harbor/adminclient`. Verify after the at-cove task: `go list -deps ./cmd/at-cove | grep -i oidc` is empty.
- **Secrets never on disk/argv/logs** — the store holds only token *hashes*; credentials stay config-sourced (Roles carry destination *names* + repo globs, never secret values).
- **Fail closed** — unknown actor, no resolvable grant, or a grant whose role was deleted ⇒ deny.
- **`DefaultProject = "default"`** — used whenever enrollment or a grant names no project.
- **TDD** — write the failing test, watch it fail, implement minimally, watch it pass, commit.
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

## File Structure

- `internal/harbor/identity.go` — rename to carry the RBAC types (`Scope`, `Role`, `Override`, `Grant`, `Actor`, `DefaultProject`); keep `MintToken`/`HashToken`.
- `internal/harbor/decide.go` — `EffectiveScope` + the existential `Decide`.
- `internal/harbor/enroll.go` — role-required `Enroll` (creates Actor + first grant).
- `internal/harbor/filestore.go` — `Store` interface (actor/role/grant/destination methods) + `FileStore` + v3 `storeFile` + v1/v2→v3 migration.
- `internal/harbor/proxy.go` — broker resolves grants→scopes (fail-closed) before `Decide`.
- `internal/harbor/admin.go` — `EnrollBody` trimmed; enroll handler role-required; `GET /admin/roster`; **Task 2** adds role/grant/project routes.
- `internal/harbor/adminclient/adminclient.go` — trimmed `Enroll`; **Task 2** adds role/grant/roster/project methods.
- `cmd/at-harbor/main.go` — **Task 3**: `role`/`grant`/`ungrant`/`roster` verbs, trimmed `enroll`.
- `cmd/at-cove/main.go` — **Task 4**: `harborPlan` auto-enroll argv trimmed.
- `docs/` — **Task 5**: INDEX row, `at-cove-config.md` harbor section, OVERVIEW if touched.

Test files mirror each (`*_test.go`) in the same packages.

---

### Task 1: Evolve `internal/harbor` to the RBAC model

This is the irreducible coupled unit: renaming `Identity→Actor` and changing the `Decide`/`Store` signatures breaks every consumer in the package at once, so types, store, migration, decision, enrollment, the broker, and the *existing* admin enroll/roster/revoke handlers move together. The **new** admin routes (roles/grants/projects) are Task 2.

**Files:**
- Modify: `internal/harbor/identity.go`
- Modify: `internal/harbor/decide.go`, `internal/harbor/decide_test.go`
- Modify: `internal/harbor/enroll.go`, `internal/harbor/enroll_test.go`
- Modify: `internal/harbor/filestore.go`, `internal/harbor/filestore_test.go`
- Modify: `internal/harbor/proxy.go`, `internal/harbor/proxy_test.go`
- Modify: `internal/harbor/admin.go`, `internal/harbor/admin_test.go`
- Modify: `internal/harbor/adminclient/adminclient.go` (enroll body only)

**Interfaces:**
- Produces (consumed by Tasks 2–4):
  - `type Scope struct { Destinations []string; Repos []string; TTL time.Duration }`
  - `type Role struct { Name string; Scope Scope }`
  - `type Override struct { Destinations []string; Repos []string }`
  - `type Grant struct { Project string; Role string; Overrides *Override }`
  - `type Actor struct { ID string; TokenHash string; Grants []Grant; Expiry time.Time }`
  - `const DefaultProject = "default"`
  - `func EffectiveScope(g Grant, r Role) Scope`
  - `func Decide(a Actor, scopes []Scope, dest Destination, ownerRepo string, now time.Time) (Decision, error)`
  - `func Enroll(store Store, id, project, role string, overrides *Override, now time.Time) (string, error)`
  - `Store` interface: `AddActor(Actor) error`, `Lookup(tokenHash string) (Actor, bool)`, `RemoveActor(id string) error`, `ListActors() []Actor`, `AddGrant(actorID string, g Grant) error`, `RemoveGrant(actorID, project, role string) error`, `PutRole(project string, r Role) error`, `GetRole(project, name string) (Role, bool)`, `RemoveRole(project, name string) error`, `ListRoles(project string) []Role`, `ListProjects() []string`, plus the unchanged destination methods.

- [ ] **Step 1: Write the failing type/resolution tests**

Replace the body of `internal/harbor/decide_test.go` with tests against the new API (the old `Identity{Destinations:…}` shape is gone):

```go
package harbor

import (
	"testing"
	"time"
)

func roleScopes(t *testing.T, scopes ...Scope) []Scope { t.Helper(); return scopes }

func TestEffectiveScopeInheritsRole(t *testing.T) {
	r := Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"}}}
	got := EffectiveScope(Grant{Project: "p", Role: "guest"}, r)
	if len(got.Destinations) != 2 || got.Repos[0] != "acme/*" {
		t.Fatalf("inherited scope = %+v", got)
	}
}

func TestEffectiveScopeOverrideReplaces(t *testing.T) {
	r := Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"}}}
	g := Grant{Project: "p", Role: "guest", Overrides: &Override{Repos: []string{"beta/*"}}}
	got := EffectiveScope(g, r)
	if len(got.Destinations) != 2 { // destinations inherited (override nil)
		t.Fatalf("destinations should inherit: %+v", got)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "beta/*" { // repos replaced
		t.Fatalf("repos should be replaced: %+v", got)
	}
}

func TestDecideAllowsWhenAGrantAuthorizes(t *testing.T) {
	dest := testConfig().Destinations[0] // anthropic
	dec, err := Decide(Actor{ID: "spider-18"}, roleScopes(t, Scope{Destinations: []string{"anthropic"}}), dest, "", time.Now())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.NeedCred || dec.CredName != "anthropic-bearer" || dec.Apply != ApplyBearer {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestDecideAdditiveAcrossGrants(t *testing.T) {
	git := testConfig().Destinations[1] // repo-scoped
	// first scope lacks git; second authorizes it — additive.
	scopes := roleScopes(t,
		Scope{Destinations: []string{"anthropic"}},
		Scope{Destinations: []string{"git"}, Repos: []string{"beta/*"}},
	)
	if _, err := Decide(Actor{ID: "x"}, scopes, git, "beta/api", time.Now()); err != nil {
		t.Fatalf("second grant should authorize: %v", err)
	}
}

func TestDecideNoCrossGrantRepoBleed(t *testing.T) {
	git := testConfig().Destinations[1]
	// grant A: git but only acme/*. grant B: beta/* but NOT git. Must deny git beta/x.
	scopes := roleScopes(t,
		Scope{Destinations: []string{"git"}, Repos: []string{"acme/*"}},
		Scope{Destinations: []string{"anthropic"}, Repos: []string{"beta/*"}},
	)
	if _, err := Decide(Actor{ID: "x"}, scopes, git, "beta/secret", time.Now()); err == nil {
		t.Fatal("expected denial: no single grant couples git with beta/*")
	}
}

func TestDecideDeniesWithNoScopes(t *testing.T) {
	if _, err := Decide(Actor{ID: "x"}, nil, testConfig().Destinations[0], "", time.Now()); err == nil {
		t.Fatal("expected denial when the actor has no resolvable grant")
	}
}

func TestDecideRejectsExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	a := Actor{ID: "x", Expiry: past}
	if _, err := Decide(a, roleScopes(t, Scope{Destinations: []string{"anthropic"}}), testConfig().Destinations[0], "", time.Now()); err == nil {
		t.Fatal("expected expired actor to be rejected")
	}
}
```

- [ ] **Step 2: Run the tests to confirm they fail to compile**

Run: `just test` (or `go test ./internal/harbor/`)
Expected: FAIL — `undefined: EffectiveScope`, `Actor`, `Scope`, `Grant`, `Override`, and `Identity`-shaped call sites no longer compile.

- [ ] **Step 3: Rewrite `identity.go` with the RBAC types**

Replace the type section of `internal/harbor/identity.go` (keep the package doc comment, `MintToken`, `HashToken` exactly as they are):

```go
// Scope is a Role's security envelope: which destinations an actor granted this
// role may reach, which repos (for repo-scoped destinations), and the default
// token lifetime applied at enrollment.
type Scope struct {
	Destinations []string      `json:"destinations"`
	Repos        []string      `json:"repos"`
	TTL          time.Duration `json:"ttl"`
}

// Role is a named, reusable security class within a project.
type Role struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
}

// Override lets one grant narrow/replace fields of its Role's scope. A nil field
// inherits the Role; a set field REPLACES the Role's field (no merge).
type Override struct {
	Destinations []string `json:"destinations,omitempty"`
	Repos        []string `json:"repos,omitempty"`
}

// Grant assigns a Role (within a Project) to an Actor, optionally narrowed.
type Grant struct {
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	Overrides *Override `json:"overrides,omitempty"`
}

// Actor is one enrolled top-level identity. The raw token is never stored — only
// its hash — so a leaked store yields no usable credentials. Grants are the
// Actor's role assignments across projects (RBAC).
type Actor struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"token_hash"`
	Grants    []Grant   `json:"grants"`
	Expiry    time.Time `json:"expiry"` // zero = no expiry
}

// DefaultProject backs harbor-side default enrollment when no project is named.
const DefaultProject = "default"
```

- [ ] **Step 4: Rewrite `decide.go` with `EffectiveScope` + existential `Decide`**

Replace the body of `internal/harbor/decide.go` (keep the `Decision` struct):

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

// EffectiveScope layers a grant's overrides over its role's scope. Each field is
// the override when set, else the role's. (Override replaces, never merges.)
func EffectiveScope(g Grant, r Role) Scope {
	s := r.Scope
	if g.Overrides != nil {
		if g.Overrides.Destinations != nil {
			s.Destinations = g.Overrides.Destinations
		}
		if g.Overrides.Repos != nil {
			s.Repos = g.Overrides.Repos
		}
	}
	return s
}

// Decide answers, for an actor and the effective scopes its grants resolve to,
// whether (dest, ownerRepo) is permitted, and which credential to inject. It is
// additive across grants but per-grant existential: a request passes iff SOME
// single scope authorizes the whole (destination, repo) pair — so one grant's
// destination never recombines with another grant's repos. Fails closed.
func Decide(a Actor, scopes []Scope, dest Destination, ownerRepo string, now time.Time) (Decision, error) {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return Decision{}, fmt.Errorf("actor %q expired", a.ID)
	}
	if dest.RepoScoped && ownerRepo == "" {
		return Decision{}, fmt.Errorf("destination %q requires owner/repo", dest.Name)
	}
	for _, s := range scopes {
		if !slices.Contains(s.Destinations, dest.Name) {
			continue
		}
		if dest.RepoScoped && !repoAllowed(ownerRepo, s.Repos) {
			continue
		}
		return Decision{Dest: dest, NeedCred: dest.CredName != "", CredName: dest.CredName, Apply: dest.Apply}, nil
	}
	return Decision{}, fmt.Errorf("actor %q not authorized for destination %q", a.ID, dest.Name)
}
```

- [ ] **Step 5: Rewrite `enroll.go` (role-required, creates Actor + first grant)**

Replace `Enroll` in `internal/harbor/enroll.go` (keep `RenderEnrollSnippet`):

```go
// Enroll mints a token and records a new Actor whose first grant is (project,
// role). The role must already exist (fail closed); its TTL sets the actor's
// expiry. Only the token hash is stored; the raw token is returned once. project
// == "" defaults to DefaultProject. now is injected for testability.
func Enroll(store Store, id, project, role string, overrides *Override, now time.Time) (string, error) {
	if id == "" {
		return "", fmt.Errorf("identity id is required")
	}
	if role == "" {
		return "", fmt.Errorf("role is required")
	}
	if project == "" {
		project = DefaultProject
	}
	r, ok := store.GetRole(project, role)
	if !ok {
		return "", fmt.Errorf("role %q not found in project %q", role, project)
	}
	tok, err := MintToken()
	if err != nil {
		return "", err
	}
	rec := Actor{
		ID:        id,
		TokenHash: HashToken(tok),
		Grants:    []Grant{{Project: project, Role: role, Overrides: overrides}},
	}
	if r.Scope.TTL > 0 {
		rec.Expiry = now.Add(r.Scope.TTL)
	}
	if err := store.AddActor(rec); err != nil {
		return "", err
	}
	return tok, nil
}
```

- [ ] **Step 6: Write the failing store tests (migration + CRUD)**

Append to `internal/harbor/filestore_test.go` (keep existing destination tests; they use the unchanged destination methods):

```go
func TestFileStoreRoleAndActorCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.PutRole("acme", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if r, ok := fs.GetRole("acme", "guest"); !ok || r.Scope.Destinations[0] != "anthropic" {
		t.Fatalf("GetRole = %+v, %v", r, ok)
	}
	if err := fs.AddActor(Actor{ID: "spider-18", TokenHash: "h1", Grants: []Grant{{Project: "acme", Role: "guest"}}}); err != nil {
		t.Fatalf("AddActor: %v", err)
	}
	if err := fs.AddActor(Actor{ID: "spider-18", TokenHash: "h2"}); err == nil {
		t.Fatal("AddActor should reject a duplicate id")
	}
	if err := fs.AddGrant("spider-18", Grant{Project: "beta", Role: "review"}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	a, ok := fs.Lookup("h1")
	if !ok || len(a.Grants) != 2 {
		t.Fatalf("after AddGrant, actor = %+v", a)
	}
	if err := fs.RemoveGrant("spider-18", "beta", "review"); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if a, _ := fs.Lookup("h1"); len(a.Grants) != 1 {
		t.Fatalf("after RemoveGrant, grants = %+v", a.Grants)
	}
	if projs := fs.ListProjects(); len(projs) == 0 {
		t.Fatal("ListProjects should include acme")
	}
}

func TestFileStoreMigratesV2Identities(t *testing.T) {
	// A v2 file: flat identities carrying inline scope. Migration must preserve
	// each actor's effective scope exactly via a synthesized role (+ override on
	// a scope that differs from the synthesized role for the same project/role).
	path := filepath.Join(t.TempDir(), "store.json")
	v2 := `{
	  "identities": {
	    "hashA": {"id":"a","token_hash":"hashA","project":"acme","role":"guest","destinations":["anthropic","git"],"repos":["acme/*"]},
	    "hashB": {"id":"b","token_hash":"hashB","project":"acme","role":"guest","destinations":["anthropic"],"repos":["acme/api"]}
	  },
	  "destinations": {}
	}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	// A role named (acme,guest) now exists.
	if _, ok := fs.GetRole("acme", "guest"); !ok {
		t.Fatal("migration should synthesize role (acme,guest)")
	}
	// Both actors resolve to their ORIGINAL effective scope.
	check := func(hash string, wantDests, wantRepos []string) {
		a, ok := fs.Lookup(hash)
		if !ok || len(a.Grants) != 1 {
			t.Fatalf("actor %s = %+v", hash, a)
		}
		r, _ := fs.GetRole(a.Grants[0].Project, a.Grants[0].Role)
		got := EffectiveScope(a.Grants[0], r)
		if strings.Join(got.Destinations, ",") != strings.Join(wantDests, ",") ||
			strings.Join(got.Repos, ",") != strings.Join(wantRepos, ",") {
			t.Fatalf("actor %s effective scope = %+v, want dests=%v repos=%v", hash, got, wantDests, wantRepos)
		}
	}
	check("hashA", []string{"anthropic", "git"}, []string{"acme/*"})
	check("hashB", []string{"anthropic"}, []string{"acme/api"})
}
```

Ensure `filestore_test.go` imports `os`, `path/filepath`, `strings`, `testing`.

- [ ] **Step 7: Run store tests to verify they fail**

Run: `go test ./internal/harbor/ -run 'FileStore'`
Expected: FAIL — `PutRole`/`GetRole`/`AddActor`/`AddGrant`/`ListProjects` undefined; migration not implemented.

- [ ] **Step 8: Rewrite `filestore.go` — interface, v3 shape, CRUD, migration**

Replace `internal/harbor/filestore.go` with:

```go
package harbor

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Store records enrolled actors (by token hash), roles (by project+name), and the
// destination table.
type Store interface {
	AddActor(a Actor) error // error if the id already exists
	Lookup(tokenHash string) (Actor, bool)
	RemoveActor(id string) error
	ListActors() []Actor

	AddGrant(actorID string, g Grant) error // upsert by (project,role); error if actor absent
	RemoveGrant(actorID, project, role string) error

	PutRole(project string, r Role) error // upsert; auto-creates the project namespace
	GetRole(project, name string) (Role, bool)
	RemoveRole(project, name string) error
	ListRoles(project string) []Role
	ListProjects() []string

	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)
}

// storeFile is the on-disk JSON shape (format v3).
type storeFile struct {
	Roles        map[string]map[string]Role `json:"roles"`        // project → roleName → Role
	Actors       map[string]Actor           `json:"actors"`       // keyed by TokenHash
	Destinations map[string]Destination     `json:"destinations"` // keyed by Name
}

// legacyIdentity is the pre-RBAC (v1/v2) per-identity record, read only during
// migration.
type legacyIdentity struct {
	ID           string    `json:"id"`
	TokenHash    string    `json:"token_hash"`
	Project      string    `json:"project"`
	Role         string    `json:"role"`
	Destinations []string  `json:"destinations"`
	Repos        []string  `json:"repos"`
	Expiry       time.Time `json:"expiry"`
}

// FileStore is a JSON-file-backed Store. Single-node MVP; the serve process is the
// sole writer, so there is no cross-process contention.
type FileStore struct {
	path   string
	mu     sync.Mutex
	roles  map[string]map[string]Role
	actors map[string]Actor
	dests  map[string]Destination
}

// NewFileStore loads (or initializes) the store at path, migrating a v1 (bare
// map[tokenHash]Identity) or v2 (identities+destinations) file into the v3 shape.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{
		path:   path,
		roles:  map[string]map[string]Role{},
		actors: map[string]Actor{},
		dests:  map[string]Destination{},
	}
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

	// v3?
	var v3 storeFile
	if err := json.Unmarshal(data, &v3); err != nil {
		return nil, fmt.Errorf("load store %s: %w", path, err)
	}
	if v3.Actors != nil || v3.Roles != nil {
		if v3.Roles != nil {
			fs.roles = v3.Roles
		}
		if v3.Actors != nil {
			fs.actors = v3.Actors
		}
		if v3.Destinations != nil {
			fs.dests = v3.Destinations
		}
		return fs, nil
	}

	// v2? (identities + destinations)
	var v2 struct {
		Identities   map[string]legacyIdentity `json:"identities"`
		Destinations map[string]Destination    `json:"destinations"`
	}
	if err := json.Unmarshal(data, &v2); err != nil {
		return nil, fmt.Errorf("load store %s (v2): %w", path, err)
	}
	if v2.Identities != nil || v2.Destinations != nil {
		if v2.Destinations != nil {
			fs.dests = v2.Destinations
		}
		fs.migrateIdentities(v2.Identities)
		return fs, nil
	}

	// v1: the whole file is a map[tokenHash]legacyIdentity.
	var v1 map[string]legacyIdentity
	if err := json.Unmarshal(data, &v1); err != nil {
		return nil, fmt.Errorf("load store %s (v1): %w", path, err)
	}
	fs.migrateIdentities(v1)
	return fs, nil
}

// migrateIdentities converts legacy identities into actors + synthesized roles,
// preserving each actor's effective scope. Caller sets up fs maps. Not locked
// (construction time, single goroutine).
func (fs *FileStore) migrateIdentities(legacy map[string]legacyIdentity) {
	for _, li := range legacy {
		project := li.Project
		if project == "" {
			project = DefaultProject
		}
		role := li.Role
		if role == "" {
			role = "default"
		}
		if fs.roles[project] == nil {
			fs.roles[project] = map[string]Role{}
		}
		existing, ok := fs.roles[project][role]
		if !ok {
			existing = Role{Name: role, Scope: Scope{Destinations: li.Destinations, Repos: li.Repos}}
			fs.roles[project][role] = existing
		}
		g := Grant{Project: project, Role: role}
		if !sameStrings(existing.Scope.Destinations, li.Destinations) || !sameStrings(existing.Scope.Repos, li.Repos) {
			g.Overrides = &Override{Destinations: li.Destinations, Repos: li.Repos}
		}
		fs.actors[li.TokenHash] = Actor{ID: li.ID, TokenHash: li.TokenHash, Expiry: li.Expiry, Grants: []Grant{g}}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// save persists the v3 shape. Caller holds fs.mu.
func (fs *FileStore) save() error {
	data, err := json.MarshalIndent(storeFile{Roles: fs.roles, Actors: fs.actors, Destinations: fs.dests}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fs.path, data, 0o600)
}

func (fs *FileStore) AddActor(a Actor) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, rec := range fs.actors {
		if rec.ID == a.ID {
			return fmt.Errorf("actor %q already exists", a.ID)
		}
	}
	fs.actors[a.TokenHash] = a
	return fs.save()
}

func (fs *FileStore) Lookup(tokenHash string) (Actor, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	a, ok := fs.actors[tokenHash]
	return a, ok
}

func (fs *FileStore) RemoveActor(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for h, rec := range fs.actors {
		if rec.ID == id {
			delete(fs.actors, h)
			return fs.save()
		}
	}
	return fmt.Errorf("actor %q not found", id)
}

func (fs *FileStore) ListActors() []Actor {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Actor, 0, len(fs.actors))
	for _, a := range fs.actors {
		out = append(out, a)
	}
	return out
}

func (fs *FileStore) AddGrant(actorID string, g Grant) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if g.Project == "" {
		g.Project = DefaultProject
	}
	for h, rec := range fs.actors {
		if rec.ID != actorID {
			continue
		}
		// upsert by (project, role)
		replaced := false
		for i := range rec.Grants {
			if rec.Grants[i].Project == g.Project && rec.Grants[i].Role == g.Role {
				rec.Grants[i] = g
				replaced = true
				break
			}
		}
		if !replaced {
			rec.Grants = append(rec.Grants, g)
		}
		fs.actors[h] = rec
		return fs.save()
	}
	return fmt.Errorf("actor %q not found", actorID)
}

func (fs *FileStore) RemoveGrant(actorID, project, role string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	for h, rec := range fs.actors {
		if rec.ID != actorID {
			continue
		}
		kept := rec.Grants[:0]
		found := false
		for _, g := range rec.Grants {
			if g.Project == project && g.Role == role {
				found = true
				continue
			}
			kept = append(kept, g)
		}
		if !found {
			return fmt.Errorf("actor %q has no grant %s/%s", actorID, project, role)
		}
		rec.Grants = kept
		fs.actors[h] = rec
		return fs.save()
	}
	return fmt.Errorf("actor %q not found", actorID)
}

func (fs *FileStore) PutRole(project string, r Role) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	if fs.roles[project] == nil {
		fs.roles[project] = map[string]Role{}
	}
	fs.roles[project][r.Name] = r
	return fs.save()
}

func (fs *FileStore) GetRole(project, name string) (Role, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	r, ok := fs.roles[project][name]
	return r, ok
}

func (fs *FileStore) RemoveRole(project, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	if _, ok := fs.roles[project][name]; !ok {
		return fmt.Errorf("role %q not found in project %q", name, project)
	}
	delete(fs.roles[project], name)
	return fs.save()
}

func (fs *FileStore) ListRoles(project string) []Role {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	out := make([]Role, 0, len(fs.roles[project]))
	for _, r := range fs.roles[project] {
		out = append(out, r)
	}
	return out
}

func (fs *FileStore) ListProjects() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	set := map[string]struct{}{}
	for p := range fs.roles {
		set[p] = struct{}{}
	}
	for _, a := range fs.actors {
		for _, g := range a.Grants {
			set[g.Project] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
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

func (fs *FileStore) Match(reqPath string) (Destination, bool) {
	return Config{Destinations: fs.ListDestinations()}.Match(reqPath)
}
```

- [ ] **Step 9: Update `proxy.go` to resolve grants→scopes (fail-closed)**

In `internal/harbor/proxy.go`, replace the lookup+decide block (lines ~43–57) and add a resolver method:

```go
	actor, ok := b.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	var ownerRepo string
	if dest.RepoScoped {
		ownerRepo, _ = RepoFromPath(dest.Route, r.URL.Path)
	}
	dec, err := Decide(actor, b.resolveScopes(actor), dest, ownerRepo, b.now())
	if err != nil {
		b.log.Warn("broker denied", "actor", actor.ID, "destination", dest.Name, "reason", err.Error())
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
```

Also rename the remaining `id.ID` references in the proxy log line (`"identity", id.ID` → `"actor", actor.ID`) and add:

```go
// resolveScopes turns an actor's grants into their effective scopes, skipping any
// grant whose role no longer exists (fail-closed: a deleted role stops
// authorizing). An actor with no resolvable grant yields nil → Decide denies.
func (b *Broker) resolveScopes(a Actor) []Scope {
	var scopes []Scope
	for _, g := range a.Grants {
		r, ok := b.store.GetRole(g.Project, g.Role)
		if !ok {
			continue
		}
		scopes = append(scopes, EffectiveScope(g, r))
	}
	return scopes
}
```

- [ ] **Step 10: Update `admin.go` — trim EnrollBody, role-required enroll, roster, revoke**

In `internal/harbor/admin.go`:

Replace `EnrollBody` and `IdentitySummary`:

```go
// EnrollBody is the POST /admin/enrollments request. Scope comes from the role;
// there are no inline destination/repo/ttl fields.
type EnrollBody struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	Overrides *Override `json:"overrides,omitempty"`
}

// ActorSummary is a GET /admin/roster item: never a token or hash. Each grant
// carries the effective destinations/repos after overrides.
type ActorSummary struct {
	ID     string         `json:"id"`
	Expiry time.Time      `json:"expiry"`
	Grants []GrantSummary `json:"grants"`
}

// GrantSummary is one grant with its resolved effective scope.
type GrantSummary struct {
	Project      string   `json:"project"`
	Role         string   `json:"role"`
	Destinations []string `json:"destinations"`
	Repos        []string `json:"repos"`
}
```

Replace the `GET /admin/enrollments` and `POST /admin/enrollments` handlers (the `GET` becomes `GET /admin/roster`; `DELETE /admin/enrollments/{id}` calls `RemoveActor`):

```go
	mux.HandleFunc("GET /admin/roster", func(w http.ResponseWriter, r *http.Request) {
		var out []ActorSummary
		for _, a := range store.ListActors() {
			sum := ActorSummary{ID: a.ID, Expiry: a.Expiry}
			for _, g := range a.Grants {
				gs := GrantSummary{Project: g.Project, Role: g.Role}
				if role, ok := store.GetRole(g.Project, g.Role); ok {
					s := EffectiveScope(g, role)
					gs.Destinations, gs.Repos = s.Destinations, s.Repos
				}
				sum.Grants = append(sum.Grants, gs)
			}
			out = append(out, sum)
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
		if b.Role == "" {
			http.Error(w, "role is required", http.StatusBadRequest)
			return
		}
		tok, err := Enroll(store, b.ID, b.Project, b.Role, b.Overrides, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin enrolled", "operator", operatorID(r), "id", b.ID, "project", b.Project, "role", b.Role)
		writeJSON(w, http.StatusCreated, EnrollResult{ID: b.ID, Token: tok})
	})
	mux.HandleFunc("DELETE /admin/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.RemoveActor(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin revoked", "operator", operatorID(r), "id", id)
		w.WriteHeader(http.StatusNoContent)
	})
```

(Note: a duplicate-id on enroll surfaces as 400 via `AddActor`'s error through `Enroll`. A dedicated 409 is a Task-2 refinement; leaving it 400 here keeps Task 1 minimal.)

- [ ] **Step 11: Update `adminclient.go` Enroll to the trimmed body**

In `internal/harbor/adminclient/adminclient.go`, change `Enroll` to stop sending the removed fields (keep `EnrollParams` fields for now — Task 3 removes the flags):

```go
func (c *Client) Enroll(p EnrollParams) (harbor.EnrollResult, error) {
	var res harbor.EnrollResult
	err := c.do("POST", "/admin/enrollments", harbor.EnrollBody{
		ID: p.ID, Project: p.Project, Role: p.Role,
	}, &res)
	return res, err
}
```

- [ ] **Step 12: Update the remaining harbor tests to the new types**

Update `internal/harbor/enroll_test.go`, `internal/harbor/admin_test.go`, and `internal/harbor/proxy_test.go` to construct `Actor`/`Role`/`Grant` and seed a role before enrolling. Representative changes:

- `enroll_test.go`: before calling `Enroll`, `store.PutRole("default", Role{Name:"guest", Scope: Scope{Destinations:[]string{"anthropic"}, TTL: time.Hour}})`; assert `Enroll(store, "id", "", "guest", nil, now)` succeeds, the stored actor has one grant `(default,guest)`, and `Enroll(store, "id2", "", "missing", nil, now)` errors (fail closed).
- `proxy_test.go`: where a test seeded an identity via `store.Add(Identity{...})`, replace with `store.PutRole(project, Role{...})` + `store.AddActor(Actor{ID, TokenHash: HashToken(tok), Grants: []Grant{{Project, Role}}})`. Add one case: a grant whose role is absent ⇒ request denied (fail-closed).
- `admin_test.go`: enroll requests now send `{id, role}` (seed the role first); the listing assertion moves from `GET /admin/enrollments` to `GET /admin/roster` and checks `ActorSummary.Grants[0].Destinations`.

- [ ] **Step 13: Run the whole harbor package + build**

Run: `just test` then `just build`
Expected: PASS; both binaries build. (`cmd/at-harbor` still compiles because `EnrollParams` keeps its now-ignored scope fields; `cmd/at-cove` has no harbor type dependency.)

- [ ] **Step 14: Commit**

```bash
git add internal/harbor
git commit -m "harbor: RBAC core — Actor/Role/Grant, live per-grant scope, v3 store + migration

Rename Identity → Actor (top-level identity holding Grants); add Role (owns
Scope{destinations,repos,ttl}) and Grant (the project/role join, optional
overrides). Decide becomes a pure, per-grant existential check over the actor's
effective scopes (additive across grants, no cross-grant repo-scope bleed).
FileStore gains a v3 shape (roles/actors/destinations) with a behavior-
preserving v1/v2 migration. Enrollment is role-required; the broker resolves
grants → scopes and fails closed on a missing role.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 2: Admin API — roles, grants, projects + adminclient

**Files:**
- Modify: `internal/harbor/admin.go`, `internal/harbor/admin_test.go`
- Modify: `internal/harbor/adminclient/adminclient.go`
- Create: `internal/harbor/adminclient/adminclient_test.go`

**Interfaces:**
- Consumes (from Task 1): the `Store` role/grant methods, `Role`, `Grant`, `Override`, `ActorSummary`, `GrantSummary`, `DefaultProject`.
- Produces (consumed by Task 3): `adminclient` methods
  `PutRole(project string, r harbor.Role) error`, `ListRoles(project string) ([]harbor.Role, error)`,
  `RemoveRole(project, name string) error`, `ListProjects() ([]string, error)`,
  `Roster() ([]harbor.ActorSummary, error)`, `AddGrant(actorID string, g harbor.Grant) error`,
  `RemoveGrant(actorID, project, role string) error`; plus wire types
  `RoleBody{Project, Name string; Destinations, Repos []string; TTLSeconds int64}` and
  `GrantBody{Project, Role string; Overrides *harbor.Override}`.

- [ ] **Step 1: Write failing admin route tests**

Add to `internal/harbor/admin_test.go` (reuse the package's existing test harness that builds the handler with a loopback authenticator — mirror the destination tests already there):

```go
func TestAdminRolesCRUD(t *testing.T) {
	h, _ := newTestAdmin(t) // existing helper: returns handler + store
	// create
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "guest", Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}, TTLSeconds: 3600})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /admin/roles = %d", rec.Code)
	}
	// list
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Name != "guest" || roles[0].TTLSeconds != 3600 {
		t.Fatalf("roles = %+v", roles)
	}
	// projects
	var projs []string
	getJSON(t, h, "/admin/projects", &projs)
	if len(projs) == 0 {
		t.Fatal("expected acme in projects")
	}
	// delete
	rec = doReq(t, h, "DELETE", "/admin/roles/acme/guest", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE role = %d", rec.Code)
	}
}

func TestAdminEnrollRequiresExistingRole(t *testing.T) {
	h, _ := newTestAdmin(t)
	// no role yet → 400 (fail closed)
	rec := doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "x", Role: "guest"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("enroll with missing role = %d, want 400", rec.Code)
	}
	// create the role, then enroll succeeds
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Name: "guest", Destinations: []string{"anthropic"}})
	rec = doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "x", Role: "guest"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d, want 201", rec.Code)
	}
}

func TestAdminGrantAddRemove(t *testing.T) {
	h, _ := newTestAdmin(t)
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "guest", Destinations: []string{"anthropic"}})
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "beta", Name: "review", Destinations: []string{"git"}, Repos: []string{"beta/*"}})
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "guest"})
	rec := doJSON(t, h, "POST", "/admin/actors/m/grants", GrantBody{Project: "beta", Role: "review"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add grant = %d", rec.Code)
	}
	var roster []ActorSummary
	getJSON(t, h, "/admin/roster", &roster)
	if len(roster) != 1 || len(roster[0].Grants) != 2 {
		t.Fatalf("roster = %+v", roster)
	}
	rec = doReq(t, h, "DELETE", "/admin/actors/m/grants/beta/review", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove grant = %d", rec.Code)
	}
}
```

If `newTestAdmin`/`doJSON`/`getJSON`/`doReq`/`doWithReq` helpers don't already exist in `admin_test.go`, add thin wrappers over `httptest.NewRecorder()` + `handler.ServeHTTP` (match the style the existing destination tests use — reuse those helpers verbatim if present).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/harbor/ -run 'Admin(Roles|Enroll|Grant)'`
Expected: FAIL — `RoleBody`/`RoleSummary`/`GrantBody` undefined, routes 404.

- [ ] **Step 3: Add the wire types + routes in `admin.go`**

Add types near `EnrollBody`:

```go
// RoleBody is the POST /admin/roles request.
type RoleBody struct {
	Project      string   `json:"project"`
	Name         string   `json:"name"`
	Destinations []string `json:"destinations"`
	Repos        []string `json:"repos"`
	TTLSeconds   int64    `json:"ttl_seconds"`
}

// RoleSummary is a GET /admin/roles item.
type RoleSummary struct {
	Project      string   `json:"project"`
	Name         string   `json:"name"`
	Destinations []string `json:"destinations"`
	Repos        []string `json:"repos"`
	TTLSeconds   int64    `json:"ttl_seconds"`
}

// GrantBody is the POST /admin/actors/{id}/grants request.
type GrantBody struct {
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	Overrides *Override `json:"overrides,omitempty"`
}
```

Register routes inside `NewAdminHandler` (before the `return authMiddleware(...)`):

```go
	mux.HandleFunc("GET /admin/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.ListProjects())
	})
	mux.HandleFunc("GET /admin/roles", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		var out []RoleSummary
		for _, ro := range store.ListRoles(project) {
			out = append(out, RoleSummary{
				Project: orDefaultProject(project), Name: ro.Name,
				Destinations: ro.Scope.Destinations, Repos: ro.Scope.Repos,
				TTLSeconds: int64(ro.Scope.TTL / time.Second),
			})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/roles", func(w http.ResponseWriter, r *http.Request) {
		var b RoleBody
		if !decode(w, r, &b) {
			return
		}
		if b.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		role := Role{Name: b.Name, Scope: Scope{Destinations: b.Destinations, Repos: b.Repos, TTL: time.Duration(b.TTLSeconds) * time.Second}}
		if err := store.PutRole(b.Project, role); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin role put", "operator", operatorID(r), "project", orDefaultProject(b.Project), "role", b.Name)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, name := r.PathValue("project"), r.PathValue("name")
		if err := store.RemoveRole(project, name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin role removed", "operator", operatorID(r), "project", project, "role", name)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /admin/actors/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		var b GrantBody
		if !decode(w, r, &b) {
			return
		}
		if b.Role == "" {
			http.Error(w, "role is required", http.StatusBadRequest)
			return
		}
		if err := store.AddGrant(r.PathValue("id"), Grant{Project: b.Project, Role: b.Role, Overrides: b.Overrides}); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin grant added", "operator", operatorID(r), "id", r.PathValue("id"), "project", orDefaultProject(b.Project), "role", b.Role)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("DELETE /admin/actors/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveGrant(r.PathValue("id"), r.PathValue("project"), r.PathValue("role")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin grant removed", "operator", operatorID(r), "id", r.PathValue("id"), "project", r.PathValue("project"), "role", r.PathValue("role"))
		w.WriteHeader(http.StatusNoContent)
	})
```

Add the helper (same file):

```go
func orDefaultProject(p string) string {
	if p == "" {
		return DefaultProject
	}
	return p
}
```

- [ ] **Step 4: Run admin tests to pass**

Run: `go test ./internal/harbor/ -run Admin`
Expected: PASS.

- [ ] **Step 5: Write failing adminclient tests**

Create `internal/harbor/adminclient/adminclient_test.go` with an `httptest.Server` asserting method+path+body and returning canned JSON, covering `PutRole`, `ListRoles`, `RemoveRole`, `ListProjects`, `Roster`, `AddGrant`, `RemoveGrant`, and the trimmed `Enroll` (body has no `destinations`/`repos`/`ttl_seconds`). Example shape:

```go
func TestClientRoleAndGrantRoundTrips(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch {
		case r.URL.Path == "/admin/roles" && r.Method == "GET":
			_, _ = w.Write([]byte(`[{"project":"acme","name":"guest","destinations":["anthropic"],"repos":["acme/*"],"ttl_seconds":3600}]`))
		case r.URL.Path == "/admin/projects":
			_, _ = w.Write([]byte(`["acme"]`))
		case r.URL.Path == "/admin/roster":
			_, _ = w.Write([]byte(`[{"id":"m","grants":[{"project":"acme","role":"guest","destinations":["anthropic"],"repos":["acme/*"]}]}]`))
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	if err := c.PutRole("acme", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}, TTL: time.Hour}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/admin/roles" || !strings.Contains(gotBody, `"ttl_seconds":3600`) {
		t.Fatalf("PutRole wire = %s %s %s", gotMethod, gotPath, gotBody)
	}
	roles, err := c.ListRoles("acme")
	if err != nil || len(roles) != 1 || roles[0].Scope.TTL != time.Hour {
		t.Fatalf("ListRoles = %+v, %v", roles, err)
	}
	if _, err := c.Roster(); err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if err := c.AddGrant("m", harbor.Grant{Project: "beta", Role: "review"}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if gotPath != "/admin/actors/m/grants" {
		t.Fatalf("AddGrant path = %s", gotPath)
	}
	if err := c.RemoveGrant("m", "beta", "review"); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if err := c.Enroll4Test(c); err != nil { /* placeholder; see Enroll assertion below */
	}
}
```

(Keep the Enroll assertion as a separate small test that captures the POST body and asserts it does **not** contain `destinations`/`repos`/`ttl_seconds`.)

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/harbor/adminclient/`
Expected: FAIL — methods undefined.

- [ ] **Step 7: Add adminclient methods**

Add to `internal/harbor/adminclient/adminclient.go`:

```go
// RoleBody / RoleSummary mirror the admin wire types.
func (c *Client) PutRole(project string, r harbor.Role) error {
	return c.do("POST", "/admin/roles", harbor.RoleBody{
		Project: project, Name: r.Name,
		Destinations: r.Scope.Destinations, Repos: r.Scope.Repos,
		TTLSeconds: int64(r.Scope.TTL / time.Second),
	}, nil)
}

func (c *Client) ListRoles(project string) ([]harbor.Role, error) {
	var out []harbor.RoleSummary
	path := "/admin/roles"
	if project != "" {
		path += "?project=" + url.QueryEscape(project)
	}
	if err := c.do("GET", path, nil, &out); err != nil {
		return nil, err
	}
	roles := make([]harbor.Role, 0, len(out))
	for _, rs := range out {
		roles = append(roles, harbor.Role{Name: rs.Name, Scope: harbor.Scope{
			Destinations: rs.Destinations, Repos: rs.Repos, TTL: time.Duration(rs.TTLSeconds) * time.Second,
		}})
	}
	return roles, nil
}

func (c *Client) RemoveRole(project, name string) error {
	return c.do("DELETE", "/admin/roles/"+project+"/"+name, nil, nil)
}

func (c *Client) ListProjects() ([]string, error) {
	var out []string
	err := c.do("GET", "/admin/projects", nil, &out)
	return out, err
}

func (c *Client) Roster() ([]harbor.ActorSummary, error) {
	var out []harbor.ActorSummary
	err := c.do("GET", "/admin/roster", nil, &out)
	return out, err
}

func (c *Client) AddGrant(actorID string, g harbor.Grant) error {
	return c.do("POST", "/admin/actors/"+actorID+"/grants", harbor.GrantBody{
		Project: g.Project, Role: g.Role, Overrides: g.Overrides,
	}, nil)
}

func (c *Client) RemoveGrant(actorID, project, role string) error {
	return c.do("DELETE", "/admin/actors/"+actorID+"/grants/"+project+"/"+role, nil, nil)
}
```

Add `"net/url"` to the imports. Remove the placeholder `Enroll4Test` line from the test; keep a real `Enroll`-body assertion test instead.

- [ ] **Step 8: Run adminclient + harbor tests**

Run: `go test ./internal/harbor/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go internal/harbor/adminclient
git commit -m "harbor: admin API + client for roles, grants, projects, roster

Adds GET /admin/projects, GET|POST /admin/roles + DELETE /admin/roles/{project}/{name},
POST|DELETE /admin/actors/{id}/grants[/{project}/{role}], and the adminclient
methods for each; enroll stays role-required. operator= logged on every mutation.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 3: `at-harbor` CLI — role / grant / ungrant / roster + trimmed enroll

**Files:**
- Modify: `cmd/at-harbor/main.go`
- Modify: `cmd/at-harbor/main_test.go`
- Modify: `internal/harbor/adminclient/adminclient.go` (drop now-unused `EnrollParams` scope fields)

**Interfaces:**
- Consumes (from Task 2): the adminclient `PutRole`/`ListRoles`/`RemoveRole`/`ListProjects`/`Roster`/`AddGrant`/`RemoveGrant`.

- [ ] **Step 1: Write a failing CLI test for `enroll` dropping scope flags**

In `cmd/at-harbor/main_test.go`, add a test that running `enroll` with `--destinations`/`--repos`/`--ttl` is a usage error (flag not defined) — i.e. those flags no longer exist. If the existing CLI tests shell out to a fake admin server, mirror that harness; otherwise assert via `cmdEnroll([]string{"--destinations","x", ...}, …)` returning exit code 2. Also add a `role`/`roster` dispatch smoke test if the command table is unit-tested.

```go
func TestEnrollRejectsScopeFlags(t *testing.T) {
	var out, errb bytes.Buffer
	code := cmdEnroll([]string{"--id", "x", "--role", "guest", "--destinations", "anthropic"}, cli.Globals{}, &out, &errb)
	if code == 0 {
		t.Fatalf("expected non-zero exit when --destinations is passed; got 0 (err=%q)", errb.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-harbor/ -run EnrollRejectsScopeFlags`
Expected: FAIL — the flag still parses (exit 0 / different path).

- [ ] **Step 3: Trim `cmdEnroll` flags and `EnrollParams`**

In `cmd/at-harbor/main.go` `cmdEnroll`: remove the `dests`, `repos`, and `ttl` flag registrations and their use; the `Enroll` call becomes:

```go
	res, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Enroll(adminclient.EnrollParams{
		ID: *id, Project: *project, Role: *role,
	})
```

In `internal/harbor/adminclient/adminclient.go`, reduce `EnrollParams` to:

```go
type EnrollParams struct {
	ID      string
	Project string
	Role    string
}
```

(The `Enroll` method body from Task 1 already references only `ID`/`Project`/`Role`.)

- [ ] **Step 4: Add `role`, `grant`, `ungrant`, `roster` commands**

Register them in the command table (beside `enroll`/`revoke`/`destination`):

```go
{Name: "role", Brief: "manage roles (add|list|rm) via the admin API", Run: cmdRole},
{Name: "grant", Brief: "grant a role to an actor", Run: cmdGrant},
{Name: "ungrant", Brief: "remove a role grant from an actor", Run: cmdUngrant},
{Name: "roster", Brief: "list actors and their grants", Run: cmdRoster},
```

Implement them following the `cmdDestination`/`cmdRevoke` pattern (same `--app`/`--admin-url`/`--token` handling via `firstNonEmpty(..., loadSettings(*app).AdminURL, defaultAdminURL)` and `resolveToken`):

```go
func cmdRole(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor role: expected add|list|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("role "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	project := fs.String("project", "", "project name (default: "+harbor.DefaultProject+")")
	name := fs.String("name", "", "role name")
	dests := fs.String("destinations", "", "comma-separated destination names")
	repos := fs.String("repos", "", "comma-separated owner/repo globs")
	ttl := fs.Duration("ttl", 0, "default token lifetime for actors of this role (0 = no expiry)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor role:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "add":
		if *name == "" {
			fmt.Fprintln(stderr, "at-harbor role add: --name is required")
			return 2
		}
		r := harbor.Role{Name: *name, Scope: harbor.Scope{Destinations: splitCSV(*dests), Repos: splitCSV(*repos), TTL: *ttl}}
		if err := c.PutRole(*project, r); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "added role", firstNonEmpty(*project, harbor.DefaultProject)+"/"+*name)
	case "list":
		roles, err := c.ListRoles(*project)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, r := range roles {
			fmt.Fprintf(stdout, "%s\tdests=%s\trepos=%s\tttl=%s\n", r.Name, strings.Join(r.Scope.Destinations, ","), strings.Join(r.Scope.Repos, ","), r.Scope.TTL)
		}
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor role rm: expected one role name")
			return 2
		}
		if err := c.RemoveRole(firstNonEmpty(*project, harbor.DefaultProject), pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed role", pos[0])
	default:
		fmt.Fprintln(stderr, "at-harbor role: unknown subcommand", sub)
		return 2
	}
	return 0
}

func cmdGrant(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	return grantCommon(args, stdout, stderr, false)
}
func cmdUngrant(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	return grantCommon(args, stdout, stderr, true)
}
func grantCommon(args []string, stdout, stderr io.Writer, remove bool) int {
	verb := "grant"
	if remove {
		verb = "ungrant"
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	id := fs.String("id", "", "actor id")
	project := fs.String("project", "", "project name (default: "+harbor.DefaultProject+")")
	role := fs.String("role", "", "role name")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor "+verb+":", err)
		return 2
	}
	if len(pos) > 0 || *id == "" || *role == "" {
		fmt.Fprintln(stderr, "at-harbor "+verb+": --id and --role are required")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	var err error
	if remove {
		err = c.RemoveGrant(*id, firstNonEmpty(*project, harbor.DefaultProject), *role)
	} else {
		err = c.AddGrant(*id, harbor.Grant{Project: *project, Role: *role})
	}
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	fmt.Fprintln(stdout, verb+"ed", *role, "to", *id)
	return 0
}

func cmdRoster(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("roster", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor roster:", err)
		return 2
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "at-harbor roster: takes no positional arguments")
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	roster, err := adminclient.New(adminURL, resolveToken(*app, *token, stderr)).Roster()
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	for _, a := range roster {
		for _, g := range a.Grants {
			fmt.Fprintf(stdout, "%s\t%s/%s\tdests=%s\trepos=%s\n", a.ID, g.Project, g.Role, strings.Join(g.Destinations, ","), strings.Join(g.Repos, ","))
		}
	}
	return 0
}
```

Ensure `cmd/at-harbor/main.go` imports `github.com/aethons-tools/cove/internal/harbor` (for `harbor.Role`/`Scope`/`Grant`/`DefaultProject`) — it already imports it for `harbor.RenderEnrollSnippet`/`harbor.Destination`.

- [ ] **Step 5: Run CLI tests + build**

Run: `go test ./cmd/at-harbor/ && just build`
Expected: PASS; binary builds.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-harbor internal/harbor/adminclient
git commit -m "harbor CLI: role add|list|rm, grant/ungrant, roster; enroll is role-only

enroll drops --destinations/--repos/--ttl (scope now comes from the role);
adds role/grant/ungrant/roster verbs over the admin client. EnrollParams loses
its scope fields.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 4: at-cove auto-enroll — drop scope flags

**Files:**
- Modify: `cmd/at-cove/main.go` (`harborPlan`)
- Modify: `cmd/at-cove/main_test.go` (`TestHarborPlanAutoEnroll`)

**Interfaces:**
- Consumes: nothing new; this only changes the argv passed to a sibling `at-harbor` (no harbor type import — preserves the go-oidc boundary).

- [ ] **Step 1: Update the failing argv test**

In `cmd/at-cove/main_test.go` `TestHarborPlanAutoEnroll`, change the asserted args to the trimmed set and assert the scope flags are **gone**:

```go
	joined := strings.Join(enroll.Args, " ")
	for _, want := range []string{"--json", "--id cove-box-1", "--role guest"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("enroll args missing %q: %s", want, joined)
		}
	}
	for _, absent := range []string{"--destinations", "--repos", "--ttl"} {
		if strings.Contains(joined, absent) {
			t.Fatalf("enroll args should no longer carry %q: %s", absent, joined)
		}
	}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-cove/ -run HarborPlanAutoEnroll`
Expected: FAIL — current argv still contains `--destinations`/`--ttl`/`--repos`.

- [ ] **Step 3: Trim the argv in `harborPlan`**

In `cmd/at-cove/main.go`, replace the auto-enroll args block:

```go
	args := []string{"enroll", "--json", "--id", coveID, "--role", "guest"}
```

Delete the following `if src.Project != "" { args = append(args, "--repos", src.Project) }` block. (`src` may now be unused in this function — remove its computation if the compiler flags it; keep it only if still referenced elsewhere in `harborPlan`.)

- [ ] **Step 4: Run the test + boundary check**

Run: `go test ./cmd/at-cove/ -run HarborPlanAutoEnroll`
Expected: PASS.

Run: `go list -deps ./cmd/at-cove | grep -i oidc`
Expected: empty output (boundary intact).

- [ ] **Step 5: Full build + test**

Run: `just test && just build`
Expected: PASS; both binaries build.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-cove
git commit -m "at-cove: auto-enroll is role-only (scope comes from harbor's guest role)

harborPlan's enroll argv drops --destinations/--repos/--ttl; the operator-defined
guest role now governs a cove's scope. Argv-only change — at-cove stays
go-oidc-free (verified via go list -deps).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 5: Docs

**Files:**
- Modify: `docs/usage/INDEX.md` (add a row for this spec)
- Modify: `docs/usage/at-cove-config.md` (harbor section)
- Modify: `docs/OVERVIEW.md` (only if it enumerates the `at-harbor` command surface or the broker scope model)

Per the harbor convention, command usage lives in the design spec until a dedicated leaf is warranted — so the docs task points at the spec and updates the one user-facing config caveat; it does **not** create a new usage leaf.

- [ ] **Step 1: Add the INDEX row**

In `docs/usage/INDEX.md`, add after the `harbor operator login` row:

```markdown
| [harbor actor roster + role model](../superpowers/specs/2026-09-12-harbor-actor-roster.md) | The RBAC spine: a top-level `Actor` (one identity + token) is granted `Role`s within `Project`s; a `Role` owns the security scope `{destinations, repos, ttl}`; the broker authorizes each request additively across the actor's grants (per-grant existential — no cross-grant repo bleed). Admin API/CLI: `role add\|list\|rm`, `grant`/`ungrant`, `roster`; `enroll` is role-required (scope comes from the role). Store migrates flat identities → actors+synthesized roles. | You are defining harbor roles, granting roles to actors/coves, viewing the roster, or enrolling a cove under the role model. |
```

- [ ] **Step 2: Update the `at-cove-config.md` harbor section**

In the `### harbor` section, add a note that the `guest` role is now an operator prerequisite and per-cove repo narrowing is deferred:

```markdown
> **Role prerequisite.** An auto-enrolling cove (no `harbor.identity`) enrolls into
> the `guest` role of harbor's default project; the operator must create it first,
> e.g. `at-harbor role add --name guest --destinations anthropic,git --repos
> 'aethons-tools/*' --ttl 24h`. The role's scope governs every cove that enrolls
> into it — per-cove repo narrowing is a planned follow-up, not available yet.
```

- [ ] **Step 3: Check OVERVIEW**

Run: `grep -n "at-harbor\|enroll\|broker scope\|Identity" docs/OVERVIEW.md`
If OVERVIEW enumerates the `at-harbor` verbs or describes broker scope as "per-identity", update it to mention `role`/`grant`/`roster` and the RBAC scope model. If it doesn't, leave it unchanged.

- [ ] **Step 4: Docs audit**

Run the docs health check (docs-audit skill / the repo's doc checker) to confirm no orphans or dangling links from the new INDEX row.
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add docs
git commit -m "docs: harbor actor roster + role model (COV-143 slice 1)

INDEX row for the RBAC spine spec; at-cove-config notes the guest-role
prerequisite and the deferred per-cove repo narrowing.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

## Self-Review

**Spec coverage:**
- Object model (Actor/Role/Grant/Override/Scope) → Task 1 Step 3. ✓
- Live additive, per-grant existential resolution, fail-closed → Task 1 Steps 4, 9 + tests Step 1, 12. ✓
- v3 store + behavior-preserving migration → Task 1 Steps 6–8. ✓
- Admin API (projects/roles/grants/roster, role-required enroll) → Task 1 Step 10 (enroll+roster+revoke) + Task 2 (roles/grants/projects). ✓
- adminclient → Task 2 Steps 5–7. ✓
- CLI (role/grant/ungrant/roster, trimmed enroll) → Task 3. ✓
- at-cove auto-enroll trim + boundary check → Task 4. ✓
- Docs → Task 5. ✓
- Invariants (fail-closed, no bleed, secrets, go-oidc boundary, hermetic) → Global Constraints + Task 1 Step 1 bleed test + Task 4 boundary check. ✓

**Placeholder scan:** The only placeholder is the deliberately-marked `Enroll4Test` stub in Task 2 Step 5, explicitly removed in Step 7 and replaced by a real Enroll-body assertion — called out inline, not a latent TODO.

**Type consistency:** `Decide(a Actor, scopes []Scope, dest Destination, ownerRepo string, now time.Time)`, `EffectiveScope(g Grant, r Role) Scope`, `Enroll(store, id, project, role string, overrides *Override, now)`, and the `Store` method set are used identically across Tasks 1–3. `RoleBody`/`RoleSummary`/`GrantBody`/`ActorSummary`/`GrantSummary` field names match between `admin.go` (Task 2 Step 3) and `adminclient` (Task 2 Step 7) and the CLI (Task 3 Step 4).
