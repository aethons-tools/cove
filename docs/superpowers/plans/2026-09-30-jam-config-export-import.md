# Jam config export/import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `at-jam export` and `at-jam import` commands that back up and restore the jam control-plane config aggregates (actors incl. token hashes, roles, kits, destinations, projects) as a single file, excluding studio (instances) and intercom (unread/squawk) state.

**Architecture:** Server-side bulk endpoints (`GET`/`POST /admin/config`) so token hashes round-trip (only the server can read/write them). A new `ConfigSnapshot` type plus two `Store` methods (`ExportConfig`/`ImportConfig`) own the config/state split; import is fail-closed (refuses unless the target config is empty). The CLI streams the snapshot over the (always-JSON) wire and converts to/from YAML only at the file boundary.

**Tech Stack:** Go (stdlib + `gopkg.in/yaml.v3` for the CLI file boundary + `jackc/pgx/v5` for the Postgres transaction). Tests are hermetic (`FileStore`, `httptest`); Postgres behavior is behind the `integration` build tag.

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Package for store/handler code: `internal/jam`; CLI: `cmd/at-jam` (package `main`); client: `internal/jam/adminclient`.
- **Config aggregates (in scope):** `actors`, `roles`, `kits`, `destinations`, `projects`. **Excluded state:** `instances`, `intercom_unread_cursors`, and the separate `internal/intercom/intercompg` + `internal/allocator/allocpg` stores. Never read or write the excluded state in export/import.
- **Token hashes are auth material** — the snapshot intentionally carries them; **never log snapshot contents** at any level (log counts/aggregate names only), per `docs/usage/observability.md`.
- **Wire is JSON only.** YAML exists solely at the CLI file boundary. Default file format is **JSON**; `--format yaml` opts in.
- **Import is fail-closed:** refuse (no writes) if *any* config aggregate has ≥1 entry. Snapshot `Version` must equal `ConfigSnapshotVersion` or import is rejected.
- **TDD, hermetic by default.** Write the failing test first. Real-Postgres tests go behind `//go:build integration`.
- **Tests are hermetic** — drive `FileStore`/`httptest`; no Docker/network/live VM.
- **Docs updated in the same change** (repo policy): route to `docs/usage/jam/` (new leaf `backup.md`) + its `INDEX.md`.
- **Commit attribution** — end every commit message with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```
- **Build/test:** `just test` (hermetic), `just build`, `just lint`. Toolchain env for `go` in the sandbox is in `docs/DEVELOPMENT.md` (`GOPROXY`/`GOPATH`); prefer `just` recipes.

---

## File Structure

- **Create** `internal/jam/config_snapshot.go` — the `ConfigSnapshot` type + `ConfigSnapshotVersion`, sentinel errors, the `memState` `ExportConfig`, and the shared import helpers (`checkImport`, `applyImport`, `deepCopySnapshot`, `nonEmptyConfigAggregates`).
- **Create** `internal/jam/config_snapshot_test.go` — hermetic tests for `ExportConfig` (excludes state, deterministic, deep-copied) and `FileStore.ImportConfig` (round-trip fidelity, refuse-unless-empty, version check, `Lookup` after import).
- **Modify** `internal/jam/filestore.go` — add `FileStore.ImportConfig`; add `ExportConfig`/`ImportConfig` to the `Store` interface.
- **Modify** `internal/jam/pgstore.go` — add `PostgresStore.ImportConfig` (single `pgx` transaction).
- **Create** `internal/jam/pgstore_importconfig_integration_test.go` — `//go:build integration`; transactional restore + atomic rollback.
- **Modify** `internal/jam/admin.go` — add `GET`/`POST /admin/config` handlers; add `"errors"` to imports.
- **Modify** `internal/jam/admin_test.go` — handler tests (GET snapshot, POST 204/409/400, auth).
- **Modify** `internal/jam/adminclient/adminclient.go` — add `ErrConflict` + 409 mapping in `do`; add `ExportConfig`/`ImportConfig`.
- **Modify** `internal/jam/adminclient/adminclient_test.go` (create if absent) — client round-trip + 409 mapping.
- **Create** `cmd/at-jam/backup.go` — `cmdExport`/`cmdImport` + format helpers.
- **Create** `cmd/at-jam/backup_test.go` — CLI end-to-end via `httptest` + `FileStore`.
- **Modify** `cmd/at-jam/main.go` — register the two commands.
- **Create** `docs/usage/jam/backup.md`; **Modify** `docs/usage/jam/INDEX.md`.

---

## Task 1: `ConfigSnapshot` type + `memState.ExportConfig`

**Files:**
- Create: `internal/jam/config_snapshot.go`
- Create: `internal/jam/config_snapshot_test.go`
- Modify: `internal/jam/filestore.go` (add `ExportConfig()` to the `Store` interface, after the destination methods block near line 49)

**Interfaces:**
- Produces:
  - `type ConfigSnapshot struct { Version int; ExportedAt time.Time; Actors []Actor; Roles map[string]map[string]Role; Kits []Kit; Destinations []Destination; Projects []Project }`
  - `const ConfigSnapshotVersion = 1`
  - `func (m *memState) ExportConfig() ConfigSnapshot`
  - `Store` interface gains `ExportConfig() ConfigSnapshot` (satisfied by both stores via the embedded `*memState`).

- [ ] **Step 1: Write the failing test**

Create `internal/jam/config_snapshot_test.go`:

```go
package jam

import (
	"path/filepath"
	"testing"
)

// populated returns a FileStore with one entry in every config aggregate AND in
// the excluded state (an instance + an unread cursor), so a test can assert the
// export includes config and excludes state.
func populated(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRole(DefaultProject, Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddActor(Actor{ID: "spider-18", TokenHash: HashToken("tok"), Grants: []Grant{{Project: DefaultProject, Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushKit("base", "image: x"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDestination(Destination{Name: "anthropic", Route: "/v1", Upstream: "https://api"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHuman(DefaultProject, Human{Name: "alice", Handle: "@alice"}); err != nil {
		t.Fatal(err)
	}
	// excluded state:
	if err := s.PutInstance(Instance{ActorID: "spider-18"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitUnread("alice", "eng", 7); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExportConfigIncludesConfigExcludesState(t *testing.T) {
	s := populated(t)
	snap := s.ExportConfig()

	if snap.Version != ConfigSnapshotVersion {
		t.Fatalf("Version = %d, want %d", snap.Version, ConfigSnapshotVersion)
	}
	if len(snap.Actors) != 1 || snap.Actors[0].ID != "spider-18" {
		t.Fatalf("actors = %+v", snap.Actors)
	}
	if snap.Actors[0].TokenHash != HashToken("tok") {
		t.Fatalf("token hash not exported: %q", snap.Actors[0].TokenHash)
	}
	if _, ok := snap.Roles[DefaultProject]["guest"]; !ok {
		t.Fatalf("roles = %+v", snap.Roles)
	}
	if len(snap.Kits) != 1 || snap.Kits[0].Versions[1] != "image: x" {
		t.Fatalf("kits = %+v", snap.Kits)
	}
	if len(snap.Destinations) != 1 || len(snap.Projects) != 1 {
		t.Fatalf("dests=%+v projects=%+v", snap.Destinations, snap.Projects)
	}
	// ExportConfig has no field for instances or unread cursors — their absence
	// from the type is the exclusion guarantee; this test documents it.
}

func TestExportConfigIsDeepCopied(t *testing.T) {
	s := populated(t)
	snap := s.ExportConfig()
	// Mutating the snapshot must not reach the store.
	snap.Roles[DefaultProject]["guest"] = Role{Name: "hacked"}
	snap.Kits[0].Versions[1] = "tampered"
	if r, _ := s.GetRole(DefaultProject, "guest"); r.Name != "guest" {
		t.Fatalf("store role mutated via snapshot: %+v", r)
	}
	if cfg, _ := s.KitConfig("base", 1); cfg != "image: x" {
		t.Fatalf("store kit mutated via snapshot: %q", cfg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `just test 2>/dev/null || go test ./internal/jam/ -run TestExportConfig -v`
Expected: FAIL — `ExportConfig` / `ConfigSnapshot` / `ConfigSnapshotVersion` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/jam/config_snapshot.go`:

```go
package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ConfigSnapshotVersion is the on-the-wire/on-disk schema version of a config
// backup. Import rejects any other value rather than mis-loading.
const ConfigSnapshotVersion = 1

// ConfigSnapshot is a backup of the jam control-plane CONFIG aggregates only:
// actors (with their token hashes and grants), roles, kits (all versions + the
// pin), destinations, and projects (roster, escalation, chat service). It never
// carries runtime/studio state (instances) or intercom unread cursors — those
// aggregates simply have no field here.
type ConfigSnapshot struct {
	Version      int                        `json:"version"`
	ExportedAt   time.Time                  `json:"exported_at"`
	Actors       []Actor                    `json:"actors"`
	Roles        map[string]map[string]Role `json:"roles"` // project → name → Role
	Kits         []Kit                      `json:"kits"`  // full: Current + all Versions
	Destinations []Destination              `json:"destinations"`
	Projects     []Project                  `json:"projects"`
}

// ErrConfigNotEmpty is returned by ImportConfig when the target already holds
// config; import is fail-closed and writes nothing in that case.
var ErrConfigNotEmpty = errors.New("import refused: target config is not empty")

// ErrUnsupportedConfigVersion is returned by ImportConfig for a snapshot whose
// Version this build does not understand.
var ErrUnsupportedConfigVersion = errors.New("unsupported config snapshot version")

// ExportConfig returns a deep copy of the five config aggregates, sorted for a
// stable/diffable backup. Runtime state (instances, unread cursors) is never
// read. Safe under the read lock; the returned snapshot shares nothing with the
// live store.
func (m *memState) ExportConfig() ConfigSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := ConfigSnapshot{Version: ConfigSnapshotVersion, ExportedAt: time.Now().UTC()}
	for _, a := range m.actors {
		snap.Actors = append(snap.Actors, a)
	}
	sort.Slice(snap.Actors, func(i, j int) bool { return snap.Actors[i].ID < snap.Actors[j].ID })

	snap.Roles = map[string]map[string]Role{}
	for p, rs := range m.roles {
		if len(rs) == 0 {
			continue
		}
		inner := make(map[string]Role, len(rs))
		for n, r := range rs {
			inner[n] = r
		}
		snap.Roles[p] = inner
	}

	for _, k := range m.kits {
		snap.Kits = append(snap.Kits, k)
	}
	sort.Slice(snap.Kits, func(i, j int) bool { return snap.Kits[i].Name < snap.Kits[j].Name })

	for _, d := range m.dests {
		snap.Destinations = append(snap.Destinations, d)
	}
	sort.Slice(snap.Destinations, func(i, j int) bool { return snap.Destinations[i].Name < snap.Destinations[j].Name })

	for name, p := range m.projects {
		p.Name = name
		snap.Projects = append(snap.Projects, p)
	}
	sort.Slice(snap.Projects, func(i, j int) bool { return snap.Projects[i].Name < snap.Projects[j].Name })

	return deepCopySnapshot(snap)
}

// deepCopySnapshot returns a copy sharing no maps/slices/pointers with s, via a
// JSON round-trip. The config structs are exactly what the store persists as
// JSON docs, so marshaling cannot fail in practice; a failure yields the zero
// snapshot rather than an aliased one.
func deepCopySnapshot(s ConfigSnapshot) ConfigSnapshot {
	b, err := json.Marshal(s)
	if err != nil {
		return ConfigSnapshot{Version: s.Version}
	}
	var out ConfigSnapshot
	if err := json.Unmarshal(b, &out); err != nil {
		return ConfigSnapshot{Version: s.Version}
	}
	return out
}

// checkImport validates a snapshot against an empty target. Caller holds the
// write lock. Returns ErrUnsupportedConfigVersion for a bad version, or
// ErrConfigNotEmpty (naming the offending aggregates) if any config aggregate
// already has entries.
func checkImport(m *memState, s ConfigSnapshot) error {
	if s.Version != ConfigSnapshotVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedConfigVersion, s.Version, ConfigSnapshotVersion)
	}
	if names := nonEmptyConfigAggregates(m); len(names) > 0 {
		return fmt.Errorf("%w (non-empty: %s)", ErrConfigNotEmpty, strings.Join(names, ", "))
	}
	return nil
}

// nonEmptyConfigAggregates names the config aggregates that currently hold
// entries. Caller holds at least the read lock.
func nonEmptyConfigAggregates(m *memState) []string {
	var names []string
	if len(m.actors) > 0 {
		names = append(names, "actors")
	}
	roleCount := 0
	for _, rs := range m.roles {
		roleCount += len(rs)
	}
	if roleCount > 0 {
		names = append(names, "roles")
	}
	if len(m.kits) > 0 {
		names = append(names, "kits")
	}
	if len(m.dests) > 0 {
		names = append(names, "destinations")
	}
	if len(m.projects) > 0 {
		names = append(names, "projects")
	}
	return names
}

// applyImport overwrites the config maps from a (deep-copied) snapshot. Caller
// holds the write lock and has already validated with checkImport. State maps
// (instances, unread) are untouched.
func applyImport(m *memState, s ConfigSnapshot) {
	s = deepCopySnapshot(s)
	m.actors = map[string]Actor{}
	for _, a := range s.Actors {
		m.actors[a.TokenHash] = a
	}
	m.roles = map[string]map[string]Role{}
	for p, rs := range s.Roles {
		inner := map[string]Role{}
		for n, r := range rs {
			inner[n] = r
		}
		m.roles[p] = inner
	}
	m.kits = map[string]Kit{}
	for _, k := range s.Kits {
		m.kits[k.Name] = k
	}
	m.dests = map[string]Destination{}
	for _, d := range s.Destinations {
		m.dests[d.Name] = d
	}
	m.projects = map[string]Project{}
	for _, p := range s.Projects {
		m.projects[p.Name] = p
	}
}
```

Add `ExportConfig` to the `Store` interface in `internal/jam/filestore.go`. Find the destination block (around line 49):

```go
	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)
```

and insert immediately after `Match(...)`:

```go
	// ExportConfig snapshots the config aggregates (actors, roles, kits,
	// destinations, projects); it never reads instances or unread cursors.
	ExportConfig() ConfigSnapshot
	// ImportConfig restores a snapshot into an EMPTY store, fail-closed: it
	// returns ErrConfigNotEmpty (writing nothing) if any config aggregate has
	// entries, or ErrUnsupportedConfigVersion for a bad version.
	ImportConfig(s ConfigSnapshot) error
```

(Both methods are declared now; `ExportConfig` is implemented on `memState` in this task, `ImportConfig` on each store in Tasks 2–3. The `var _ Store` assertions will fail to compile until Task 2 adds `FileStore.ImportConfig` and Task 3 adds the Postgres one — so build only `go test ./internal/jam/ -run TestExportConfig` won't link until Task 2. To keep this task independently green, temporarily add `ImportConfig` stubs in this step; Task 2 replaces the FileStore stub with the real one.)

Add these temporary stubs at the end of `internal/jam/config_snapshot.go`:

```go
// ImportConfig stubs — replaced with real implementations in later tasks.
// (Present so the Store interface compiles the moment it declares the method.)
func (fs *FileStore) ImportConfig(s ConfigSnapshot) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := checkImport(fs.memState, s); err != nil {
		return err
	}
	applyImport(fs.memState, s)
	return fs.save()
}
```

> Note: this "stub" is already the full FileStore implementation — Task 2 only adds its tests. There is no separate Postgres stub here because `PostgresStore` is compiled only when its file is; add the Postgres method in Task 3. If `go vet ./internal/jam/` complains that `*PostgresStore` no longer satisfies `Store`, that is expected until Task 3; run Task 1's test with `go test ./internal/jam/ -run 'TestExportConfig|TestImportConfig'` which still compiles the package (the `var _ Store = (*PostgresStore)(nil)` assertion in `pgstore.go` WILL break the package build). **Therefore: do Task 1, 2, and 3's implementation edits before running the package test.** See Step 2 caveat — if the package won't compile, proceed to Task 3's Step 3 first, then run all three tasks' tests together.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/jam/ -run 'TestExportConfig' -v`
Expected: PASS (after Task 3's `PostgresStore.ImportConfig` exists, so the package links). If the package fails to link on `*PostgresStore` not satisfying `Store`, complete Task 3 Step 3 first.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/config_snapshot.go internal/jam/config_snapshot_test.go internal/jam/filestore.go
git commit -m "$(cat <<'EOF'
feat(jam): ConfigSnapshot + memState.ExportConfig (config-only backup)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Task 2: `FileStore.ImportConfig` fidelity + refuse-unless-empty tests

**Files:**
- Modify: `internal/jam/config_snapshot_test.go` (add tests)
- (Implementation already added in Task 1 as `FileStore.ImportConfig`.)

**Interfaces:**
- Consumes: `FileStore.ImportConfig(ConfigSnapshot) error`, `ExportConfig() ConfigSnapshot`, `ErrConfigNotEmpty`, `ErrUnsupportedConfigVersion` (Task 1).

- [ ] **Step 1: Write the failing test**

Append to `internal/jam/config_snapshot_test.go`:

```go
import "errors" // add to the existing import block if not present

func TestImportConfigRoundTripFidelity(t *testing.T) {
	src := populated(t)
	snap := src.ExportConfig()

	dst, err := NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.ImportConfig(snap); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}

	// Re-export and compare: config round-trips exactly.
	got := dst.ExportConfig()
	got.ExportedAt = snap.ExportedAt // timestamps differ; not part of fidelity
	if a, b := mustJSON(t, got), mustJSON(t, snap); a != b {
		t.Fatalf("round-trip mismatch:\n got=%s\nwant=%s", a, b)
	}

	// The restored token hash still resolves the actor.
	if a, ok := dst.Lookup(HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("Lookup after import = %+v, ok=%v", a, ok)
	}
}

func TestImportConfigRefusesWhenNotEmpty(t *testing.T) {
	snap := populated(t).ExportConfig()

	cases := map[string]func(*FileStore){
		"actors":       func(s *FileStore) { _ = s.AddActor(Actor{ID: "x", TokenHash: "hx"}) },
		"roles":        func(s *FileStore) { _ = s.PutRole(DefaultProject, Role{Name: "r"}) },
		"kits":         func(s *FileStore) { _, _ = s.PushKit("k", "cfg") },
		"destinations": func(s *FileStore) { _ = s.AddDestination(Destination{Name: "d"}) },
		"projects":     func(s *FileStore) { _ = s.AddHuman(DefaultProject, Human{Name: "h"}) },
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			dst, err := NewFileStore(filepath.Join(t.TempDir(), "d.json"))
			if err != nil {
				t.Fatal(err)
			}
			seed(dst)
			err = dst.ImportConfig(snap)
			if !errors.Is(err, ErrConfigNotEmpty) {
				t.Fatalf("err = %v, want ErrConfigNotEmpty", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error should name %q: %v", name, err)
			}
		})
	}
}

func TestImportConfigRejectsBadVersion(t *testing.T) {
	dst, err := NewFileStore(filepath.Join(t.TempDir(), "d.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.ImportConfig(ConfigSnapshot{Version: 999}); !errors.Is(err, ErrUnsupportedConfigVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedConfigVersion", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
```

Add `"encoding/json"` and `"strings"` to the test file's import block if missing.

- [ ] **Step 2: Run test to verify it fails, then passes**

Run: `go test ./internal/jam/ -run 'TestImportConfig' -v`
Expected: initially may fail to compile if Task 3 not done (see Task 1 note); once the package links, these PASS (the implementation exists from Task 1).

- [ ] **Step 3: Commit**

```bash
git add internal/jam/config_snapshot_test.go
git commit -m "$(cat <<'EOF'
test(jam): FileStore.ImportConfig fidelity + fail-closed on non-empty/bad version

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Task 3: `PostgresStore.ImportConfig` (transactional)

**Files:**
- Modify: `internal/jam/pgstore.go` (add the method; remove the FileStore-only stub note — the FileStore impl stays in `config_snapshot.go`)
- Create: `internal/jam/pgstore_importconfig_integration_test.go`

**Interfaces:**
- Consumes: `checkImport`, `applyImport` (Task 1); `pgx.BeginFunc` (already imported in `pgstore.go`).
- Produces: `func (s *PostgresStore) ImportConfig(snap ConfigSnapshot) error`.

- [ ] **Step 1: Write the implementation** (Postgres is integration-only; write impl first so the package links, then the gated test)

Add to `internal/jam/pgstore.go` (near the other mutators; `context`, `encoding/json`, `fmt`, and the `pgx` import are already present):

```go
// ImportConfig restores a config snapshot into an empty Postgres store in a
// single transaction: either every aggregate row is inserted or none is. It is
// fail-closed (checkImport) and never touches instances or unread cursors.
func (s *PostgresStore) ImportConfig(snap ConfigSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkImport(s.memState, snap); err != nil {
		return err
	}
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, a := range snap.Actors {
			doc, err := json.Marshal(a)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO actors (token_hash, id, doc) VALUES ($1,$2,$3)`, a.TokenHash, a.ID, doc); err != nil {
				return err
			}
		}
		for project, rs := range snap.Roles {
			for _, r := range rs {
				doc, err := json.Marshal(r)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO roles (project, name, doc) VALUES ($1,$2,$3)`, project, r.Name, doc); err != nil {
					return err
				}
			}
		}
		for _, k := range snap.Kits {
			doc, err := json.Marshal(k)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO kits (name, doc) VALUES ($1,$2)`, k.Name, doc); err != nil {
				return err
			}
		}
		for _, d := range snap.Destinations {
			doc, err := json.Marshal(d)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO destinations (name, doc) VALUES ($1,$2)`, d.Name, doc); err != nil {
				return err
			}
		}
		for _, p := range snap.Projects {
			doc, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO projects (name, doc) VALUES ($1,$2)`, p.Name, doc); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("pgstore: ImportConfig: %w", err)
	}
	applyImport(s.memState, snap)
	return nil
}
```

- [ ] **Step 2: Verify the whole package compiles**

Run: `go build ./internal/jam/`
Expected: builds — both `var _ Store` assertions now satisfied. Now run the Task 1 + 2 tests:
Run: `go test ./internal/jam/ -run 'TestExportConfig|TestImportConfig' -v`
Expected: PASS.

- [ ] **Step 3: Write the integration test**

Create `internal/jam/pgstore_importconfig_integration_test.go`:

```go
//go:build integration

package jam

import (
	"context"
	"errors"
	"testing"
)

// newTestPGStore mirrors the existing pgstore integration test setup. If the
// repo already has a helper (e.g. in pgstore_integration_test.go), call that
// instead of duplicating; this signature documents the contract needed here.
func newTestPGStore(t *testing.T) *PostgresStore // provided by the existing integration harness

func TestPGImportConfigRoundTrip(t *testing.T) {
	src := populated(t) // FileStore helper from config_snapshot_test.go
	snap := src.ExportConfig()

	pg := newTestPGStore(t)
	if err := pg.ImportConfig(snap); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	if a, ok := pg.Lookup(HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("Lookup after import = %+v ok=%v", a, ok)
	}
	// durable across reload
	if err := pg.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := pg.GetRole(DefaultProject, "guest"); !ok {
		t.Fatal("role not durable after reload")
	}
}

func TestPGImportConfigRefusesWhenNotEmpty(t *testing.T) {
	snap := populated(t).ExportConfig()
	pg := newTestPGStore(t)
	if err := pg.AddDestination(Destination{Name: "d", Route: "/r", Upstream: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := pg.ImportConfig(snap); !errors.Is(err, ErrConfigNotEmpty) {
		t.Fatalf("err = %v, want ErrConfigNotEmpty", err)
	}
}
```

> Before writing, open the existing `internal/jam/*_integration_test.go` (or `internal/jam/pgstore_test.go`) to reuse its real Postgres bootstrap helper name/signature; replace the `newTestPGStore` forward declaration above with the actual helper. Do not invent a second DB harness.

- [ ] **Step 4: Run integration test (only where Postgres is available)**

Run: `just integration 2>/dev/null || go test -tags integration ./internal/jam/ -run 'TestPGImportConfig' -v`
Expected: PASS where a test Postgres is available; SKIP/te setup-gated otherwise. Do not block the plan on an unavailable DB — the hermetic path is covered by Tasks 1–2.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/pgstore.go internal/jam/pgstore_importconfig_integration_test.go
git commit -m "$(cat <<'EOF'
feat(jam): PostgresStore.ImportConfig — transactional config restore

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Task 4: Admin endpoints `GET`/`POST /admin/config`

**Files:**
- Modify: `internal/jam/admin.go` (add `"errors"` import; add two routes inside `NewAdminHandler`, e.g. after the destinations routes ~line 335)
- Modify: `internal/jam/admin_test.go`

**Interfaces:**
- Consumes: `store.ExportConfig()`, `store.ImportConfig()`, `ErrConfigNotEmpty`, `ErrUnsupportedConfigVersion`, `writeJSON`, `decode`, `OperatorID` (all existing).
- Produces: HTTP routes `GET /admin/config` (200 + `ConfigSnapshot` JSON), `POST /admin/config` (204 / 409 / 400).

- [ ] **Step 1: Write the failing test**

Add to `internal/jam/admin_test.go`:

```go
func TestAdminConfigExportImport(t *testing.T) {
	src := populated(t) // helper from config_snapshot_test.go (same package)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srcH := NewAdminHandler(src, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil)
	srcTS := httptest.NewServer(srcH)
	defer srcTS.Close()

	// GET export
	resp, err := http.Get(srcTS.URL + "/admin/config")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// POST import into a fresh store → 204
	dst, _ := NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	dstH := NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil)
	dstTS := httptest.NewServer(dstH)
	defer dstTS.Close()

	post := func(payload []byte) int {
		r, err := http.Post(dstTS.URL+"/admin/config", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if code := post(body); code != http.StatusNoContent {
		t.Fatalf("POST import status = %d, want 204", code)
	}
	if a, ok := dst.Lookup(HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("import did not restore actor: %+v ok=%v", a, ok)
	}
	// second import → 409 (not empty)
	if code := post(body); code != http.StatusConflict {
		t.Fatalf("second POST status = %d, want 409", code)
	}
}

func TestAdminConfigImportBadVersion(t *testing.T) {
	dst, _ := NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil))
	defer ts.Close()
	r, err := http.Post(ts.URL+"/admin/config", "application/json", strings.NewReader(`{"version":999}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}
```

Ensure `admin_test.go` imports `bytes`, `io`, `net/http`, `net/http/httptest`, `path/filepath`, `strings`, `log/slog` (most already present — add any missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/jam/ -run 'TestAdminConfig' -v`
Expected: FAIL — routes 404 (handlers not registered yet).

- [ ] **Step 3: Write minimal implementation**

In `internal/jam/admin.go`, add `"errors"` to the import block:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	...
)
```

Inside `NewAdminHandler`, after the `DELETE /admin/destinations/{name}` handler, add:

```go
	mux.HandleFunc("GET /admin/config", func(w http.ResponseWriter, r *http.Request) {
		// The snapshot carries token hashes; it is written to the client but
		// never logged.
		writeJSON(w, http.StatusOK, store.ExportConfig())
	})
	mux.HandleFunc("POST /admin/config", func(w http.ResponseWriter, r *http.Request) {
		var snap ConfigSnapshot
		if !decode(w, r, &snap) {
			return
		}
		if err := store.ImportConfig(snap); err != nil {
			switch {
			case errors.Is(err, ErrConfigNotEmpty):
				http.Error(w, err.Error(), http.StatusConflict)
			case errors.Is(err, ErrUnsupportedConfigVersion):
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		log.Info("admin config imported", "operator", OperatorID(r),
			"actors", len(snap.Actors), "kits", len(snap.Kits),
			"destinations", len(snap.Destinations), "projects", len(snap.Projects))
		w.WriteHeader(http.StatusNoContent)
	})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/jam/ -run 'TestAdminConfig' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/admin.go internal/jam/admin_test.go
git commit -m "$(cat <<'EOF'
feat(jam): GET/POST /admin/config bulk config export/import endpoints

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Task 5: adminclient `ExportConfig`/`ImportConfig` + `ErrConflict`

**Files:**
- Modify: `internal/jam/adminclient/adminclient.go`
- Modify/Create: `internal/jam/adminclient/adminclient_test.go`

**Interfaces:**
- Consumes: `jam.ConfigSnapshot` (Task 1); `Client.do` (existing).
- Produces: `var ErrConflict = errors.New("conflict")`; `func (c *Client) ExportConfig() (jam.ConfigSnapshot, error)`; `func (c *Client) ImportConfig(s jam.ConfigSnapshot) error`.

- [ ] **Step 1: Write the failing test**

Create/append `internal/jam/adminclient/adminclient_test.go`:

```go
package adminclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestExportImportConfigClient(t *testing.T) {
	want := jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Actors: []jam.Actor{{ID: "a", TokenHash: "h"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = writeJSONTest(w, want)
	})
	var imported jam.ConfigSnapshot
	mux.HandleFunc("POST /admin/config", func(w http.ResponseWriter, r *http.Request) {
		_ = decodeJSONTest(r, &imported)
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New(ts.URL, "")
	got, err := c.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Actors) != 1 || got.Actors[0].ID != "a" {
		t.Fatalf("export = %+v", got)
	}
	if err := c.ImportConfig(want); err != nil {
		t.Fatal(err)
	}
	if imported.Actors[0].ID != "a" {
		t.Fatalf("server received %+v", imported)
	}
}

func TestImportConfigConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "import refused: target config is not empty", http.StatusConflict)
	}))
	defer ts.Close()
	err := New(ts.URL, "").ImportConfig(jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}
```

Add tiny local test helpers at the bottom of the test file:

```go
func writeJSONTest(w http.ResponseWriter, v any) error { return jsonEncode(w, v) }
func decodeJSONTest(r *http.Request, v any) error       { return jsonDecode(r.Body, v) }
```

> If the package has no `jsonEncode`/`jsonDecode`, inline `json.NewEncoder(w).Encode(v)` and `json.NewDecoder(r.Body).Decode(v)` (import `encoding/json`) directly in the handlers instead of these helpers.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/jam/adminclient/ -run 'ConfigClient|ConfigConflict' -v`
Expected: FAIL — `ExportConfig`/`ImportConfig`/`ErrConflict` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/jam/adminclient/adminclient.go`, add the sentinel next to `ErrNotFound`:

```go
// ErrConflict wraps a 409 from the admin API — e.g. importing config into a
// store that is not empty.
var ErrConflict = errors.New("conflict")
```

In `do`, extend the error mapping (the `resp.StatusCode >= 300` block) to also wrap 409:

```go
		if resp.StatusCode == http.StatusNotFound {
			err = fmt.Errorf("%w: %s", ErrNotFound, err)
		}
		if resp.StatusCode == http.StatusConflict {
			err = fmt.Errorf("%w: %s", ErrConflict, err)
		}
		return err
```

Add the two methods (near the other typed methods):

```go
// ExportConfig fetches a full config snapshot (GET /admin/config).
func (c *Client) ExportConfig() (jam.ConfigSnapshot, error) {
	var s jam.ConfigSnapshot
	err := c.do("GET", "/admin/config", nil, &s)
	return s, err
}

// ImportConfig restores a snapshot (POST /admin/config). A non-empty target
// surfaces as ErrConflict.
func (c *Client) ImportConfig(s jam.ConfigSnapshot) error {
	return c.do("POST", "/admin/config", s, nil)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/jam/adminclient/ -run 'ConfigClient|ConfigConflict' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/adminclient/adminclient.go internal/jam/adminclient/adminclient_test.go
git commit -m "$(cat <<'EOF'
feat(adminclient): ExportConfig/ImportConfig + ErrConflict (409) mapping

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Task 6: CLI `at-jam export` / `at-jam import`

**Files:**
- Create: `cmd/at-jam/backup.go`
- Create: `cmd/at-jam/backup_test.go`
- Modify: `cmd/at-jam/main.go` (register two commands in the `Commands` slice)

**Interfaces:**
- Consumes: `adminclient.New`, `Client.ExportConfig`, `Client.ImportConfig`, `adminclient.ErrConflict`, `jam.ConfigSnapshot`; existing CLI helpers `defaultApp`, `defaultAdminURL`, `loadSettings`, `resolveToken`, `adminTokenEnv`, `validateApp`, `firstNonEmpty`, `cli.ParseFlags` (all in `cmd/at-jam`).
- Produces: `func cmdExport(args []string, _ cli.Globals, stdout, stderr io.Writer) int`, `func cmdImport(...) int`.

- [ ] **Step 1: Write the failing test**

Create `cmd/at-jam/backup_test.go`:

```go
package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func newBackupServer(t *testing.T, store jam.Store) *httptest.Server {
	t.Helper()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func seedStore(t *testing.T) *jam.FileStore {
	t.Helper()
	s, err := jam.NewFileStore(filepath.Join(t.TempDir(), "src.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRole(jam.DefaultProject, jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddActor(jam.Actor{ID: "spider-18", TokenHash: jam.HashToken("tok"), Grants: []jam.Grant{{Project: jam.DefaultProject, Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExportImportRoundTripJSON(t *testing.T) {
	src := seedStore(t)
	srcTS := newBackupServer(t, src)

	file := filepath.Join(t.TempDir(), "backup.json")
	var out, errb bytes.Buffer
	if code := run([]string{"export", "--admin-url", srcTS.URL, file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("export exit=%d stderr=%s", code, errb.String())
	}
	data, err := os.ReadFile(file)
	if err != nil || len(data) == 0 {
		t.Fatalf("backup file empty: %v", err)
	}

	dst, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	dstTS := newBackupServer(t, dst)
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", "--admin-url", dstTS.URL, file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("import exit=%d stderr=%s", code, errb.String())
	}
	if a, ok := dst.Lookup(jam.HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("import did not restore actor: %+v ok=%v", a, ok)
	}
}

func TestExportYAMLThenImportSniffs(t *testing.T) {
	src := seedStore(t)
	srcTS := newBackupServer(t, src)
	file := filepath.Join(t.TempDir(), "backup.yaml")
	var out, errb bytes.Buffer
	if code := run([]string{"export", "--admin-url", srcTS.URL, "--format", "yaml", file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("export yaml exit=%d stderr=%s", code, errb.String())
	}
	data, _ := os.ReadFile(file)
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		t.Fatalf("expected YAML, got JSON-looking output: %s", data)
	}
	dst, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	dstTS := newBackupServer(t, dst)
	// No --format: import must sniff YAML.
	if code := run([]string{"import", "--admin-url", dstTS.URL, file}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("import sniff exit=%d stderr=%s", code, errb.String())
	}
	if _, ok := dst.GetRole(jam.DefaultProject, "guest"); !ok {
		t.Fatal("role not restored from YAML")
	}
}

func TestImportIntoNonEmptyFails(t *testing.T) {
	src := seedStore(t)
	srcTS := newBackupServer(t, src)
	file := filepath.Join(t.TempDir(), "backup.json")
	var out, errb bytes.Buffer
	run([]string{"export", "--admin-url", srcTS.URL, file}, func(string) string { return "" }, &out, &errb)

	dst := seedStore(t) // already has config
	dstTS := newBackupServer(t, dst)
	out.Reset()
	errb.Reset()
	code := run([]string{"import", "--admin-url", dstTS.URL, file}, func(string) string { return "" }, &out, &errb)
	if code == 0 {
		t.Fatalf("import into non-empty should fail; stderr=%s", errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("not empty")) {
		t.Fatalf("stderr should explain non-empty target: %s", errb.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-jam/ -run 'TestExportImport|TestExportYAML|TestImportIntoNonEmpty' -v`
Expected: FAIL — unknown command `export` (exit 2 / usage error).

- [ ] **Step 3: Write minimal implementation**

Create `cmd/at-jam/backup.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"gopkg.in/yaml.v3"
)

// cmdExport fetches the config snapshot and writes it to a file (or stdout).
func cmdExport(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	format := fs.String("format", "json", "output format: json|yaml")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if *format != "json" && *format != "yaml" {
		fmt.Fprintln(stderr, "at-jam export: --format must be json or yaml")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam export:", err)
		return 2
	}
	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))

	snap, err := c.ExportConfig()
	if err != nil {
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	out, err := marshalSnapshot(snap, *format)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam export:", err)
		return 1
	}
	if len(pos) == 0 || pos[0] == "-" {
		stdout.Write(out)
		return 0
	}
	// 0o600: the backup carries token hashes — treat it as sensitive.
	if err := os.WriteFile(pos[0], out, 0o600); err != nil {
		fmt.Fprintln(stderr, "at-jam export:", err)
		return 1
	}
	fmt.Fprintln(stdout, "exported config to", pos[0])
	return 0
}

// cmdImport reads a snapshot file (or stdin) and restores it into an empty Jam.
func cmdImport(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	format := fs.String("format", "", "input format: json|yaml (default: sniff)")
	pos, code, ok := cli.ParseFlags(fs, args, stdout, stderr)
	if !ok {
		return code
	}
	if *format != "" && *format != "json" && *format != "yaml" {
		fmt.Fprintln(stderr, "at-jam import: --format must be json or yaml")
		return 2
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam import:", err)
		return 2
	}

	var data []byte
	var err error
	if len(pos) == 0 || pos[0] == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(pos[0])
	}
	if err != nil {
		fmt.Fprintln(stderr, "at-jam import:", err)
		return 1
	}
	snap, err := unmarshalSnapshot(data, *format)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam import:", err)
		return 1
	}

	adminURL := firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL)
	c := adminclient.New(adminURL, resolveToken(*app, *token, stderr))
	if err := c.ImportConfig(snap); err != nil {
		if errors.Is(err, adminclient.ErrConflict) {
			fmt.Fprintln(stderr, "at-jam import: target Jam already has config (import requires an empty config); refusing")
			return 1
		}
		fmt.Fprintln(stderr, "at-jam:", err)
		return 1
	}
	fmt.Fprintf(stdout, "imported config: %d actors, %d kits, %d destinations, %d projects\n",
		len(snap.Actors), len(snap.Kits), len(snap.Destinations), len(snap.Projects))
	return 0
}

func marshalSnapshot(s jam.ConfigSnapshot, format string) ([]byte, error) {
	if format == "yaml" {
		return yaml.Marshal(s)
	}
	return json.MarshalIndent(s, "", "  ")
}

// unmarshalSnapshot decodes data as the given format, or sniffs when format is
// "": a leading '{' (after whitespace) is JSON, otherwise YAML.
func unmarshalSnapshot(data []byte, format string) (jam.ConfigSnapshot, error) {
	var s jam.ConfigSnapshot
	if format == "" {
		if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
			format = "json"
		} else {
			format = "yaml"
		}
	}
	if format == "yaml" {
		return s, yaml.Unmarshal(data, &s)
	}
	return s, json.Unmarshal(data, &s)
}
```

> Note: `jam.ConfigSnapshot` uses `json` struct tags only. `yaml.v3` falls back to lowercased field names, which round-trips cleanly for YAML-in/YAML-out here. If a future field needs a specific YAML key, add a `yaml:` tag then — not now (YAGNI).

Register the commands in `cmd/at-jam/main.go`, in the `Commands` slice (after the `kit` entry is a natural home):

```go
			{Name: "export", Brief: "export the Jam config (actors, roles, kits, destinations, projects) to a file (or stdout) via the admin API", Run: cmdExport},
			{Name: "import", Brief: "import a Jam config backup into an EMPTY Jam via the admin API (refuses if config already exists)", Run: cmdImport},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/at-jam/ -run 'TestExportImport|TestExportYAML|TestImportIntoNonEmpty' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-jam/backup.go cmd/at-jam/backup_test.go cmd/at-jam/main.go
git commit -m "$(cat <<'EOF'
feat(at-jam): export/import commands for config backup & restore

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Task 7: Documentation

**Files:**
- Create: `docs/usage/jam/backup.md`
- Modify: `docs/usage/jam/INDEX.md` (add a row to the "Which doc for which task" table)

**Interfaces:** none (docs only). Follow the docs-author skill; keep single-source-of-truth (link to `roster.md`/`kits.md` for what the aggregates are, don't re-explain them).

- [ ] **Step 1: Write the doc**

Create `docs/usage/jam/backup.md`:

```markdown
---
summary: Back up and restore a Jam's control-plane CONFIG (actors, roles, kits, destinations, projects) with `at-jam export` / `at-jam import` — a single file, excluding studio and intercom state.
read_when: You are snapshotting a Jam's config for backup, or restoring it onto a fresh/rebuilt Jam.
owns: the `at-jam export` / `at-jam import` command surface and the backup file's scope + semantics
prereqs: operators.md for signing in (`--app`/`--token`); roster.md and kits.md for what the aggregates are
tier: leaf
updated: 2026-09-30
---

# Backing up and restoring Jam config

`at-jam export` and `at-jam import` snapshot and restore the Jam **config** — the
five control-plane aggregates:

- **actors** (identities, their **token hashes**, and grants)
- **roles** (scope incl. egress, allocation incl. standing sessions)
- **kits** (all versions **and** the pin)
- **destinations**
- **projects** (roster, escalation policy, chat service)

They deliberately **exclude** runtime/studio state (raised instances), intercom
unread cursors, the intercom squawk log, and allocator events. A backup restores
*who can reach what*, not *what is currently running*.

## Export

```
at-jam export [--format json|yaml] [FILE|-]
```

Writes the snapshot to `FILE` (or stdout when `FILE` is omitted or `-`). Default
format is **JSON**; `--format yaml` emits YAML.

```
at-jam export backup.json
at-jam export --format yaml - > backup.yaml
```

> **The backup contains token hashes** — it is authentication material. A restored
> Jam accepts every actor's existing token unchanged, which is the point, but it
> also means the file must be protected like a credential store. `export` writes
> files with `0600` permissions; keep them that way.

## Import

```
at-jam import [--format json|yaml] <FILE|->
```

Restores a snapshot into a **fresh** Jam. Import is **fail-closed**: if the target
already holds any config, it refuses and writes nothing (so it can never
half-merge or clobber a running server). Format is sniffed from the content when
`--format` is omitted.

```
at-jam import backup.json
```

Because existing tokens keep working after a restore, there is nothing to
re-hand-out: point the studios at the restored Jam and they authenticate as
before.

## Why server-side

Only the Jam server can read and write the stored token hashes, so export/import
are bulk admin endpoints (`GET`/`POST /admin/config`) that the CLI streams to and
from — not a client-side replay over the per-aggregate endpoints (which would
re-mint every token). The wire is JSON; YAML exists only at the file boundary.

## Migrating between store backends

The snapshot is backend-neutral: export from a file-store Jam and import into a
Postgres-backed one (or vice versa) — the config aggregates are identical across
backends. See [serve.md](serve.md#store) for choosing a store.
```

- [ ] **Step 2: Add the INDEX row**

In `docs/usage/jam/INDEX.md`, add to the "Which doc for which task" table (after the `kits.md` row):

```markdown
| [backup.md](backup.md) | Backing up or restoring a Jam's config (actors, roles, kits, destinations, projects) with `at-jam export`/`import` — the file's scope, the refuse-unless-empty restore, and the token-hash sensitivity note. |
```

- [ ] **Step 3: Verify docs health**

Run the docs-audit skill (or its checker) over `docs/`:
Expected: no orphans, no dangling links, `backup.md` reachable from the INDEX, frontmatter valid.

- [ ] **Step 4: Commit**

```bash
git add docs/usage/jam/backup.md docs/usage/jam/INDEX.md
git commit -m "$(cat <<'EOF'
docs(jam): document at-jam export/import config backup & restore

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
EOF
)"
```

---

## Task 8: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full hermetic suite**

Run: `just test`
Expected: PASS (all jam, adminclient, and at-jam tests green).

- [ ] **Step 2: Lint + build**

Run: `just lint && just build`
Expected: no lint findings; `at-jam` builds.

- [ ] **Step 3: Manual smoke (optional, hermetic)**

Build and round-trip against a temp file-store-backed serve, or rely on the CLI tests from Task 6 as the smoke. If running a live serve, confirm:
- `at-jam export --admin-url <url> /tmp/b.json` writes a `0600` file.
- `at-jam import --admin-url <fresh-url> /tmp/b.json` restores it; a second import returns the non-empty refusal.

- [ ] **Step 4: Confirm exclusions**

Grep the snapshot type to confirm it has no instance/unread fields:
Run: `grep -n "Instance\|Unread\|unread\|instances" internal/jam/config_snapshot.go`
Expected: no matches (the exclusion is structural).

---

## Self-Review notes (for the implementer)

- **Spec coverage:** every spec section maps to a task — snapshot type/exclusions (T1), export deep-copy (T1), import fail-closed + version (T1–T2), Postgres transactionality (T3), endpoints + status codes (T4), client + 409 (T5), CLI + format-at-boundary + sensitivity `0600` (T6), docs (T7), verification (T8).
- **Type consistency:** `ConfigSnapshot`, `ConfigSnapshotVersion`, `ErrConfigNotEmpty`, `ErrUnsupportedConfigVersion` (package `jam`); `adminclient.ErrConflict`; method names `ExportConfig`/`ImportConfig` are used identically across store, handler, client, and CLI.
- **Ordering caveat (important):** the `Store` interface gains two methods in T1, so the package does not link until **both** `FileStore.ImportConfig` (T1) and `PostgresStore.ImportConfig` (T3) exist. Implement T1's code, then T3's `pgstore.go` method, before running any `./internal/jam/` test. T2's tests then run against the already-present FileStore impl.
```
