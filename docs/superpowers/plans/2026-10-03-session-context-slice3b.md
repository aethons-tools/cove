# Session context — slice 3b (authoring surfaces: UI panels, kit notes, kit tools) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Operators can edit role, project and Jam-wide session context from the admin UI; kits can ship leaves (`notes:`); every kit layer gains a generated `kit/tools.md` from its build-args.

**Architecture:** The context YAML parser and the checked write paths move from the CLI/handlers into the `jam` package so API, CLI and UI share one parser and one rule set. A shared `context_panel.html` partial renders a layer (core, byte counter, leaves, resources) with a single YAML textarea editor on the role page, the project page and the dashboard. `StudioKit` gains `Notes` (client-side `file:` resolution at `kit push`, like `context-dir`); `sessionctx.KitLayer` composes prompt + notes + generated tools leaf at raise.

**Tech Stack:** Go 1.27, `html/template` + htmx (existing admin UI), `gopkg.in/yaml.v3`.

**Spec:** [`docs/superpowers/specs/2026-10-02-session-context-layers-design.md`](../specs/2026-10-02-session-context-layers-design.md) — slice 3, authoring-surface half. Builds on 3a (#316).

## Global Constraints

- One parser, one rule set: UI and CLI parse with `jam.ParseContextYAML`; API, CLI and UI write through `jam.SetRoleContextChecked` / `SetProjectContextChecked` / `SetJamContextChecked` (400/404 `WriteError`s).
- UI editor = one textarea in the CLI's YAML format; `file:` is refused in the UI (no host filesystem). Per-leaf editing is out of scope.
- Budgets unchanged (Project/Role 1200, Jam 800, Kit 800); the panel shows `N / budget bytes` of the core as a session will receive it (project: including the resources pointer).
- Kit notes: same leaf rules as `ValidateLayer` (≤ 20, names, read-when ≤ 160 B, ≤ 64 KiB); reserved name `tools.md`. Notes are a raise-time input — **excluded from the build digest** (like `prompt`).
- `kit/tools.md`: one row per build-arg, sorted; keys ending `_VERSION` render as `<tool> <value>` (tool = lowercased prefix, `_` → `-`); other keys raw. Build-args are never secrets (Validate already rejects collisions).
- Writes from the UI go through the existing `guardWrite` (Origin CSRF) and log the operator; no secret values anywhere.
- TDD; hermetic; docs in the same PR; `GOPROXY=https://proxy.golang.org,direct` here.

## Review Focus

1. **A UI post with `file:` in a leaf** — expected: 400 "file is not allowed here", nothing stored. Pinned in Task 1.
2. **Saving the YAML shown by the panel unchanged** — expected: no-op round trip (same stored layer). Pinned in Task 2.
3. **A kit note named `tools.md` or with an escaping `file:`** — expected: `kit push` refuses. Pinned in Task 4.
4. **A kit-prompt-only or notes-only edit** — expected: build digest unchanged (no image rebuild). Pinned in Task 4.
5. **A cross-origin POST to a context route** — expected: refused like other UI writes. Pinned in Task 2.

---

## File structure

| File | Responsibility |
|------|----------------|
| `internal/jam/contextyaml.go` (create), `contextyaml_test.go` (create) | `ParseContextYAML`, `MarshalContextYAML`, `Set*ContextChecked` |
| `internal/jam/context_admin.go` (modify) | handlers call the checked writers |
| `cmd/at-jam/context.go`, `context_test.go` (modify) | CLI uses `jam.ParseContextYAML` with a dir-bounded reader |
| `internal/jam/adminui/templates/context_panel.html` (create) | shared panel partial |
| `internal/jam/adminui/context_edit.go` (create), `context_edit_test.go` (create) | panel data + POST/DELETE routes |
| `internal/jam/adminui/adminui.go`, `role_detail.go`, `projects.go`, templates `role.html`, `project.html`, `dashboard.html` | wire the panel |
| `internal/studio/studiokit.go`, `notes.go` (create), tests | `Notes`, `ResolveNoteFiles`, `CheckNotes` |
| `cmd/at-jam/main.go` (kit push) | resolve + check notes |
| `internal/jam/sessionctx/kit.go` (create), `kit_test.go` (create) | `KitLayer(prompt, notes, buildArgs)` |
| `internal/jam/supervisor.go` | use `KitLayer` |
| Docs: `docs/usage/jam/kits.md`, `ui-pages.md`, `session-context.md`, `session-context-authoring.md` | |

---

### Task 1: Shared context YAML and checked writers

**Files:** Create `internal/jam/contextyaml.go`, `internal/jam/contextyaml_test.go`. Modify `internal/jam/context_admin.go`, `cmd/at-jam/context.go`, `cmd/at-jam/context_test.go`.

**Interfaces:**
- Produces:
  - `func ParseContextYAML(data []byte, readFile func(name string) ([]byte, error)) (ContextBody, error)` — strict (unknown keys refused); a leaf sets `body` or `file`, not both; `readFile == nil` refuses `file:` with "file is not allowed here"; a body that sets nothing refuses with a pointer to clear.
  - `func MarshalContextYAML(b ContextBody) ([]byte, error)`
  - `func SetRoleContextChecked(store Store, project, role string, b ContextBody) error`
  - `func SetProjectContextChecked(store Store, project string, b ContextBody) error`
  - `func SetJamContextChecked(store Store, b ContextBody) error` — an empty body clears.
- The CLI keeps `parseContextFile(data, dir)` as a thin wrapper (reader enforcing `filepath.IsLocal`), so its tests stay.

- [ ] **Step 1: Write the failing tests** (`contextyaml_test.go`)

```go
package jam

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestParseContextYAML(t *testing.T) {
	src := "core: C\nleaves:\n  - name: a.md\n    read-when: w\n    body: B\nresources:\n  - {name: cove, kind: repo, ref: aethons-tools/cove}\n"
	b, err := ParseContextYAML([]byte(src), nil)
	if err != nil || b.Core != "C" || len(b.Leaves) != 1 || b.Leaves[0].Body != "B" || len(b.Resources) != 1 {
		t.Fatalf("parse = %+v, %v", b, err)
	}
	out, err := MarshalContextYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseContextYAML(out, nil)
	if err != nil || again.Core != b.Core || again.Leaves[0] != b.Leaves[0] || again.Resources[0] != b.Resources[0] {
		t.Fatalf("round trip = %+v, %v\n%s", again, err, out)
	}
	if _, err := ParseContextYAML([]byte("core: C\nleaves:\n  - name: a.md\n    read-when: w\n    file: a.md\n"), nil); err == nil || !strings.Contains(err.Error(), "not allowed here") {
		t.Errorf("file without a reader: %v", err)
	}
	read := func(name string) ([]byte, error) {
		if name == "a.md" {
			return []byte("FROM FILE"), nil
		}
		return nil, errors.New("nope")
	}
	if b, err := ParseContextYAML([]byte("core: C\nleaves:\n  - name: a.md\n    read-when: w\n    file: a.md\n"), read); err != nil || b.Leaves[0].Body != "FROM FILE" {
		t.Errorf("file with a reader = %+v, %v", b, err)
	}
	for _, bad := range []string{"", "core: \"\"\n", "nope: 1\n", "core: C\nleaves:\n  - {name: a.md, read-when: w, body: B, file: a.md}\n"} {
		if _, err := ParseContextYAML([]byte(bad), read); err == nil {
			t.Errorf("want an error for %q", bad)
		}
	}
}

func TestCheckedWriters(t *testing.T) {
	store := NewMemStore()
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if err := SetRoleContextChecked(store, "default", "dev", ContextBody{Resources: []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "x"}}}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Errorf("resources on a role = %v, want 400", err)
	}
	if err := SetProjectContextChecked(store, "ghost", ContextBody{Core: "C"}); WriteStatus(err, 0) != http.StatusNotFound {
		t.Errorf("unknown project = %v, want 404", err)
	}
	if err := SetProjectContextChecked(store, "default", ContextBody{Leaves: []sessionctx.Leaf{{Name: sessionctx.ResourcesLeaf, ReadWhen: "w"}}}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Errorf("reserved resources.md = %v, want 400", err)
	}
	if err := SetJamContextChecked(store, ContextBody{Core: strings.Repeat("x", sessionctx.BudgetJam+1)}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Errorf("over budget = %v, want 400", err)
	}
	if err := SetJamContextChecked(store, ContextBody{Core: "J"}); err != nil || store.GetJamContext().Core != "J" {
		t.Fatalf("set jam: %v", err)
	}
	if err := SetJamContextChecked(store, ContextBody{}); err != nil || !store.GetJamContext().Empty() {
		t.Fatalf("clear jam: %v", err)
	}
}
```

(`ProjectErrStatus`-style mapping: `SetProjectContextChecked` must return a 404 `WriteError` for `ErrProjectNotFound` — wrap it with `writeErr(projectErrStatus(err, 500), …)`.)

- [ ] **Step 2: Run** `go test ./internal/jam/ -run 'ParseContextYAML|CheckedWriters'` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement** `contextyaml.go`: move `contextFile`'s struct and the parse body from `cmd/at-jam/context.go` into `ParseContextYAML`, replacing the `os.ReadFile(filepath.Join(dir, …))` with `readFile(lf.File)` (nil → `fmt.Errorf("leaf %q: file is not allowed here; inline the body", lf.Name)`). `MarshalContextYAML` = `yaml.Marshal(b)`. Move the validation now inline in `context_admin.go`'s `setProject`/`setJam`/role PUT into the three `Set*ContextChecked` functions (returning `writeErr(400, …)` / mapped 404), and make the handlers: decode → checked writer → `http.Error(w, err.Error(), WriteStatus(err, 500))` on error → log → 204. In `cmd/at-jam/context.go`, `parseContextFile(data, dir)` becomes:

```go
// parseContextFile parses a context YAML whose leaf files are read relative to
// dir and may not escape it.
func parseContextFile(data []byte, dir string) (jam.ContextBody, error) {
	return jam.ParseContextYAML(data, func(name string) ([]byte, error) {
		if !filepath.IsLocal(name) {
			return nil, fmt.Errorf("file %q must be a relative path inside %s", name, dir)
		}
		return os.ReadFile(filepath.Join(dir, name))
	})
}
```

and `show` prints `jam.MarshalContextYAML(b)`.

- [ ] **Step 4: Run** `go test ./internal/jam/... ./cmd/at-jam/` — Expected: PASS (3a's endpoint and CLI tests unchanged).
- [ ] **Step 5: Commit** `refactor(jam): one context YAML parser and checked writers for API, CLI and UI`

---

### Task 2: The context panel and the role page

**Files:** Create `internal/jam/adminui/templates/context_panel.html`, `internal/jam/adminui/context_edit.go`, `internal/jam/adminui/context_edit_test.go`. Modify `adminui.go` (`pages`: add `context_panel.html` to `role`, `project`, `dashboard`; call `registerContextEdits`), `role_detail.go` (`roleDetail.Context contextPanel`), `templates/role.html` (include the panel after Egress).

**Interfaces:**
- Produces:
  - `type contextPanel struct { Scope, Action, Target, ID, Core string; CoreBytes, Budget int; Over, Empty, ShowResources bool; Leaves []sessionctx.Leaf; Resources []sessionctx.Resource; YAML string }`
  - `func newContextPanel(scope, action, target string, l sessionctx.Layer, rs []sessionctx.Resource, budget int, showResources bool) contextPanel` — `CoreBytes` is the delivered core (`ProjectLayer` applied when `rs` is non-empty); `YAML` = `jam.MarshalContextYAML` of the stored body (empty string when the layer is empty).
  - Template `{{define "context-panel"}}` taking a `contextPanel`.
  - Routes: `POST|DELETE /ui/roles/{project}/{name}/context` (field `yaml`) → re-render `role-body`.

- [ ] **Step 1: Write the failing tests** (`context_edit_test.go`)

```go
package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func roleStore(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "review", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRoleContextPanelShowsAndEdits(t *testing.T) {
	store := roleStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	body := get(t, h, "/ui/roles/acme/review").Body.String()
	for _, want := range []string{"Session context", `hx-post="/ui/roles/acme/review/context"`, "0 / 1200 bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("role page missing %q", want)
		}
	}
	yml := "core: Review every PR within a day.\nleaves:\n  - name: style.md\n    read-when: you are commenting on style\n    body: Prefer small diffs.\n"
	rec := post(t, h, "/ui/roles/acme/review/context", url.Values{"yaml": {yml}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="role"`) {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	r, _ := store.GetRole("acme", "review")
	if r.Context.Core != "Review every PR within a day." || len(r.Context.Leaves) != 1 {
		t.Fatalf("stored = %+v", r.Context)
	}
	page := get(t, h, "/ui/roles/acme/review").Body.String()
	for _, want := range []string{"Review every PR within a day.", "style.md", "you are commenting on style", "29 / 1200 bytes"} {
		if !strings.Contains(page, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	// Saving the YAML the panel shows is a no-op.
	shown, _ := jam.MarshalContextYAML(jam.ContextBody{Core: r.Context.Core, Leaves: r.Context.Leaves})
	if rec := post(t, h, "/ui/roles/acme/review/context", url.Values{"yaml": {string(shown)}}); rec.Code != http.StatusOK {
		t.Fatalf("re-save = %d", rec.Code)
	}
	if again, _ := store.GetRole("acme", "review"); again.Context.Core != r.Context.Core || again.Context.Leaves[0] != r.Context.Leaves[0] {
		t.Fatalf("round trip changed the layer: %+v", again.Context)
	}
	for name, form := range map[string]url.Values{
		"file leaf":   {"yaml": {"core: C\nleaves:\n  - {name: a.md, read-when: w, file: a.md}\n"}},
		"over budget": {"yaml": {"core: " + strings.Repeat("x", 1300) + "\n"}},
		"empty":       {"yaml": {""}},
	} {
		if rec := post(t, h, "/ui/roles/acme/review/context", form); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if rec := del(t, h, "/ui/roles/acme/review/context"); rec.Code != http.StatusOK {
		t.Fatalf("clear = %d", rec.Code)
	}
	if r, _ := store.GetRole("acme", "review"); !r.Context.Empty() {
		t.Fatalf("cleared = %+v", r.Context)
	}
}

func TestContextEditsRefuseCrossOrigin(t *testing.T) {
	h := adminui.Handler(roleStore(t), testLogger(), nil, nil, credAny, nil)
	for _, path := range []string{"/ui/roles/acme/review/context", "/ui/projects/acme/context", "/ui/jam/context"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("yaml=core%3A+C"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s cross-origin = %d, want 403", path, rec.Code)
		}
	}
}
```

(Add `net/http/httptest` to the imports.) `"29 / 1200 bytes"` is `len("Review every PR within a day.")`.

- [ ] **Step 2: Run** `go test ./internal/jam/adminui/ -run 'RoleContextPanel|ContextEditsRefuse'` — Expected: FAIL.

- [ ] **Step 3: Implement**

`templates/context_panel.html`:

```html
{{define "context-panel"}}
<section class="card full"{{if .ID}} id="{{.ID}}"{{end}}>
  <header><h2>Session context</h2><span class="sub">{{.Scope}} — what every session is told at raise</span></header>
  <div class="body">
    <p class="facts"><span class="chip{{if .Over}} bad{{end}}">{{.CoreBytes}} / {{.Budget}} bytes</span>{{if .Leaves}}<span class="chip">{{len .Leaves}} leaves</span>{{end}}{{if .Resources}}<span class="chip">{{len .Resources}} resources</span>{{end}}</p>
    {{if .Empty}}<span class="unset">None — sessions get no {{.Scope}} section.</span>{{else}}
    {{if .Core}}<pre class="ctx-core">{{.Core}}</pre>{{end}}
    {{if .Leaves}}<ul class="ctx-leaves">{{range .Leaves}}<li><span class="mono">{{.Name}}</span> — read when {{.ReadWhen}}</li>{{end}}</ul>{{end}}
    {{if .Resources}}<table><thead><tr><th>Name</th><th>Kind</th><th>Ref</th><th>Note</th></tr></thead><tbody>{{range .Resources}}<tr><td>{{.Name}}</td><td>{{.Kind}}</td><td class="mono">{{.Ref}}</td><td>{{.Note}}</td></tr>{{end}}</tbody></table>{{end}}
    {{end}}
  </div>
  <details class="edit">
    <summary>Edit session context</summary>
    <form hx-post="{{.Action}}" hx-target="#{{.Target}}" hx-swap="outerHTML">
      <label class="wide">YAML <span class="hint"><code>core</code>, <code>leaves</code> (<code>name</code>, <code>read-when</code>, <code>body</code>){{if .Resources}}, <code>resources</code>{{end}} — the <code>at-jam context</code> file format, bodies inline. Never put a credential here.</span>
        <textarea name="yaml" rows="14" spellcheck="false">{{.YAML}}</textarea></label>
      <div class="form-actions">
        {{if not .Empty}}<button type="button" class="danger left" hx-delete="{{.Action}}" hx-target="#{{.Target}}" hx-swap="outerHTML" hx-confirm="Remove this {{.Scope}} session context?">Clear</button>{{end}}
        <button type="submit" class="primary">Save context</button>
      </div>
    </form>
  </details>
</section>
{{end}}
```

In the hint, use `{{if .ShowResources}}` instead of `{{if .Resources}}` so project pages always mention `resources`.

`context_edit.go`:

```go
package adminui

import (
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// contextPanel is the shared "Session context" card: the stored layer, the
// size of the core a session receives against its budget, and the YAML editor.
type contextPanel struct {
	Scope, Action, Target string // Target: the hx-target element id (#role, #project, #jam-context)
	ID                    string // the section's own id; set for the Jam panel, which swaps itself
	Core                  string
	CoreBytes, Budget     int
	Over, Empty           bool
	ShowResources         bool
	Leaves                []sessionctx.Leaf
	Resources             []sessionctx.Resource
	YAML                  string
}

func newContextPanel(scope, action, target string, l sessionctx.Layer, rs []sessionctx.Resource, budget int, showResources bool) contextPanel {
	delivered := sessionctx.ProjectLayer(l, rs) // a no-op without resources
	p := contextPanel{
		Scope: scope, Action: action, Target: target, Core: strings.TrimSpace(l.Core),
		CoreBytes: len(strings.TrimSpace(delivered.Core)), Budget: budget,
		Leaves: l.Leaves, Resources: rs, ShowResources: showResources,
		Empty: l.Empty() && len(rs) == 0,
	}
	p.Over = p.CoreBytes > budget
	if !p.Empty {
		if y, err := jam.MarshalContextYAML(jam.ContextBody{Core: l.Core, Leaves: l.Leaves, Resources: rs}); err == nil {
			p.YAML = string(y)
		}
	}
	return p
}

// parseContextForm reads the panel's YAML; the UI can't read host files.
func parseContextForm(r *http.Request) (jam.ContextBody, error) {
	b, err := jam.ParseContextYAML([]byte(r.FormValue("yaml")), nil)
	if err != nil {
		return jam.ContextBody{}, &jam.WriteError{Status: http.StatusBadRequest, Msg: err.Error()}
	}
	return b, nil
}
```

(Match `jam.WriteError`'s real field names — `roleedit.go` constructs one as `&WriteError{Status: …, Msg: …}`.)

`role_detail.go`: add `Context contextPanel` to `roleDetail`; in `buildRoleDetail` set
`d.Context = newContextPanel("role", "/ui/roles/"+project+"/"+name+"/context", "role", role.Context, nil, sessionctx.BudgetRole, false)`.
`role.html`: after the Egress `</section>`, `{{template "context-panel" .Context}}` (Target `role`, no ID).
`role_edit.go` (inside `registerRoleEdits`, using its `edit` wrapper):

```go
	mux.HandleFunc("POST /ui/roles/{project}/{name}/context", edit("context set", func(r *http.Request, project, name string) error {
		b, err := parseContextForm(r)
		if err != nil {
			return err
		}
		return jam.SetRoleContextChecked(store, project, name, b)
	}))
	mux.HandleFunc("DELETE /ui/roles/{project}/{name}/context", edit("context cleared", func(r *http.Request, project, name string) error {
		return jam.ClearRoleContext(store, project, name)
	}))
```

`adminui.go` `pages`: add `"context_panel.html"` to the `role`, `project` and `dashboard` sets.

- [ ] **Step 4: Run** `go test ./internal/jam/adminui/` — Expected: the role test PASSES; the cross-origin test still FAILS for the project and Jam paths (Task 3 adds them) — temporarily run `-run RoleContextPanel` and keep the cross-origin test for Task 3's Step 4.
- [ ] **Step 5: Commit** `feat(adminui): session context panel on the role page`

---

### Task 3: Project panel and the Jam panel on the dashboard

**Files:** Modify `internal/jam/adminui/projects.go` (`projectDetail.Context`), `project_edit.go` (routes), `templates/project.html`, `adminui.go` (dashboard data + Jam routes), `templates/dashboard.html`. Test: `context_edit_test.go`.

**Interfaces:**
- Produces: `POST|DELETE /ui/projects/{name}/context` → re-render `project-body` (panel Target `project`); `POST|DELETE /ui/jam/context` → `renderFragment(w, "dashboard", "context-panel", jamPanel(store))` (panel Target and ID both `jam-context`, so it swaps itself).

- [ ] **Step 1: Write the failing tests**

```go
func TestProjectAndJamContextPanels(t *testing.T) {
	store := roleStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	yml := "core: Ship 1.0.\nresources:\n  - {name: cove, kind: repo, ref: aethons-tools/cove, note: main repo}\n"
	if rec := post(t, h, "/ui/projects/acme/context", url.Values{"yaml": {yml}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="project"`) {
		t.Fatalf("project save = %d: %s", rec.Code, rec.Body.String())
	}
	if p, _ := store.GetProject("acme"); p.Context.Core != "Ship 1.0." || len(p.Resources) != 1 {
		t.Fatalf("stored = %+v", p)
	}
	page := get(t, h, "/ui/projects/acme").Body.String()
	for _, want := range []string{"Ship 1.0.", "aethons-tools/cove", "main repo", `hx-post="/ui/projects/acme/context"`} {
		if !strings.Contains(page, want) {
			t.Errorf("project panel missing %q", want)
		}
	}
	if rec := post(t, h, "/ui/projects/ghost/context", url.Values{"yaml": {"core: C\n"}}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}

	dash := get(t, h, "/ui/").Body.String()
	for _, want := range []string{`id="jam-context"`, `hx-post="/ui/jam/context"`, "0 / 800 bytes"} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	rec := post(t, h, "/ui/jam/context", url.Values{"yaml": {"core: Never push to main.\n"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Never push to main.") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("jam save = %d: %s", rec.Code, rec.Body.String())
	}
	if store.GetJamContext().Core != "Never push to main." {
		t.Fatal("jam context not stored")
	}
	if rec := del(t, h, "/ui/jam/context"); rec.Code != http.StatusOK || !store.GetJamContext().Empty() {
		t.Fatalf("jam clear = %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/jam/adminui/ -run 'ProjectAndJam'` — Expected: FAIL.

- [ ] **Step 3: Implement**
  - `projects.go`: `projectDetail.Context contextPanel`; in `buildProjectDetail`: `newContextPanel("project", "/ui/projects/"+name+"/context", "project", p.Context, p.Resources, sessionctx.BudgetProject, true)`; `project.html`: `{{template "context-panel" .Context}}` after the Chat service card.
  - `project_edit.go`: inside `registerProjectEdits` with its wrapper: POST → `parseContextForm` then `jam.SetProjectContextChecked(store, name, b)`; DELETE → `jam.SetProjectContextChecked(store, name, jam.ContextBody{})`.
  - `adminui.go`: a `jamPanel(store)` helper → `p := newContextPanel("Jam-wide", "/ui/jam/context", "jam-context", store.GetJamContext(), nil, sessionctx.BudgetJam, false); p.ID = "jam-context"`; pass it as `"JamContext"` in the dashboard data; register `POST|DELETE /ui/jam/context` (guardWrite, ParseForm, `parseContextForm`, `jam.SetJamContextChecked`, log `"ui jam context set|cleared"` with operator, then `renderFragment(w, "dashboard", "context-panel", jamPanel(store))`; on error `renderError(w, jam.WriteStatus(err, 500), err.Error())`).
  - `dashboard.html`: `{{template "context-panel" .JamContext}}` after the tiles.

- [ ] **Step 4: Run** `go test ./internal/jam/adminui/` — Expected: PASS, including `TestContextEditsRefuseCrossOrigin`.
- [ ] **Step 5: Commit** `feat(adminui): project and Jam-wide session context panels`

---

### Task 4: `StudioKit.Notes`

**Files:** Create `internal/studio/notes.go`, `internal/studio/notes_test.go`. Modify `internal/studio/studiokit.go` (field), `cmd/at-jam/main.go` (kit push), `internal/jam/studiokit.go` (`ensureStudioKit`: `CheckNotes`).

**Interfaces:**
- Produces: `type KitNote struct { Name, ReadWhen, Body string; File string }` (yaml `name`, `read-when`, `body,omitempty`, `file,omitempty`; json `name`, `read_when`, `body`, File `json:"-"`); `StudioKit.Notes []KitNote` (yaml/json `notes,omitempty`); `func (sk *StudioKit) ResolveNoteFiles(dir string) error`; `func (sk StudioKit) CheckNotes() error`; `const ToolsLeaf = "tools.md"` in sessionctx (Task 5 uses it; define it here in `sessionctx/kit.go` as a one-line file if Task 5 hasn't run).

- [ ] **Step 1: Write the failing tests** (`notes_test.go`)

```go
package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotesParseResolveAndCheck(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.md"), []byte("STEPS"), 0o644); err != nil {
		t.Fatal(err)
	}
	sk, err := ParseStudioKit([]byte("kind: studio\nnotes:\n  - name: release.md\n    read-when: you are releasing\n    file: release.md\n  - name: style.md\n    read-when: w\n    body: B\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sk.ResolveNoteFiles(dir); err != nil {
		t.Fatal(err)
	}
	if sk.Notes[0].Body != "STEPS" || sk.Notes[0].File != "" || sk.Notes[1].Body != "B" {
		t.Fatalf("notes = %+v", sk.Notes)
	}
	if err := sk.CheckNotes(); err != nil {
		t.Fatal(err)
	}
	j, _ := sk.ToJSON()
	if strings.Contains(string(j), "release.md\",\"file") || !strings.Contains(string(j), "STEPS") {
		t.Fatalf("stored JSON must carry bodies, not files: %s", j)
	}
	for _, bad := range []StudioKit{
		{Kind: Kind, Notes: []KitNote{{Name: "tools.md", ReadWhen: "w"}}},
		{Kind: Kind, Notes: []KitNote{{Name: "x.md"}}},
		{Kind: Kind, Notes: []KitNote{{Name: "../x.md", ReadWhen: "w"}}},
	} {
		if err := bad.CheckNotes(); err == nil {
			t.Errorf("want an error for %+v", bad.Notes)
		}
	}
	esc := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "x.md", ReadWhen: "w", File: "../../etc/passwd"}}}
	if err := esc.ResolveNoteFiles(dir); err == nil {
		t.Error("an escaping note file must be refused")
	}
	both := StudioKit{Kind: Kind, Notes: []KitNote{{Name: "x.md", ReadWhen: "w", File: "release.md", Body: "B"}}}
	if err := both.ResolveNoteFiles(dir); err == nil {
		t.Error("body and file together must be refused")
	}
}

func TestNotesDoNotChangeBuildDigest(t *testing.T) {
	a := StudioKit{Kind: Kind, Egress: []string{"github.com"}}
	b := a
	b.Prompt, b.Notes = "P", []KitNote{{Name: "n.md", ReadWhen: "w", Body: "B"}}
	if BuildDigest(a) != BuildDigest(b) {
		t.Fatal("prompt/notes are raise-time inputs; the build digest must ignore them")
	}
}
```

In `internal/jam` (e.g. `studiokit_test.go`), add: `PushStudioKit` with a note named `tools.md` → 400.

- [ ] **Step 2: Run** `go test ./internal/studio/ ./internal/jam/ -run 'Notes|PushStudioKit'` — Expected: FAIL.

- [ ] **Step 3: Implement** `notes.go`:

```go
package studio

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// KitNote is one leaf a kit ships into its sessions' context (kit/<name>).
// File is a CLIENT-ONLY authoring convenience resolved into Body at push.
type KitNote struct {
	Name     string `yaml:"name" json:"name"`
	ReadWhen string `yaml:"read-when" json:"read_when"`
	Body     string `yaml:"body,omitempty" json:"body"`
	File     string `yaml:"file,omitempty" json:"-"`
}

// ResolveNoteFiles reads each note's file (relative to dir, never escaping it)
// into its body. A note may set body or file, not both.
func (sk *StudioKit) ResolveNoteFiles(dir string) error {
	for i, n := range sk.Notes {
		if n.File == "" {
			continue
		}
		if n.Body != "" {
			return fmt.Errorf("note %q: set body or file, not both", n.Name)
		}
		if !filepath.IsLocal(n.File) {
			return fmt.Errorf("note %q: file %q must be a relative path inside %s", n.Name, n.File, dir)
		}
		raw, err := os.ReadFile(filepath.Join(dir, n.File))
		if err != nil {
			return fmt.Errorf("note %q: %w", n.Name, err)
		}
		sk.Notes[i].Body, sk.Notes[i].File = string(raw), ""
	}
	return nil
}

// Leaves returns the notes as session-context leaves.
func (sk StudioKit) Leaves() []sessionctx.Leaf {
	out := make([]sessionctx.Leaf, len(sk.Notes))
	for i, n := range sk.Notes {
		out[i] = sessionctx.Leaf{Name: n.Name, ReadWhen: n.ReadWhen, Body: n.Body}
	}
	return out
}

// CheckNotes applies the authored-leaf rules (an authoring check, on push
// only, like CheckPrompt); tools.md is reserved for the generated tool list.
func (sk StudioKit) CheckNotes() error {
	for _, n := range sk.Notes {
		if n.Name == sessionctx.ToolsLeaf {
			return fmt.Errorf("studio kit: note name %s is reserved for the generated tool list", sessionctx.ToolsLeaf)
		}
	}
	if err := sessionctx.ValidateLayer(sessionctx.Layer{Leaves: sk.Leaves()}, 0); err != nil {
		return fmt.Errorf("studio kit: notes: %w", err)
	}
	return nil
}
```

`studiokit.go`: add `Notes []KitNote \`yaml:"notes,omitempty" json:"notes,omitempty"\`` after `Prompt`; update the struct/BuildDigest comments ("prompt and notes are raise-time inputs"). `cmd/at-jam/main.go` kit push: after `ResolveContextDir`, `sk.ResolveNoteFiles(baseDir)` then `sk.CheckNotes()` (each error → `at-jam kit push: …`, exit 1). `internal/jam/studiokit.go` `ensureStudioKit`: `CheckNotes()` after `CheckPrompt()` → 400.

- [ ] **Step 4: Run** `go test ./internal/studio/ ./internal/jam/... ./cmd/at-jam/` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(studio): kit notes — leaves a kit ships into its sessions' context`

---

### Task 5: Kit layer = prompt + notes + generated `tools.md`

**Files:** Create `internal/jam/sessionctx/kit.go`, `kit_test.go`. Modify `internal/jam/supervisor.go`. Test: `internal/jam/supervisor_test.go`.

**Interfaces:**
- Produces: `const ToolsLeaf = "tools.md"` (if not added in Task 4); `func KitLayer(prompt string, notes []Leaf, buildArgs map[string]string) Layer`.

- [ ] **Step 1: Write the failing tests**

```go
package sessionctx

import (
	"strings"
	"testing"
)

func TestKitLayer(t *testing.T) {
	l := KitLayer("Work on cove.", []Leaf{{Name: "release.md", ReadWhen: "w", Body: "B"}}, map[string]string{"GO_VERSION": "1.27.1", "HADOLINT_VERSION": "v2.15.1", "CC_SKILLS_GOLANG_VERSION": "2.0.0", "PROFILE": "ci"})
	if l.Core != "Work on cove." || len(l.Leaves) != 2 || l.Leaves[0].Name != "release.md" || l.Leaves[1].Name != ToolsLeaf {
		t.Fatalf("layer = %+v", l)
	}
	tools := l.Leaves[1].Body
	for _, want := range []string{"| cc-skills-golang | 2.0.0 |", "| go | 1.27.1 |", "| hadolint | v2.15.1 |", "| PROFILE | ci |"} {
		if !strings.Contains(tools, want) {
			t.Errorf("tools.md missing %q:\n%s", want, tools)
		}
	}
	if strings.Index(tools, "cc-skills-golang") > strings.Index(tools, "| go |") {
		t.Error("rows must be sorted")
	}
	if got := KitLayer("", nil, nil); !got.Empty() {
		t.Fatalf("nothing in, nothing out: %+v", got)
	}
}
```

`supervisor_test.go`: raise with a studio kit that has `BuildArgs{"GO_VERSION": "1.27.1"}` and a note → `Context.Files["kit/tools.md"]` contains `| go | 1.27.1 |` and `Context.Files["kit/<note>"]` exists (model on `TestRaiseCompilesContext`).

- [ ] **Step 2: Run** `go test ./internal/jam/sessionctx/ ./internal/jam/ -run 'KitLayer|RaiseCompiles'` — Expected: FAIL.

- [ ] **Step 3: Implement** `kit.go`:

```go
package sessionctx

import (
	"fmt"
	"slices"
	"strings"
)

// ToolsLeaf is the kit leaf generated from the kit's build-args.
const ToolsLeaf = "tools.md"

// KitLayer is the kit's layer: its prompt as the core, its notes as leaves,
// plus a generated tools.md listing the build-args the image was built with
// (X_VERSION keys render as the tool "x").
func KitLayer(prompt string, notes []Leaf, buildArgs map[string]string) Layer {
	l := Layer{Core: prompt, Leaves: slices.Clone(notes)}
	if len(buildArgs) == 0 {
		return l
	}
	type row struct{ tool, value string }
	var rows []row
	for k, v := range buildArgs {
		tool := k
		if p, ok := strings.CutSuffix(k, "_VERSION"); ok && p != "" {
			tool = strings.ReplaceAll(strings.ToLower(p), "_", "-")
		}
		rows = append(rows, row{tool, v})
	}
	slices.SortFunc(rows, func(a, b row) int { return strings.Compare(a.tool, b.tool) })
	var b strings.Builder
	b.WriteString("Tools and versions this kit's image was built with (its build-args):\n\n| Tool | Version / value |\n|------|-----------------|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", oneLine(r.tool), oneLine(r.value))
	}
	l.Leaves = append(l.Leaves, Leaf{Name: ToolsLeaf, ReadWhen: "you need which tool versions the image has", Body: b.String()})
	return l
}
```

`supervisor.go`: replace `in.Kit = sessionctx.Layer{Core: def.Kit.Prompt}` with `in.Kit = sessionctx.KitLayer(def.Kit.Prompt, def.Kit.Leaves(), def.Kit.BuildArgs)`.

- [ ] **Step 4: Run** `go test ./internal/jam/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(sessionctx): kit layer carries notes and a generated tools.md`

---

### Task 6: Docs

- [ ] **Step 1: Edit** (docs-author skill):
  - `docs/usage/jam/kits.md`: field table row `notes` — "Leaves the kit ships into its sessions' [context](session-context.md) (`name`, `read-when`, `body` or a `file` relative to the kit file, read at `kit push`); `tools.md` is reserved — the kit layer always gains a generated `kit/tools.md` from `build-args`. Raise-time input: editing notes does not rebuild the image."; add `notes:` to the example.
  - `docs/usage/jam/ui-pages.md`: role, project and dashboard pages gain a **Session context** card (core, `N / budget bytes`, leaves, resources; YAML editor in the `at-jam context` format; Clear).
  - `docs/usage/jam/session-context.md`: Kit row source → "the studio kit's `prompt`, its `notes` as leaves, and a generated `kit/tools.md`".
  - `docs/usage/jam/session-context-authoring.md`: "or edit it in the admin UI's Session context card (same YAML; `file:` isn't available there)".
  - Bump `updated:`.
- [ ] **Step 2: Verify** docs-audit (no new findings vs `main`), `just test && just lint`.
- [ ] **Step 3: Commit** `docs(jam): context panels, kit notes and kit tools`

---

## Out of scope

Per-leaf editing in the UI; lint warnings in the UI at save time (a 3a deferred minor); slice 4 (`GET /context` per-turn refresh, change notices, per-kind resume prompts).
