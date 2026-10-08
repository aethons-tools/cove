# Session context — slice 3a (authored Project / Role / Jam layers, backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Operators can author context for a role, a project (plus a resource list) and the whole Jam; every raise compiles those layers into the session context after Studio.

**Architecture:** `sessionctx` gains three authored layers (Project, Role, Jam), a `Resource` type rendered into a `project/resources.md` leaf, and `ValidateLayer` for authoring-time budgets. Storage rides existing jsonb docs — `Role.Context`, `Project.Context`/`Resources` — plus a new `jam_settings` table (migration 0004) for the Jam-wide layer, included in config export/import. Each has admin endpoints, adminclient methods, and one CLI group, `at-jam context show|set|clear`. `Supervisor.Raise` feeds them into `Compile`.

**Tech Stack:** Go 1.27 stdlib, `gopkg.in/yaml.v3` (already a dependency), Postgres via the existing pgstore; `just test`, `just integration-jam` for the Postgres conformance run if available.

**Spec:** [`docs/superpowers/specs/2026-10-02-session-context-layers-design.md`](../specs/2026-10-02-session-context-layers-design.md) — slice 3, backend half (3a). 3b (admin UI panels, `StudioKit.Notes`, generated `kit/tools.md`) is a separate PR.

## Global Constraints

- Delivery order: Boilerplate → Kit → Studio → **Project → Role → Jam**. Precedence text unchanged (later wins: Jam > Role > Project > Studio > Kit > Boilerplate).
- Core budgets (bytes): Project **1200**, Role **1200**, Jam **800**. Enforced at **authoring** (API → 400 naming the size); `Compile` still truncates defensively and never fails a raise.
- Leaves: name must pass `ValidLeafName`, names unique per layer, `read_when` non-empty, ≤ **64 KiB** of leaf bodies per layer (authoring check).
- Resources: `{name, kind, ref, note}`, kind ∈ `repo|doc|tracker|url`, ≤ **50** per project; rendered to the `project/resources.md` leaf with a one-line pointer in the project core.
- **Owner decision (2026-10-02):** no generated `project/contacts.md` — the Studio layer's message targets own "who to message".
- A role re-put keeps `Context` (like egress and standing). Managed only by the context endpoints.
- No secrets: authored text is operator prose; the docs warn never to put credentials in it.
- Tests hermetic; TDD; docs in the same PR; `GOPROXY=https://proxy.golang.org,direct` in this sandbox.

## Review Focus

1. **A role re-put (CLI `role put`, UI allocation edit)** — expected: `Context` survives. Pinned in Task 2.
2. **Config export → import round trip** — expected: role, project and Jam context all come back. Pinned in Task 4.
3. **An over-budget core or a bad leaf name via the API** — expected: 400 naming the problem, nothing stored. Pinned in Tasks 1–4.
4. **Clearing a layer** — expected: the section vanishes from the next raise's context (empty layer emits nothing). Pinned in Task 6.
5. **A project context set on an unknown project** — expected: 404, as other project writes. Pinned in Task 3.

---

## File structure

| File | Responsibility |
|------|----------------|
| `internal/jam/sessionctx/authored.go` (create), `authored_test.go` (create) | `Resource`, `ProjectLayer`, `ValidateLayer`, `ValidateResources`, budgets |
| `internal/jam/sessionctx/sessionctx.go` (modify) | layer consts, `Inputs.Project/Role/Jam`, Compile order, lint each authored core, YAML tags on `Leaf`/`Layer` |
| `internal/jam/identity.go` (modify `Role`, `Project`) | `Role.Context`, `Project.Context`, `Project.Resources` |
| `internal/jam/roleedit.go` (modify) | `SetRoleContext`, `ClearRoleContext`; `PutRoleKeeping` keeps Context |
| `internal/jam/adminui/role_edit.go` (~171) | allocation edit keeps Context |
| `internal/jam/memstate.go`, `memstore.go`, `pgstore.go`, `store.go` | `SetProjectContext`; Jam context get/set; `copyProject` |
| `internal/jam/migrations/0004_jam_settings.sql` (create) | `jam_settings` table |
| `internal/jam/config_snapshot.go`, `pgstore.go` (`ImportConfig`) | `JamContext` in the snapshot |
| `internal/jam/context_admin.go` (create), `context_admin_test.go` (create) | `registerContext`: role/project/jam endpoints |
| `internal/jam/admin.go` (~795) | mount `registerContext` |
| `internal/jam/adminclient/adminclient.go` | context client methods |
| `internal/jam/storetest/conformance.go` | project + jam context subtests |
| `cmd/at-jam/context.go` (create), `context_test.go` (create); `main.go` (command table) | `at-jam context` |
| `internal/jam/supervisor.go` (compile block) | feed the layers |
| Docs: `docs/usage/jam/session-context.md` (Authoring section), `projects.md`, `roster.md` (one pointer each), `docs/usage/jam/INDEX.md` row text | |

---

### Task 1: `sessionctx` authored layers

**Files:** Create `internal/jam/sessionctx/authored.go`, `authored_test.go`; modify `sessionctx.go`.

**Interfaces:**
- Produces:
  - consts `LayerProject = "project"`, `LayerRole = "role"`, `LayerJam = "jam"`; `BudgetProject = 1200`, `BudgetRole = 1200`, `BudgetJam = 800`; `MaxLeafBytes = 64 << 10`; `MaxResources = 50`
  - `type Resource struct { Name, Kind, Ref, Note string }` (json `name,kind,ref,note,omitempty`; yaml same)
  - `func ValidateLayer(l Layer, budget int) error`
  - `func ValidateResources(rs []Resource) error`
  - `func ProjectLayer(l Layer, rs []Resource) Layer` — appends a `resources.md` leaf and a pointer line when `rs` is non-empty
  - `Inputs.Project, Inputs.Role, Inputs.Jam Layer`
  - yaml tags: `Leaf{Name yaml:"name"; ReadWhen yaml:"read-when"; Body yaml:"body"}`, `Layer{Core yaml:"core"; Leaves yaml:"leaves,omitempty"}`

- [ ] **Step 1: Write the failing tests** (`authored_test.go`)

```go
package sessionctx

import (
	"strings"
	"testing"
)

func TestValidateLayer(t *testing.T) {
	ok := Layer{Core: "c", Leaves: []Leaf{{Name: "a.md", ReadWhen: "w", Body: "b"}}}
	if err := ValidateLayer(ok, 10); err != nil {
		t.Fatalf("valid layer refused: %v", err)
	}
	for name, tc := range map[string]struct {
		l    Layer
		want string
	}{
		"over budget":  {Layer{Core: strings.Repeat("x", 11)}, "11 bytes"},
		"bad name":     {Layer{Leaves: []Leaf{{Name: "../x.md", ReadWhen: "w"}}}, `"../x.md"`},
		"dup name":     {Layer{Leaves: []Leaf{{Name: "a.md", ReadWhen: "w"}, {Name: "a.md", ReadWhen: "w"}}}, "twice"},
		"no read-when": {Layer{Leaves: []Leaf{{Name: "a.md"}}}, "read-when"},
		"huge leaves":  {Layer{Leaves: []Leaf{{Name: "a.md", ReadWhen: "w", Body: strings.Repeat("x", MaxLeafBytes+1)}}}, "leaf"},
	} {
		if err := ValidateLayer(tc.l, 10); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want mention of %q", name, err, tc.want)
		}
	}
}

func TestValidateResources(t *testing.T) {
	if err := ValidateResources([]Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]Resource{
		{{Name: "", Kind: "repo", Ref: "x"}},
		{{Name: "x", Kind: "wiki", Ref: "x"}},
		{{Name: "x", Kind: "url", Ref: ""}},
		make([]Resource, MaxResources+1),
	} {
		if err := ValidateResources(bad); err == nil {
			t.Errorf("want an error for %+v", bad[0])
		}
	}
}

func TestProjectLayerRendersResources(t *testing.T) {
	l := ProjectLayer(Layer{Core: "Ship the thing."}, []Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove", Note: "main\nrepo"}})
	if !strings.Contains(l.Core, "Ship the thing.") || !strings.Contains(l.Core, "1 project resource") {
		t.Fatalf("core: %q", l.Core)
	}
	if len(l.Leaves) != 1 || l.Leaves[0].Name != "resources.md" || !strings.Contains(l.Leaves[0].Body, "| cove | repo | aethons-tools/cove | main repo |") {
		t.Fatalf("leaves: %+v", l.Leaves)
	}
	if got := ProjectLayer(Layer{}, nil); !got.Empty() {
		t.Fatalf("nothing in, nothing out: %+v", got)
	}
}

func TestCompileAuthoredOrderAndLint(t *testing.T) {
	b := Compile(Inputs{
		Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "acme", Role: "dev", Kit: "web@v1"},
		Kit:     Layer{Core: "K"},
		Studio:  StudioFacts{Egress: []string{"example.org"}, EgressKnown: true},
		Project: Layer{Core: "PROJECT"},
		Role:    Layer{Core: "ROLE uses example.org"},
		Jam:     Layer{Core: "JAM"},
	})
	order := []string{"## Kit", "## Studio", "## Project — acme", "## Role — dev", "## Jam"}
	last := -1
	for _, h := range order {
		i := strings.Index(b.Core, h)
		if i < 0 || i < last {
			t.Fatalf("want %v in order:\n%s", order, b.Core)
		}
		last = i
	}
	if !strings.Contains(strings.Join(b.Warnings, "\n"), "role core restates egress host example.org") {
		t.Fatalf("authored layers are linted too: %v", b.Warnings)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/jam/sessionctx/` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`sessionctx.go`: add the three layer and budget consts; add yaml tags to `Leaf` and `Layer` as listed; add `Project, Role, Jam Layer` to `Inputs`; in `Compile` append after the Studio section:

```go
		{LayerProject, titled("Project", in.Session.Project), in.Project, BudgetProject},
		{LayerRole, titled("Role", in.Session.Role), in.Role, BudgetRole},
		{LayerJam, "Jam", in.Jam, BudgetJam},
```

with `func titled(t, name string) string { if name == "" { return t }; return t + " — " + name }` (gofmt-formatted), and replace the single kit lint line with:

```go
	for _, a := range []struct {
		name string
		l    Layer
	}{{LayerKit, in.Kit}, {LayerProject, in.Project}, {LayerRole, in.Role}, {LayerJam, in.Jam}} {
		b.Warnings = append(b.Warnings, lintAuthored(a.name, a.l.Core, in.Studio)...)
	}
```

`authored.go`:

```go
package sessionctx

import (
	"fmt"
	"strings"
)

// Authored-layer limits, enforced when an operator writes a layer.
const (
	BudgetProject = 1200
	BudgetRole    = 1200
	BudgetJam     = 800
	MaxLeafBytes  = 64 << 10 // per layer, all leaf bodies
	MaxResources  = 50
)

var resourceKinds = map[string]bool{"repo": true, "doc": true, "tracker": true, "url": true}

// Resource is one project resource (a repo, doc, tracker or URL) sessions
// should know about; rendered into the project layer's resources.md leaf.
type Resource struct {
	Name string `json:"name" yaml:"name"`
	Kind string `json:"kind" yaml:"kind"` // repo | doc | tracker | url
	Ref  string `json:"ref" yaml:"ref"`
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
}

// ValidateLayer checks an authored layer: core within budget; leaves safely
// named, unique, each with a read-when; leaf bodies within MaxLeafBytes.
func ValidateLayer(l Layer, budget int) error {
	if n := len(strings.TrimSpace(l.Core)); n > budget {
		return fmt.Errorf("core is %d bytes; the budget is %d — move detail into leaves", n, budget)
	}
	seen := map[string]bool{}
	total := 0
	for _, lf := range l.Leaves {
		if !ValidLeafName(lf.Name) {
			return fmt.Errorf("leaf name %q: want lowercase [a-z0-9._-] ending .md (not CORE.md or INDEX.md)", lf.Name)
		}
		if seen[lf.Name] {
			return fmt.Errorf("leaf %q appears twice", lf.Name)
		}
		seen[lf.Name] = true
		if strings.TrimSpace(lf.ReadWhen) == "" {
			return fmt.Errorf("leaf %q needs a read-when", lf.Name)
		}
		total += len(lf.Body)
	}
	if total > MaxLeafBytes {
		return fmt.Errorf("leaf bodies total %d bytes; at most %d", total, MaxLeafBytes)
	}
	return nil
}

// ValidateResources checks a project's resource list.
func ValidateResources(rs []Resource) error {
	if len(rs) > MaxResources {
		return fmt.Errorf("%d resources; at most %d", len(rs), MaxResources)
	}
	for i, r := range rs {
		switch {
		case strings.TrimSpace(r.Name) == "":
			return fmt.Errorf("resource %d: name is required", i+1)
		case !resourceKinds[r.Kind]:
			return fmt.Errorf("resource %q: kind %q; want repo, doc, tracker or url", r.Name, r.Kind)
		case strings.TrimSpace(r.Ref) == "":
			return fmt.Errorf("resource %q: ref is required", r.Name)
		}
	}
	return nil
}

// ProjectLayer is the project's authored layer plus its resources, rendered
// as a resources.md leaf with a pointer line in the core.
func ProjectLayer(l Layer, rs []Resource) Layer {
	if len(rs) == 0 {
		return l
	}
	var b strings.Builder
	b.WriteString("| Name | Kind | Ref | Note |\n|------|------|-----|------|\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", oneLine(r.Name), oneLine(r.Kind), oneLine(r.Ref), oneLine(r.Note))
	}
	out := Layer{Core: strings.TrimSpace(l.Core + fmt.Sprintf("\n%d project resource(s) (repos, docs, trackers) are listed in project/resources.md.", len(rs)))}
	out.Leaves = append(append([]Leaf(nil), l.Leaves...), Leaf{Name: "resources.md", ReadWhen: "you need the project's repos, docs or trackers", Body: b.String()})
	return out
}
```

Note: `"1 project resource"` in the test matches `"1 project resource(s)"`.

- [ ] **Step 4: Run** `go test ./internal/jam/sessionctx/` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(sessionctx): authored project, role and jam layers`

---

### Task 2: `Role.Context`

**Files:** Modify `internal/jam/identity.go` (`Role`), `internal/jam/roleedit.go`, `internal/jam/adminui/role_edit.go` (~171). Test: `internal/jam/roleedit_test.go` (create if absent; else append), `internal/jam/adminui/role_edit_test.go`.

**Interfaces:**
- Consumes: `sessionctx.Layer`, `ValidateLayer`, `BudgetRole`.
- Produces: `Role.Context sessionctx.Layer` (`json:"context,omitzero"`); `func SetRoleContext(store Store, project, name string, l sessionctx.Layer) error` (400 on invalid, 404 on missing role); `func ClearRoleContext(store Store, project, name string) error`.

- [ ] **Step 1: Write the failing tests**

```go
func TestRoleContextSetKeepAndClear(t *testing.T) {
	store := NewMemStore()
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	l := sessionctx.Layer{Core: "Review PRs.", Leaves: []sessionctx.Leaf{{Name: "how.md", ReadWhen: "reviewing", Body: "b"}}}
	if err := SetRoleContext(store, "default", "dev", l); err != nil {
		t.Fatal(err)
	}
	if err := PutRoleKeeping(store, "default", Role{Name: "dev", Scope: Scope{TTL: 2 * time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := store.GetRole("default", "dev"); r.Context.Core != "Review PRs." || len(r.Context.Leaves) != 1 {
		t.Fatalf("a role re-put must keep Context: %+v", r.Context)
	}
	if err := SetRoleContext(store, "default", "dev", sessionctx.Layer{Core: strings.Repeat("x", sessionctx.BudgetRole+1)}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("over budget = %v, want 400", err)
	}
	if err := SetRoleContext(store, "default", "ghost", l); WriteStatus(err, 0) != http.StatusNotFound {
		t.Fatalf("missing role = %v, want 404", err)
	}
	if err := ClearRoleContext(store, "default", "dev"); err != nil {
		t.Fatal(err)
	}
	if r, _ := store.GetRole("default", "dev"); !r.Context.Empty() {
		t.Fatalf("cleared: %+v", r.Context)
	}
}
```

(Use the store constructor the package's other tests use — `grep -n "NewMemStore\|newMemStore" internal/jam/*_test.go | head -3`.)

In `adminui/role_edit_test.go`, add a test that sets a role's context with `jam.SetRoleContext`, posts the allocation edit form exactly as the existing allocation test does, then asserts `GetRole(...).Context.Core` is unchanged.

- [ ] **Step 2: Run** `go test ./internal/jam/ ./internal/jam/adminui/ -run 'RoleContext|Allocation'` — Expected: FAIL.

- [ ] **Step 3: Implement**

`identity.go` `Role`:

```go
	// Context is the role's authored session-context layer (rules for this
	// role). Managed only by the context endpoints (`at-jam context … --role`);
	// a role re-put keeps it.
	Context sessionctx.Layer `json:"context,omitzero"`
```

`roleedit.go`: in `PutRoleKeeping`, add `role.Context = existing.Context` and extend its doc comment ("…standing declarations, egress policy and context…"); add:

```go
// SetRoleContext replaces the role's authored context layer. An invalid layer
// is a 400 WriteError; a missing role 404.
func SetRoleContext(store Store, project, name string, l sessionctx.Layer) error {
	if err := sessionctx.ValidateLayer(l, sessionctx.BudgetRole); err != nil {
		return writeErr(http.StatusBadRequest, "role context: %s", err.Error())
	}
	return UpdateRole(store, project, name, func(r *Role) error {
		r.Context = sessionctx.Layer{Core: l.Core, Leaves: slices.Clone(l.Leaves)}
		return nil
	})
}

// ClearRoleContext removes the role's authored context layer.
func ClearRoleContext(store Store, project, name string) error {
	return UpdateRole(store, project, name, func(r *Role) error {
		r.Context = sessionctx.Layer{}
		return nil
	})
}
```

`adminui/role_edit.go` ~171: wherever Standing is carried over by hand, also carry `Context` (read the code; if it calls `jam.PutRoleKeeping`, nothing to do and the test passes after the `PutRoleKeeping` change).

- [ ] **Step 4: Run** `go test ./internal/jam/... ` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): Role.Context — the role's authored session-context layer`

---

### Task 3: `Project.Context` and `Project.Resources`

**Files:** Modify `internal/jam/identity.go` (`Project`), `store.go` (interface), `memstate.go` (`setProjectContext`, `copyProject`), `memstore.go`, `pgstore.go`; `internal/jam/storetest/conformance.go`.

**Interfaces:**
- Produces: `Project.Context sessionctx.Layer` (`json:"context,omitzero"`), `Project.Resources []sessionctx.Resource` (`json:"resources,omitempty"`); `Store.SetProjectContext(project string, l sessionctx.Layer, rs []sessionctx.Resource) error` — `ErrProjectNotFound` for an unknown project (except `DefaultProject`, materialized like other project writes). Validation is the caller's (Task 5 handler).

- [ ] **Step 1: Write the failing conformance subtest** (in `RunConformance`):

```go
	t.Run("project_context_set_and_copy", func(t *testing.T) {
		s := newStoreWithAcme(t)
		l := sessionctx.Layer{Core: "Goals.", Leaves: []sessionctx.Leaf{{Name: "a.md", ReadWhen: "w", Body: "b"}}}
		rs := []sessionctx.Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}
		if err := s.SetProjectContext("acme", l, rs); err != nil {
			t.Fatalf("SetProjectContext: %v", err)
		}
		p, _ := s.GetProject("acme")
		if p.Context.Core != "Goals." || len(p.Context.Leaves) != 1 || len(p.Resources) != 1 {
			t.Fatalf("got %+v", p)
		}
		p.Resources[0].Name = "mutated"
		p.Context.Leaves[0].Name = "mutated.md"
		if again, _ := s.GetProject("acme"); again.Resources[0].Name != "cove" || again.Context.Leaves[0].Name != "a.md" {
			t.Fatal("GetProject must return a copy")
		}
		if err := s.SetProjectContext("ghost", l, nil); !errors.Is(err, jam.ErrProjectNotFound) {
			t.Fatalf("unknown project = %v, want ErrProjectNotFound", err)
		}
	})
```

- [ ] **Step 2: Run** `go test ./internal/jam/ -run Conformance` — Expected: FAIL (method undefined).

- [ ] **Step 3: Implement**

`identity.go` `Project`:

```go
	// Context is the project's authored session-context layer (its goals) and
	// Resources the repos/docs/trackers sessions should know; both managed by
	// the context endpoints (`at-jam context … --project`).
	Context   sessionctx.Layer      `json:"context,omitzero"`
	Resources []sessionctx.Resource `json:"resources,omitempty"`
```

`memstate.go`:

```go
// setProjectContext returns p with its authored context and resources replaced.
func setProjectContext(p Project, l sessionctx.Layer, rs []sessionctx.Resource) Project {
	p.Context = sessionctx.Layer{Core: l.Core, Leaves: slices.Clone(l.Leaves)}
	p.Resources = slices.Clone(rs)
	return p
}
```

and in `copyProject` add `p.Context.Leaves = slices.Clone(p.Context.Leaves)` and `p.Resources = slices.Clone(p.Resources)`.

`store.go`: add `SetProjectContext(project string, l sessionctx.Layer, rs []sessionctx.Resource) error` next to `SetChatService`.

`memstore.go` / `pgstore.go`: copy `SetChatService`'s body, calling `setProjectContext(p, l, rs)` (pgstore: `setProjectContext(copyProject(p), l, rs)` then `s.putProject`).

- [ ] **Step 4: Run** `go test ./internal/jam/...` (and `just integration-jam` if Postgres is available here; otherwise CI's `store-integration` covers it) — Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): Project.Context and Project.Resources`

---

### Task 4: Jam-wide context (`jam_settings`)

**Files:** Create `internal/jam/migrations/0004_jam_settings.sql`. Modify `store.go`, `memstate.go` (field + getter + setter), `memstore.go`, `pgstore.go` (load, write, `ImportConfig`), `config_snapshot.go` (`ConfigSnapshot.JamContext`, export, `nonEmptyConfigAggregates`, `applyImport`). Test: `storetest/conformance.go`, `internal/jam/config_snapshot_test.go`.

**Interfaces:**
- Produces: `Store.GetJamContext() sessionctx.Layer`, `Store.SetJamContext(l sessionctx.Layer) error` (an empty layer deletes the row); `ConfigSnapshot.JamContext *sessionctx.Layer` (`json:"jam_context,omitempty"`).

- [ ] **Step 1: Write the failing tests**

Conformance subtest:

```go
	t.Run("jam_context_set_get_clear", func(t *testing.T) {
		s := newStore(t)
		if got := s.GetJamContext(); !got.Empty() {
			t.Fatalf("fresh store: %+v", got)
		}
		l := sessionctx.Layer{Core: "Never push to main.", Leaves: []sessionctx.Leaf{{Name: "r.md", ReadWhen: "w", Body: "b"}}}
		if err := s.SetJamContext(l); err != nil {
			t.Fatal(err)
		}
		got := s.GetJamContext()
		if got.Core != l.Core || len(got.Leaves) != 1 {
			t.Fatalf("got %+v", got)
		}
		got.Leaves[0].Name = "mutated.md"
		if s.GetJamContext().Leaves[0].Name != "r.md" {
			t.Fatal("GetJamContext must return a copy")
		}
		if err := s.SetJamContext(sessionctx.Layer{}); err != nil || !s.GetJamContext().Empty() {
			t.Fatalf("clear: err=%v got=%+v", err, s.GetJamContext())
		}
	})
```

`config_snapshot_test.go` (model on the file's existing round-trip test; use the same two-store pattern):

```go
func TestConfigSnapshotCarriesContext(t *testing.T) {
	src := NewMemStore()
	if err := src.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if err := SetRoleContext(src, "default", "dev", sessionctx.Layer{Core: "ROLE"}); err != nil {
		t.Fatal(err)
	}
	if err := src.SetProjectContext("default", sessionctx.Layer{Core: "PROJECT"}, []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "https://x"}}); err != nil {
		t.Fatal(err)
	}
	if err := src.SetJamContext(sessionctx.Layer{Core: "JAM"}); err != nil {
		t.Fatal(err)
	}
	snap := src.ExportConfig()
	dst := NewMemStore()
	if err := dst.ImportConfig(snap); err != nil {
		t.Fatal(err)
	}
	r, _ := dst.GetRole("default", "dev")
	p, _ := dst.GetProject("default")
	if r.Context.Core != "ROLE" || p.Context.Core != "PROJECT" || len(p.Resources) != 1 || dst.GetJamContext().Core != "JAM" {
		t.Fatalf("round trip lost context: role=%q project=%q res=%d jam=%q", r.Context.Core, p.Context.Core, len(p.Resources), dst.GetJamContext().Core)
	}
	if err := NewMemStore().ImportConfig(ConfigSnapshot{Version: ConfigSnapshotVersion, JamContext: &sessionctx.Layer{Core: "J"}}); err != nil {
		t.Fatalf("a jam-context-only snapshot imports: %v", err)
	}
}
```

Add a case to the existing "import refuses a non-empty target" test (find with `grep -n "ErrConfigNotEmpty" internal/jam/*_test.go`): a target holding only a Jam context refuses import.

- [ ] **Step 2: Run** `go test ./internal/jam/ -run 'Conformance|Snapshot'` — Expected: FAIL.

- [ ] **Step 3: Implement**

`migrations/0004_jam_settings.sql`:

```sql
-- Jam-wide settings, one jsonb doc per key. Key 'context' holds the Jam
-- session-context layer (sessionctx.Layer).
CREATE TABLE jam_settings (
    key        text PRIMARY KEY,
    doc        jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
```

`memstate.go`: field `jamContext sessionctx.Layer` (comment: "Jam-wide authored context layer; zero = none"); reads:

```go
// GetJamContext returns a copy of the Jam-wide authored context layer.
func (m *memState) GetJamContext() sessionctx.Layer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return sessionctx.Layer{Core: m.jamContext.Core, Leaves: slices.Clone(m.jamContext.Leaves)}
}

// applySetJamContext replaces the cached layer. Caller holds mu.Lock().
func (m *memState) applySetJamContext(l sessionctx.Layer) {
	m.jamContext = sessionctx.Layer{Core: l.Core, Leaves: slices.Clone(l.Leaves)}
}
```

`memstore.go`: `SetJamContext` locks and calls `applySetJamContext`.

`pgstore.go`: in `load()` after projects:

```go
	var jc []byte
	switch err := s.pool.QueryRow(ctx, `SELECT doc FROM jam_settings WHERE key = 'context'`).Scan(&jc); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("pgstore: load jam_settings: %w", err)
	default:
		if err := json.Unmarshal(jc, &s.jamContext); err != nil {
			return fmt.Errorf("pgstore: decode jam_settings context: %w", err)
		}
	}
```

and:

```go
func (s *PostgresStore) SetJamContext(l sessionctx.Layer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.Empty() {
		if err := s.exec("clearJamContext", `DELETE FROM jam_settings WHERE key = 'context'`); err != nil {
			return err
		}
	} else {
		doc, err := json.Marshal(l)
		if err != nil {
			return err
		}
		if err := s.exec("setJamContext",
			`INSERT INTO jam_settings (key, doc) VALUES ('context', $1)
			 ON CONFLICT (key) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`, doc); err != nil {
			return err
		}
	}
	s.applySetJamContext(l)
	return nil
}
```

(Match `s.exec`'s real signature — read it first; `pgx` is already imported if `ErrNoRows` is used elsewhere, else use the package's existing no-rows idiom.) In `ImportConfig`'s transaction, after destinations: if `snap.JamContext != nil && !snap.JamContext.Empty()`, insert the row the same way.

`config_snapshot.go`: add the field; in `ExportConfig` set `snap.JamContext = &l` when `!m.jamContext.Empty()`; in `nonEmptyConfigAggregates` add `"jam_context"` when non-empty; in `applyImport` set `m.jamContext` from the snapshot (zero when nil). Update the `ConfigSnapshot` and `Store` doc comments that list the aggregates.

- [ ] **Step 4: Run** `go test ./internal/jam/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): Jam-wide session context in jam_settings, exported with config`

---

### Task 5: Admin API and client

**Files:** Create `internal/jam/context_admin.go`, `context_admin_test.go`. Modify `internal/jam/admin.go` (mount next to `registerEgress`), `internal/jam/adminclient/adminclient.go`.

**Interfaces:**
- Produces:
  - `type ContextBody struct { Core string; Leaves []sessionctx.Leaf; Resources []sessionctx.Resource }` (json `core`, `leaves,omitempty`, `resources,omitempty`) — the GET view and PUT body for all three scopes (`Resources` only meaningful for projects; a non-empty `Resources` on role/jam is a 400).
  - Routes: `GET|PUT|DELETE /admin/roles/{project}/{role}/context`, `GET|PUT|DELETE /admin/projects/{project}/context`, `GET|PUT|DELETE /admin/jam/context`. PUT/DELETE → 204.
  - Client: `GetContext(s ContextScope) (jam.ContextBody, error)`, `SetContext(s ContextScope, b jam.ContextBody) error`, `ClearContext(s ContextScope) error`, with `type ContextScope struct { Project, Role string; Jam bool }` in adminclient.

- [ ] **Step 1: Write the failing tests** (`context_admin_test.go`; build the admin mux the way `egress_test.go` does — copy its setup helper):

```go
func TestContextEndpoints(t *testing.T) {
	store, h := /* the egress tests' admin-handler setup */
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/roles/default/dev/context", "/admin/projects/default/context", "/admin/jam/context"} {
		body := `{"core":"C","leaves":[{"name":"a.md","read_when":"w","body":"b"}]}`
		if rec := do(t, h, "PUT", path, body); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT %s = %d %s", path, rec.Code, rec.Body)
		}
		rec := do(t, h, "GET", path, "")
		var got ContextBody
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Core != "C" || len(got.Leaves) != 1 {
			t.Fatalf("GET %s = %s", path, rec.Body)
		}
		if rec := do(t, h, "PUT", path, `{"core":"`+strings.Repeat("x", 1300)+`"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("over-budget PUT %s = %d, want 400", path, rec.Code)
		}
		if rec := do(t, h, "DELETE", path, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d", path, rec.Code)
		}
		rec = do(t, h, "GET", path, "")
		if json.Unmarshal(rec.Body.Bytes(), &got); got.Core != "" {
			t.Errorf("after DELETE %s: %s", path, rec.Body)
		}
	}
	if rec := do(t, h, "PUT", "/admin/projects/default/context", `{"resources":[{"name":"r","kind":"wiki","ref":"x"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad resource kind = %d, want 400", rec.Code)
	}
	if rec := do(t, h, "PUT", "/admin/roles/default/dev/context", `{"resources":[{"name":"r","kind":"url","ref":"x"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("resources on a role = %d, want 400", rec.Code)
	}
	if rec := do(t, h, "PUT", "/admin/projects/ghost/context", `{"core":"C"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}
	if rec := do(t, h, "PUT", "/admin/roles/default/ghost/context", `{"core":"C"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown role = %d, want 404", rec.Code)
	}
}
```

Write the setup and the `do(t, h, method, path, body)` helper concretely from `egress_test.go`'s pattern (the `/* … */` above marks where its two lines go). Add an adminclient round-trip test modeled on the client's egress test (`grep -n "Egress" internal/jam/adminclient/adminclient_test.go`), exercising all three scopes.

- [ ] **Step 2: Run** `go test ./internal/jam/ ./internal/jam/adminclient/ -run Context` — Expected: FAIL.

- [ ] **Step 3: Implement** `context_admin.go`:

```go
package jam

import (
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ContextBody is an authored session-context layer as the admin API reads and
// writes it. Resources apply to projects only.
type ContextBody struct {
	Core      string                `json:"core"`
	Leaves    []sessionctx.Leaf     `json:"leaves,omitempty"`
	Resources []sessionctx.Resource `json:"resources,omitempty"`
}

func (b ContextBody) layer() sessionctx.Layer { return sessionctx.Layer{Core: b.Core, Leaves: b.Leaves} }

func viewOf(l sessionctx.Layer, rs []sessionctx.Resource) ContextBody {
	return ContextBody{Core: l.Core, Leaves: l.Leaves, Resources: rs}
}

// registerContext mounts the authored session-context endpoints for roles,
// projects and the Jam (see docs/usage/jam/session-context.md).
func registerContext(mux *http.ServeMux, store Store, log *slog.Logger) {
	fail := func(w http.ResponseWriter, err error, fallback int) {
		http.Error(w, err.Error(), WriteStatus(err, projectErrStatus(err, fallback)))
	}
	// Roles.
	mux.HandleFunc("GET /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		role, ok := store.GetRole(r.PathValue("project"), r.PathValue("role"))
		if !ok {
			http.Error(w, "role does not exist", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, viewOf(role.Context, nil))
	})
	mux.HandleFunc("PUT /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		var b ContextBody
		if !decode(w, r, &b) {
			return
		}
		if len(b.Resources) > 0 {
			http.Error(w, "resources apply to projects only", http.StatusBadRequest)
			return
		}
		if err := SetRoleContext(store, r.PathValue("project"), r.PathValue("role"), b.layer()); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("admin role context set", "operator", OperatorID(r), "project", r.PathValue("project"), "role", r.PathValue("role"), "core_bytes", len(b.Core), "leaves", len(b.Leaves))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		if err := ClearRoleContext(store, r.PathValue("project"), r.PathValue("role")); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("admin role context cleared", "operator", OperatorID(r), "project", r.PathValue("project"), "role", r.PathValue("role"))
		w.WriteHeader(http.StatusNoContent)
	})
	// Projects.
	mux.HandleFunc("GET /admin/projects/{project}/context", func(w http.ResponseWriter, r *http.Request) {
		p, _ := store.GetProject(r.PathValue("project"))
		writeJSON(w, http.StatusOK, viewOf(p.Context, p.Resources))
	})
	setProject := func(w http.ResponseWriter, r *http.Request, b ContextBody) {
		if err := sessionctx.ValidateLayer(b.layer(), sessionctx.BudgetProject); err != nil {
			http.Error(w, "project context: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := sessionctx.ValidateResources(b.Resources); err != nil {
			http.Error(w, "project resources: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SetProjectContext(r.PathValue("project"), b.layer(), b.Resources); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("admin project context set", "operator", OperatorID(r), "project", r.PathValue("project"), "core_bytes", len(b.Core), "leaves", len(b.Leaves), "resources", len(b.Resources))
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("PUT /admin/projects/{project}/context", func(w http.ResponseWriter, r *http.Request) {
		var b ContextBody
		if decode(w, r, &b) {
			setProject(w, r, b)
		}
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/context", func(w http.ResponseWriter, r *http.Request) {
		setProject(w, r, ContextBody{})
	})
	// The Jam.
	mux.HandleFunc("GET /admin/jam/context", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, viewOf(store.GetJamContext(), nil))
	})
	setJam := func(w http.ResponseWriter, r *http.Request, b ContextBody) {
		if len(b.Resources) > 0 {
			http.Error(w, "resources apply to projects only", http.StatusBadRequest)
			return
		}
		if err := sessionctx.ValidateLayer(b.layer(), sessionctx.BudgetJam); err != nil {
			http.Error(w, "jam context: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SetJamContext(b.layer()); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("admin jam context set", "operator", OperatorID(r), "core_bytes", len(b.Core), "leaves", len(b.Leaves))
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("PUT /admin/jam/context", func(w http.ResponseWriter, r *http.Request) {
		var b ContextBody
		if decode(w, r, &b) {
			setJam(w, r, b)
		}
	})
	mux.HandleFunc("DELETE /admin/jam/context", func(w http.ResponseWriter, r *http.Request) {
		setJam(w, r, ContextBody{})
	})
}
```

(If `WriteStatus(err, fallback)` already maps `ErrProjectNotFound`, simplify `fail`; read `WriteStatus` first.) Mount: `registerContext(mux, store, log)` beside `registerEgress` in `admin.go`. Confirm the admin auth middleware covers `/admin/jam/…` the same as other `/admin/` routes (it wraps the mux; check by reading where `registerEgress`'s mux is wrapped).

adminclient:

```go
// ContextScope names one authored session-context layer: a role (Project+Role),
// a project (Project only) or the Jam (Jam).
type ContextScope struct {
	Project, Role string
	Jam           bool
}

func contextPath(s ContextScope) string {
	switch {
	case s.Jam:
		return "/admin/jam/context"
	case s.Role != "":
		return "/admin/roles/" + url.PathEscape(s.Project) + "/" + url.PathEscape(s.Role) + "/context"
	default:
		return "/admin/projects/" + url.PathEscape(s.Project) + "/context"
	}
}

// GetContext returns the authored context layer for s.
func (c *Client) GetContext(s ContextScope) (jam.ContextBody, error) {
	var out jam.ContextBody
	err := c.do("GET", contextPath(s), nil, &out)
	return out, err
}

// SetContext replaces the authored context layer for s; Jam validates budgets.
func (c *Client) SetContext(s ContextScope, b jam.ContextBody) error {
	return c.do("PUT", contextPath(s), b, nil)
}

// ClearContext removes the authored context layer for s.
func (c *Client) ClearContext(s ContextScope) error {
	return c.do("DELETE", contextPath(s), nil, nil)
}
```

- [ ] **Step 4: Run** `go test ./internal/jam/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): admin API and client for authored session context`

---

### Task 6: CLI `at-jam context` and compile at raise

**Files:** Create `cmd/at-jam/context.go`, `cmd/at-jam/context_test.go`. Modify `cmd/at-jam/main.go` (command table), `internal/jam/supervisor.go`. Test: `internal/jam/supervisor_test.go`.

**Interfaces:**
- Consumes: Task 5 client; Task 1–4 fields.
- Produces: `at-jam context show|set|clear (--role P/R | --project P | --jam) [--file f.yml]`. File format (YAML):

```yaml
core: |
  Ship the release by Friday. Ask in channel:ops before touching prod.
leaves:
  - name: release.md
    read-when: you are cutting a release
    file: release.md        # or body: | … ; file is relative to this YAML
resources:                  # --project only
  - {name: cove, kind: repo, ref: aethons-tools/cove, note: main repo}
```

- [ ] **Step 1: Write the failing tests**

`cmd/at-jam/context_test.go` — the parse step is pure, so test it directly:

```go
func TestParseContextFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.md"), []byte("STEPS"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "core: C\nleaves:\n  - name: release.md\n    read-when: releasing\n    file: release.md\n  - name: inline.md\n    read-when: w\n    body: B\nresources:\n  - {name: cove, kind: repo, ref: aethons-tools/cove}\n"
	b, err := parseContextFile([]byte(src), dir)
	if err != nil {
		t.Fatal(err)
	}
	if b.Core != "C" || len(b.Leaves) != 2 || b.Leaves[0].Body != "STEPS" || b.Leaves[0].ReadWhen != "releasing" || b.Leaves[1].Body != "B" || len(b.Resources) != 1 {
		t.Fatalf("parsed = %+v", b)
	}
	for _, bad := range []string{
		"core: C\nleaves:\n  - name: x.md\n    read-when: w\n    body: B\n    file: f.md\n", // both
		"core: C\nleaves:\n  - name: x.md\n    read-when: w\n    file: ../../etc/passwd\n", // escapes dir
		"core: C\nnope: 1\n", // unknown key
	} {
		if _, err := parseContextFile([]byte(bad), dir); err == nil {
			t.Errorf("want an error for:\n%s", bad)
		}
	}
}

func TestContextScopeFlags(t *testing.T) {
	for args, want := range map[string]adminclient.ContextScope{
		"--role acme/dev": {Project: "acme", Role: "dev"},
		"--role dev":      {Project: jam.DefaultProject, Role: "dev"},
		"--project acme":  {Project: "acme"},
		"--jam":           {Jam: true},
	} {
		got, err := contextScope(strings.Fields(args))
		if err != nil || got != want {
			t.Errorf("%s → %+v, %v; want %+v", args, got, err, want)
		}
	}
	for _, bad := range []string{"", "--jam --project acme", "--role a/b --project c"} {
		if _, err := contextScope(strings.Fields(bad)); err == nil {
			t.Errorf("%q: want exactly one scope", bad)
		}
	}
}
```

(`contextScope` parses only the scope flags from an already-parsed FlagSet's values; shape it as `contextScope(args []string)` over a throwaway FlagSet so the test stays pure, or adjust the test to the helper you write — keep both tests.)

`supervisor_test.go`:

```go
// Authored layers reach the session context; a cleared one vanishes.
func TestRaiseCompilesAuthoredLayers(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	if err := SetRoleContext(store, "default", "guest", sessionctx.Layer{Core: "ROLE-RULES"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectContext("default", sessionctx.Layer{Core: "PROJECT-GOALS"}, []sessionctx.Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetJamContext(sessionctx.Layer{Core: "JAM-RULES"}); err != nil {
		t.Fatal(err)
	}
	raise := func(id string) *sessionctx.Bundle {
		if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Project: "default", Role: "guest", Prompt: "P"}); err != nil {
			t.Fatal(err)
		}
		return fl.gotSpec.Context
	}
	c := raise("w1")
	for _, want := range []string{"ROLE-RULES", "PROJECT-GOALS", "JAM-RULES", "project/resources.md"} {
		if !strings.Contains(c.Core, want) {
			t.Errorf("context missing %q", want)
		}
	}
	if !strings.Contains(c.Files["project/resources.md"], "aethons-tools/cove") {
		t.Error("resources leaf missing")
	}
	if err := store.SetJamContext(sessionctx.Layer{}); err != nil {
		t.Fatal(err)
	}
	if c := raise("w2"); strings.Contains(c.Core, "## Jam") {
		t.Error("a cleared layer must vanish")
	}
}
```

- [ ] **Step 2: Run** `go test ./cmd/at-jam/ ./internal/jam/ -run 'ContextFile|ContextScope|AuthoredLayers'` — Expected: FAIL.

- [ ] **Step 3: Implement**

`supervisor.go`, before `bundle := sessionctx.Compile(in)`:

```go
	if roleOK {
		in.Role = role.Context
	}
	proj := orDefaultProject(spec.Project)
	if p, ok := s.store.GetProject(proj); ok {
		in.Project = sessionctx.ProjectLayer(p.Context, p.Resources)
	}
	in.Jam = s.store.GetJamContext()
```

(`role`/`roleOK` are the values `Raise` already read for egress; check the variable names in place.)

`cmd/at-jam/context.go`:

```go
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminclient"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// contextFile is the YAML an operator writes for `at-jam context set`.
type contextFile struct {
	Core   string `yaml:"core"`
	Leaves []struct {
		Name     string `yaml:"name"`
		ReadWhen string `yaml:"read-when"`
		Body     string `yaml:"body"`
		File     string `yaml:"file"`
	} `yaml:"leaves"`
	Resources []sessionctx.Resource `yaml:"resources"`
}

// parseContextFile decodes a context YAML strictly; a leaf's file is read
// relative to dir and may not escape it.
func parseContextFile(data []byte, dir string) (jam.ContextBody, error) {
	var f contextFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return jam.ContextBody{}, err
	}
	b := jam.ContextBody{Core: f.Core, Resources: f.Resources}
	for _, lf := range f.Leaves {
		body := lf.Body
		switch {
		case lf.Body != "" && lf.File != "":
			return jam.ContextBody{}, fmt.Errorf("leaf %q: set body or file, not both", lf.Name)
		case lf.File != "":
			if !filepath.IsLocal(lf.File) {
				return jam.ContextBody{}, fmt.Errorf("leaf %q: file %q must be a relative path inside %s", lf.Name, lf.File, dir)
			}
			raw, err := os.ReadFile(filepath.Join(dir, lf.File))
			if err != nil {
				return jam.ContextBody{}, fmt.Errorf("leaf %q: %w", lf.Name, err)
			}
			body = string(raw)
		}
		b.Leaves = append(b.Leaves, sessionctx.Leaf{Name: lf.Name, ReadWhen: lf.ReadWhen, Body: body})
	}
	return b, nil
}

// contextScope parses exactly one of --role P/R (P defaults), --project P, --jam.
func contextScope(args []string) (adminclient.ContextScope, error) {
	fs := flag.NewFlagSet("context scope", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	role := fs.String("role", "", "")
	project := fs.String("project", "", "")
	jamWide := fs.Bool("jam", false, "")
	if err := fs.Parse(args); err != nil {
		return adminclient.ContextScope{}, err
	}
	return scopeOf(*role, *project, *jamWide)
}

func scopeOf(role, project string, jamWide bool) (adminclient.ContextScope, error) {
	n := 0
	for _, set := range []bool{role != "", project != "", jamWide} {
		if set {
			n++
		}
	}
	if n != 1 {
		return adminclient.ContextScope{}, errors.New("pass exactly one of --role [project/]role, --project p, --jam")
	}
	switch {
	case jamWide:
		return adminclient.ContextScope{Jam: true}, nil
	case project != "":
		return adminclient.ContextScope{Project: project}, nil
	}
	p, r, ok := strings.Cut(role, "/")
	if !ok {
		p, r = jam.DefaultProject, role
	}
	return adminclient.ContextScope{Project: p, Role: r}, nil
}

func cmdContext(args []string, _ cli.Globals, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "at-jam context: expected show|set|clear")
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("context "+sub, flag.ContinueOnError)
	app := fs.String("app", defaultApp, "settings/token profile")
	adminURLFlag := fs.String("admin-url", "", "Jam admin API URL (overrides the app's settings)")
	token := fs.String("token", adminTokenEnv(stderr), "operator token (env: AT_JAM_ADMIN_TOKEN)")
	role := fs.String("role", "", "[project/]role whose context to manage")
	project := fs.String("project", "", "project whose context (and resources) to manage")
	jamWide := fs.Bool("jam", false, "manage the Jam-wide context")
	file := fs.String("file", "", "context YAML (set only; - = stdin)")
	if _, code, ok := cli.ParseFlags(fs, rest, stdout, stderr); !ok {
		return code
	}
	if err := validateApp(*app); err != nil {
		fmt.Fprintln(stderr, "at-jam context:", err)
		return 2
	}
	scope, err := scopeOf(*role, *project, *jamWide)
	if err != nil {
		fmt.Fprintln(stderr, "at-jam context:", err)
		return 2
	}
	c := adminclient.New(firstNonEmpty(*adminURLFlag, loadSettings(*app).AdminURL, defaultAdminURL), resolveToken(*app, *token, stderr))
	switch sub {
	case "show":
		b, err := c.GetContext(scope)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		out, _ := yaml.Marshal(b)
		stdout.Write(out)
	case "set":
		if *file == "" {
			fmt.Fprintln(stderr, "at-jam context set: --file is required")
			return 2
		}
		data, err := readConfig(*file)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		dir := "."
		if *file != "-" {
			dir = filepath.Dir(*file)
		}
		b, err := parseContextFile(data, dir)
		if err != nil {
			fmt.Fprintln(stderr, "at-jam context set:", err)
			return 1
		}
		if err := c.SetContext(scope, b); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "context set")
	case "clear":
		if err := c.ClearContext(scope); err != nil {
			fmt.Fprintln(stderr, "at-jam:", err)
			return 1
		}
		fmt.Fprintln(stdout, "context cleared")
	default:
		fmt.Fprintf(stderr, "at-jam context: unknown subcommand %q (want show|set|clear)\n", sub)
		return 2
	}
	return 0
}
```

`show` emits YAML; give `jam.ContextBody` yaml tags (`core`, `leaves,omitempty`, `resources,omitempty`) so the output round-trips into `set` (leaves come out with `body:` inline). Register in `main.go`'s table after `egress`: `{Name: "context", Brief: "show, set or clear authored session context for a role, project or the Jam (show|set|clear) via the admin API; applied at the next raise", Run: cmdContext}`. Match `readConfig`'s and `cli.ParseFlags`' real signatures.

- [ ] **Step 4: Run** `go test ./cmd/at-jam/ ./internal/jam/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(jam): at-jam context, and authored layers compiled at raise`

---

### Task 7: Docs

**Files:** `docs/usage/jam/session-context.md` (layer table rows for Project, Role, Jam; drop "follow later"; new **Authoring** section: the YAML format, `at-jam context` commands, budgets and limits, "applied at the next raise", "never put a credential in context"), `docs/usage/jam/projects.md` and `docs/usage/jam/roster.md` (one pointer line each: a project's / role's session context is managed with `at-jam context` — see session-context.md), `docs/usage/jam/backup.md` (config export now includes authored context incl. the Jam-wide layer — check how it lists aggregates), `docs/usage/jam/INDEX.md` (session-context row's read-when gains "authoring role/project/Jam context").

- [ ] **Step 1: Edit** with the docs-author skill; keep `session-context.md` within the leaf budget (split an `authoring` leaf if it would exceed it, with an INDEX row).
- [ ] **Step 2: Verify** docs-audit shows no new findings vs `main`; `just test && just lint` PASS.
- [ ] **Step 3: Commit** `docs(jam): authoring session context for roles, projects and the Jam`

---

## Out of scope

Slice 3b: admin UI panels (role, project and Jam pages) with byte counters and lint; `StudioKit.Notes`; generated `kit/tools.md`. Slice 4: `GET /context` per-turn refresh, change notices, per-kind resume prompts.
