# Harbor Admin UI (read-only observability) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `at-harbor` a loopback-only, read-only web UI that renders the live cove runtime and the control-plane roster/roles/kits/destinations, server-rendered with `html/template` + htmx.

**Architecture:** A new `internal/harbor/adminui` package returns an unauthenticated `http.Handler` (its own `ServeMux`) that reads state directly from the `harbor.Store` and renders embedded templates. `harbor.NewAdminHandler` gains a trailing `ui http.Handler` parameter and mounts it under `/ui/` (plus a `/` → `/ui/` redirect) **inside the existing `authMiddleware`**, so the UI inherits the loopback/OIDC gate unchanged. Live updates use htmx polling of a table fragment (same handler branches on the `HX-Request` header). To avoid an import cycle, the shared roster-summary logic is exported as `harbor.RosterSummaries`; `adminui` imports `harbor`, never the reverse.

**Tech Stack:** Go stdlib (`net/http`, `html/template`, `embed`), htmx 2.0.4 (single vendored, embedded JS file). No JS build step, no new Go dependency.

## Global Constraints

- **Module path:** `github.com/aethons-tools/cove`. The new package is `github.com/aethons-tools/cove/internal/harbor/adminui`.
- **stdlib-only for the UI:** no new third-party Go dependency; htmx is a committed static asset served from the binary, never fetched at runtime.
- **Security boundary is untouched:** do NOT weaken, bypass, or duplicate `authMiddleware`. The UI is mounted *inside* it. No mutation routes in this slice (GET only).
- **No secrets rendered:** never render a token, token hash, launch secret, or resolved credential value. Only fields already exposed by the JSON summaries or the safe display fields named in each task.
- **Tests are hermetic:** drive the in-memory `harbor.NewFileStore(t.TempDir()+"/store.json")` (or existing fakes) via `httptest`. No Docker, network, or live VM. No `integration` build tag.
- **TDD:** write the failing test first, watch it fail, implement minimally, watch it pass, commit.
- **Build/test:** `just test` runs the hermetic unit tests; `just build` builds; `just lint` lints. Per-package: `go test ./internal/harbor/... ./cmd/at-harbor/...`.
- **Commit attribution:** end every commit message with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```
- **Docs rule:** the work is not done until `docs/` is updated in the same branch (Task 7).

---

### Task 1: `adminui` package skeleton — embed htmx, serve static asset + a stub index

**Files:**
- Create: `internal/harbor/adminui/htmx.min.js` (vendored, htmx 2.0.4)
- Create: `internal/harbor/adminui/adminui.go`
- Create: `internal/harbor/adminui/templates/layout.html`
- Create: `internal/harbor/adminui/templates/index.html`
- Test: `internal/harbor/adminui/adminui_test.go`

**Interfaces:**
- Consumes: `harbor.Store` (existing interface).
- Produces:
  - `func Handler(store harbor.Store) http.Handler` — the UI mux (no auth wrap).
  - Routes registered on that mux: `GET /ui/{$}` (index page), `GET /ui/static/htmx.min.js` (the embedded asset, `Content-Type: text/javascript`).

- [ ] **Step 1: Vendor the htmx asset**

Egress to `cdnjs.cloudflare.com` has been added to the sandbox kit. Fetch the pinned version:

```bash
curl -fsSL https://cdnjs.cloudflare.com/ajax/libs/htmx/2.0.4/htmx.min.js \
  -o internal/harbor/adminui/htmx.min.js
test -s internal/harbor/adminui/htmx.min.js && echo "htmx vendored: $(wc -c < internal/harbor/adminui/htmx.min.js) bytes"
```
Expected: a non-empty file (~48KB) is written and the byte count prints.

- [ ] **Step 2: Write the failing test**

`internal/harbor/adminui/adminui_test.go`:

```go
package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

func newStore(t *testing.T) harbor.Store {
	t.Helper()
	st, err := harbor.NewFileStore(t.TempDir() + "/store.json")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return st
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestIndexRenders(t *testing.T) {
	h := adminui.Handler(newStore(t))
	rec := get(t, h, "/ui/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/ = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "harbor") {
		t.Errorf("index body missing title marker; got:\n%s", rec.Body.String())
	}
}

func TestStaticHtmxServed(t *testing.T) {
	h := adminui.Handler(newStore(t))
	rec := get(t, h, "/ui/static/htmx.min.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET htmx.min.js = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("htmx Content-Type = %q, want text/javascript*", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("htmx asset body is empty")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/harbor/adminui/ -run 'TestIndexRenders|TestStaticHtmxServed' -v`
Expected: FAIL — package `adminui` does not compile / `adminui.Handler` undefined.

- [ ] **Step 4: Write `templates/layout.html`**

```html
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>harbor — {{.Title}}</title>
  <script src="/ui/static/htmx.min.js"></script>
  <style>
    body { font: 14px system-ui, sans-serif; margin: 0; color: #1a1a1a; }
    nav { background: #111; padding: 10px 16px; }
    nav a { color: #ddd; margin-right: 14px; text-decoration: none; }
    nav a:hover { color: #fff; }
    main { padding: 16px; }
    table { border-collapse: collapse; width: 100%; }
    th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid #e4e4e4; }
    th { background: #f6f6f6; }
    .empty { color: #777; font-style: italic; }
  </style>
</head>
<body>
  <nav>
    <a href="/ui/">Dashboard</a>
    <a href="/ui/coves">Coves</a>
    <a href="/ui/roster">Roster</a>
    <a href="/ui/roles">Roles</a>
    <a href="/ui/kits">Kits</a>
    <a href="/ui/destinations">Destinations</a>
  </nav>
  <main>{{template "content" .}}</main>
</body>
</html>{{end}}
```

- [ ] **Step 5: Write `templates/index.html`**

```html
{{define "content"}}
<h1>harbor</h1>
<p>Read-only observability. Pick a view above.</p>
{{end}}
```

- [ ] **Step 6: Write `internal/harbor/adminui/adminui.go`**

```go
// Package adminui is harbor's read-only, server-rendered observability UI. It
// reads state directly from a harbor.Store and renders embedded html/template
// pages, with htmx polling for the live cove view. It exposes an unauthenticated
// http.Handler; the operator-auth gate is applied by harbor.NewAdminHandler,
// which mounts this handler inside its existing middleware.
package adminui

import (
	"embed"
	"html/template"
	"net/http"

	"github.com/aethons-tools/cove/internal/harbor"
)

//go:embed templates/*.html htmx.min.js
var files embed.FS

// page holds one parsed template set (layout + that page's content). Each set's
// full page is rendered via ExecuteTemplate(w, "layout", data).
var pages = map[string]*template.Template{
	"index": mustParse("index.html"),
}

func mustParse(names ...string) *template.Template {
	paths := make([]string, 0, len(names)+1)
	paths = append(paths, "templates/layout.html")
	for _, n := range names {
		paths = append(paths, "templates/"+n)
	}
	return template.Must(template.ParseFS(files, paths...))
}

// Handler returns the UI mux (no auth wrap). store is the only dependency:
// every view is an in-process read.
func Handler(store harbor.Store) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(files))))

	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
		render(w, "index", map[string]any{"Title": "Dashboard"})
	})

	return mux
}

// render executes the named page's "layout" template.
func render(w http.ResponseWriter, page string, data any) {
	t, ok := pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

Note on the static route: `http.FileServer(http.FS(files))` serves from the embed root, so after `StripPrefix("/ui/static/")` a request for `/ui/static/htmx.min.js` maps to the embedded `htmx.min.js`. Go's `FileServer` sets `Content-Type: text/javascript` for `.js` by extension.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ -v`
Expected: PASS (`TestIndexRenders`, `TestStaticHtmxServed`).

- [ ] **Step 8: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): package skeleton — embed htmx, serve static + index

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 2: Mount the UI in `NewAdminHandler` behind the existing auth gate

**Files:**
- Modify: `internal/harbor/admin.go:161` (add `ui http.Handler` param; mount routes)
- Modify: `cmd/at-harbor/main.go:848` (build `adminui.Handler(st)`, pass it in)
- Modify (compile fix — pass `nil`): `internal/harbor/admin_test.go` (5 calls), `cmd/at-harbor/login_test.go` (2 calls), `cmd/at-harbor/main_test.go` (5 calls), `internal/harbor/adminclient/adminclient_test.go` (2 calls)
- Test: `internal/harbor/admin_test.go` (new test `TestAdminHandlerMountsUI`)

**Interfaces:**
- Consumes: `adminui.Handler(store harbor.Store) http.Handler` (Task 1).
- Produces: `func NewAdminHandler(store Store, sup *Supervisor, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger, ui http.Handler) http.Handler` — the trailing `ui` param (may be nil). When non-nil, the returned handler serves `GET /{$}` → 302 `/ui/` and mounts `ui` at `/ui/`, all inside `authMiddleware`.

- [ ] **Step 1: Write the failing test**

Add to `internal/harbor/admin_test.go`:

```go
func TestAdminHandlerMountsUI(t *testing.T) {
	store := newTestStore(t) // existing helper in this file
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("UI:" + r.URL.Path))
	})
	h := NewAdminHandler(store, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), ui)

	// Root redirects to /ui/.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Errorf("redirect Location = %q, want /ui/", loc)
	}

	// /ui/ reaches the mounted handler (loopback allowed).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/coves", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "UI:/ui/coves" {
		t.Fatalf("GET /ui/coves = %d %q, want 200 UI:/ui/coves", rec.Code, rec.Body.String())
	}

	// Off-loopback is still refused by the gate the UI is mounted inside.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-loopback GET /ui/coves = %d, want 403", rec.Code)
	}
}
```

If `newTestStore` does not exist in the file, use the store constructor the other tests here use (grep the file for how `store` is built at the top of an existing test and mirror it).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/ -run TestAdminHandlerMountsUI -v`
Expected: FAIL — `NewAdminHandler` takes 6 args, not 7 (compile error).

- [ ] **Step 3: Add the `ui` param and mount routes in `admin.go`**

Change the signature at `internal/harbor/admin.go:161`:

```go
func NewAdminHandler(store Store, sup *Supervisor, auth OperatorAuthenticator, credExists func(string) bool, login *OperatorLoginConfig, log *slog.Logger, ui http.Handler) http.Handler {
	mux := http.NewServeMux()
```

Then, immediately before the final `return authMiddleware(auth, log, mux)` line, add:

```go
	if ui != nil {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
		mux.Handle("/ui/", ui)
	}

	// Auth gate wraps every route.
	return authMiddleware(auth, log, mux)
```

(The existing `// Auth gate wraps every route.` comment + return stays; just insert the `if ui != nil` block above it.)

- [ ] **Step 4: Update `cmd/at-harbor/main.go` to build and pass the UI**

At `cmd/at-harbor/main.go:848`, add the import `"github.com/aethons-tools/cove/internal/harbor/adminui"` and change the construction to:

```go
		ui := adminui.Handler(st)
		admin := harbor.NewAdminHandler(st, sup, auth, credExists, cfg.operatorLoginConfig(), log, ui)
```

- [ ] **Step 5: Pass `nil` at every other call site (compile fix)**

Append `, nil` as the final argument to the `NewAdminHandler(...)` calls at:
- `internal/harbor/admin_test.go:24`, `:163`, `:214`, `:237`, `:417`
- `cmd/at-harbor/login_test.go:57`, `:188`
- `cmd/at-harbor/main_test.go:38`, `:68`, `:105`, `:222`, `:345`
- `internal/harbor/adminclient/adminclient_test.go:40`, `:94`

(These tests exercise the JSON API only; they don't need a UI.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/harbor/... ./cmd/at-harbor/...`
Expected: PASS across the board (new `TestAdminHandlerMountsUI` passes; no call-site compile errors).

- [ ] **Step 7: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go cmd/at-harbor/ internal/harbor/adminclient/adminclient_test.go
git commit -m "$(cat <<'EOF'
feat(harbor): mount the read-only UI inside the admin auth gate

NewAdminHandler gains a trailing ui http.Handler; when set it serves a
/ -> /ui/ redirect and mounts the UI under /ui/, all inside the existing
authMiddleware so exposure/auth are unchanged.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 3: Export `RosterSummaries` (shared roster-scope resolution)

**Files:**
- Modify: `internal/harbor/admin.go` (extract helper; call it from the `GET /admin/roster` handler at :209–224)
- Test: `internal/harbor/admin_test.go` (new `TestRosterSummaries`)

**Interfaces:**
- Produces: `func RosterSummaries(store Store) []ActorSummary` — one `ActorSummary` per actor, each grant carrying its effective `Destinations`/`Repos` after `EffectiveScope`. Never includes a token or hash.

- [ ] **Step 1: Write the failing test**

Add to `internal/harbor/admin_test.go`:

```go
func TestRosterSummaries(t *testing.T) {
	store := newTestStore(t)
	if err := store.PutRole("default", Role{Name: "worker", Scope: Scope{Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(Actor{ID: "spider-1", TokenHash: "deadbeef", Grants: []Grant{{Project: "default", Role: "worker"}}}); err != nil {
		t.Fatal(err)
	}
	out := RosterSummaries(store)
	if len(out) != 1 || out[0].ID != "spider-1" {
		t.Fatalf("summaries = %+v, want one actor spider-1", out)
	}
	if len(out[0].Grants) != 1 || out[0].Grants[0].Role != "worker" {
		t.Fatalf("grants = %+v, want worker", out[0].Grants)
	}
	g := out[0].Grants[0]
	if len(g.Destinations) != 1 || g.Destinations[0] != "anthropic" {
		t.Errorf("effective destinations = %v, want [anthropic]", g.Destinations)
	}
}
```

Match `newTestStore`/`AddActor`/`PutRole` to the exact helpers and field names already used elsewhere in this test file; adjust only if the local names differ.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/ -run TestRosterSummaries -v`
Expected: FAIL — `RosterSummaries` undefined.

- [ ] **Step 3: Extract the helper**

Add to `internal/harbor/admin.go` (near the roster handler):

```go
// RosterSummaries returns one ActorSummary per enrolled actor, each grant
// carrying its effective destinations/repos after override resolution. It never
// includes a token or hash. The JSON roster handler and the read-only UI both
// render from this, so the two surfaces cannot drift.
func RosterSummaries(store Store) []ActorSummary {
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
	return out
}
```

Then replace the body of the `GET /admin/roster` handler (currently :209–224) with:

```go
	mux.HandleFunc("GET /admin/roster", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, RosterSummaries(store))
	})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/harbor/... ./cmd/at-harbor/...`
Expected: PASS (`TestRosterSummaries` passes; the existing roster JSON test still passes — behavior unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/admin.go internal/harbor/admin_test.go
git commit -m "$(cat <<'EOF'
refactor(harbor): export RosterSummaries for JSON + UI to share

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 4: Coves view + live htmx polling fragment

**Files:**
- Modify: `internal/harbor/adminui/adminui.go` (register `GET /ui/coves`; add "coves" to `pages`; `HX-Request` branch)
- Create: `internal/harbor/adminui/templates/coves.html`
- Test: `internal/harbor/adminui/adminui_test.go` (`TestCovesFullPage`, `TestCovesFragment`, `TestCovesNoSecretLeak`)

**Interfaces:**
- Consumes: `harbor.Store.ListInstances() []harbor.Instance` (existing).
- Produces: `GET /ui/coves` — full page on a normal request; the `<table id="coves">` fragment alone when `HX-Request: true`.

- [ ] **Step 1: Write the failing test**

Add to `internal/harbor/adminui/adminui_test.go`:

```go
func seedCove(t *testing.T, store harbor.Store) {
	t.Helper()
	if err := store.PutInstance(harbor.Instance{
		ActorID: "spider-9", Project: "acme", Role: "worker", Unit: "COV-1",
		Phase: harbor.PhaseLive, Activity: harbor.ActivityRunning,
		Lease: harbor.Lease{Holder: "harbor-a"},
		RaisedAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCovesFullPage(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	rec := get(t, adminui.Handler(store), "/ui/coves")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/coves = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<nav", `id="coves"`, "spider-9", "live", "running", `hx-trigger="every 3s"`} {
		if !strings.Contains(body, want) {
			t.Errorf("full page missing %q", want)
		}
	}
}

func TestCovesFragment(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/coves", nil)
	req.Header.Set("HX-Request", "true")
	adminui.Handler(store).ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `id="coves"`) || !strings.Contains(body, "spider-9") {
		t.Errorf("fragment missing table/row; got:\n%s", body)
	}
	if strings.Contains(body, "<nav") || strings.Contains(body, "<html") {
		t.Errorf("fragment must not include page chrome; got:\n%s", body)
	}
}

func TestCovesNoSecretLeak(t *testing.T) {
	store := newStore(t)
	if err := store.PutInstance(harbor.Instance{
		ActorID: "spider-9", Project: "acme", Role: "worker",
		Phase: harbor.PhaseLive, LaunchSecretHash: "SECRET-HASH-XYZ",
		Lease: harbor.Lease{Holder: "h"}, RaisedAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, adminui.Handler(store), "/ui/coves")
	if strings.Contains(rec.Body.String(), "SECRET-HASH-XYZ") {
		t.Error("cove view leaked the launch-secret hash")
	}
}
```

Add `"time"` to the test file imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/adminui/ -run TestCoves -v`
Expected: FAIL — `/ui/coves` 404s (route not registered), assertions unmet.

- [ ] **Step 3: Write `templates/coves.html`**

```html
{{define "content"}}
<h1>Coves</h1>
{{template "coves-table" .}}
{{end}}

{{define "coves-table"}}
<table id="coves" hx-get="/ui/coves" hx-trigger="every 3s" hx-swap="outerHTML">
  <thead>
    <tr><th>ID</th><th>Project</th><th>Role</th><th>Unit</th><th>Phase</th><th>Activity</th><th>Lease</th><th>Raised</th><th>Last seen</th></tr>
  </thead>
  <tbody>
    {{range .Coves}}
    <tr>
      <td>{{.ActorID}}</td><td>{{.Project}}</td><td>{{.Role}}</td><td>{{.Unit}}</td>
      <td>{{.Phase}}</td><td>{{.Activity}}</td><td>{{.Lease.Holder}}</td>
      <td>{{.RaisedAt.Format "2006-01-02 15:04:05"}}</td>
      <td>{{.LastSeen.Format "2006-01-02 15:04:05"}}</td>
    </tr>
    {{else}}
    <tr><td colspan="9" class="empty">No coves.</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
```

The rendered fields are exactly `harbor.Instance`'s safe operational fields; `LaunchSecretHash` and `Backend` are never referenced.

- [ ] **Step 4: Register the route and add the page + fragment branch in `adminui.go`**

Add `"coves"` to the `pages` map:

```go
var pages = map[string]*template.Template{
	"index": mustParse("index.html"),
	"coves": mustParse("coves.html"),
}
```

Add inside `Handler`, after the index route:

```go
	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": "Coves", "Coves": store.ListInstances()}
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "coves", "coves-table", data)
			return
		}
		render(w, "coves", data)
	})
```

Add the fragment renderer next to `render`:

```go
// renderFragment executes a single named template (e.g. an htmx-swapped table)
// without the page chrome.
func renderFragment(w http.ResponseWriter, page, tmpl string, data any) {
	t, ok := pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, tmpl, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

> **Post-review correction:** the plan text above passes raw `store.ListInstances()`
> (`[]harbor.Instance`) to the template and ranges over `.ActorID`/`.Lease.Holder`.
> Final review flagged this as resting the no-secret guarantee on template
> discipline rather than the type. The as-built code instead reuses the scrubbed
> summary pattern from Task 3: an exported `harbor.CoveSummaries(store) []CoveSummary`
> (extracted from the `GET /admin/coves` JSON handler, same as `RosterSummaries`) is
> what both the JSON handler and `GET /ui/coves`/the dashboard render from. The
> `coves-table` template ranges over `CoveSummary` fields, so `.ActorID` → `.ID` and
> `.Lease.Holder` → `.LeaseHolder`; `.Project`, `.Role`, `.Unit`, `.Phase`,
> `.Activity`, `.RaisedAt`, `.LastSeen` are unchanged.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ -v`
Expected: PASS (`TestCovesFullPage`, `TestCovesFragment`, `TestCovesNoSecretLeak`, plus Task 1 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): live coves view with htmx polling fragment

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 5: Roster, roles, kits, destinations tables

**Files:**
- Modify: `internal/harbor/adminui/adminui.go` (four routes; four `pages` entries; a `roleRow` helper)
- Create: `internal/harbor/adminui/templates/roster.html`, `roles.html`, `kits.html`, `destinations.html`
- Test: `internal/harbor/adminui/adminui_test.go` (`TestRosterView`, `TestRolesView`, `TestKitsView`, `TestDestinationsView`, `TestRosterViewNoSecretLeak`)

**Interfaces:**
- Consumes: `harbor.RosterSummaries(store)` (Task 3); `harbor.Store.ListProjects()`, `ListRoles(project)`, `ListKits()`, `ListDestinations()` (existing).
- Produces: `GET /ui/roster`, `GET /ui/roles`, `GET /ui/kits`, `GET /ui/destinations` — full-page read-only tables.

- [ ] **Step 1: Write the failing test**

Add to `internal/harbor/adminui/adminui_test.go`:

```go
func TestRosterView(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker", Scope: harbor.Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(harbor.Actor{ID: "spider-2", TokenHash: "HASH-NOPE", Grants: []harbor.Grant{{Project: "acme", Role: "worker"}}}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store), "/ui/roster").Body.String()
	for _, want := range []string{"spider-2", "worker", "anthropic"} {
		if !strings.Contains(body, want) {
			t.Errorf("roster view missing %q", want)
		}
	}
}

func TestRosterViewNoSecretLeak(t *testing.T) {
	store := newStore(t)
	if err := store.AddActor(harbor.Actor{ID: "spider-2", TokenHash: "HASH-NOPE"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(get(t, adminui.Handler(store), "/ui/roster").Body.String(), "HASH-NOPE") {
		t.Error("roster view leaked a token hash")
	}
}

func TestRolesView(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "review", Scope: harbor.Scope{Destinations: []string{"git"}, Repos: []string{"acme/*"}, TTL: time.Hour}, Kit: ""}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store), "/ui/roles").Body.String()
	for _, want := range []string{"acme", "review", "git", "acme/*"} {
		if !strings.Contains(body, want) {
			t.Errorf("roles view missing %q", want)
		}
	}
}

func TestKitsView(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "listen: :443"); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store), "/ui/kits").Body.String()
	if !strings.Contains(body, "base") {
		t.Errorf("kits view missing kit name; got:\n%s", body)
	}
}

func TestDestinationsView(t *testing.T) {
	store := newStore(t)
	if err := store.AddDestination(harbor.Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", CredName: "anthropic-key"}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store), "/ui/destinations").Body.String()
	for _, want := range []string{"anthropic", "/anthropic/", "https://api.anthropic.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("destinations view missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/adminui/ -run 'TestRosterView|TestRolesView|TestKitsView|TestDestinationsView' -v`
Expected: FAIL — the four routes 404.

- [ ] **Step 3: Write the four templates**

`templates/roster.html`:

```html
{{define "content"}}
<h1>Roster</h1>
<table>
  <thead><tr><th>Actor</th><th>Expiry</th><th>Project</th><th>Role</th><th>Destinations</th><th>Repos</th></tr></thead>
  <tbody>
    {{range .Actors}}
      {{$id := .ID}}{{$exp := .Expiry}}
      {{if .Grants}}
        {{range .Grants}}
        <tr><td>{{$id}}</td><td>{{if $exp.IsZero}}—{{else}}{{$exp.Format "2006-01-02"}}{{end}}</td>
          <td>{{.Project}}</td><td>{{.Role}}</td>
          <td>{{range .Destinations}}{{.}} {{end}}</td>
          <td>{{range .Repos}}{{.}} {{end}}</td></tr>
        {{end}}
      {{else}}
        <tr><td>{{$id}}</td><td>{{if $exp.IsZero}}—{{else}}{{$exp.Format "2006-01-02"}}{{end}}</td><td colspan="4" class="empty">no grants</td></tr>
      {{end}}
    {{else}}
      <tr><td colspan="6" class="empty">No actors enrolled.</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
```

`templates/roles.html`:

```html
{{define "content"}}
<h1>Roles</h1>
<table>
  <thead><tr><th>Project</th><th>Role</th><th>Destinations</th><th>Repos</th><th>TTL</th><th>Kit</th></tr></thead>
  <tbody>
    {{range .Roles}}
    <tr><td>{{.Project}}</td><td>{{.Name}}</td>
      <td>{{range .Destinations}}{{.}} {{end}}</td>
      <td>{{range .Repos}}{{.}} {{end}}</td>
      <td>{{.TTL}}</td><td>{{if .Kit}}{{.Kit}}{{else}}—{{end}}</td></tr>
    {{else}}
    <tr><td colspan="6" class="empty">No roles.</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
```

`templates/kits.html`:

```html
{{define "content"}}
<h1>Kits</h1>
<table>
  <thead><tr><th>Name</th><th>Current version</th><th>Versions</th></tr></thead>
  <tbody>
    {{range .Kits}}
    <tr><td>{{.Name}}</td><td>{{.Current}}</td><td>{{len .Versions}}</td></tr>
    {{else}}
    <tr><td colspan="3" class="empty">No kits.</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
```

`templates/destinations.html`:

```html
{{define "content"}}
<h1>Destinations</h1>
<table>
  <thead><tr><th>Name</th><th>Route</th><th>Upstream</th><th>Repo-scoped</th></tr></thead>
  <tbody>
    {{range .Destinations}}
    <tr><td>{{.Name}}</td><td>{{.Route}}</td><td>{{.Upstream}}</td><td>{{if .RepoScoped}}yes{{else}}no{{end}}</td></tr>
    {{else}}
    <tr><td colspan="4" class="empty">No destinations.</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}
```

Destinations deliberately renders only `Name`/`Route`/`Upstream`/`RepoScoped` — not `CredName`, `IdentityIn`, or `Apply` (kept minimal; the credential name is not shown in the UI).

- [ ] **Step 4: Register routes, `pages` entries, and the `roleRow` helper in `adminui.go`**

Add the four entries to `pages`:

```go
	"roster":       mustParse("roster.html"),
	"roles":        mustParse("roles.html"),
	"kits":         mustParse("kits.html"),
	"destinations": mustParse("destinations.html"),
```

Add a `roleRow` type + helper (roles need project + role flattened for the template):

```go
// roleRow is one project/role pair flattened for the roles table.
type roleRow struct {
	Project      string
	Name         string
	Destinations []string
	Repos        []string
	TTL          time.Duration
	Kit          string
}

func roleRows(store harbor.Store) []roleRow {
	var out []roleRow
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			out = append(out, roleRow{
				Project: p, Name: r.Name,
				Destinations: r.Scope.Destinations, Repos: r.Scope.Repos,
				TTL: r.Scope.TTL, Kit: r.Kit,
			})
		}
	}
	return out
}
```

Add `"time"` and `"github.com/aethons-tools/cove/internal/harbor"` are already imported. Register the routes in `Handler`:

```go
	mux.HandleFunc("GET /ui/roster", func(w http.ResponseWriter, r *http.Request) {
		render(w, "roster", map[string]any{"Title": "Roster", "Actors": harbor.RosterSummaries(store)})
	})
	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
		render(w, "roles", map[string]any{"Title": "Roles", "Roles": roleRows(store)})
	})
	mux.HandleFunc("GET /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		render(w, "kits", map[string]any{"Title": "Kits", "Kits": store.ListKits()})
	})
	mux.HandleFunc("GET /ui/destinations", func(w http.ResponseWriter, r *http.Request) {
		render(w, "destinations", map[string]any{"Title": "Destinations", "Destinations": store.ListDestinations()})
	})
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ -v`
Expected: PASS (all five new tests + prior tests).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): roster, roles, kits, destinations read-only tables

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 6: Dashboard at `/ui/` (live coves + roster summary)

**Files:**
- Modify: `internal/harbor/adminui/adminui.go` (repoint `GET /ui/{$}` to a dashboard page; add "dashboard" to `pages`, parsing coves.html too so it can reuse `coves-table`)
- Create: `internal/harbor/adminui/templates/dashboard.html`
- Modify/Remove: `internal/harbor/adminui/templates/index.html` (superseded by dashboard; delete it and drop the `"index"` pages entry)
- Test: `internal/harbor/adminui/adminui_test.go` (`TestDashboard`)

**Interfaces:**
- Consumes: `store.ListInstances()`, `harbor.RosterSummaries(store)`, the `coves-table` template (from coves.html).
- Produces: `GET /ui/{$}` renders the dashboard — the live coves table (polling) + a compact roster list.

- [ ] **Step 1: Write the failing test**

Replace `TestIndexRenders` (from Task 1) with:

```go
func TestDashboard(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	if err := store.AddActor(harbor.Actor{ID: "mgr-1"}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, adminui.Handler(store), "/ui/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/ = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="coves"`, "spider-9", `hx-trigger="every 3s"`, "mgr-1"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/harbor/adminui/ -run TestDashboard -v`
Expected: FAIL — `/ui/` still renders the stub index (no coves table / roster).

- [ ] **Step 3: Write `templates/dashboard.html`**

```html
{{define "content"}}
<h1>Dashboard</h1>
<h2>Live coves</h2>
{{template "coves-table" .}}
<h2>Roster</h2>
<ul>
  {{range .Actors}}<li>{{.ID}} — {{len .Grants}} grant(s)</li>{{else}}<li class="empty">No actors enrolled.</li>{{end}}
</ul>
{{end}}
```

- [ ] **Step 4: Wire the dashboard page in `adminui.go`**

In `pages`, remove the `"index"` entry and add a `"dashboard"` set that also parses `coves.html` (so `coves-table` is defined in the set):

```go
	"dashboard": mustParse("coves.html", "dashboard.html"),
```

(`mustParse` already prepends layout.html; passing both `coves.html` and `dashboard.html` yields a set with `layout`, `content` (from dashboard.html — the last "content" define wins, which is dashboard's), and `coves-table`. Ensure `dashboard.html` is listed **after** `coves.html` so dashboard's `content` is the active one.)

Repoint the root route:

```go
	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
		render(w, "dashboard", map[string]any{
			"Title":  "Dashboard",
			"Coves":  store.ListInstances(),
			"Actors": harbor.RosterSummaries(store),
		})
	})
```

Delete `internal/harbor/adminui/templates/index.html`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/harbor/adminui/ -v`
Expected: PASS (`TestDashboard` + all prior; no references to the deleted index remain).

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/adminui/
git commit -m "$(cat <<'EOF'
feat(harbor/adminui): dashboard at /ui/ — live coves + roster summary

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 7: Documentation

**Files:**
- Create: `docs/usage/harbor/ui.md`
- Modify: `docs/usage/harbor/INDEX.md` (add the row + a line in "which doc for which task")
- Test: `just docs-audit` (or the docs checker the repo uses) — no orphans/dangling links

**Interfaces:** none (docs only).

- [ ] **Step 1: Write `docs/usage/harbor/ui.md`**

```markdown
---
summary: The read-only harbor admin UI — a loopback-only, server-rendered web view of the live coves and the control-plane roster/roles/kits/destinations, served by `at-harbor serve`.
read_when: You want to watch a running harbor in a browser — the live cove fleet and the roster/roles/kits/destinations — without running admin CLI verbs.
owns: the `/ui/` read-only observability surface (what it shows, how to reach it, its loopback-only exposure)
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; INDEX.md for the service overview
tier: leaf
updated: 2026-09-13
---

# The harbor admin UI (`/ui/`)

`at-harbor serve` serves a **read-only** web UI on the same **admin listener** as
the JSON admin API. Point a browser at the admin URL and open `/ui/` (`/`
redirects there):

```
http://127.0.0.1:8081/ui/
```

It renders, all read-only:

- **Dashboard** (`/ui/`) — the live cove fleet + a roster summary.
- **Coves** (`/ui/coves`) — every managed cove's id, project/role, unit, phase,
  activity, lease holder, raised-at, last-seen. The table **auto-refreshes every
  3 seconds** (htmx polling); no page reload.
- **Roster / Roles / Kits / Destinations** — the control-plane objects as tables.

## Exposure — loopback only (this cut)

The UI is mounted **inside the same operator-auth gate as the admin API**, so its
exposure is exactly the admin API's (see the fail-closed rule in
[serve.md](serve.md#exposing-the-admin-api-fail-closed)):

- On a loopback `admin-listen`, the UI is reachable from the local host only. To
  view it from your laptop against a remote harbor, SSH-tunnel the admin port.
- Off-loopback, the gate requires an OIDC **bearer** token, which a browser does
  not send — so the UI is **not** reachable from a remote browser yet. Browser
  session sign-in is a later increment.

The UI never renders a token, token hash, launch secret, or credential value, and
adds **no mutation paths** — enroll/raise/teardown/edit stay on the
[admin verbs](operators.md).
```

- [ ] **Step 2: Add the row to `docs/usage/harbor/INDEX.md`**

In the "Which doc for which task" table (after the `dispatcher.md` row), add:

```markdown
| [ui.md](ui.md) | You want to watch a running harbor in a browser — the live coves and the roster/roles/kits/destinations — read-only, loopback-only. |
```

- [ ] **Step 3: Run the docs audit**

Run: `just docs-audit` (if the recipe exists; otherwise run the checker `just` lists). 
Expected: no orphans, no dangling links, frontmatter valid, `ui.md` reachable from `INDEX.md`.

- [ ] **Step 4: Commit**

```bash
git add docs/usage/harbor/ui.md docs/usage/harbor/INDEX.md
git commit -m "$(cat <<'EOF'
docs(harbor): document the read-only admin UI (/ui/)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
EOF
)"
```

---

### Task 8: Full verification pass

**Files:** none (verification only).

- [ ] **Step 1: Full hermetic test suite**

Run: `just test`
Expected: PASS (all packages, including `internal/harbor/adminui` and the updated `internal/harbor`, `cmd/at-harbor`).

- [ ] **Step 2: Build**

Run: `just build`
Expected: builds `at-cove`, `at-task`, `at-switchboard`, `at-harbor` with no errors (the embed compiles the vendored htmx into the binary).

- [ ] **Step 3: Lint**

Run: `just lint`
Expected: clean (no new findings in `internal/harbor/adminui` or the modified files).

- [ ] **Step 4: Smoke-run the UI (optional, manual)**

Start a harbor with a loopback `admin-listen` per [serve.md], then:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/ui/
curl -sS http://127.0.0.1:8081/ui/coves | grep -q 'id="coves"' && echo "coves table ok"
```
Expected: `200`, and the coves table marker is present.

---

## Self-Review

**1. Spec coverage:**
- Read-only observability, no mutation → Tasks 4–6 are GET-only; constraint restated. ✓
- Server-rendered Go + htmx, single vendored embedded JS → Task 1 (embed + vendored htmx.min.js). ✓
- Loopback-only, reuse existing gate → Task 2 mounts inside `authMiddleware`; `TestAdminHandlerMountsUI` asserts off-loopback 403. ✓
- `internal/harbor/adminui` package, in-process store reads, no self-HTTP → Tasks 1/4/5/6 call `store` methods directly. ✓
- Shared summary builder (`RosterSummaries`) → Task 3, consumed by Tasks 5/6. ✓
- Views: dashboard/coves/roster/roles/kits/destinations → Tasks 4/5/6. ✓
- htmx polling, `HX-Request` branch, 3s → Task 4 (`TestCovesFragment`, `hx-trigger="every 3s"`). ✓
- `/ui/*` namespace + `/`→`/ui/` redirect → Task 2. ✓
- No-secret-leak → Tasks 4 & 5 assertions. ✓
- Hermetic tests → all tasks use `httptest` + in-memory store. ✓
- Docs (`ui.md` + INDEX link) → Task 7. ✓
- Vendoring via added CDN egress → Task 1 Step 1. ✓

**2. Placeholder scan:** No TBD/TODO; every code and template step contains full content; no "similar to Task N" cross-references (code repeated where needed). ✓

**3. Type consistency:**
- `adminui.Handler(store harbor.Store) http.Handler` — used identically in Tasks 1/2/4/5/6. ✓
- `harbor.RosterSummaries(store Store) []ActorSummary` — defined Task 3, consumed Tasks 5/6. ✓
- `NewAdminHandler(..., ui http.Handler)` trailing param — defined Task 2, all call sites updated in the same task. ✓
- Template data keys are per-page maps (`Coves`, `Actors`, `Roles`, `Kits`, `Destinations`, `Title`) matching each template's `{{range .X}}`. ✓
- `render` (full page via "layout") vs `renderFragment` (single named template) — introduced Task 1/4, reused consistently. ✓

Note carried to execution: in Task 6, `mustParse("coves.html", "dashboard.html")` relies on `dashboard.html` being parsed last so its `{{define "content"}}` wins over coves.html's. The task text states this ordering requirement explicitly.
