# intercom 1a-2: project ids + memberships — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every project a surrogate id (`prj_…`) that the identity registry resolves, and add project **memberships** `(project_id, user_id)`. Both stores, one conformance suite. Like 1a-1, this is dormant: no product path reads memberships yet (1a-3's humans → users cutover does).

**Architecture:** `Project` gains `ID`. The in-memory `projects` map stays keyed by name, and every store method keeps taking a project *name* (see "Re-sequencing" below). Every code path that creates a project record mints its id. Postgres gets an `id` column (unique, FK → `participants`). `load` backfills ids for rows written before this change and persists them. Memberships live in a new `memberships` table, plus a `memState` set, behind `AddMember` / `RemoveMember` / `ListMembers` / `ListMemberships` / `IsMember`. Removing a user drops their memberships, and removing a project is refused while it has members.

**Tech Stack:** Go 1.26, stdlib, pgx v5, embedded migrations.

**Spec:** `docs/superpowers/specs/2026-10-06-intercom-slice1-identity-registry-design.md` §2 (schema), §3 (domain model). Builds on 1a-1 (`internal/ident`, `RegistryStore`).

## Re-sequencing (decided 2026-10-06)

The 1a-1 plan listed 1a-2 as "`Project.ID`, tombstone/rename for projects, roles/grants/instances by project id, memberships". An inventory of project-name references found the name baked into:

- standing actor ids, which name the containers, volumes and `harbor.cove.state` label;
- allocator stream ids (`<project>/<role>`, which carry the live reservation counts);
- session events;
- relay cursor keys.

Plan 1b reworks all of these. Renaming a project before 1b would tear down its standing sessions. So:

- **1a-2 (this plan):** project ids + registry resolution + memberships. Store APIs stay name-keyed, and removing a project is still a hard delete. Its id is never reused, because ids are random and the `participants` row stays.
- **1b gains:** roles, grants and instances keyed by project id, project tombstones and project rename, moved together with the session, allocator and standing ids.

## Global Constraints

- Every project record carries a valid `prj_` id. That covers `CreateProject`, `DefaultProject` materialization, import of a snapshot without ids, and rows loaded from an older database.
- A snapshot's project ids round-trip through export/import. A given id must be a valid, unused project id. `ConfigSnapshotVersion` stays 1, because the field is additive; v2 is 1a-5.
- Memberships reference a live user and an existing project, and are unique per `(project, user)`. Adding an existing membership is a no-op.
- Tests are hermetic (`MemStore`). Postgres runs the same suite under `-tags integration`.
- Use the agreed vocabulary (*user*, *operator*, *member*).

## Review Focus

1. **Every project write path persists the id.** `putProject` (roster/escalation/context edits), `execWithProjects` (DefaultProject materialization), `CreateProject` and `ImportConfig` must all write the `id` column and a `participants` row. Otherwise a reload re-mints a different id. Pinned by `project_id_survives_edits` (conformance) and the pg reload test.
2. **The load-time backfill is stable:** a legacy row gets one id, persisted, and the same id on every later load. Pinned by `TestPostgresProjectIDBackfill` (integration).
3. **`RemoveUser` drops memberships** in both stores, in the same transaction as the tombstone. Pinned by `membership_dropped_on_user_removal`.
4. **`ImportConfig` mints exactly once.** `PostgresStore.ImportConfig` runs `withReferencedProjects` before its SQL, and `applyImport` runs it again. The second pass must keep the first pass's ids. Pinned by `import_mints_missing_project_ids` plus the pg run.

## File Structure

| File | Change |
|---|---|
| `internal/jam/identity.go` | `Project.ID`; `ErrMembershipNotFound` |
| `internal/jam/registry.go` | `Membership`; `Directory.IsMember`; `RegistryStore` membership methods |
| `internal/jam/memstate.go` | `members` map; `newProject(name)` mints; `requireProject`, `backfillProjects` use it; `projectReference` counts members |
| `internal/jam/registry_state.go` | project cases in `Resolve`/`LookupName`/`idExists`; `projectByID`; membership reads and prepares; `prepareRemoveUser` returns dropped memberships |
| `internal/jam/memstore.go`, `memstore_registry.go` | `CreateProject` mints; membership mutators; `RemoveUser` drops memberships |
| `internal/jam/config_snapshot.go` | `withReferencedProjects` mints missing ids; `checkImport` validates given ids |
| `internal/jam/pgstore.go` | project rows written with `id` + participant via `insertProjectTx`/`upsertProjectTx`; `load` loads memberships and calls `ensureProjectIDs` |
| `internal/jam/pgstore_registry.go` | membership mutators; `RemoveUser` deletes memberships |
| `internal/jam/migrations/0007_project_ids_memberships.sql` | new |
| `internal/jam/pgstore_testhelpers.go` | truncate `memberships`; reset `members` |
| `internal/jam/storetest/registry.go` | project-id and membership conformance |
| `internal/jam/pgstore_integration_test.go` | `TestPostgresProjectIDBackfill` |
| `docs/usage/jam/roster.md` | one line: projects carry ids; memberships exist (dormant) |

---

### Task 1: Project ids (conformance + memState + MemStore)

**Interfaces produced:** `Project.ID ident.ID` (json `id,omitempty`); `func newProject(name string) Project`; `func (m *memState) projectByID(id ident.ID) (Project, bool)`.

- [ ] **Step 1: Failing conformance tests** (append to `runRegistryConformance`):

```go
	t.Run("project_has_id_and_resolves", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		p, _ := s.GetProject("acme")
		if p.ID.Kind() != ident.Project {
			t.Fatalf("project id = %q", p.ID)
		}
		if _, err := ident.Parse(string(p.ID)); err != nil {
			t.Fatalf("project id does not parse: %v", err)
		}
		if id, ok := s.LookupName(ident.Project, "acme"); !ok || id != p.ID {
			t.Fatalf("LookupName = %q, %v", id, ok)
		}
		if e, ok := s.Resolve(p.ID); !ok || e.Kind != ident.Project || e.Label() != "acme" {
			t.Fatalf("Resolve = %+v, %v", e, ok)
		}
		if err := s.CreateProject("beta"); err != nil {
			t.Fatal(err)
		}
		if b, _ := s.GetProject("beta"); b.ID == p.ID {
			t.Fatal("two projects share an id")
		}
	})

	t.Run("default_project_materialized_with_id", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRole("", jam.Role{Name: "r"}); err != nil {
			t.Fatal(err)
		}
		if p, ok := s.GetProject(jam.DefaultProject); !ok || p.ID.Kind() != ident.Project {
			t.Fatalf("default project = %+v, %v", p, ok)
		}
	})

	t.Run("project_id_survives_edits", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		before, _ := s.GetProject("acme")
		if err := s.SetChatService("acme", "discord"); err != nil {
			t.Fatal(err)
		}
		if after, _ := s.GetProject("acme"); after.ID != before.ID {
			t.Fatalf("id changed on edit: %q → %q", before.ID, after.ID)
		}
	})

	t.Run("project_recreated_gets_new_id", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		old, _ := s.GetProject("acme")
		if err := s.RemoveProject("acme"); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		if p, _ := s.GetProject("acme"); p.ID == old.ID {
			t.Fatal("a re-created project must get a new id")
		}
	})
```

- [ ] **Step 2: Run them and confirm they fail.** `go test ./internal/jam/ -run TestMemStoreConformance` should fail with `p.ID undefined`.
- [ ] **Step 3: Implement.**
  - `Project.ID ident.ID \`json:"id,omitempty"\``, placed first in the struct.
  - `newProject(name) Project { return Project{ID: ident.New(ident.Project), Name: name} }`. Use it in `requireProject` (DefaultProject), `backfillProjects`, `MemStore.CreateProject` and `PostgresStore.CreateProject`.
  - `Resolve`: add `case ident.Project:` → `projectByID`, returning `Entry{Kind: ident.Project, Name: p.Name, Status: StatusLive}`.
  - `LookupName`: add `case ident.Project:` → `m.projects[name]`, when its `ID != ""`.
  - `idExists`: also scan projects.
  - `projectByID` is a linear scan, consistent with the rest of the registry. The 1a-1 deferred minor already covers indexing it.
- [ ] **Step 4: Run** `go test ./internal/jam/...` and confirm it passes.
- [ ] **Step 5: Commit** `feat(jam): projects carry a surrogate id the registry resolves`.

### Task 2: Ids through export/import

- [ ] **Step 1: Failing conformance tests**:

```go
	t.Run("export_import_keeps_project_ids", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		want, _ := s.GetProject("acme")
		s2 := newStore(t)
		if err := s2.ImportConfig(s.ExportConfig()); err != nil {
			t.Fatalf("ImportConfig: %v", err)
		}
		if got, _ := s2.GetProject("acme"); got.ID != want.ID {
			t.Fatalf("imported id = %q, want %q", got.ID, want.ID)
		}
	})

	t.Run("import_mints_missing_project_ids", func(t *testing.T) {
		s := newStore(t)
		snap := jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion,
			Projects: []jam.Project{{Name: "acme"}},
			Roles:    map[string]map[string]jam.Role{"beta": {"r": {Name: "r"}}}}
		if err := s.ImportConfig(snap); err != nil {
			t.Fatalf("ImportConfig: %v", err)
		}
		for _, name := range []string{"acme", "beta"} {
			p, ok := s.GetProject(name)
			if !ok || p.ID.Kind() != ident.Project {
				t.Fatalf("%s = %+v, %v", name, p, ok)
			}
			if e, ok := s.Resolve(p.ID); !ok || e.Name != name {
				t.Fatalf("Resolve(%s) = %+v, %v", name, e, ok)
			}
		}
	})

	t.Run("import_rejects_bad_project_ids", func(t *testing.T) {
		dup := ident.New(ident.Project)
		for name, ps := range map[string][]jam.Project{
			"wrong kind": {{ID: ident.New(ident.User), Name: "acme"}},
			"malformed":  {{ID: "prj_nope", Name: "acme"}},
			"duplicate":  {{ID: dup, Name: "acme"}, {ID: dup, Name: "beta"}},
		} {
			s := newStore(t)
			if err := s.ImportConfig(jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Projects: ps}); !errors.Is(err, jam.ErrInvalidConfig) {
				t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
			}
		}
	})
```

- [ ] **Step 2: Run and confirm they fail.**
- [ ] **Step 3: Implement.**
  - `withReferencedProjects`: copy `s.Projects` (it is reassigned, never mutated in place), and give every project with an empty `ID` a `newProject` id. Referenced-only projects get `newProject(name)`. This is idempotent: a second pass sees the ids and keeps them.
  - `checkImport` → new `validateSnapshotProjectIDs(m, s)`: each non-empty id must `ident.Parse`, be of kind `ident.Project`, be unique within the snapshot, and not already exist in the registry (`m.idExists`). Errors wrap `ErrInvalidConfig`.
- [ ] **Step 4: Run and confirm they pass.** Then commit `feat(jam): project ids round-trip through config export/import`.

### Task 3: Memberships (conformance + memState + MemStore)

**Interfaces produced:**

```go
// registry.go
type Membership struct {
	ProjectID ident.ID `json:"project_id"`
	UserID    ident.ID `json:"user_id"`
}
// Directory gains:
	IsMember(project, user ident.ID) bool
// RegistryStore gains:
	// AddMember makes a live user a member of a project (no-op if already).
	AddMember(project, user ident.ID) error
	// RemoveMember ends a membership (ErrMembershipNotFound if none).
	RemoveMember(project, user ident.ID) error
	ListMembers(project ident.ID) []ident.ID     // user ids, sorted
	ListMemberships(user ident.ID) []ident.ID    // project ids, sorted
// identity.go
	ErrMembershipNotFound = errors.New("membership not found")
```

`memState.members map[ident.ID]map[ident.ID]bool` (project → user set). `RemoveUser`'s prepare now also returns the project ids whose membership it drops. `projectReference` reports `"N member(s)"` so `RemoveProject` refuses with `ErrProjectInUse`.

- [ ] **Step 1: Failing conformance tests**:

```go
	mustProject := func(t *testing.T, s jam.Store, name string) ident.ID {
		t.Helper()
		if err := s.CreateProject(name); err != nil {
			t.Fatal(err)
		}
		p, _ := s.GetProject(name)
		return p.ID
	}

	t.Run("membership_lifecycle", func(t *testing.T) {
		s := newStore(t)
		acme, beta := mustProject(t, s, "acme"), mustProject(t, s, "beta")
		a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		for _, m := range [][2]ident.ID{{acme, a.ID}, {acme, b.ID}, {beta, a.ID}, {acme, a.ID}} {
			if err := s.AddMember(m[0], m[1]); err != nil {
				t.Fatalf("AddMember %v: %v", m, err)
			}
		}
		if !s.IsMember(acme, a.ID) || s.IsMember(beta, b.ID) {
			t.Fatal("IsMember wrong")
		}
		if got := s.ListMembers(acme); len(got) != 2 {
			t.Fatalf("ListMembers = %v, want 2 (re-adding is a no-op)", got)
		}
		if got := s.ListMemberships(a.ID); len(got) != 2 {
			t.Fatalf("ListMemberships = %v", got)
		}
		if err := s.RemoveMember(acme, b.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveMember(acme, b.ID); !errors.Is(err, jam.ErrMembershipNotFound) {
			t.Fatalf("second RemoveMember: %v, want ErrMembershipNotFound", err)
		}
		if err := s.AddMember(ident.New(ident.Project), a.ID); !errors.Is(err, jam.ErrProjectNotFound) {
			t.Fatalf("unknown project: %v, want ErrProjectNotFound", err)
		}
		if err := s.AddMember(acme, ident.New(ident.User)); !errors.Is(err, jam.ErrUserNotFound) {
			t.Fatalf("unknown user: %v, want ErrUserNotFound", err)
		}
	})

	t.Run("membership_dropped_on_user_removal", func(t *testing.T) {
		s := newStore(t)
		acme := mustProject(t, s, "acme")
		a := mustUser(t, s, "alice")
		if err := s.AddMember(acme, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatal(err)
		}
		if s.IsMember(acme, a.ID) || len(s.ListMembers(acme)) != 0 {
			t.Fatal("a removed user must not stay a member")
		}
		if err := s.AddMember(acme, a.ID); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("add removed user: %v, want ErrRemoved", err)
		}
	})

	t.Run("project_with_members_not_removable", func(t *testing.T) {
		s := newStore(t)
		acme := mustProject(t, s, "acme")
		a := mustUser(t, s, "alice")
		if err := s.AddMember(acme, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveProject("acme"); !errors.Is(err, jam.ErrProjectInUse) {
			t.Fatalf("RemoveProject with members: %v, want ErrProjectInUse", err)
		}
		if err := s.RemoveMember(acme, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveProject("acme"); err != nil {
			t.Fatalf("RemoveProject after the last member left: %v", err)
		}
	})
```

- [ ] **Step 2: Run and confirm they fail.**
- [ ] **Step 3: Implement** the memState reads (`IsMember`, `ListMembers`, `ListMemberships`) and prepares:
  - `prepareAddMember(project, user) (added bool, err error)`: `projectByID`, else `ErrProjectNotFound`; then `liveUser`.
  - `prepareRemoveMember(project, user) error`.

  Then add the `applyAddMember`/`applyRemoveMember` mutators and the MemStore mutators. In `prepareRemoveUser`, also return the dropped project ids; `MemStore.RemoveUser` applies them. Count members in `projectReference`.
- [ ] **Step 4: Run and confirm they pass.** Then commit `feat(jam): project memberships (MemStore)`.

### Task 4: Postgres — migration 0007, project rows with ids, memberships, backfill

- [ ] **Step 1: Write the migration** `0007_project_ids_memberships.sql`:

```sql
-- Project ids (intercom slice 1a-2). Nullable at first: rows written before
-- this change get their id from PostgresStore.load (ensureProjectIDs), which
-- mints in Go (internal/ident) and persists it. Memberships are (project,
-- user) pairs; removing a user deletes theirs, removing a project is refused
-- while it has any.
ALTER TABLE projects ADD COLUMN id text UNIQUE REFERENCES participants(id);

CREATE TABLE memberships (
    project_id text NOT NULL REFERENCES projects(id),
    user_id    text NOT NULL REFERENCES users(id),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX memberships_user ON memberships (user_id);
```

- [ ] **Step 2: Write project rows with their id.**
  - `insertProjectTx(ctx, tx, p)` does the participant insert, then `INSERT INTO projects (name, id, doc)`.
  - `upsertProjectTx` is the same with `ON CONFLICT (name) DO UPDATE SET id = EXCLUDED.id, doc = EXCLUDED.doc, version = projects.version + 1, updated_at = now()`.
  - Use them in `execWithProjects`, `ImportConfig` (projects first), `CreateProject` (now one tx) and `putProject` (now one tx).
- [ ] **Step 3: Load.** After the `projects` docs, call `ensureProjectIDs(ctx)`: for each cached project with an empty `ID`, mint one with `newProject`'s id and, in one tx, insert the participant and `UPDATE projects SET id=$1, doc=$2 WHERE name=$3`, then update the cache. Then load memberships: `SELECT project_id, user_id FROM memberships`.
- [ ] **Step 4: Memberships in Postgres.**
  - `AddMember`: `INSERT … ON CONFLICT DO NOTHING`.
  - `RemoveMember`: `DELETE`.
  - `RemoveUser`'s tx also runs `DELETE FROM memberships WHERE user_id = $1`.
  - `RemoveProject` is unchanged. The memState check refuses while members exist, and the FK backs it up.
- [ ] **Step 5: Test helpers.** `TruncateAllForTest` adds `memberships` and resets `s.members`.
- [ ] **Step 6: Integration test** `TestPostgresProjectIDBackfill`:
  1. Open a store and truncate.
  2. Insert a legacy row directly: `INSERT INTO projects (name, doc) VALUES ('legacy', '{"name":"legacy","roster":{}}')`.
  3. Open a second store on the same DSN. `GetProject("legacy").ID` must be a valid `prj_` id, and `Resolve` must find it.
  4. Open a third store. The id must be the same.
- [ ] **Step 7: Verify.** Run `go vet -tags integration ./internal/jam/...` and `go test ./internal/jam/...`. The pg run happens in CI (`store-integration.yml`), or locally with `JAM_TEST_POSTGRES_DSN` if available.
- [ ] **Step 8: Commit** `feat(jam): Postgres project ids and memberships (migration 0007)`.

### Task 5: Docs

- [ ] In `docs/usage/jam/roster.md`, extend the registry sentence: projects now carry `prj_` ids, and memberships exist but nothing uses them yet. Stay within the 200-line leaf budget.
- [ ] In the slice 1 spec, update the "Plans" note to record the re-sequencing: project references, tombstones and rename move to 1b.
- [ ] Run the docs audit and confirm it introduces no new findings. Then commit.

## Final verification

- [ ] `go build ./... && go vet ./... && go test ./...`, plus `scripts/lint.sh`.
- [ ] `store-integration` is green on the PR. That CI job runs the pg conformance and `TestPostgresProjectIDBackfill`.
