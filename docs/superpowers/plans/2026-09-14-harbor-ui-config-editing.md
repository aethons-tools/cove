# Harbor Admin UI — Config-Plane Editing (kits + destinations) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add kit-registry writes (push/pin/delete) and destination writes (add/remove) to the admin UI's Kits and Destinations pages, reusing the existing write foundation.

**Architecture:** New `/ui/kits*` and `/ui/destinations*` routes in `internal/harbor/adminui/writes.go`, CSRF-guarded and gated like every other `/ui/` write, calling the `Store` directly (`PushKit`/`PinKit`/`RemoveKit` + `RoleReferencingKit`, `AddDestination`/`RemoveDestination`). Destination-add validates `cred-name` via a `credExists func(string) bool` threaded into `adminui.Handler`. The Kits/Destinations tables become named htmx fragments; forms + per-row buttons drive the writes.

**Tech Stack:** Go stdlib (`net/http`, `log/slog`, `strconv`, `fmt`, `html/template`), htmx (vendored). No new dependency.

## Global Constraints

- **Module:** `github.com/aethons-tools/cove`. Work stays in `internal/harbor/adminui`, `cmd/at-harbor/main.go`, and `docs/`.
- **Write-path:** config writes go through `/ui/` inside the UI gate — never through `/admin/*` (unchanged). Reuse `Store` methods; do not reimplement.
- **CSRF:** every `POST`/`DELETE` under `/ui/` calls `guardWrite(w, r)` as its FIRST statement → 403 on mismatch.
- **No secret surface:** a kit config references secrets by name (no values) and `cred-name` is a reference key — never render a secret value, and log only names/versions/routes (never the kit config body or cred-name value). The destinations table stays name/route/upstream/repo-scoped (no cred-name column).
- **Referenced-kit guard:** `DELETE /ui/kits/{name}` must refuse (409) when `store.RoleReferencingKit(name)` reports a referencing role — mirroring `DELETE /admin/kits/{name}`.
- **Tests hermetic:** `httptest` + `harbor.NewFileStore` + a stub `credExists`. No Docker/network/VM, no `integration` tag.
- **TDD:** failing test first, watch it fail, implement minimally, watch it pass, commit.
- **Build/test:** `go test ./internal/harbor/adminui/ ./cmd/at-harbor/`; `go build ./...`; `just lint`. (Cold cache: prefix `GOPROXY=https://proxy.golang.org GOSUMDB=off`.)
- **Commit trailer:** end every commit message with EXACTLY, verbatim regardless of the implementing model:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```
- **Docs rule:** not done until `docs/usage/harbor/ui.md` is updated (Task 3).

## Reference — current shapes (already in the tree)

- `func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor) http.Handler` (`adminui/adminui.go:79`); `func registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger, sup *harbor.Supervisor)` (`writes.go:82`). Helpers in `writes.go`: `guardWrite`, `sameOrigin`, `renderError`, `orDefaultProject`, `splitCSV` (`strconv` already imported).
- `Store`: `PushKit(name, config string) (int, error)`, `PinKit(name string, version int) error`, `RemoveKit(name string) error`, `RoleReferencingKit(name string) (project, role string, ok bool)`, `ListKits() []Kit`, `AddDestination(Destination) error`, `RemoveDestination(name string) error`, `ListDestinations() []Destination`.
- `harbor.Destination{Name, Route, Upstream string; IdentityIn ApplyMethod; CredName string; Apply ApplyMethod; RepoScoped bool}`; `harbor.ApplyMethod` values `"bearer"`, `"basic-password"`, `"x-api-key"`.
- `renderFragment(w, page, tmpl, data)` renders a named fragment; `render(w, page, data)` renders the full page.
- `cmd/at-harbor/main.go` already has `credExists := func(n string) bool { _, ok := specs[n]; return ok }` (passed to `NewAdminHandler`).
- Current `templates/kits.html` table columns: Name / Current version / Versions (colspan 3). `templates/destinations.html`: Name / Route / Upstream / Repo-scoped (colspan 4).

---

### Task 1: kit-registry writes (push / pin / delete)

**Files:**
- Modify: `internal/harbor/adminui/writes.go` (kit handlers; add `"fmt"` import if absent)
- Modify: `internal/harbor/adminui/templates/kits.html` (`kits-table` fragment + add form + per-row pin/delete)
- Test: `internal/harbor/adminui/kits_write_test.go` (new)

**Interfaces:**
- Produces: `POST /ui/kits`, `POST /ui/kits/{name}/pin`, `DELETE /ui/kits/{name}` — each returns the `kits-table` fragment; delete 409s on a referenced kit. (No `Handler` signature change — kits use only `store`.)

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/adminui/kits_write_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

func uiHandler(t *testing.T, store harbor.Store) http.Handler {
	t.Helper()
	return adminui.Handler(store, testLogger(), nil)
}

func TestPushKit(t *testing.T) {
	store := newStore(t)
	h := uiHandler(t, store)
	rec := post(t, h, "/ui/kits", url.Values{"name": {"base"}, "config": {"listen: :443"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("push kit = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.GetKit("base"); !ok {
		t.Fatal("kit base not created")
	}
	if !strings.Contains(rec.Body.String(), "base") {
		t.Errorf("kits fragment should list the kit; got:\n%s", rec.Body.String())
	}
	// missing config → 400
	bad := post(t, h, "/ui/kits", url.Values{"name": {"x"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("missing config = %d, want 400", bad.Code)
	}
}

func TestPinKit(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PushKit("base", "v2"); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	rec := post(t, h, "/ui/kits/base/pin", url.Values{"version": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("pin = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if k, _ := store.GetKit("base"); k.Current != 1 {
		t.Errorf("current = %d, want 1 after pin", k.Current)
	}
	// non-integer version → 400
	bad := post(t, h, "/ui/kits/base/pin", url.Values{"version": {"notanint"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad version = %d, want 400", bad.Code)
	}
}

func TestDeleteKit(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "v1"); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/ui/kits/base", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete kit = %d, want 200", rec.Code)
	}
	if _, ok := store.GetKit("base"); ok {
		t.Error("kit should be gone after delete")
	}
}

func TestDeleteKitReferenced409(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", harbor.Role{Name: "worker", Kit: "base"}); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/ui/kits/base", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete referenced kit = %d, want 409", rec.Code)
	}
	if _, ok := store.GetKit("base"); !ok {
		t.Error("referenced kit must not be deleted")
	}
}
```

(`post`, `newStore`, `testLogger` already exist in the package's test files. If `harbor.Role` needs a `Kit` field — it has one.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/adminui/ -run 'Kit' -v`
Expected: FAIL — the `/ui/kits*` write routes don't exist (405/404).

- [ ] **Step 3: Add the kit handlers** to `registerWrites` in `writes.go` (add `"fmt"` to imports if not present):

```go
	mux.HandleFunc("POST /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		config := r.FormValue("config")
		if name == "" || config == "" {
			renderError(w, http.StatusBadRequest, "name and config are required")
			return
		}
		v, err := store.PushKit(name, config)
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui kit pushed", "operator", harbor.OperatorID(r), "kit", name, "version", v)
		renderFragment(w, "kits", "kits-table", map[string]any{"Kits": store.ListKits()})
	})

	mux.HandleFunc("POST /ui/kits/{name}/pin", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("version")))
		if err != nil {
			renderError(w, http.StatusBadRequest, "version must be an integer")
			return
		}
		if err := store.PinKit(r.PathValue("name"), v); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui kit pinned", "operator", harbor.OperatorID(r), "kit", r.PathValue("name"), "version", v)
		renderFragment(w, "kits", "kits-table", map[string]any{"Kits": store.ListKits()})
	})

	mux.HandleFunc("DELETE /ui/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		name := r.PathValue("name")
		if project, role, ok := store.RoleReferencingKit(name); ok {
			renderError(w, http.StatusConflict, fmt.Sprintf("kit %q is referenced by role %s/%s", name, project, role))
			return
		}
		if err := store.RemoveKit(name); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui kit removed", "operator", harbor.OperatorID(r), "kit", name)
		renderFragment(w, "kits", "kits-table", map[string]any{"Kits": store.ListKits()})
	})
```

- [ ] **Step 4: Update `templates/kits.html`**

```html
{{define "content"}}
<h1>Kits</h1>
<form hx-post="/ui/kits" hx-target="#kits" hx-swap="outerHTML">
  <input name="name" placeholder="kit name" required>
  <textarea name="config" placeholder="kit config (config.yml)" required></textarea>
  <button type="submit">Push kit</button>
</form>
{{template "kits-table" .}}
{{end}}

{{define "kits-table"}}
<table id="kits">
  <thead><tr><th>Name</th><th>Current version</th><th>Versions</th><th>Actions</th></tr></thead>
  <tbody>
    {{range .Kits}}
    <tr>
      <td>{{.Name}}</td><td>{{.Current}}</td><td>{{len .Versions}}</td>
      <td>
        <form hx-post="/ui/kits/{{.Name}}/pin" hx-target="#kits" hx-swap="outerHTML" style="display:inline">
          <input name="version" type="number" min="1" placeholder="ver">
          <button type="submit">Pin</button>
        </form>
        <button hx-delete="/ui/kits/{{.Name}}" hx-target="#kits" hx-swap="outerHTML"
                hx-confirm="Delete kit {{.Name}}?">Delete</button>
      </td>
    </tr>
    {{else}}
    <tr><td colspan="4" class="empty">No kits.</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ -v 2>&1 | tail -20`
Expected: PASS (kit push/pin/delete/referenced-409 + all prior).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): kit-registry writes (push/pin/delete) from the UI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 2: destination writes (add / remove) + thread `credExists`

**Files:**
- Modify: `internal/harbor/adminui/adminui.go` (`Handler` gains `credExists`; pass to `registerWrites`)
- Modify: `internal/harbor/adminui/writes.go` (`registerWrites` gains `credExists`; add destination handlers)
- Modify: `internal/harbor/adminui/templates/destinations.html` (`destinations-table` fragment + add form + delete button)
- Modify: `cmd/at-harbor/main.go` (pass `credExists` to `adminui.Handler`)
- Test: `internal/harbor/adminui/destinations_write_test.go` (new); update `adminui.Handler(...)` call sites

**Interfaces:**
- Produces:
  - `func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor, credExists func(string) bool) http.Handler`
  - `POST /ui/destinations` (name/route/upstream required; identity-in/apply/cred-name/repo-scoped optional; `cred-name` validated via `credExists`), `DELETE /ui/destinations/{name}` — each returns the `destinations-table` fragment.

- [ ] **Step 1: Write the failing test**

Create `internal/harbor/adminui/destinations_write_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

// credOK recognizes "known-cred" and rejects everything else.
func credOK(name string) bool { return name == "known-cred" }

func destHandler(t *testing.T, store harbor.Store) http.Handler {
	t.Helper()
	return adminui.Handler(store, testLogger(), nil, credOK)
}

func TestAddDestination(t *testing.T) {
	store := newStore(t)
	h := destHandler(t, store)
	rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"anthropic"}, "route": {"/anthropic/"}, "upstream": {"https://api.anthropic.com"},
		"identity-in": {"x-api-key"}, "cred-name": {"known-cred"}, "apply": {"x-api-key"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("add destination = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, d := range store.ListDestinations() {
		if d.Name == "anthropic" {
			found = true
		}
	}
	if !found {
		t.Fatal("destination not added")
	}
}

func TestAddDestinationBadCred400(t *testing.T) {
	store := newStore(t)
	h := destHandler(t, store)
	rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"x"}, "route": {"/x/"}, "upstream": {"https://x"}, "cred-name": {"ghost"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cred-name") {
		t.Fatalf("bad cred = %d %q, want 400 mentioning cred-name", rec.Code, rec.Body.String())
	}
	if len(store.ListDestinations()) != 0 {
		t.Error("destination with bad cred must not be added")
	}
}

func TestRemoveDestination(t *testing.T) {
	store := newStore(t)
	if err := store.AddDestination(harbor.Destination{Name: "d1", Route: "/d1/", Upstream: "https://d1"}); err != nil {
		t.Fatal(err)
	}
	h := destHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/ui/destinations/d1", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove destination = %d, want 200", rec.Code)
	}
	if len(store.ListDestinations()) != 0 {
		t.Error("destination should be gone after remove")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/harbor/adminui/ 2>&1 | head`
Expected: FAIL — `Handler` now needs 4 args (compile error until call sites updated) and the destination routes don't exist.

- [ ] **Step 3: Thread `credExists` in `adminui.go`**

```go
func Handler(store harbor.Store, log *slog.Logger, sup *harbor.Supervisor, credExists func(string) bool) http.Handler {
	// ... unchanged body ...
	registerWrites(mux, store, log, sup, credExists)
	return mux
}
```

- [ ] **Step 4: Add `credExists` + destination handlers in `writes.go`**

Change the signature: `func registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger, sup *harbor.Supervisor, credExists func(string) bool) {` and add:

```go
	mux.HandleFunc("POST /ui/destinations", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		d := harbor.Destination{
			Name:       strings.TrimSpace(r.FormValue("name")),
			Route:      strings.TrimSpace(r.FormValue("route")),
			Upstream:   strings.TrimSpace(r.FormValue("upstream")),
			IdentityIn: harbor.ApplyMethod(strings.TrimSpace(r.FormValue("identity-in"))),
			CredName:   strings.TrimSpace(r.FormValue("cred-name")),
			Apply:      harbor.ApplyMethod(strings.TrimSpace(r.FormValue("apply"))),
			RepoScoped: r.FormValue("repo-scoped") != "",
		}
		if d.Name == "" || d.Route == "" || d.Upstream == "" {
			renderError(w, http.StatusBadRequest, "name, route and upstream are required")
			return
		}
		if d.CredName != "" && !credExists(d.CredName) {
			renderError(w, http.StatusBadRequest, "cred-name does not resolve to a configured credential")
			return
		}
		if err := store.AddDestination(d); err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui destination added", "operator", harbor.OperatorID(r), "name", d.Name, "route", d.Route, "upstream", d.Upstream)
		renderFragment(w, "destinations", "destinations-table", map[string]any{"Destinations": store.ListDestinations()})
	})

	mux.HandleFunc("DELETE /ui/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := store.RemoveDestination(r.PathValue("name")); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui destination removed", "operator", harbor.OperatorID(r), "name", r.PathValue("name"))
		renderFragment(w, "destinations", "destinations-table", map[string]any{"Destinations": store.ListDestinations()})
	})
```

- [ ] **Step 5: Update `templates/destinations.html`**

```html
{{define "content"}}
<h1>Destinations</h1>
<form hx-post="/ui/destinations" hx-target="#destinations" hx-swap="outerHTML">
  <input name="name" placeholder="name" required>
  <input name="route" placeholder="route (e.g. /anthropic/)" required>
  <input name="upstream" placeholder="upstream URL" required>
  <select name="identity-in"><option value="bearer">bearer</option><option value="basic-password">basic-password</option><option value="x-api-key">x-api-key</option></select>
  <input name="cred-name" placeholder="cred-name (optional)">
  <select name="apply"><option value="bearer">bearer</option><option value="basic-password">basic-password</option><option value="x-api-key">x-api-key</option></select>
  <label><input type="checkbox" name="repo-scoped" value="1"> repo-scoped</label>
  <button type="submit">Add destination</button>
</form>
{{template "destinations-table" .}}
{{end}}

{{define "destinations-table"}}
<table id="destinations">
  <thead><tr><th>Name</th><th>Route</th><th>Upstream</th><th>Repo-scoped</th><th>Actions</th></tr></thead>
  <tbody>
    {{range .Destinations}}
    <tr>
      <td>{{.Name}}</td><td>{{.Route}}</td><td>{{.Upstream}}</td><td>{{if .RepoScoped}}yes{{else}}no{{end}}</td>
      <td><button hx-delete="/ui/destinations/{{.Name}}" hx-target="#destinations" hx-swap="outerHTML"
          hx-confirm="Delete destination {{.Name}}?">Delete</button></td>
    </tr>
    {{else}}
    <tr><td colspan="5" class="empty">No destinations.</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
```

(Note the colspan bump 4→5 for the new Actions column.)

- [ ] **Step 6: Update call sites**

- `cmd/at-harbor/main.go`: `gate.Wrap(adminui.Handler(st, log, sup))` → `gate.Wrap(adminui.Handler(st, log, sup, credExists))` (the `credExists` in scope, already built for `NewAdminHandler`).
- Every `adminui.Handler(<store>, testLogger(), <sup>)` in the adminui `_test.go` files → add a 4th arg. Add one shared test helper (e.g. in an existing `_test.go`): `func anyCred(string) bool { return true }`, and pass `anyCred` at those sites. The new `destinations_write_test.go` uses `credOK` for its negative case. `grep -rn 'adminui.Handler(' internal/harbor/adminui/` finds them; `uiHandler` (Task 1) and `destHandler` (Task 2) already encapsulate the call for the new tests.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ ./cmd/at-harbor/ -v 2>&1 | tail -25`
Expected: PASS (destination add/bad-cred-400/remove + all prior with the added arg).

- [ ] **Step 8: Commit**

```bash
git add internal/harbor/adminui/ cmd/at-harbor/main.go
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): destination writes (add/remove) with cred validation

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 3: Documentation

**Files:**
- Modify: `docs/usage/harbor/ui.md` (extend the Editing section)
- Test: the docs-audit checker

- [ ] **Step 1: Extend the Editing section** in `docs/usage/harbor/ui.md`:

```markdown
### Config plane (kits & destinations)

- **Kits** — push a new version (name + config), pin the current pointer to an
  existing version, and delete a kit. A kit still referenced by a role cannot be
  deleted (the UI reports a conflict). See [kits.md](kits.md).
- **Destinations** — add a brokered destination (name, route, upstream,
  identity-in, cred-name, apply, repo-scoped) and remove one. A `cred-name` must
  resolve to a configured credential, or the add is rejected. See
  [serve.md#destinations](serve.md#destinations).

A kit config references credentials by name only (no secret values), and a
destination's `cred-name` is a reference, not a secret — the UI shows and logs
neither secret values nor the credential itself. These actions obey the same
gate, CSRF, and audit-logging as the other edits.
```

- [ ] **Step 2: Run the docs audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no NEW findings touching `ui.md` (pre-existing `superpowers/` backlog out of scope). Confirm the new `kits.md` / `serve.md#destinations` links resolve.

- [ ] **Step 3: Commit**

```bash
git add docs/usage/harbor/ui.md
git commit -m "$(cat <<'EOF'
docs(harbor): document kit + destination editing in the admin UI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 4: Full verification pass

**Files:** none.

- [ ] **Step 1: Full tests** — Run: `GOPROXY=https://proxy.golang.org GOSUMDB=off go test ./...` — Expected: all PASS.
- [ ] **Step 2: Build** — Run: `go build ./...` — Expected: clean.
- [ ] **Step 3: Vet + lint** — Run: `go vet ./internal/harbor/... ./cmd/at-harbor/...` then `just lint` — Expected: clean.
- [ ] **Step 4: No-secret-leak grep** — Run: `grep -nE 'log\.(Info|Warn|Error|Debug)' internal/harbor/adminui/writes.go` and confirm the kit/destination log lines carry only names/versions/routes — never the kit config body or a credential value. Expected: no secret material logged.
- [ ] **Step 5: CSRF spot-check** — confirm all five new handlers (`POST /ui/kits`, `POST /ui/kits/{name}/pin`, `DELETE /ui/kits/{name}`, `POST /ui/destinations`, `DELETE /ui/destinations/{name}`) call `guardWrite` first. Expected: all five guarded.

---

## Self-Review

**1. Spec coverage:**
- Kit push/pin/delete (+ referenced-409) → Task 1; `TestPushKit`/`TestPinKit`/`TestDeleteKit`/`TestDeleteKitReferenced409`. ✓
- Destination add/remove (+ bad-cred-400) → Task 2; `TestAddDestination`/`TestAddDestinationBadCred400`/`TestRemoveDestination`. ✓
- `credExists` threaded into `Handler` → Task 2 signature + main wiring + call-site updates. ✓
- No secret surface (config/cred-name not logged/rendered; destinations table unchanged sans cred-name) → handlers log names/versions/routes only; Task 4 grep; table markup keeps 4 data columns + Actions. ✓
- CSRF/gate/attribution reused → `guardWrite` first in all five handlers; `harbor.OperatorID` in logs; CSRF covered by the existing `sameOrigin` (the `post` helper sends a matching Origin; negative CSRF already locked by prior slices' tests + Task 4 spot-check). ✓
- htmx fragments (`kits-table`/`destinations-table`) + forms + confirmed deletes → Tasks 1 & 2 templates. ✓
- Docs → Task 3. ✓ Hermetic tests → all. ✓

**2. Placeholder scan:** All handler/template/test code is complete; no TBD/TODO. Template edits give full new file bodies for the two small templates.

**3. Type consistency:**
- `Handler(store, log, sup, credExists func(string) bool)` — Task 2 defines; main + all test call sites updated same task; Task 1's `uiHandler` predates the change and is updated in Task 2's call-site sweep (it passes `nil` sup; after Task 2 it must also pass a cred func — fold `uiHandler` into the Task 2 sweep: `adminui.Handler(store, testLogger(), nil, anyCred)`).
- `registerWrites(mux, store, log, sup, credExists)` — Task 2 defines; Task 1's kit handlers are added to the same function before the signature grows (Task 1 adds them with the 4-arg-less signature; Task 2 grows the signature and the kit handlers are unaffected since they don't use `credExists`). ✓
- `store.PushKit(name, config) (int, error)`, `PinKit(name, int) error`, `RemoveKit(name) error`, `RoleReferencingKit(name) (string,string,bool)`, `AddDestination(Destination) error`, `RemoveDestination(name) error` — all match the `Store` interface. ✓
- `harbor.ApplyMethod` form values `bearer|basic-password|x-api-key` match the constants. ✓
- Fragment names `kits-table`/`destinations-table` ↔ `renderFragment(w, "kits"|"destinations", "<name>-table", data)`. ✓

Note carried to execution: Task 1 introduces the `uiHandler(t, store)` test helper calling the **3-arg** `Handler`; Task 2 changes `Handler` to 4 args, so Task 2's call-site sweep MUST update `uiHandler` too (add `, anyCred`). Both `uiHandler` and `destHandler` then exist — that's fine, or consolidate to one helper taking the cred func. The implementer should ensure no `adminui.Handler(` call is left on the 3-arg form after Task 2.
