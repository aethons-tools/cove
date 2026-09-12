# Harbor Kit Registry + Role→Kit Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give harbor a kit registry — named kit entries holding immutable, monotonically-numbered versions of a kit config behind a mutable "current" pointer — and let a Role reference a kit by name. Pure harbor-side (`internal/harbor` + `cmd/at-harbor`); the cove-side resolve-from-harbor path is a later slice.

**Architecture:** Extend the shipped RBAC store. `Role` gains an optional `Kit` name. A new `Kit{Name, Current, Versions map[int]string}` store collection (v4 storeFile, additive v3→v4 migration) holds versioned configs; `PushKit` mints `max+1` and advances `Current`; `PinKit` rolls `Current`. `PutRole` fails closed if a non-empty `Kit` doesn't exist; a kit referenced by a Role can't be removed (409). Admin API/CLI mirror the roles/destinations machinery; the `at-harbor kit push` CLI validates the config with `kit.ParseConfig` before sending.

**Tech Stack:** Go 1.25; `internal/harbor` stays stdlib-only (no kit dependency — stores opaque config text); `cmd/at-harbor` may import `internal/kit` (go-oidc-free) for push-time validation; `net/http` ServeMux admin API; JSON file store; `httptest` + temp-file hermetic tests.

## Global Constraints

- **Hermetic tests only** — store tests use `t.TempDir()`; admin tests drive `httptest`; CLI validation tests use in-test config fixtures. No Docker/network/VM.
- **go-oidc boundary** — all changes are harbor-side; `cmd/at-cove`, `internal/connect`, `internal/dispatchrun` are untouched this slice. `internal/harbor` does NOT import `internal/kit` (store holds opaque text); only `cmd/at-harbor` may.
- **Fail closed** — `PutRole` rejects a non-empty `Kit` that doesn't exist; `DELETE /admin/kits/{name}` returns **409** if a Role references the kit.
- **Secrets never stored** — kit configs declare secret *names/descriptions*, never values; the store persists only config text + version ints (+ the existing token hashes).
- **Versioning** — version ids are monotonic integers per kit name, minted only by `PushKit` (`max(existing)+1`); versions are immutable (never mutated/deleted in this slice); `Current` is the only mutable pointer. `KitConfig(name, 0)` means "the Current version".
- **Store back-compat** — v3 files load unchanged; `Kits` and `Role.Kit` are additive (empty/`""` when absent).
- **TDD** — failing test first, watch it fail, implement, watch it pass, commit.
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

## File Structure

- `internal/harbor/identity.go` — add `Kit` type; add `Kit string` to `Role`.
- `internal/harbor/filestore.go` — `storeFile.Kits`; `FileStore.kits`; load/save; `PushKit`/`GetKit`/`KitConfig`/`PinKit`/`ListKits`/`RemoveKit`/`RoleReferencingKit`; `PutRole` kit-existence validation; `Store` interface additions.
- `internal/harbor/admin.go` — kit wire types + routes; `RoleBody`/`RoleSummary` gain `Kit`; `POST /admin/roles` carries `Kit` with a 400 on a missing kit.
- `internal/harbor/adminclient/adminclient.go` — `PushKit`/`ListKits`/`GetKit`/`KitVersions`/`PinKit`/`RemoveKit`; `PutRole`/`ListRoles` carry `Kit`.
- `cmd/at-harbor/main.go` — `cmdKit` (push|list|show|versions|pin|rm); `cmdRole` gains `--kit`.
- `docs/` — INDEX row; OVERVIEW kit verbs.

Tests live in the matching `*_test.go` files.

---

### Task 1: Store — Kit type, v4 shape, migration, methods, Role.Kit

**Files:**
- Modify: `internal/harbor/identity.go`
- Modify: `internal/harbor/filestore.go`, `internal/harbor/filestore_test.go`

**Interfaces:**
- Produces (consumed by Tasks 2–3):
  - `type Kit struct { Name string; Current int; Versions map[int]string }`
  - `Role` gains `Kit string` (json `kit,omitempty`).
  - `Store` gains: `PushKit(name, config string) (int, error)`, `GetKit(name string) (Kit, bool)`, `KitConfig(name string, version int) (string, bool)`, `PinKit(name string, version int) error`, `ListKits() []Kit`, `RemoveKit(name string) error`, `RoleReferencingKit(name string) (project, role string, ok bool)`.
  - `PutRole` now rejects a non-empty `Role.Kit` that names no existing kit.

- [ ] **Step 1: Write failing store tests**

Append to `internal/harbor/filestore_test.go`:

```go
func TestFileStoreKitPushPinResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	v1, err := fs.PushKit("web", "name: web\nversion: one\n")
	if err != nil || v1 != 1 {
		t.Fatalf("PushKit v1 = %d, %v", v1, err)
	}
	v2, err := fs.PushKit("web", "name: web\nversion: two\n")
	if err != nil || v2 != 2 {
		t.Fatalf("PushKit v2 = %d, %v", v2, err)
	}
	// Current resolves to the latest push.
	if cfg, ok := fs.KitConfig("web", 0); !ok || cfg != "name: web\nversion: two\n" {
		t.Fatalf("Current config = %q, %v", cfg, ok)
	}
	// A specific version is addressable.
	if cfg, ok := fs.KitConfig("web", 1); !ok || cfg != "name: web\nversion: one\n" {
		t.Fatalf("v1 config = %q, %v", cfg, ok)
	}
	// Pin rolls Current back.
	if err := fs.PinKit("web", 1); err != nil {
		t.Fatalf("PinKit: %v", err)
	}
	if cfg, _ := fs.KitConfig("web", 0); cfg != "name: web\nversion: one\n" {
		t.Fatalf("after pin, Current = %q", cfg)
	}
	// Pin to an absent version fails.
	if err := fs.PinKit("web", 99); err == nil {
		t.Fatal("PinKit to absent version should fail")
	}
	if k, ok := fs.GetKit("web"); !ok || k.Current != 1 || len(k.Versions) != 2 {
		t.Fatalf("GetKit = %+v, %v", k, ok)
	}
	if err := fs.RemoveKit("nope"); err == nil {
		t.Fatal("RemoveKit of absent kit should fail")
	}
}

func TestPutRoleValidatesKitExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, _ := NewFileStore(path)
	// Binding a non-existent kit is rejected (fail closed).
	if err := fs.PutRole("acme", Role{Name: "impl", Kit: "ghost"}); err == nil {
		t.Fatal("PutRole with a non-existent kit should fail")
	}
	// After the kit exists, the binding persists and RoleReferencingKit finds it.
	if _, err := fs.PushKit("builder", "name: builder\n"); err != nil {
		t.Fatalf("PushKit: %v", err)
	}
	if err := fs.PutRole("acme", Role{Name: "impl", Kit: "builder"}); err != nil {
		t.Fatalf("PutRole with existing kit: %v", err)
	}
	if r, ok := fs.GetRole("acme", "impl"); !ok || r.Kit != "builder" {
		t.Fatalf("role.Kit = %+v, %v", r, ok)
	}
	proj, role, ok := fs.RoleReferencingKit("builder")
	if !ok || proj != "acme" || role != "impl" {
		t.Fatalf("RoleReferencingKit = %q/%q/%v", proj, role, ok)
	}
	// A role with no kit is always allowed.
	if err := fs.PutRole("acme", Role{Name: "free"}); err != nil {
		t.Fatalf("PutRole with empty kit: %v", err)
	}
}

func TestFileStoreV3LoadsWithEmptyKitRegistry(t *testing.T) {
	// A v3 file (roles/actors/destinations, no "kits" key) loads with an empty
	// kit registry and roles whose Kit is "".
	path := filepath.Join(t.TempDir(), "store.json")
	v3 := `{
	  "roles": {"acme": {"guest": {"name":"guest","scope":{"destinations":["anthropic"],"repos":null,"ttl":0}}}},
	  "actors": {},
	  "destinations": {}
	}`
	if err := os.WriteFile(path, []byte(v3), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if len(fs.ListKits()) != 0 {
		t.Fatalf("expected empty kit registry, got %d", len(fs.ListKits()))
	}
	if r, ok := fs.GetRole("acme", "guest"); !ok || r.Kit != "" {
		t.Fatalf("migrated role = %+v, %v", r, ok)
	}
}
```

Ensure `filestore_test.go` imports `os`, `path/filepath`, `testing`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/harbor/ -run 'Kit|PutRoleValidates|V3Loads'`
Expected: FAIL — `PushKit`/`KitConfig`/`PinKit`/`GetKit`/`ListKits`/`RemoveKit`/`RoleReferencingKit` undefined; `Role.Kit` undefined.

- [ ] **Step 3: Add the `Kit` type and `Role.Kit` field in `identity.go`**

Add the `Kit` type (near `Role`) and extend `Role`:

```go
// Kit is one named registry entry: immutable, monotonically-numbered versions of
// a kit config (config.yml text) behind a mutable Current pointer. A Role
// references a Kit by name; the name resolves to Current. Pushing a new version
// advances Current; pinning rolls it to an existing version.
type Kit struct {
	Name     string         `json:"name"`
	Current  int            `json:"current"`
	Versions map[int]string `json:"versions"` // version number → config.yml text (immutable)
}
```

```go
// Role is a named, reusable security class within a project.
type Role struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
	Kit   string `json:"kit,omitempty"` // optional kit name; "" = no kit
}
```

- [ ] **Step 4: Extend the store in `filestore.go`**

Add `Kits` to `storeFile` and `FileStore`, init + load + save, the kit methods, and the `PutRole` validation.

`storeFile`:
```go
type storeFile struct {
	Roles        map[string]map[string]Role `json:"roles"`
	Actors       map[string]Actor           `json:"actors"`
	Destinations map[string]Destination     `json:"destinations"`
	Kits         map[string]Kit             `json:"kits"` // keyed by Kit.Name
}
```

`FileStore` struct — add the field:
```go
	kits map[string]Kit
```

`NewFileStore` — init the map alongside the others:
```go
		kits:   map[string]Kit{},
```
and in the v3/v4 load branch (the `if v3.Actors != nil || v3.Roles != nil {` block), after the destinations load:
```go
		if v3.Kits != nil {
			fs.kits = v3.Kits
		}
```

`save()` — include kits:
```go
	data, err := json.MarshalIndent(storeFile{Roles: fs.roles, Actors: fs.actors, Destinations: fs.dests, Kits: fs.kits}, "", "  ")
```

`Store` interface — add the methods (after the role methods):
```go
	PushKit(name, config string) (int, error)
	GetKit(name string) (Kit, bool)
	KitConfig(name string, version int) (string, bool)
	PinKit(name string, version int) error
	ListKits() []Kit
	RemoveKit(name string) error
	RoleReferencingKit(name string) (project, role string, ok bool)
```

Kit methods (all lock `fs.mu`; `PushKit` mints `max(existing)+1`):
```go
func (fs *FileStore) PushKit(name, config string) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if name == "" || config == "" {
		return 0, fmt.Errorf("kit name and config are required")
	}
	k, ok := fs.kits[name]
	if !ok {
		k = Kit{Name: name, Versions: map[int]string{}}
	}
	next := 0
	for v := range k.Versions {
		if v > next {
			next = v
		}
	}
	next++
	k.Versions[next] = config
	k.Current = next
	fs.kits[name] = k
	return next, fs.save()
}

func (fs *FileStore) GetKit(name string) (Kit, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k, ok := fs.kits[name]
	return k, ok
}

func (fs *FileStore) KitConfig(name string, version int) (string, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k, ok := fs.kits[name]
	if !ok {
		return "", false
	}
	if version == 0 {
		version = k.Current
	}
	cfg, ok := k.Versions[version]
	return cfg, ok
}

func (fs *FileStore) PinKit(name string, version int) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k, ok := fs.kits[name]
	if !ok {
		return fmt.Errorf("kit %q not found", name)
	}
	if _, ok := k.Versions[version]; !ok {
		return fmt.Errorf("kit %q has no version %d", name, version)
	}
	k.Current = version
	fs.kits[name] = k
	return fs.save()
}

func (fs *FileStore) ListKits() []Kit {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Kit, 0, len(fs.kits))
	for _, k := range fs.kits {
		// copy the versions map so callers can't mutate the store
		vs := make(map[int]string, len(k.Versions))
		for v, c := range k.Versions {
			vs[v] = c
		}
		out = append(out, Kit{Name: k.Name, Current: k.Current, Versions: vs})
	}
	return out
}

func (fs *FileStore) RemoveKit(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.kits[name]; !ok {
		return fmt.Errorf("kit %q not found", name)
	}
	delete(fs.kits, name)
	return fs.save()
}

func (fs *FileStore) RoleReferencingKit(name string) (string, string, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for project, roles := range fs.roles {
		for _, r := range roles {
			if r.Kit == name {
				return project, r.Name, true
			}
		}
	}
	return "", "", false
}
```

`PutRole` — add the kit-existence check (lock-safe: read `fs.kits` directly, NOT via `GetKit`, since the mutex is non-reentrant). Insert right after the `project` defaulting, before the `fs.roles[project]` nil check:
```go
	if r.Kit != "" {
		if _, ok := fs.kits[r.Kit]; !ok {
			return fmt.Errorf("kit %q not found", r.Kit)
		}
	}
```

- [ ] **Step 5: Run the store tests to pass**

Run: `go test ./internal/harbor/ -run 'Kit|PutRoleValidates|V3Loads'`
Expected: PASS.

- [ ] **Step 6: Run the whole harbor package**

Run: `go test ./internal/harbor/...`
Expected: PASS (existing role/actor/destination tests unaffected — `Role.Kit` defaults `""`, `PutRole` validation only triggers on a non-empty Kit; any mock `Store` implementations, if present, must gain the new methods — search `_test.go` for a hand-written `Store` and extend it, else none exists).

- [ ] **Step 7: Commit**

```bash
git add internal/harbor/identity.go internal/harbor/filestore.go internal/harbor/filestore_test.go
git commit -m "harbor: kit registry store — versioned kits + Role.Kit binding

Add Kit{Name,Current,Versions} to the store (v4, additive v3→v4 migration):
PushKit mints max+1 and advances Current, PinKit rolls Current, KitConfig
resolves Current or a specific version. Role gains an optional Kit name;
PutRole fails closed if it names no existing kit. RoleReferencingKit supports
a fail-closed kit removal.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 2: Admin API + adminclient — kit routes + Role.Kit wiring

**Files:**
- Modify: `internal/harbor/admin.go`, `internal/harbor/admin_test.go`
- Modify: `internal/harbor/adminclient/adminclient.go`, `internal/harbor/adminclient/adminclient_test.go`

**Interfaces:**
- Consumes (from Task 1): the `Store` kit methods, `Kit`, `Role.Kit`.
- Produces (consumed by Task 3): wire types `KitBody{Name,Config}`, `KitResult{Name,Version}`, `KitSummary{Name,Current,Versions int}`, `KitConfigResult{Name,Version,Config}`, `PinBody{Version}`; `RoleBody`/`RoleSummary` gain `Kit string`; adminclient methods `PushKit(name,config string)(int,error)`, `ListKits()([]harbor.KitSummary,error)`, `GetKit(name string, version int)(harbor.KitConfigResult,error)`, `KitVersions(name string)([]int,error)`, `PinKit(name string, version int)error`, `RemoveKit(name string)error`.

- [ ] **Step 1: Write failing admin tests**

Add to `internal/harbor/admin_test.go` (reuse the existing `newTestAdmin`/`doJSON`/`getJSON`/`doReq` helpers — mirror the role/grant tests):

```go
func TestAdminKitsCRUD(t *testing.T) {
	h, _ := newTestAdmin(t)
	// push v1, v2
	var r1 KitResult
	decodeJSON(t, doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "name: web\nv: 1\n"}), &r1)
	if r1.Version != 1 {
		t.Fatalf("push v1 = %+v", r1)
	}
	var r2 KitResult
	decodeJSON(t, doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "name: web\nv: 2\n"}), &r2)
	if r2.Version != 2 {
		t.Fatalf("push v2 = %+v", r2)
	}
	// list
	var kits []KitSummary
	getJSON(t, h, "/admin/kits", &kits)
	if len(kits) != 1 || kits[0].Current != 2 || kits[0].Versions != 2 {
		t.Fatalf("list = %+v", kits)
	}
	// show current + specific version
	var cur KitConfigResult
	getJSON(t, h, "/admin/kits/web", &cur)
	if cur.Version != 2 || cur.Config != "name: web\nv: 2\n" {
		t.Fatalf("show current = %+v", cur)
	}
	var old KitConfigResult
	getJSON(t, h, "/admin/kits/web?version=1", &old)
	if old.Version != 1 || old.Config != "name: web\nv: 1\n" {
		t.Fatalf("show v1 = %+v", old)
	}
	// versions
	var vers []int
	getJSON(t, h, "/admin/kits/web/versions", &vers)
	if len(vers) != 2 || vers[0] != 1 || vers[1] != 2 {
		t.Fatalf("versions = %+v", vers)
	}
	// pin back to 1
	if rec := doJSON(t, h, "POST", "/admin/kits/web/pin", PinBody{Version: 1}); rec.Code != http.StatusNoContent {
		t.Fatalf("pin = %d", rec.Code)
	}
	getJSON(t, h, "/admin/kits/web", &cur)
	if cur.Version != 1 {
		t.Fatalf("after pin, current = %d", cur.Version)
	}
	// pin to absent → 404
	if rec := doJSON(t, h, "POST", "/admin/kits/web/pin", PinBody{Version: 9}); rec.Code != http.StatusNotFound {
		t.Fatalf("pin absent = %d", rec.Code)
	}
	// rm
	if rec := doReq(t, h, "DELETE", "/admin/kits/web", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("rm = %d", rec.Code)
	}
}

func TestAdminKitRemoveBlockedByRole(t *testing.T) {
	h, _ := newTestAdmin(t)
	doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "builder", Config: "name: builder\n"})
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "impl", Kit: "builder"}); rec.Code != http.StatusCreated {
		t.Fatalf("role add = %d", rec.Code)
	}
	// rm while referenced → 409
	if rec := doReq(t, h, "DELETE", "/admin/kits/builder", nil); rec.Code != http.StatusConflict {
		t.Fatalf("rm referenced kit = %d, want 409", rec.Code)
	}
}

func TestAdminRoleRejectsMissingKit(t *testing.T) {
	h, _ := newTestAdmin(t)
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Name: "impl", Kit: "ghost"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("role with missing kit = %d, want 400", rec.Code)
	}
	// roster/role summary reflects a valid kit
	doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "builder", Config: "name: builder\n"})
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "impl", Kit: "builder"})
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Kit != "builder" {
		t.Fatalf("role summary = %+v", roles)
	}
}
```

If `decodeJSON` (decode a recorder body into a struct) isn't already a helper in `admin_test.go`, add a thin one next to the existing helpers; reuse `doJSON`/`getJSON`/`doReq` as they are.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/harbor/ -run 'AdminKit|AdminRoleRejects'`
Expected: FAIL — wire types + routes undefined; `RoleBody.Kit` undefined.

- [ ] **Step 3: Add wire types + `Kit` on role types in `admin.go`**

Add near `RoleBody`:
```go
// KitBody is the POST /admin/kits request.
type KitBody struct {
	Name   string `json:"name"`
	Config string `json:"config"`
}

// KitResult is the POST /admin/kits response.
type KitResult struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// KitSummary is a GET /admin/kits item.
type KitSummary struct {
	Name     string `json:"name"`
	Current  int    `json:"current"`
	Versions int    `json:"versions"` // count
}

// KitConfigResult is a GET /admin/kits/{name} item.
type KitConfigResult struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Config  string `json:"config"`
}

// PinBody is the POST /admin/kits/{name}/pin request.
type PinBody struct {
	Version int `json:"version"`
}
```

Extend `RoleBody` and `RoleSummary` with:
```go
	Kit string `json:"kit,omitempty"`
```

- [ ] **Step 4: Wire `Kit` into the role routes**

In `POST /admin/roles`, set the kit on the role and pre-check existence for a clean 400 (the store also validates as an invariant, but map its error cleanly). Change the role construction + put:
```go
		if b.Kit != "" {
			if _, ok := store.GetKit(b.Kit); !ok {
				http.Error(w, "kit does not exist", http.StatusBadRequest)
				return
			}
		}
		role := Role{Name: b.Name, Scope: Scope{Destinations: b.Destinations, Repos: b.Repos, TTL: time.Duration(b.TTLSeconds) * time.Second}, Kit: b.Kit}
		if err := store.PutRole(b.Project, role); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
```
(The `PutRole` error status becomes 400 rather than 500 — its only non-IO error is kit validation; a genuine IO failure surfacing as 400 is acceptable and rare for a loopback file store.)

In `GET /admin/roles`, include the kit in each summary:
```go
			out = append(out, RoleSummary{
				Project: orDefaultProject(project), Name: ro.Name,
				Destinations: ro.Scope.Destinations, Repos: ro.Scope.Repos,
				TTLSeconds: int64(ro.Scope.TTL / time.Second),
				Kit:        ro.Kit,
			})
```

- [ ] **Step 5: Add the kit routes**

Inside `NewAdminHandler`, before `return authMiddleware(...)`:
```go
	mux.HandleFunc("POST /admin/kits", func(w http.ResponseWriter, r *http.Request) {
		var b KitBody
		if !decode(w, r, &b) {
			return
		}
		if b.Name == "" || b.Config == "" {
			http.Error(w, "name and config are required", http.StatusBadRequest)
			return
		}
		v, err := store.PushKit(b.Name, b.Config)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("admin kit pushed", "operator", operatorID(r), "kit", b.Name, "version", v)
		writeJSON(w, http.StatusCreated, KitResult{Name: b.Name, Version: v})
	})
	mux.HandleFunc("GET /admin/kits", func(w http.ResponseWriter, r *http.Request) {
		var out []KitSummary
		for _, k := range store.ListKits() {
			out = append(out, KitSummary{Name: k.Name, Current: k.Current, Versions: len(k.Versions)})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /admin/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		version := 0
		if q := r.URL.Query().Get("version"); q != "" {
			n, err := strconv.Atoi(q)
			if err != nil {
				http.Error(w, "version must be an integer", http.StatusBadRequest)
				return
			}
			version = n
		}
		cfg, ok := store.KitConfig(name, version)
		if !ok {
			http.Error(w, "no such kit or version", http.StatusNotFound)
			return
		}
		if version == 0 {
			k, _ := store.GetKit(name)
			version = k.Current
		}
		writeJSON(w, http.StatusOK, KitConfigResult{Name: name, Version: version, Config: cfg})
	})
	mux.HandleFunc("GET /admin/kits/{name}/versions", func(w http.ResponseWriter, r *http.Request) {
		k, ok := store.GetKit(r.PathValue("name"))
		if !ok {
			http.Error(w, "no such kit", http.StatusNotFound)
			return
		}
		vers := make([]int, 0, len(k.Versions))
		for v := range k.Versions {
			vers = append(vers, v)
		}
		sort.Ints(vers)
		writeJSON(w, http.StatusOK, vers)
	})
	mux.HandleFunc("POST /admin/kits/{name}/pin", func(w http.ResponseWriter, r *http.Request) {
		var b PinBody
		if !decode(w, r, &b) {
			return
		}
		if err := store.PinKit(r.PathValue("name"), b.Version); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin kit pinned", "operator", operatorID(r), "kit", r.PathValue("name"), "version", b.Version)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if project, role, ok := store.RoleReferencingKit(name); ok {
			http.Error(w, fmt.Sprintf("kit %q is referenced by role %s/%s", name, project, role), http.StatusConflict)
			return
		}
		if err := store.RemoveKit(name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Info("admin kit removed", "operator", operatorID(r), "kit", name)
		w.WriteHeader(http.StatusNoContent)
	})
```

Add imports to `admin.go`: `"sort"`, `"strconv"`, and `"fmt"` (if not already imported — check the existing import block and only add what's missing).

- [ ] **Step 6: Run admin tests**

Run: `go test ./internal/harbor/ -run 'AdminKit|AdminRole'`
Expected: PASS.

- [ ] **Step 7: Write failing adminclient tests**

Add to `internal/harbor/adminclient/adminclient_test.go` an `httptest.Server` test covering `PushKit` (asserts POST body `{name,config}`, returns `{name,version}`), `ListKits`, `GetKit` (with and without version — asserts the `?version=` query), `KitVersions`, `PinKit`, `RemoveKit`, and that `PutRole`/`ListRoles` carry `Kit`. Mirror the existing `TestClientRoleAndGrantRoundTrips` style (one handler switching on path, asserting method/path/body). Example core:

```go
func TestClientKitRoundTrips(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch {
		case r.URL.Path == "/admin/kits" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"name":"web","version":3}`))
		case r.URL.Path == "/admin/kits" && r.Method == "GET":
			_, _ = w.Write([]byte(`[{"name":"web","current":3,"versions":3}]`))
		case r.URL.Path == "/admin/kits/web/versions":
			_, _ = w.Write([]byte(`[1,2,3]`))
		case r.URL.Path == "/admin/kits/web":
			_, _ = w.Write([]byte(`{"name":"web","version":2,"config":"name: web\n"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	v, err := c.PushKit("web", "name: web\n")
	if err != nil || v != 3 {
		t.Fatalf("PushKit = %d, %v (body=%s)", v, err, gotBody)
	}
	if gotMethod != "POST" || gotPath != "/admin/kits" || !strings.Contains(gotBody, `"config":"name: web\n"`) {
		t.Fatalf("push wire = %s %s %s", gotMethod, gotPath, gotBody)
	}
	if kits, err := c.ListKits(); err != nil || len(kits) != 1 || kits[0].Current != 3 {
		t.Fatalf("ListKits = %+v, %v", kits, err)
	}
	if got, err := c.GetKit("web", 2); err != nil || got.Version != 2 {
		t.Fatalf("GetKit = %+v, %v", got, err)
	}
	if gotPath != "/admin/kits/web?version=2" {
		t.Fatalf("GetKit path = %s", gotPath)
	}
	if vers, err := c.KitVersions("web"); err != nil || len(vers) != 3 {
		t.Fatalf("KitVersions = %+v, %v", vers, err)
	}
	if err := c.PinKit("web", 1); err != nil {
		t.Fatalf("PinKit: %v", err)
	}
	if err := c.RemoveKit("web"); err != nil {
		t.Fatalf("RemoveKit: %v", err)
	}
}

func TestClientPutRoleCarriesKit(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	if err := New(srv.URL, "").PutRole("acme", harbor.Role{Name: "impl", Kit: "builder"}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if !strings.Contains(gotBody, `"kit":"builder"`) {
		t.Fatalf("PutRole body missing kit: %s", gotBody)
	}
}
```

- [ ] **Step 8: Run to verify failure**

Run: `go test ./internal/harbor/adminclient/`
Expected: FAIL — kit methods undefined; `RoleBody.Kit` not sent.

- [ ] **Step 9: Add adminclient methods + carry `Kit`**

In `adminclient.go` add:
```go
func (c *Client) PushKit(name, config string) (int, error) {
	var res harbor.KitResult
	err := c.do("POST", "/admin/kits", harbor.KitBody{Name: name, Config: config}, &res)
	return res.Version, err
}

func (c *Client) ListKits() ([]harbor.KitSummary, error) {
	var out []harbor.KitSummary
	err := c.do("GET", "/admin/kits", nil, &out)
	return out, err
}

func (c *Client) GetKit(name string, version int) (harbor.KitConfigResult, error) {
	var out harbor.KitConfigResult
	path := "/admin/kits/" + name
	if version > 0 {
		path += "?version=" + strconv.Itoa(version)
	}
	err := c.do("GET", path, nil, &out)
	return out, err
}

func (c *Client) KitVersions(name string) ([]int, error) {
	var out []int
	err := c.do("GET", "/admin/kits/"+name+"/versions", nil, &out)
	return out, err
}

func (c *Client) PinKit(name string, version int) error {
	return c.do("POST", "/admin/kits/"+name+"/pin", harbor.PinBody{Version: version}, nil)
}

func (c *Client) RemoveKit(name string) error {
	return c.do("DELETE", "/admin/kits/"+name, nil, nil)
}
```
Add `"strconv"` to the imports.

Carry `Kit` in `PutRole` (add `Kit: r.Kit` to the `harbor.RoleBody{...}`) and in `ListRoles` (set `Kit: rs.Kit` on each mapped `harbor.Role`):
```go
func (c *Client) PutRole(project string, r harbor.Role) error {
	return c.do("POST", "/admin/roles", harbor.RoleBody{
		Project: project, Name: r.Name,
		Destinations: r.Scope.Destinations, Repos: r.Scope.Repos,
		TTLSeconds: int64(r.Scope.TTL / time.Second),
		Kit:        r.Kit,
	}, nil)
}
```
```go
		roles = append(roles, harbor.Role{Name: rs.Name, Kit: rs.Kit, Scope: harbor.Scope{
			Destinations: rs.Destinations, Repos: rs.Repos, TTL: time.Duration(rs.TTLSeconds) * time.Second,
		}})
```

- [ ] **Step 10: Run harbor + adminclient tests**

Run: `go test ./internal/harbor/...`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go internal/harbor/adminclient
git commit -m "harbor: admin API + client for the kit registry

POST/GET /admin/kits, GET /admin/kits/{name}[?version=], .../versions,
.../pin, DELETE (409 if a role references it); RoleBody/RoleSummary carry an
optional kit, POST /admin/roles rejects a missing kit (400). adminclient gains
PushKit/ListKits/GetKit/KitVersions/PinKit/RemoveKit and carries role.Kit.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 3: `at-harbor` CLI — kit verbs + role --kit

**Files:**
- Modify: `cmd/at-harbor/main.go`, `cmd/at-harbor/main_test.go`

**Interfaces:**
- Consumes (from Task 2): adminclient `PushKit`/`ListKits`/`GetKit`/`KitVersions`/`PinKit`/`RemoveKit`; `harbor.Role.Kit`.
- Uses `internal/kit` (`kit.Load`/`kit.ParseConfig`) for push-time validation — `cmd/at-harbor` may import it (go-oidc-free).

- [ ] **Step 1: Write failing CLI tests**

In `cmd/at-harbor/main_test.go`, add a test that `kit push` rejects a malformed config (non-zero exit, and stderr mentions the parse problem) and a round-trip against a real `httptest` admin server for push→list→show→versions→pin→rm, plus `role add --kit`. Mirror the existing `TestRoleGrantUngrantRosterCommands` harness (real `harbor.NewAdminHandler` behind `httptest.NewServer`, driving `run(...)` or the `cmd*` funcs). Core:

```go
func TestKitPushRejectsMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yml")
	if err := os.WriteFile(bad, []byte("name: x\nnope_unknown_key: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := cmdKit([]string{"push", "--name", "x", "--config", bad}, cli.Globals{}, &out, &errb)
	if code == 0 {
		t.Fatalf("expected non-zero exit for a malformed kit config; stderr=%q", errb.String())
	}
}

func TestKitCommandsRoundTrip(t *testing.T) {
	// real admin handler + store behind httptest (mirror TestRoleGrantUngrantRosterCommands)
	// ... set up store, handler, srv; write a VALID minimal kit config to a temp file ...
	// cmdKit push --name web --config <valid.yml> --admin-url <srv>  → exit 0
	// cmdKit list / show / versions / pin / rm → exit 0, output reflects state
	// cmdRole add --name impl --kit web --destinations anthropic → exit 0
}
```
For the valid config fixture, the minimum `kit.ParseConfig` accepts is `name: <something>` (the only required field per `internal/kit/config.go`). Confirm by reading `ParseConfig` and use the smallest config that passes (e.g. `name: web\n`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-harbor/ -run 'Kit'`
Expected: FAIL — `cmdKit` undefined.

- [ ] **Step 3: Register and implement `cmdKit`; add `--kit` to `cmdRole`**

Add to the command table (beside `role`/`grant`/`roster`):
```go
{Name: "kit", Brief: "manage the kit registry (push|list|show|versions|pin|rm)", Run: cmdKit},
```

Implement `cmdKit` following the `cmdDestination`/`cmdRole` pattern (same `--app`/`--admin-url`/`--token` handling via `firstNonEmpty(..., loadSettings(*app).AdminURL, defaultAdminURL)` + `resolveToken`):
```go
func cmdKit(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-harbor kit: expected push|list|show|versions|pin|rm")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("kit "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "harbor admin API URL (overrides the app's settings)")
	token := fs.String("token", os.Getenv("AT_HARBOR_ADMIN_TOKEN"), "operator token (env: AT_HARBOR_ADMIN_TOKEN)")
	name := fs.String("name", "", "kit name")
	config := fs.String("config", "", "path to the kit config.yml (or - for stdin); push only")
	version := fs.Int("version", 0, "kit version (show; 0 = current)")
	pos, code, ok := cli.ParseFlags(fs, rest, stdout, stderr)
	if !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-harbor kit:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	switch sub {
	case "push":
		if *name == "" || *config == "" {
			fmt.Fprintln(stderr, "at-harbor kit push: --name and --config are required")
			return 2
		}
		data, err := readConfig(*config) // file path or "-" for stdin
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		if _, err := kit.ParseConfig(data); err != nil {
			fmt.Fprintln(stderr, "at-harbor kit push: invalid kit config:", err)
			return 1
		}
		v, err := c.PushKit(*name, string(data))
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "pushed %s v%d\n", *name, v)
	case "list":
		kits, err := c.ListKits()
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, k := range kits {
			fmt.Fprintf(stdout, "%s\tcurrent=v%d\tversions=%d\n", k.Name, k.Current, k.Versions)
		}
	case "show":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor kit show: expected one kit name")
			return 2
		}
		res, err := c.GetKit(pos[0], *version)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprint(stdout, res.Config)
	case "versions":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor kit versions: expected one kit name")
			return 2
		}
		vers, err := c.KitVersions(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		for _, v := range vers {
			fmt.Fprintf(stdout, "v%d\n", v)
		}
	case "pin":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "at-harbor kit pin: expected <name> <version>")
			return 2
		}
		v, err := strconv.Atoi(pos[1])
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor kit pin: version must be an integer")
			return 2
		}
		if err := c.PinKit(pos[0], v); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintf(stdout, "pinned %s to v%d\n", pos[0], v)
	case "rm":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "at-harbor kit rm: expected one kit name")
			return 2
		}
		if err := c.RemoveKit(pos[0]); err != nil {
			fmt.Fprintln(stderr, "at-harbor:", err)
			return 1
		}
		fmt.Fprintln(stdout, "removed kit", pos[0])
	default:
		fmt.Fprintln(stderr, "at-harbor kit: unknown subcommand", sub)
		return 2
	}
	return 0
}

// readConfig reads a config file path, or stdin when path == "-".
func readConfig(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
```

Add `--kit` to `cmdRole`'s `add` subcommand: register `kitName := fs.String("kit", "", "bind a registered kit (name)")` and set it on the role:
```go
		r := harbor.Role{Name: *name, Kit: *kitName, Scope: harbor.Scope{Destinations: splitCSV(*dests), Repos: splitCSV(*repos), TTL: *ttl}}
```

Imports for `cmd/at-harbor/main.go`: add `"strconv"` and `"github.com/aethons-tools/cove/internal/kit"` if not already present. Note `cmdKit`'s `readConfig` uses `io`/`os` (already imported).

- [ ] **Step 4: Run CLI tests + boundary check**

Run: `go test ./cmd/at-harbor/`
Expected: PASS.

Confirm at-cove still has no harbor/oidc dependency (cmd/at-harbor importing internal/kit must not leak into at-cove):
Run: `go list -deps ./cmd/at-cove | grep -i oidc`
Expected: empty.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-harbor
git commit -m "harbor CLI: kit push|list|show|versions|pin|rm; role add --kit

kit push reads a config file (or - for stdin), validates it with kit.ParseConfig
before sending, and the other verbs round-trip the registry admin API. role add
gains --kit to bind a registered kit by name.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 4: Docs

**Files:**
- Modify: `docs/usage/INDEX.md`
- Modify: `docs/OVERVIEW.md` (only if it enumerates the `at-harbor` verbs / the harbor command surface)

Follow the repo's progressive-disclosure conventions: `INDEX.md` is a map (one row per doc, no prose); the harbor convention is that a slice's row points at its design spec (no new usage leaf). Use the docs-author/docs-audit skills if available.

- [ ] **Step 1: Add the INDEX row**

In `docs/usage/INDEX.md`, after the `harbor actor roster + role model` row:
```markdown
| [harbor kit registry](../superpowers/specs/2026-09-12-harbor-kit-registry.md) | Harbor stores named kit definitions as immutable, monotonically-numbered versions behind a mutable current pointer; a Role references a kit by name (resolves to current). Admin API/CLI: `kit push\|list\|show\|versions\|pin\|rm`, `role add --kit`; `kit push` validates the config before sending; a kit referenced by a role can't be removed. Harbor-side only — a cove resolving its kit from harbor is a later slice. | You are registering/versioning kits in harbor, binding a kit to a role, or pinning/rolling a kit version. |
```

- [ ] **Step 2: Update OVERVIEW if it enumerates the at-harbor verbs**

Run: `grep -n "at-harbor\|role/grant\|roster\|kit" docs/OVERVIEW.md`
If OVERVIEW lists the `at-harbor` verbs (it was updated for COV-143 to mention `role`/`grant`/`ungrant`/`roster`), add `kit` and the Role→Kit binding. If it doesn't enumerate them, leave it unchanged and say so.

- [ ] **Step 3: Docs health check**

Run the docs-audit checker (or confirm the new INDEX link resolves): `ls docs/superpowers/specs/2026-09-12-harbor-kit-registry.md`. Confirm no new errors vs. the pre-existing baseline.

- [ ] **Step 4: Commit**

```bash
git add docs
git commit -m "docs: harbor kit registry (COV-144 slice 1)

INDEX row for the kit-registry spec; OVERVIEW gains the kit verbs + Role→Kit
binding where the at-harbor command surface is enumerated.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

## Self-Review

**Spec coverage:**
- Kit type + immutable monotonic versions + Current pointer → Task 1 Steps 3–4. ✓
- Role.Kit (by name) + fail-closed PutRole validation → Task 1 Step 4 + tests Step 1. ✓
- v4 store + additive v3→v4 migration → Task 1 Step 4 + `TestFileStoreV3LoadsWithEmptyKitRegistry`. ✓
- Admin API (push/list/show/versions/pin/rm + 409-on-referenced + role kit + 400-on-missing-kit) → Task 2. ✓
- adminclient → Task 2 Steps 7–9. ✓
- CLI (kit verbs + config validation + role --kit) → Task 3. ✓
- Docs → Task 4. ✓
- Invariants (fail-closed, secrets, go-oidc boundary, back-compat, hermetic) → Global Constraints + Task 1/2 tests + Task 3 Step 4 boundary check. ✓

**Placeholder scan:** Task 3 Step 1's `TestKitCommandsRoundTrip` body is sketched (prose) rather than full code, because it must mirror the repo's existing `TestRoleGrantUngrantRosterCommands` harness, which the implementer reads in-place; the RED test that fully blocks the task (`TestKitPushRejectsMalformedConfig`) is complete. This is intentional (follow the existing harness), not an un-filled requirement — the malformed-config RED test is concrete and the round-trip mirrors a named existing test.

**Type/signature consistency:** `Kit{Name,Current,Versions}`, `KitConfig(name, version)` (0 = Current), `PushKit(name,config)(int,error)`, `PinKit(name,version)`, the wire types (`KitBody`/`KitResult`/`KitSummary`/`KitConfigResult`/`PinBody`), and the adminclient methods are used identically across Tasks 1–3. `Role.Kit` (json `kit,omitempty`) is consistent in `identity.go`, `RoleBody`/`RoleSummary`, and the adminclient.
