# Admin UI IA — Slice 1 (nav sections + project tree) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship slice 1 of [the admin UI IA spec](../specs/2026-10-07-admin-ui-ia-design.md): a six-section top nav highlighted by section, and the project page as a left-side tree with one URL per section, the role page moved under its project, and `/ui/roles…` redirects.

**Architecture:** Each page's template set is parsed with its nav section bound as a `navSection` template func (`mustParse(section, files…)`), so the layout highlights by section with no payload changes. Project pages share one `projectDetail` payload carrying a `Section`, a `projectTree` and breadcrumbs; `project.html` renders the tree frame and switches to a per-section body template (`project-<section>`, each wrapped in `#project`), and each project write re-renders only its own section. The squawk table is extracted to `squawks.html` and its filtering to `filterSquawks`, so the Intercom page and the project's Intercom section share both.

**Tech Stack:** Go 1.26 `net/http` mux patterns, `html/template`, htmx (already vendored via `internal/jam/uiassets`), hermetic `httptest` tests.

## Global Constraints

- Top nav, in order: `Dashboard · Projects · Users · Agents · Specs · Intercom`; Agents links `/ui/coves`, Specs links `/ui/kits` (until slices 2–3).
- Exactly one top-nav item carries `aria-current="page"` on every page except search (none).
- Project tree order: `Overview · Members · Agents · Roles · Intercom · Escalation`; routes `/ui/projects/{name}[/members|/agents|/roles|/intercom|/escalation]`, role page `/ui/projects/{project}/roles/{name}`.
- Redirects are **301**, query string preserved: `/ui/roles` → `/ui/projects`; `/ui/roles/{p}/{r}` → `/ui/projects/{p}/roles/{r}`.
- Write endpoints keep their paths (`/ui/roles`, `/ui/roles/{p}/{r}/…`, `/ui/projects/{p}/members|channels|escalation|chat-service|context`).
- The project's Intercom section shows at most **50** newest squawks (`recentProjectSquawks`).
- Narrow-screen breakpoint stays `max-width:720px`.
- No new dependencies; tests stay hermetic (`httptest`, in-memory store, in-memory logs).
- Docs change in the same branch (AGENTS.md); use the docs-author / docs-audit skills.

## How to apply the code in this plan

The code was prototyped and verified before writing this plan: each task's patch applies cleanly (`git apply --whitespace=error`) on top of the previous tasks, starting from `main` at the spec commit. Every task gives its **test patch** and its **code patch** separately so you work test-first:

1. Save the test patch to a file and `git apply` it. Run the tests: they fail (RED) — the expected failures are listed.
2. Save the code patch and `git apply` it. Run the tests: they pass (GREEN).
3. Read the applied diff before committing — you own it. If a patch does not apply, stop and report rather than hand-merging.

Copy each patch exactly from its fenced block (the content between the `````diff` line and the closing ``````).

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/adminui/nav.go` (new) | `navSection` constants, `navItems` (the top nav), `redirect` (301 keeping the query) |
| `internal/jam/adminui/adminui.go` | `pages` bound to sections; `mustParse(section, …)`; `/ui/roles…` redirects; nested role route |
| `internal/jam/adminui/templates/layout.html` | nav rendered from `navItems`/`navSection`; narrow-screen tree collapse |
| `internal/jam/adminui/projtree.go` (new) | `projectSection`, `projectSections`, `projectSectionURL`, `projectTree` + `buildProjectTree` |
| `internal/jam/adminui/templates/projtree.html` (new) | `project-tree` and `crumbs` templates + tree styles |
| `internal/jam/adminui/projects.go` | `crumb`, `projectCrumbs`, `projectDetail` (Section/Tree/Crumbs/Squawks), per-section GET routes |
| `internal/jam/adminui/project_edit.go` | each write re-renders its own section |
| `internal/jam/adminui/templates/project.html` | tree frame + `project-<section>` bodies |
| `internal/jam/adminui/role_detail.go`, `templates/role.html` | `roleURL` nested, `roleWriteBase`, tree + crumbs on the role page |
| `internal/jam/adminui/intercom.go`, `templates/squawks.html` (new), `templates/intercom.html` | `squawkFilter`/`filterSquawks`/`squawkTable`; shared `squawk-table` template |
| `internal/jam/adminui/writes.go` | role create/delete answer without the removed roles table |
| `internal/jam/adminui/templates/roles.html` | **deleted** |
| `internal/jam/adminui/nav_test.go` (new) + existing tests | nav, redirects, tree, section writes, project roles/intercom |
| `docs/usage/jam/ui.md`, `ui-pages.md`, `ui-projects.md` (new), `ui-editing.md` (new), `INDEX.md`, `projects.md`, `escalation.md`, `personal-sessions.md` | docs |

---

### Task 1: Top nav by section

**Files:**
- Create: `internal/jam/adminui/nav.go`, `internal/jam/adminui/nav_test.go`
- Modify: `internal/jam/adminui/adminui.go` (the `pages` map, `mustParse`, `funcs`), `internal/jam/adminui/templates/layout.html` (`<nav>`)
- Test updates: `polish_test.go`, `dest_detail_test.go`, `kits_page_test.go`, `model_specs_test.go`, `role_detail_test.go`, `studio_page_test.go`, `studio_text_test.go`, `users_test.go`

**Interfaces:**
- Produces: `type navSection string` with `navNone, navDashboard, navProjects, navUsers, navAgents, navSpecs, navIntercom`; `type navItem struct{ Section navSection; Label, Href string }`; `var navItems []navItem`; `func mustParse(section navSection, names ...string) *template.Template`; template funcs `navSection` (per set) and `navItems`.

- [ ] **Step 1: Apply the test patch**

````diff
diff --git a/internal/jam/adminui/dest_detail_test.go b/internal/jam/adminui/dest_detail_test.go
index 6dcb69a..3d17100 100644
--- a/internal/jam/adminui/dest_detail_test.go
+++ b/internal/jam/adminui/dest_detail_test.go
@@ -69,7 +69,7 @@ func TestDestinationDetailEnvRolesAndConflicts(t *testing.T) {
 		`href="/ui/roles/acme/dev"`, `href="/ui/roles/acme/ops"`,
 		"gh-pat-acme", // dev's own mapping
 		`class="banner error conflict"`, "gh-alt",
-		`aria-current="page">Destinations`,
+		`aria-current="page">Specs`,
 	} {
 		if !strings.Contains(body, want) {
 			t.Errorf("github-api detail missing %q", want)
diff --git a/internal/jam/adminui/kits_page_test.go b/internal/jam/adminui/kits_page_test.go
index 8ec1dca..59a3c28 100644
--- a/internal/jam/adminui/kits_page_test.go
+++ b/internal/jam/adminui/kits_page_test.go
@@ -77,7 +77,7 @@ func TestKitPageShowsCurrentVersion(t *testing.T) {
 	}
 	body := rec.Body.String()
 	for _, want := range []string{
-		"<h1>web</h1>", `aria-current="page">Kits`,
+		"<h1>web</h1>", `aria-current="page">Specs`,
 		`href="/ui/kits/web?v=1"`, `class="ver current`,
 		"ghcr.io/acme/web@sha256:abc",
 		"github.com", "anthropic.com", // egress + excluded by the ceiling
diff --git a/internal/jam/adminui/model_specs_test.go b/internal/jam/adminui/model_specs_test.go
index 79e534c..97d2e14 100644
--- a/internal/jam/adminui/model_specs_test.go
+++ b/internal/jam/adminui/model_specs_test.go
@@ -65,7 +65,7 @@ func TestModelSpecsListAndNav(t *testing.T) {
 	body := rec.Body.String()
 	for _, want := range []string{
 		`href="/ui/model-specs/default"`, "2.1.0", "anth", "claude-opus-5-5", "vertex",
-		`aria-current="page">Model-specs`,
+		`aria-current="page">Specs`,
 		`hx-post="/ui/model-specs"`,                // the create form
 		`<option value="anth">anth</option>`,       // credential select, names only
 		`<option value="bedrock">bedrock</option>`, // provider select
@@ -105,7 +105,7 @@ func TestModelSpecDetailPrefilled(t *testing.T) {
 		"ANTHROPIC_VERTEX_PROJECT_ID=proj\nCLOUD_ML_REGION=us-east5</textarea>",
 		"superpowers@claude-plugins-official</textarea>",
 		"&#34;theme&#34;: &#34;dark&#34;",
-		`aria-current="page">Model-specs`,
+		`aria-current="page">Specs`,
 	} {
 		if !strings.Contains(body, want) {
 			t.Errorf("detail missing %q", want)
diff --git a/internal/jam/adminui/nav_test.go b/internal/jam/adminui/nav_test.go
new file mode 100644
index 0000000..2c312dd
--- /dev/null
+++ b/internal/jam/adminui/nav_test.go
@@ -0,0 +1,46 @@
+package adminui_test
+
+import (
+	"strings"
+	"testing"
+)
+
+// The top nav is six sections, in order, and a page highlights its section
+// whatever its title: a detail page highlights its list's section.
+func TestTopNavSections(t *testing.T) {
+	h := projHandler(seedProjects(t))
+	body := get(t, h, "/ui/").Body.String()
+	nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
+	last := -1
+	for _, label := range []string{">Dashboard<", ">Projects<", ">Users<", ">Agents<", ">Specs<", ">Intercom<"} {
+		i := strings.Index(nav, label)
+		if i < 0 || i < last {
+			t.Fatalf("nav order: %q missing or out of order in\n%s", label, nav)
+		}
+		last = i
+	}
+	for _, gone := range []string{">Roles<", ">Actors<", ">Studios<", ">Kits<"} {
+		if strings.Contains(nav, gone) {
+			t.Errorf("nav still has %s", gone)
+		}
+	}
+	for path, section := range map[string]string{
+		"/ui/roles/acme/dev": "Projects",
+		"/ui/projects/acme":  "Projects",
+		"/ui/coves":          "Agents",
+		"/ui/actors":         "Agents",
+		"/ui/kits":           "Specs",
+		"/ui/model-specs":    "Specs",
+		"/ui/users":          "Users",
+	} {
+		body := get(t, h, path).Body.String()
+		nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
+		if n := strings.Count(nav, `aria-current="page"`); n != 1 || !strings.Contains(nav, `aria-current="page">`+section+"<") {
+			t.Errorf("%s: want only %s current in the top nav, got %d marked", path, section, n)
+		}
+	}
+	// search belongs to no section
+	if body := get(t, h, "/ui/search?q=acme").Body.String(); strings.Contains(body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")], `aria-current`) {
+		t.Errorf("search should highlight no section")
+	}
+}
diff --git a/internal/jam/adminui/polish_test.go b/internal/jam/adminui/polish_test.go
index ada1a91..ec9ea00 100644
--- a/internal/jam/adminui/polish_test.go
+++ b/internal/jam/adminui/polish_test.go
@@ -14,9 +14,11 @@ func TestNavMarksCurrentPage(t *testing.T) {
 	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
 	for path, href := range map[string]string{
 		"/ui/":             `href="/ui/"`,
-		"/ui/actors":       `href="/ui/actors"`,
-		"/ui/kits":         `href="/ui/kits"`,
-		"/ui/destinations": `href="/ui/destinations"`,
+		"/ui/actors":       `href="/ui/coves"`, // Agents
+		"/ui/kits":         `href="/ui/kits"`,  // Specs
+		"/ui/destinations": `href="/ui/kits"`,
+		"/ui/users":        `href="/ui/users"`,
+		"/ui/projects":     `href="/ui/projects"`,
 	} {
 		body := get(t, h, path).Body.String()
 		if !strings.Contains(body, href+` aria-current="page"`) {
diff --git a/internal/jam/adminui/role_detail_test.go b/internal/jam/adminui/role_detail_test.go
index f96d272..5065823 100644
--- a/internal/jam/adminui/role_detail_test.go
+++ b/internal/jam/adminui/role_detail_test.go
@@ -87,7 +87,7 @@ func TestRoleDetailShowsEverything(t *testing.T) {
 		"nightly", "run the nightly sweep",
 		"holder-plain", "holder-ovr", "override",
 		"studio-of-review",
-		`aria-current="page">Roles`, // still under the Roles tab
+		`aria-current="page">Projects`, // a role lives in its project
 	} {
 		if !strings.Contains(body, want) {
 			t.Errorf("role detail missing %q", want)
diff --git a/internal/jam/adminui/studio_page_test.go b/internal/jam/adminui/studio_page_test.go
index 925eb5d..2d90dd4 100644
--- a/internal/jam/adminui/studio_page_test.go
+++ b/internal/jam/adminui/studio_page_test.go
@@ -56,7 +56,7 @@ func TestStudioPageShowsRuntime(t *testing.T) {
 	}
 	body := rec.Body.String()
 	for _, want := range []string{
-		`<h1 class="mono">sess-1</h1>`, `aria-current="page">Studios`,
+		`<h1 class="mono">sess-1</h1>`, `aria-current="page">Agents`,
 		`class="pill phase-live"`, "waiting",
 		"personal", "alice", // kind + owner
 		`href="/ui/projects/acme"`, `href="/ui/roles/acme/dev"`, "COV-9",
diff --git a/internal/jam/adminui/studio_text_test.go b/internal/jam/adminui/studio_text_test.go
index 63fb6a6..a6f23af 100644
--- a/internal/jam/adminui/studio_text_test.go
+++ b/internal/jam/adminui/studio_text_test.go
@@ -19,7 +19,7 @@ func TestUISaysStudioAndJam(t *testing.T) {
 		t.Fatalf("GET /ui/coves = %d", page.Code)
 	}
 	body := page.Body.String()
-	for _, want := range []string{"<title>Jam — Studios</title>", "<h1>Studios</h1>", `<a href="/ui/coves" aria-current="page">Studios</a>`, "No studios."} {
+	for _, want := range []string{"<title>Jam — Studios</title>", "<h1>Studios</h1>", `<a href="/ui/coves" aria-current="page">Agents</a>`, "No studios."} {
 		if !strings.Contains(body, want) {
 			t.Errorf("studios page missing %q; got:\n%s", want, body)
 		}
diff --git a/internal/jam/adminui/users_test.go b/internal/jam/adminui/users_test.go
index 8f82338..1896126 100644
--- a/internal/jam/adminui/users_test.go
+++ b/internal/jam/adminui/users_test.go
@@ -160,7 +160,7 @@ func TestProjectMembersSection(t *testing.T) {
 
 func TestActorsPageReplacesRoster(t *testing.T) {
 	h := projHandler(seedProjects(t))
-	if rec := get(t, h, "/ui/actors"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `aria-current="page">Actors`) {
+	if rec := get(t, h, "/ui/actors"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `aria-current="page">Agents`) {
 		t.Fatalf("actors page = %d", rec.Code)
 	}
 	if rec := get(t, h, "/ui/roster"); rec.Code != http.StatusNotFound {
````

- [ ] **Step 2: Run to verify RED**

Run: `go test ./internal/jam/adminui/`
Expected: FAIL — `TestTopNavSections` (nav order / Roles, Actors, Studios, Kits tabs still present) and the updated `aria-current="page">Specs|Agents|Projects` assertions.

- [ ] **Step 3: Apply the code patch**

````diff
diff --git a/internal/jam/adminui/adminui.go b/internal/jam/adminui/adminui.go
index 84a5c69..23519ae 100644
--- a/internal/jam/adminui/adminui.go
+++ b/internal/jam/adminui/adminui.go
@@ -21,27 +21,29 @@ import (
 var files embed.FS
 
 // page holds one parsed template set (layout + that page's content). Each set's
-// full page is rendered via ExecuteTemplate(w, "layout", data).
+// full page is rendered via ExecuteTemplate(w, "layout", data). The first
+// argument is the page's top-nav section (see nav.go), which the layout
+// highlights.
 var pages = map[string]*template.Template{
-	"dashboard":    mustParse("coves.html", "context_panel.html", "dashboard.html"),
-	"coves":        mustParse("coves.html"),
-	"roster":       mustParse("roster.html"),
-	"users":        mustParse("users.html"),
-	"user":         mustParse("user.html"),
-	"roles":        mustParse("roles.html"),
-	"kits":         mustParse("kits.html"),
-	"destinations": mustParse("dest_fields.html", "destinations.html"),
-	"intercom":     mustParse("intercom.html"),
-	"session":      mustParse("session.html"),
-	"role":         mustParse("coves.html", "context_panel.html", "role.html"),
-	"destination":  mustParse("dest_fields.html", "destination.html"),
-	"model-specs":  mustParse("model_spec_fields.html", "model_specs.html"),
-	"model-spec":   mustParse("model_spec_fields.html", "model_spec.html"),
-	"kit":          mustParse("kit.html"),
-	"projects":     mustParse("projects.html"),
-	"project":      mustParse("coves.html", "context_panel.html", "project.html"),
-	"studio":       mustParse("studio.html"),
-	"search":       mustParse("search.html"),
+	"dashboard":    mustParse(navDashboard, "coves.html", "context_panel.html", "dashboard.html"),
+	"coves":        mustParse(navAgents, "coves.html"),
+	"roster":       mustParse(navAgents, "roster.html"),
+	"users":        mustParse(navUsers, "users.html"),
+	"user":         mustParse(navUsers, "user.html"),
+	"roles":        mustParse(navProjects, "roles.html"),
+	"kits":         mustParse(navSpecs, "kits.html"),
+	"destinations": mustParse(navSpecs, "dest_fields.html", "destinations.html"),
+	"intercom":     mustParse(navIntercom, "intercom.html"),
+	"session":      mustParse(navAgents, "session.html"),
+	"role":         mustParse(navProjects, "coves.html", "context_panel.html", "role.html"),
+	"destination":  mustParse(navSpecs, "dest_fields.html", "destination.html"),
+	"model-specs":  mustParse(navSpecs, "model_spec_fields.html", "model_specs.html"),
+	"model-spec":   mustParse(navSpecs, "model_spec_fields.html", "model_spec.html"),
+	"kit":          mustParse(navSpecs, "kit.html"),
+	"projects":     mustParse(navProjects, "projects.html"),
+	"project":      mustParse(navProjects, "coves.html", "context_panel.html", "project.html"),
+	"studio":       mustParse(navAgents, "studio.html"),
+	"search":       mustParse(navNone, "search.html"),
 }
 
 // roleRow is one project/role pair flattened for the roles table.
@@ -69,13 +71,15 @@ func roleRows(store jam.Store) []roleRow {
 	return out
 }
 
-func mustParse(names ...string) *template.Template {
+func mustParse(section navSection, names ...string) *template.Template {
 	paths := make([]string, 0, len(names)+1)
 	paths = append(paths, "templates/layout.html")
 	for _, n := range names {
 		paths = append(paths, "templates/"+n)
 	}
-	return template.Must(template.New("").Funcs(funcs).ParseFS(files, paths...))
+	return template.Must(template.New("").Funcs(funcs).Funcs(template.FuncMap{
+		"navSection": func() navSection { return section },
+	}).ParseFS(files, paths...))
 }
 
 // covesData, rosterData and rolesData are the payloads of those pages and
@@ -102,6 +106,7 @@ var funcs = template.FuncMap{
 		return fmtDur(d)
 	},
 	"roleURL":    roleURL,
+	"navItems":   func() []navItem { return navItems },
 	"hl":         highlight,
 	"stylesheet": func() string { return uiassets.StylesheetHref("/ui/static/") },
 	"destURL":    destURL,
diff --git a/internal/jam/adminui/nav.go b/internal/jam/adminui/nav.go
new file mode 100644
index 0000000..39daa51
--- /dev/null
+++ b/internal/jam/adminui/nav.go
@@ -0,0 +1,34 @@
+package adminui
+
+// navSection is a top-nav section. Each page's template set is bound to one
+// (mustParse), and the layout highlights the nav item whose Section matches —
+// so a detail page highlights its section whatever its title.
+type navSection string
+
+const (
+	navNone      navSection = "" // highlights nothing (search)
+	navDashboard navSection = "dashboard"
+	navProjects  navSection = "projects"
+	navUsers     navSection = "users"
+	navAgents    navSection = "agents"
+	navSpecs     navSection = "specs"
+	navIntercom  navSection = "intercom"
+)
+
+// navItem is one top-nav link.
+type navItem struct {
+	Section navSection
+	Label   string
+	Href    string
+}
+
+// navItems is the top nav, in order. Agents and Specs land on today's studio
+// and kit lists until their own pages exist.
+var navItems = []navItem{
+	{navDashboard, "Dashboard", "/ui/"},
+	{navProjects, "Projects", "/ui/projects"},
+	{navUsers, "Users", "/ui/users"},
+	{navAgents, "Agents", "/ui/coves"},
+	{navSpecs, "Specs", "/ui/kits"},
+	{navIntercom, "Intercom", "/ui/intercom"},
+}
diff --git a/internal/jam/adminui/templates/layout.html b/internal/jam/adminui/templates/layout.html
index 33fa917..eee44b9 100644
--- a/internal/jam/adminui/templates/layout.html
+++ b/internal/jam/adminui/templates/layout.html
@@ -107,17 +107,10 @@
   <header class="topbar">
     <span class="brand"><span class="dot"></span>Jam <small>Admin</small></span>
     <nav>
-      {{- $t := .Title}}
-      <a href="/ui/"{{if eq $t "Dashboard"}} aria-current="page"{{end}}>Dashboard</a>
-      <a href="/ui/projects"{{if eq $t "Projects"}} aria-current="page"{{end}}>Projects</a>
-      <a href="/ui/coves"{{if eq $t "Studios"}} aria-current="page"{{end}}>Studios</a>
-      <a href="/ui/users"{{if eq $t "Users"}} aria-current="page"{{end}}>Users</a>
-      <a href="/ui/actors"{{if eq $t "Actors"}} aria-current="page"{{end}}>Actors</a>
-      <a href="/ui/roles"{{if eq $t "Roles"}} aria-current="page"{{end}}>Roles</a>
-      <a href="/ui/kits"{{if eq $t "Kits"}} aria-current="page"{{end}}>Kits</a>
-      <a href="/ui/destinations"{{if eq $t "Destinations"}} aria-current="page"{{end}}>Destinations</a>
-      <a href="/ui/model-specs"{{if eq $t "Model-specs"}} aria-current="page"{{end}}>Model-specs</a>
-      <a href="/ui/intercom"{{if eq $t "Intercom"}} aria-current="page"{{end}}>Intercom</a>
+      {{- $s := navSection}}
+      {{- range navItems}}
+      <a href="{{.Href}}"{{if eq .Section $s}} aria-current="page"{{end}}>{{.Label}}</a>
+      {{- end}}
     </nav>
     <form class="topsearch" action="/ui/search" method="get" role="search">
       <input id="q-top" name="q" placeholder="Search  /" aria-label="Search" autocomplete="off">
````

- [ ] **Step 4: Run to verify GREEN**

Run: `go test ./internal/jam/adminui/ && go vet ./internal/jam/adminui/ && gofmt -l internal/jam/adminui && just lint`
Expected: `ok`, no gofmt output, lint clean.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/adminui
git commit -m "feat(adminui): top nav by section — Dashboard, Projects, Users, Agents, Specs, Intercom"
```

---

### Task 2: Project tree, section pages, role page under its project, redirects

**Files:**
- Create: `internal/jam/adminui/projtree.go`, `templates/projtree.html`, `templates/squawks.html`
- Modify: `adminui.go`, `nav.go` (`redirect`), `projects.go`, `project_edit.go`, `role_detail.go`, `intercom.go`, `writes.go`, `templates/project.html`, `templates/role.html`, `templates/intercom.html`, `templates/layout.html` (narrow-screen collapse script)
- Delete: `templates/roles.html`
- Tests: `nav_test.go` (+ redirects, tree, section writes, create role, project intercom) and updates to `adminui_test.go`, `context_edit_test.go`, `dest_detail_test.go`, `image_test.go`, `kits_page_test.go`, `polish_test.go`, `project_edit_test.go`, `projects_test.go`, `role_detail_test.go`, `role_edit_test.go`, `role_request_test.go`, `search_test.go`, `studio_page_test.go`, `typeahead_test.go`, `users_test.go`, `writes_test.go`

**Interfaces:**
- Consumes (Task 1): `navSection` consts, `mustParse(section, …)`.
- Produces:
  - `func redirect(w http.ResponseWriter, r *http.Request, target string)` — 301, appends `?RawQuery`.
  - `type projectSection string` (`sectionOverview|Members|Agents|Roles|Intercom|Escalation`), `projectSections` (order + labels), `func projectSectionURL(project string, s projectSection) string`.
  - `type projectTree struct{ Project, Href, Current string; Nodes []treeNode }`; `func buildProjectTree(store jam.Store, img jam.ImageResolver, project string, section projectSection, role string) projectTree`.
  - `type crumb struct{ Label, Href string }`; `func projectCrumbs(project string, section projectSection, role string) []crumb`.
  - `func buildProjectDetail(store jam.Store, img jam.ImageResolver, msgs SquawkReader, name string, section projectSection) (projectDetail, bool)`; `registerProjects(mux, store, img, msgs, canRequest bool, log, guardWrite)`; `registerProjectEdits(mux, store, img, msgs, log, guardWrite)`.
  - `func roleURL(project, name string) string` → `/ui/projects/{p}/roles/{name}`; `func roleWriteBase(project, name string) string` → `/ui/roles/{p}/{name}`.
  - `type squawkFilter`, `type squawkTable`, `func filterSquawks(msgs SquawkReader, f squawkFilter, limit int) squawkTable`; template `squawk-table`.
  - Templates `project-tree`, `crumbs`, `project-overview|members|agents|roles|intercom|escalation`.

Behavior notes the patch implements (read them before reviewing the diff):
- `POST /ui/roles` now answers `200` with only `HX-Redirect` (the new role page); `DELETE /ui/roles/{p}/{r}` answers `200` with no body (the role page's Delete navigates to the project's Roles). The roles table fragment they used to return is gone with `roles.html`.
- A project's Roles section keeps the per-role **Request** button when a supervisor runs (`projectDetail.CanRequest`).
- On a role page, the tree's Agents branch lists all of the project's live agents; the role's own studio table still lists only its studios (the role-detail test now checks the `#role` body for that).

- [ ] **Step 1: Apply the test patch**

````diff
diff --git a/internal/jam/adminui/adminui_test.go b/internal/jam/adminui/adminui_test.go
index 8f3c259..ac61dbb 100644
--- a/internal/jam/adminui/adminui_test.go
+++ b/internal/jam/adminui/adminui_test.go
@@ -146,10 +146,10 @@ func TestRolesView(t *testing.T) {
 	if err := store.PutRole("acme", jam.Role{Name: "review", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour}, Kit: ""}); err != nil {
 		t.Fatal(err)
 	}
-	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/roles").Body.String()
-	for _, want := range []string{"acme", "review", "git"} {
+	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/projects/acme/roles").Body.String()
+	for _, want := range []string{`href="/ui/projects/acme/roles/review"`, "git", `<input type="hidden" name="project" value="acme">`} {
 		if !strings.Contains(body, want) {
-			t.Errorf("roles view missing %q", want)
+			t.Errorf("project roles view missing %q", want)
 		}
 	}
 }
diff --git a/internal/jam/adminui/context_edit_test.go b/internal/jam/adminui/context_edit_test.go
index 3eff1ed..1ebf4de 100644
--- a/internal/jam/adminui/context_edit_test.go
+++ b/internal/jam/adminui/context_edit_test.go
@@ -25,7 +25,7 @@ func roleStore(t *testing.T) jam.Store {
 func TestRoleContextPanelShowsAndEdits(t *testing.T) {
 	store := roleStore(t)
 	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
-	body := get(t, h, "/ui/roles/acme/review").Body.String()
+	body := get(t, h, "/ui/projects/acme/roles/review").Body.String()
 	for _, want := range []string{"Session context", `hx-post="/ui/roles/acme/review/context"`, "0 / 1200 bytes"} {
 		if !strings.Contains(body, want) {
 			t.Errorf("role page missing %q", want)
@@ -40,7 +40,7 @@ func TestRoleContextPanelShowsAndEdits(t *testing.T) {
 	if r.Context.Core != "Review every PR within a day." || len(r.Context.Leaves) != 1 {
 		t.Fatalf("stored = %+v", r.Context)
 	}
-	page := get(t, h, "/ui/roles/acme/review").Body.String()
+	page := get(t, h, "/ui/projects/acme/roles/review").Body.String()
 	for _, want := range []string{"Review every PR within a day.", "style.md", "you are commenting on style", "29 / 1200 bytes"} {
 		if !strings.Contains(page, want) {
 			t.Errorf("panel missing %q", want)
@@ -127,7 +127,7 @@ func TestProjectAndJamContextPanels(t *testing.T) {
 // (which has none of the role/project page styles) and wraps long cores.
 func TestContextCardStyledEverywhere(t *testing.T) {
 	h := adminui.Handler(roleStore(t), testLogger(), nil, nil, credAny, nil)
-	for _, path := range []string{"/ui/", "/ui/roles/acme/review", "/ui/projects/acme"} {
+	for _, path := range []string{"/ui/", "/ui/projects/acme/roles/review", "/ui/projects/acme"} {
 		body := get(t, h, path).Body.String()
 		for _, want := range []string{`class="card full ctx-card"`, ".ctx-card>header", ".ctx-card .ctx-core{white-space:pre-wrap"} {
 			if !strings.Contains(body, want) {
diff --git a/internal/jam/adminui/dest_detail_test.go b/internal/jam/adminui/dest_detail_test.go
index 3d17100..6d6c035 100644
--- a/internal/jam/adminui/dest_detail_test.go
+++ b/internal/jam/adminui/dest_detail_test.go
@@ -66,7 +66,7 @@ func TestDestinationDetailEnvRolesAndConflicts(t *testing.T) {
 	for _, want := range []string{
 		"<h1>github-api</h1>", "/api/v3/", "https://api.github.com",
 		"GH_HOST", "{host}", "GH_ENTERPRISE_TOKEN", "{token}",
-		`href="/ui/roles/acme/dev"`, `href="/ui/roles/acme/ops"`,
+		`href="/ui/projects/acme/roles/dev"`, `href="/ui/projects/acme/roles/ops"`,
 		"gh-pat-acme", // dev's own mapping
 		`class="banner error conflict"`, "gh-alt",
 		`aria-current="page">Specs`,
diff --git a/internal/jam/adminui/image_test.go b/internal/jam/adminui/image_test.go
index 6933483..b0bea35 100644
--- a/internal/jam/adminui/image_test.go
+++ b/internal/jam/adminui/image_test.go
@@ -53,7 +53,7 @@ func TestImageStaleIsFlagged(t *testing.T) {
 	if !strings.Contains(page, "<th>Image</th>") || !strings.Contains(page, "<td>ok</td>") {
 		t.Fatalf("fresh image not shown ok; page:\n%s", page)
 	}
-	if strings.Contains(get(t, h, "/ui/roles/acme/review").Body.String(), "image stale") {
+	if strings.Contains(get(t, h, "/ui/projects/acme/roles/review").Body.String(), "image stale") {
 		t.Fatal("a fresh standing studio must not be flagged")
 	}
 
@@ -62,7 +62,7 @@ func TestImageStaleIsFlagged(t *testing.T) {
 	if !strings.Contains(page, `title="raised on an older image than its role would run now">stale</span>`) {
 		t.Fatalf("stale image not flagged; page:\n%s", page)
 	}
-	if !strings.Contains(get(t, h, "/ui/roles/acme/review").Body.String(), ">image stale</span>") {
+	if !strings.Contains(get(t, h, "/ui/projects/acme/roles/review").Body.String(), ">image stale</span>") {
 		t.Fatal("stale standing studio not flagged on the role page")
 	}
 }
@@ -114,7 +114,7 @@ func TestEditStandingUpgrade(t *testing.T) {
 	const flash = `<div id="flash" hx-swap-oob="innerHTML"><p class="ok">`
 	const path = "/ui/roles/acme/review/standing/nightly/upgrade"
 
-	body := get(t, h, "/ui/roles/acme/review").Body.String()
+	body := get(t, h, "/ui/projects/acme/roles/review").Body.String()
 	if !strings.Contains(body, `<button class="small" `+btn) || !strings.Contains(body, `hx-confirm="Upgrade standing session nightly?`) {
 		t.Fatalf("role page lacks a confirmed upgrade button:\n%s", body)
 	}
@@ -124,7 +124,7 @@ func TestEditStandingUpgrade(t *testing.T) {
 	}
 
 	asm = "a2" // stale
-	if body := get(t, h, "/ui/roles/acme/review").Body.String(); !strings.Contains(body, `<button class="small primary" `+btn) {
+	if body := get(t, h, "/ui/projects/acme/roles/review").Body.String(); !strings.Contains(body, `<button class="small primary" `+btn) {
 		t.Fatalf("a stale studio's upgrade button must be emphasized:\n%s", body)
 	}
 	rec := post(t, h, path, url.Values{})
@@ -142,7 +142,7 @@ func TestEditStandingUpgrade(t *testing.T) {
 	}
 
 	ro := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
-	if strings.Contains(get(t, ro, "/ui/roles/acme/review").Body.String(), "/standing/nightly/upgrade") {
+	if strings.Contains(get(t, ro, "/ui/projects/acme/roles/review").Body.String(), "/standing/nightly/upgrade") {
 		t.Error("no supervisor: the upgrade button must be hidden")
 	}
 	if rec := post(t, ro, path, url.Values{}); rec.Code != http.StatusServiceUnavailable {
diff --git a/internal/jam/adminui/kits_page_test.go b/internal/jam/adminui/kits_page_test.go
index 59a3c28..06945b2 100644
--- a/internal/jam/adminui/kits_page_test.go
+++ b/internal/jam/adminui/kits_page_test.go
@@ -83,7 +83,7 @@ func TestKitPageShowsCurrentVersion(t *testing.T) {
 		"github.com", "anthropic.com", // egress + excluded by the ceiling
 		"GO_VERSION", "1.23", "GH_TOKEN", "clone access",
 		"You work on the web and api services.",
-		`href="/ui/roles/acme/builder"`,
+		`href="/ui/projects/acme/roles/builder"`,
 		`hx-post="/ui/kits/web/versions"`,
 	} {
 		if !strings.Contains(body, want) {
@@ -97,7 +97,7 @@ func TestKitPageShowsCurrentVersion(t *testing.T) {
 
 func TestDefaultKitListsImplicitRoles(t *testing.T) {
 	body := get(t, kitsHandler(seedKits(t)), "/ui/kits/default").Body.String()
-	if !strings.Contains(body, `href="/ui/roles/acme/plain"`) || !strings.Contains(body, "no kit set") {
+	if !strings.Contains(body, `href="/ui/projects/acme/roles/plain"`) || !strings.Contains(body, "no kit set") {
 		t.Errorf("default kit should list roles with no kit set")
 	}
 }
diff --git a/internal/jam/adminui/nav_test.go b/internal/jam/adminui/nav_test.go
index 2c312dd..d822755 100644
--- a/internal/jam/adminui/nav_test.go
+++ b/internal/jam/adminui/nav_test.go
@@ -1,8 +1,15 @@
 package adminui_test
 
 import (
+	"net/http"
+	"net/url"
 	"strings"
 	"testing"
+
+	"github.com/aethons-tools/cove/internal/ident"
+	"github.com/aethons-tools/cove/internal/intercom"
+	"github.com/aethons-tools/cove/internal/jam"
+	"github.com/aethons-tools/cove/internal/jam/adminui"
 )
 
 // The top nav is six sections, in order, and a page highlights its section
@@ -25,13 +32,13 @@ func TestTopNavSections(t *testing.T) {
 		}
 	}
 	for path, section := range map[string]string{
-		"/ui/roles/acme/dev": "Projects",
-		"/ui/projects/acme":  "Projects",
-		"/ui/coves":          "Agents",
-		"/ui/actors":         "Agents",
-		"/ui/kits":           "Specs",
-		"/ui/model-specs":    "Specs",
-		"/ui/users":          "Users",
+		"/ui/projects/acme/roles/dev": "Projects",
+		"/ui/projects/acme/members":   "Projects",
+		"/ui/coves":                   "Agents",
+		"/ui/actors":                  "Agents",
+		"/ui/kits":                    "Specs",
+		"/ui/model-specs":             "Specs",
+		"/ui/users":                   "Users",
 	} {
 		body := get(t, h, path).Body.String()
 		nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
@@ -44,3 +51,149 @@ func TestTopNavSections(t *testing.T) {
 		t.Errorf("search should highlight no section")
 	}
 }
+
+// Moved pages answer 301 to their new home, keeping the query string.
+func TestMovedPagesRedirect(t *testing.T) {
+	h := projHandler(seedProjects(t))
+	for from, to := range map[string]string{
+		"/ui/roles":          "/ui/projects",
+		"/ui/roles/acme/dev": "/ui/projects/acme/roles/dev",
+		"/ui/roles?x=1":      "/ui/projects?x=1",
+	} {
+		rec := get(t, h, from)
+		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
+			t.Errorf("GET %s = %d → %q, want 301 → %q", from, rec.Code, rec.Header().Get("Location"), to)
+		}
+	}
+}
+
+// The tree opens the branch holding the current page and marks its node; the
+// breadcrumb follows the path, every segment a link.
+func TestProjectTreeMarksCurrent(t *testing.T) {
+	h := projHandler(seedProjects(t))
+	tree := func(body string) string {
+		return body[strings.Index(body, `class="ptree"`):strings.Index(body, "</nav>\n</details>")]
+	}
+	body := get(t, h, "/ui/projects/acme/roles/dev").Body.String()
+	tr := tree(body)
+	if !strings.Contains(tr, `<a class="mono" href="/ui/projects/acme/roles/dev" aria-current="page">dev</a>`) {
+		t.Errorf("role page: its leaf should be current:\n%s", tr)
+	}
+	if strings.Count(tr, "<details open>") != 1 || !strings.Contains(tr, `<details open>
+        <summary><a href="/ui/projects/acme/roles">Roles`) {
+		t.Errorf("role page: only the Roles branch should be open:\n%s", tr)
+	}
+	if !strings.Contains(body, `<summary>acme ▸ Roles ▸ dev</summary>`) {
+		t.Errorf("narrow-screen summary should name the current node")
+	}
+	for _, crumb := range []string{`<a href="/ui/projects">Projects</a>`, `<a href="/ui/projects/acme">acme</a>`,
+		`<a href="/ui/projects/acme/roles">Roles</a>`, `<a href="/ui/projects/acme/roles/dev">dev</a>`} {
+		if !strings.Contains(body[strings.Index(body, `class="crumbs"`):], crumb) {
+			t.Errorf("role page crumbs missing %s", crumb)
+		}
+	}
+
+	tr = tree(get(t, h, "/ui/projects/acme/members").Body.String())
+	if !strings.Contains(tr, `<a href="/ui/projects/acme/members" aria-current="page">Members</a>`) || strings.Contains(tr, "<details open>") {
+		t.Errorf("members page: Members current, no branch open:\n%s", tr)
+	}
+	tr = tree(get(t, h, "/ui/projects/acme").Body.String())
+	if !strings.Contains(tr, `<a href="/ui/projects/acme" aria-current="page">Overview</a>`) {
+		t.Errorf("project page: Overview current:\n%s", tr)
+	}
+	if !strings.Contains(tr, `>Agents <span class="count">(1)</span>`) {
+		t.Errorf("Agents should count acme's one live agent:\n%s", tr)
+	}
+}
+
+// A section's write answers with that section re-rendered for its #project
+// swap — not the whole project.
+func TestProjectEditsAnswerWithTheirSection(t *testing.T) {
+	h := projHandler(seedProjects(t))
+	for _, c := range []struct {
+		path    string
+		form    url.Values
+		section string
+	}{
+		{"/ui/projects/acme/escalation", url.Values{"category": {"infra"}, "tiers": {"human:alice@10m"}}, "<h1>Escalation</h1>"},
+		{"/ui/projects/acme/channels", url.Values{"name": {"ops"}, "service": {"discord"}, "ref": {"chan-ops"}}, "<h1>Intercom</h1>"},
+		{"/ui/projects/acme/chat-service", url.Values{"service": {""}}, "<h1>acme</h1>"},
+	} {
+		rec := post(t, h, c.path, c.form)
+		body := rec.Body.String()
+		if rec.Code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(body), `<div id="project">`) || !strings.Contains(body, c.section) {
+			t.Errorf("POST %s = %d, want its section %s:\n%s", c.path, rec.Code, c.section, body)
+		}
+		if strings.Contains(body, "<html") || strings.Contains(body, `class="ptree"`) {
+			t.Errorf("POST %s should answer with the section only", c.path)
+		}
+	}
+}
+
+// A role is created from its project's Roles page (the project comes from the
+// page) and the response sends the browser to the new role's page.
+func TestCreateRoleFromProject(t *testing.T) {
+	store := seedProjects(t)
+	h := projHandler(store)
+	page := get(t, h, "/ui/projects/acme/roles").Body.String()
+	if !strings.Contains(page, `<form hx-post="/ui/roles" hx-swap="none">`) || strings.Contains(page, `data-ta="projects"`) {
+		t.Errorf("roles section should post the role with its project fixed:\n%s", page)
+	}
+	rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"qa"}})
+	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/projects/acme/roles/qa" {
+		t.Fatalf("create = %d → %q", rec.Code, rec.Header().Get("HX-Redirect"))
+	}
+	if _, ok := store.GetRole("acme", "qa"); !ok {
+		t.Error("role not created")
+	}
+}
+
+// The Intercom section lists the project's rooms above its recent messages,
+// linking the full, filtered log.
+func TestProjectIntercomSection(t *testing.T) {
+	store := jam.NewMemStore()
+	for _, p := range []string{"acme", "beta"} {
+		if err := store.CreateProject(p); err != nil {
+			t.Fatal(err)
+		}
+	}
+	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice"}); err != nil {
+		t.Fatal(err)
+	}
+	alice, _ := store.LookupName(ident.User, "alice")
+	acme, _ := store.GetProject("acme")
+	beta, _ := store.GetProject("beta")
+	eng, err := store.CreateChannel(jam.Channel{ProjectID: acme.ID, Kind: jam.SourceRoom, Key: "eng", Label: "eng"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	ops, err := store.CreateChannel(jam.Channel{ProjectID: beta.ID, Kind: jam.SourceRoom, Key: "ops", Label: "ops"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	lg := intercom.NewMemLog(nil)
+	ic := jam.NewIntercom(store, func() (ident.ID, bool) { return "", false }, lg, nil, nil)
+	for _, c := range []struct {
+		ch   jam.Channel
+		body string
+	}{{eng, "deploy started"}, {ops, "beta status"}} {
+		if _, err := ic.PostTrusted(c.ch, intercom.Squawk{From: alice, Body: c.body}); err != nil {
+			t.Fatal(err)
+		}
+	}
+	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, adminui.NewSquawkReader(store, ic, lg, nil))
+	body := get(t, h, "/ui/projects/acme/intercom").Body.String()
+	for _, want := range []string{"<h1>Intercom</h1>", "deploy started", `href="/ui/intercom?project=acme"`, `hx-post="/ui/projects/acme/channels"`} {
+		if !strings.Contains(body, want) {
+			t.Errorf("intercom section missing %q", want)
+		}
+	}
+	if strings.Contains(body, "beta status") {
+		t.Error("intercom section shows another project's message")
+	}
+	// no log configured: the rooms still render, the log says so
+	body = get(t, projHandler(store), "/ui/projects/acme/intercom").Body.String()
+	if !strings.Contains(body, "The message log is unavailable.") || !strings.Contains(body, "Add room") {
+		t.Errorf("intercom section without a log:\n%s", body)
+	}
+}
diff --git a/internal/jam/adminui/polish_test.go b/internal/jam/adminui/polish_test.go
index ec9ea00..6db8576 100644
--- a/internal/jam/adminui/polish_test.go
+++ b/internal/jam/adminui/polish_test.go
@@ -33,7 +33,7 @@ func TestNavMarksCurrentPage(t *testing.T) {
 // Every page carries the flash region and the htmx error hook, so a 4xx/5xx
 // write response is shown to the operator instead of silently dropped.
 func TestLayoutShipsFlashAndErrorHook(t *testing.T) {
-	body := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/roles").Body.String()
+	body := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/projects").Body.String()
 	for _, want := range []string{`id="flash"`, "htmx:responseError", `href="/ui/static/jam.css`} {
 		if !strings.Contains(body, want) {
 			t.Errorf("layout missing %q", want)
@@ -126,7 +126,7 @@ func TestRoleRequestTargetsFlash(t *testing.T) {
 	if err := store.PutRole("acme", jam.Role{Name: "pair"}); err != nil {
 		t.Fatal(err)
 	}
-	body := get(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil), "/ui/roles").Body.String()
+	body := get(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil), "/ui/projects/acme/roles").Body.String()
 	if !strings.Contains(body, `hx-target="#flash"`) || strings.Contains(body, "role-request-msg") {
 		t.Errorf("Request should target #flash; got:\n%s", body)
 	}
diff --git a/internal/jam/adminui/project_edit_test.go b/internal/jam/adminui/project_edit_test.go
index 76643ec..d2d94bc 100644
--- a/internal/jam/adminui/project_edit_test.go
+++ b/internal/jam/adminui/project_edit_test.go
@@ -11,18 +11,23 @@ import (
 	"github.com/aethons-tools/cove/internal/jam"
 )
 
+// Each project section offers its own edits, prefilled.
 func TestProjectPageOffersPrefilledEdits(t *testing.T) {
-	body := get(t, projHandler(seedProjects(t)), "/ui/projects/acme").Body.String()
-	for _, want := range []string{
-		`hx-post="/ui/projects/acme/members"`, "discord:dm-alice", `hx-delete="/ui/projects/acme/members/usr_`,
-		`hx-post="/ui/projects/acme/channels"`, `hx-delete="/ui/projects/acme/channels/eng"`,
-		`hx-post="/ui/projects/acme/escalation"`,
-		"human:alice@30m", "human:alice,channel:eng@10m",
-		`hx-delete="/ui/projects/acme/escalation?category=deploy"`,
-		`hx-post="/ui/projects/acme/chat-service"`, `<option value="discord" selected`,
+	h := projHandler(seedProjects(t))
+	for path, wants := range map[string][]string{
+		"/ui/projects/acme/members":  {`hx-post="/ui/projects/acme/members"`, "discord:dm-alice", `hx-delete="/ui/projects/acme/members/usr_`},
+		"/ui/projects/acme/intercom": {`hx-post="/ui/projects/acme/channels"`, `hx-delete="/ui/projects/acme/channels/eng"`},
+		"/ui/projects/acme/escalation": {
+			`hx-post="/ui/projects/acme/escalation"`, "human:alice@30m", "human:alice,channel:eng@10m",
+			`hx-delete="/ui/projects/acme/escalation?category=deploy"`,
+		},
+		"/ui/projects/acme": {`hx-post="/ui/projects/acme/chat-service"`, `<option value="discord" selected`},
 	} {
-		if !strings.Contains(body, want) {
-			t.Errorf("project page missing %q", want)
+		body := get(t, h, path).Body.String()
+		for _, want := range wants {
+			if !strings.Contains(body, want) {
+				t.Errorf("%s missing %q", path, want)
+			}
 		}
 	}
 }
@@ -70,7 +75,7 @@ func TestEditEscalation(t *testing.T) {
 	if rec := del(t, h, "/ui/projects/acme/escalation?category=deploy"); rec.Code != http.StatusOK {
 		t.Fatalf("clear deploy = %d", rec.Code)
 	}
-	body := get(t, h, "/ui/projects/acme").Body.String()
+	body := get(t, h, "/ui/projects/acme/escalation").Body.String()
 	if strings.Contains(body, "category <span class=\"mono\">deploy</span>") {
 		t.Errorf("a cleared chain should not be shown")
 	}
@@ -85,7 +90,7 @@ func TestEscalationFlagsUnknownTargets(t *testing.T) {
 	if err := store.SetEscalationPolicy("acme", "", []jam.EscalationTier{{Targets: []string{"human:ghost", "human:alice"}, Timeout: time.Minute}}); err != nil {
 		t.Fatal(err)
 	}
-	body := get(t, projHandler(store), "/ui/projects/acme").Body.String()
+	body := get(t, projHandler(store), "/ui/projects/acme/escalation").Body.String()
 	if n := strings.Count(body, `class="chip unknown-target"`); n != 1 {
 		t.Errorf("want 1 flagged target (human:ghost), got %d", n)
 	}
diff --git a/internal/jam/adminui/projects_test.go b/internal/jam/adminui/projects_test.go
index 0d24035..fbaf76d 100644
--- a/internal/jam/adminui/projects_test.go
+++ b/internal/jam/adminui/projects_test.go
@@ -129,29 +129,41 @@ func TestCreateAndDeleteProject(t *testing.T) {
 	}
 }
 
+// Every project page shows the tree; each section shows its own content and
+// nothing from another project.
 func TestProjectPage(t *testing.T) {
-	rec := get(t, projHandler(seedProjects(t)), "/ui/projects/acme")
-	if rec.Code != http.StatusOK {
-		t.Fatalf("project page = %d", rec.Code)
-	}
-	body := rec.Body.String()
-	for _, want := range []string{
-		"<h1>acme</h1>", `aria-current="page">Projects`,
-		`href="/ui/roles/acme/dev"`, `href="/ui/roles/acme/ops"`,
-		"a1", "a2", "studio-acme",
-		"alice", "alice-h", "dm-alice", `href="/ui/users/usr_`,
-		"eng", "chan-eng",
-		"human:alice", "30m", "deploy", "channel:eng", "10m",
-		"discord",
+	h := projHandler(seedProjects(t))
+	tree := []string{
+		`aria-current="page">Projects`, `class="ptree"`,
+		`href="/ui/projects/acme/members"`, `href="/ui/projects/acme/agents"`, `href="/ui/projects/acme/roles"`,
+		`href="/ui/projects/acme/intercom"`, `href="/ui/projects/acme/escalation"`,
+		`href="/ui/projects/acme/roles/dev"`, `href="/ui/projects/acme/roles/ops"`, // roles under Roles
+		"studio-acme", // its live agent under Agents
+		">eng<",       // its room under Intercom
+	}
+	for path, wants := range map[string][]string{
+		"/ui/projects/acme":            {"<h1>acme</h1>", "discord"},
+		"/ui/projects/acme/members":    {"<h1>Members</h1>", "alice", "alice-h", "dm-alice", `href="/ui/users/usr_`},
+		"/ui/projects/acme/agents":     {"<h1>Agents</h1>", ">a1<", ">a2<", "studio-acme"},
+		"/ui/projects/acme/roles":      {"<h1>Roles</h1>", `<input type="hidden" name="project" value="acme">`},
+		"/ui/projects/acme/intercom":   {"<h1>Intercom</h1>", "chan-eng"},
+		"/ui/projects/acme/escalation": {"<h1>Escalation</h1>", "human:alice", "30m", "deploy", "channel:eng", "10m"},
 	} {
-		if !strings.Contains(body, want) {
-			t.Errorf("project page missing %q", want)
+		rec := get(t, h, path)
+		if rec.Code != http.StatusOK {
+			t.Fatalf("%s = %d", path, rec.Code)
 		}
-	}
-	// ">a3<", not "a3": the page carries random ids (user links) that may contain it.
-	for _, gone := range []string{">a3<", "studio-solo", "/ui/roles/default/solo"} {
-		if strings.Contains(body, gone) {
-			t.Errorf("acme page shows %q from another project", gone)
+		body := rec.Body.String()
+		for _, want := range append(wants, tree...) {
+			if !strings.Contains(body, want) {
+				t.Errorf("%s missing %q", path, want)
+			}
+		}
+		// ">a3<", not "a3": the page carries random ids (user links) that may contain it.
+		for _, gone := range []string{">a3<", "studio-solo", "/ui/projects/default/roles/solo"} {
+			if strings.Contains(body, gone) {
+				t.Errorf("%s shows %q from another project", path, gone)
+			}
 		}
 	}
 }
@@ -168,7 +180,7 @@ func TestProjectPageNotFound(t *testing.T) {
 // project, so a typo can't land anything.
 func TestProjectPickers(t *testing.T) {
 	h := projHandler(seedProjects(t))
-	for _, path := range []string{"/ui/roles", "/ui/actors", "/ui/coves"} {
+	for _, path := range []string{"/ui/actors", "/ui/coves"} {
 		body := get(t, h, path).Body.String()
 		if !strings.Contains(body, `<input name="project" data-ta="projects" value="default"`) || strings.Contains(body, `<select name="project"`) {
 			t.Errorf("%s: project should be a type-ahead prefilled with default", path)
@@ -187,11 +199,11 @@ func TestProjectPickers(t *testing.T) {
 
 func TestProjectLinks(t *testing.T) {
 	h := projHandler(seedProjects(t))
-	if body := get(t, h, "/ui/roles/acme/dev").Body.String(); !strings.Contains(body, `href="/ui/projects/acme"`) {
+	if body := get(t, h, "/ui/projects/acme/roles/dev").Body.String(); !strings.Contains(body, `href="/ui/projects/acme"`) {
 		t.Errorf("role page should link its project")
 	}
-	if body := get(t, h, "/ui/roles").Body.String(); !strings.Contains(body, `href="/ui/projects/acme"`) {
-		t.Errorf("roles table should link projects")
+	if body := get(t, h, "/ui/projects/acme/roles").Body.String(); !strings.Contains(body, `href="/ui/projects/acme/roles/dev"`) {
+		t.Errorf("a project's roles should link their pages")
 	}
 }
 
diff --git a/internal/jam/adminui/role_detail_test.go b/internal/jam/adminui/role_detail_test.go
index 5065823..1e32400 100644
--- a/internal/jam/adminui/role_detail_test.go
+++ b/internal/jam/adminui/role_detail_test.go
@@ -69,7 +69,7 @@ func seedRichRole(t *testing.T) jam.Store {
 
 func TestRoleDetailShowsEverything(t *testing.T) {
 	h := adminui.Handler(seedRichRole(t), testLogger(), nil, nil, anyCred, nil)
-	rec := get(t, h, "/ui/roles/acme/review")
+	rec := get(t, h, "/ui/projects/acme/roles/review")
 	if rec.Code != http.StatusOK {
 		t.Fatalf("GET role detail = %d", rec.Code)
 	}
@@ -93,8 +93,11 @@ func TestRoleDetailShowsEverything(t *testing.T) {
 			t.Errorf("role detail missing %q", want)
 		}
 	}
+	// The project tree lists every live agent in acme; the role's own content
+	// shows only its own studios.
+	roleBody := body[strings.Index(body, `<div id="role">`):]
 	for _, gone := range []string{"not-a-holder", "studio-of-other", `hx-trigger="every 3s"`} {
-		if strings.Contains(body, gone) {
+		if strings.Contains(roleBody, gone) {
 			t.Errorf("role detail should not contain %q", gone)
 		}
 	}
@@ -106,7 +109,7 @@ func TestRoleDetailUnmanagedEgressAndEmptyAddressing(t *testing.T) {
 	if err := store.PutRole("acme", jam.Role{Name: "bare"}); err != nil {
 		t.Fatal(err)
 	}
-	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/roles/acme/bare").Body.String()
+	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/projects/acme/roles/bare").Body.String()
 	for _, want := range []string{"Kit default", "No comms targets"} {
 		if !strings.Contains(body, want) {
 			t.Errorf("bare role detail missing %q", want)
@@ -115,7 +118,7 @@ func TestRoleDetailUnmanagedEgressAndEmptyAddressing(t *testing.T) {
 }
 
 func TestRoleDetailNotFound(t *testing.T) {
-	rec := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/roles/acme/nope")
+	rec := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/projects/acme/roles/nope")
 	if rec.Code != http.StatusNotFound {
 		t.Fatalf("missing role = %d, want 404", rec.Code)
 	}
@@ -126,15 +129,15 @@ func TestRoleDetailNotFound(t *testing.T) {
 
 func TestRoleLinksFromRolesAndRoster(t *testing.T) {
 	h := adminui.Handler(seedRichRole(t), testLogger(), nil, nil, anyCred, nil)
-	roles := get(t, h, "/ui/roles").Body.String()
-	if !strings.Contains(roles, `href="/ui/roles/acme/review"`) {
+	roles := get(t, h, "/ui/projects/acme/roles").Body.String()
+	if !strings.Contains(roles, `href="/ui/projects/acme/roles/review"`) {
 		t.Errorf("roles table should link to the detail page")
 	}
 	if !strings.Contains(roles, "git-pat") {
 		t.Errorf("roles table should show the credential mapping")
 	}
 	roster := get(t, h, "/ui/actors").Body.String()
-	if !strings.Contains(roster, `href="/ui/roles/acme/review"`) {
+	if !strings.Contains(roster, `href="/ui/projects/acme/roles/review"`) {
 		t.Errorf("roster grant chips should link to the role")
 	}
 }
@@ -149,13 +152,13 @@ func TestRoleDetailShowsTurnEnd(t *testing.T) {
 		t.Fatal(err)
 	}
 	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
-	body := get(t, h, "/ui/roles/acme/worker").Body.String()
+	body := get(t, h, "/ui/projects/acme/roles/worker").Body.String()
 	for _, want := range []string{"Idle timeout", "45m", "On idle", "teardown"} {
 		if !strings.Contains(body, want) {
 			t.Errorf("worker role detail missing %q", want)
 		}
 	}
-	bare := get(t, h, "/ui/roles/acme/bare").Body.String()
+	bare := get(t, h, "/ui/projects/acme/roles/bare").Body.String()
 	for _, want := range []string{"Idle timeout", "none", "On idle", "wake"} {
 		if !strings.Contains(bare, want) {
 			t.Errorf("bare role detail missing %q", want)
diff --git a/internal/jam/adminui/role_edit_test.go b/internal/jam/adminui/role_edit_test.go
index 68f5a12..e27ec22 100644
--- a/internal/jam/adminui/role_edit_test.go
+++ b/internal/jam/adminui/role_edit_test.go
@@ -29,7 +29,7 @@ func del(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
 func credKnown(n string) bool { return n == "git-pat" || n == "other-pat" }
 
 func TestRolePagePrefillsEditForms(t *testing.T) {
-	body := get(t, adminui.Handler(seedRichRole(t), testLogger(), nil, nil, credKnown, nil), "/ui/roles/acme/review").Body.String()
+	body := get(t, adminui.Handler(seedRichRole(t), testLogger(), nil, nil, credKnown, nil), "/ui/projects/acme/roles/review").Body.String()
 	for _, want := range []string{
 		`hx-post="/ui/roles/acme/review/scope"`,
 		`value="git=git-pat,anthropic"`,
@@ -180,7 +180,7 @@ func TestEditStandingReset(t *testing.T) {
 		t.Fatal(err)
 	}
 	h := adminui.Handler(store, testLogger(), sup, nil, credKnown, nil)
-	body := get(t, h, "/ui/roles/acme/review").Body.String()
+	body := get(t, h, "/ui/projects/acme/roles/review").Body.String()
 	if !strings.Contains(body, `hx-post="/ui/roles/acme/review/standing/nightly/reset"`) || !strings.Contains(body, `hx-confirm="Reset standing session nightly?`) {
 		t.Fatalf("role page lacks a confirmed reset button:\n%s", body)
 	}
@@ -206,7 +206,7 @@ func TestEditStandingReset(t *testing.T) {
 	}
 
 	ro := adminui.Handler(store, testLogger(), nil, nil, credKnown, nil)
-	if strings.Contains(get(t, ro, "/ui/roles/acme/review").Body.String(), "/standing/nightly/reset") {
+	if strings.Contains(get(t, ro, "/ui/projects/acme/roles/review").Body.String(), "/standing/nightly/reset") {
 		t.Error("no supervisor: the reset button must be hidden")
 	}
 	if rec := post(t, ro, "/ui/roles/acme/review/standing/nightly/reset", url.Values{}); rec.Code != http.StatusServiceUnavailable {
@@ -220,7 +220,7 @@ func TestCreateRoleRedirectsAndRefusesExisting(t *testing.T) {
 	mustCreateProject(t, store, "acme")
 	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
 	rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"w"}})
-	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/roles/acme/w" {
+	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/projects/acme/roles/w" {
 		t.Fatalf("create = %d redirect=%q", rec.Code, rec.Header().Get("HX-Redirect"))
 	}
 	if rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"w"}}); rec.Code != http.StatusConflict {
@@ -269,7 +269,7 @@ func TestEditScopeBindsModelSpec(t *testing.T) {
 		!strings.Contains(rec.Body.String(), "does not exist") {
 		t.Fatalf("unknown model-spec = %d %s", rec.Code, rec.Body)
 	}
-	body := get(t, h, "/ui/roles/acme/review").Body.String()
+	body := get(t, h, "/ui/projects/acme/roles/review").Body.String()
 	if !strings.Contains(body, `href="/ui/model-specs/claude-default"`) || !strings.Contains(body, `placeholder="blank = claude-default"`) {
 		t.Errorf("unbound role page should show the claude-default resolution")
 	}
@@ -283,7 +283,7 @@ func TestEditScopeBindsModelSpec(t *testing.T) {
 	if r, _ := store.GetRole("acme", "review"); r.ModelSpec != "opus" {
 		t.Fatalf("binding = %q", r.ModelSpec)
 	}
-	body = get(t, h, "/ui/roles/acme/review").Body.String()
+	body = get(t, h, "/ui/projects/acme/roles/review").Body.String()
 	if !strings.Contains(body, `name="model-spec" data-ta="model-specs" value="opus"`) {
 		t.Errorf("role page should prefill the binding")
 	}
diff --git a/internal/jam/adminui/role_request_test.go b/internal/jam/adminui/role_request_test.go
index 014edff..0a8b4b3 100644
--- a/internal/jam/adminui/role_request_test.go
+++ b/internal/jam/adminui/role_request_test.go
@@ -69,9 +69,9 @@ func requestAs(t *testing.T, h http.Handler, op, path string) *httptest.Response
 
 func TestRolesPageOffersRequest(t *testing.T) {
 	_, _, _, h := requestKit(t)
-	body := get(t, h, "/ui/roles").Body.String()
+	body := get(t, h, "/ui/projects/acme/roles").Body.String()
 	if !strings.Contains(body, `hx-post="/ui/roles/acme/pair/request"`) {
-		t.Errorf("roles page should offer a Request action per role; got:\n%s", body)
+		t.Errorf("a project's roles should offer a Request action per role; got:\n%s", body)
 	}
 }
 
diff --git a/internal/jam/adminui/search_test.go b/internal/jam/adminui/search_test.go
index 18beb54..500bbff 100644
--- a/internal/jam/adminui/search_test.go
+++ b/internal/jam/adminui/search_test.go
@@ -58,17 +58,17 @@ func TestSearchFindsEveryKind(t *testing.T) {
 	}
 	body := rec.Body.String()
 	for _, want := range []string{
-		`href="/ui/projects/zephyr-labs"`,      // project by name
-		`href="/ui/roles/acme/zephyr-dev"`,     // role by name
-		`href="/ui/coves/studio-7"`,            // studio by role
-		"bot-<mark>zephyr</mark>",              // actor by id
-		"human:zoe",                            // human by handle
-		"channel:ops",                          // channel by ref
-		`href="/ui/kits/web"`,                  // kit by prompt
-		`href="/ui/destinations/gh"`,           // destination by upstream
-		"<mark>zephyr</mark> update 11",        // newest squawk, highlighted
-		`href="/ui/intercom?q=ZEPHYR"`,         // the rest of the squawks
-		`data-group="squawks" data-count="12"`, // full count, 10 shown
+		`href="/ui/projects/zephyr-labs"`,           // project by name
+		`href="/ui/projects/acme/roles/zephyr-dev"`, // role by name
+		`href="/ui/coves/studio-7"`,                 // studio by role
+		"bot-<mark>zephyr</mark>",                   // actor by id
+		"human:zoe",                                 // human by handle
+		"channel:ops",                               // channel by ref
+		`href="/ui/kits/web"`,                       // kit by prompt
+		`href="/ui/destinations/gh"`,                // destination by upstream
+		"<mark>zephyr</mark> update 11",             // newest squawk, highlighted
+		`href="/ui/intercom?q=ZEPHYR"`,              // the rest of the squawks
+		`data-group="squawks" data-count="12"`,      // full count, 10 shown
 	} {
 		if !strings.Contains(body, want) {
 			t.Errorf("search results missing %q", want)
@@ -96,7 +96,7 @@ func TestSearchExactMatchJumps(t *testing.T) {
 	for q, want := range map[string]string{
 		"studio-7":        "/ui/coves/studio-7",
 		"web":             "/ui/kits/web",
-		"acme/zephyr-dev": "/ui/roles/acme/zephyr-dev",
+		"acme/zephyr-dev": "/ui/projects/acme/roles/zephyr-dev",
 	} {
 		rec := get(t, h, "/ui/search?go=1&q="+q)
 		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
@@ -120,7 +120,7 @@ func TestSearchShortQueryAndNoMatches(t *testing.T) {
 }
 
 func TestSearchBoxOnEveryPage(t *testing.T) {
-	body := get(t, searchFixture(t), "/ui/roles").Body.String()
+	body := get(t, searchFixture(t), "/ui/projects").Body.String()
 	for _, want := range []string{`action="/ui/search"`, `id="q-top"`, `name="go" value="1"`} {
 		if !strings.Contains(body, want) {
 			t.Errorf("topbar search missing %q", want)
diff --git a/internal/jam/adminui/studio_page_test.go b/internal/jam/adminui/studio_page_test.go
index 2d90dd4..a391d77 100644
--- a/internal/jam/adminui/studio_page_test.go
+++ b/internal/jam/adminui/studio_page_test.go
@@ -59,7 +59,7 @@ func TestStudioPageShowsRuntime(t *testing.T) {
 		`<h1 class="mono">sess-1</h1>`, `aria-current="page">Agents`,
 		`class="pill phase-live"`, "waiting",
 		"personal", "alice", // kind + owner
-		`href="/ui/projects/acme"`, `href="/ui/roles/acme/dev"`, "COV-9",
+		`href="/ui/projects/acme"`, `href="/ui/projects/acme/roles/dev"`, "COV-9",
 		"jam-a", "colima",
 		"wait seq 7",       // wake-on baseline
 		"tier 1", "deploy", // open escalation
diff --git a/internal/jam/adminui/typeahead_test.go b/internal/jam/adminui/typeahead_test.go
index 34515e0..f3aa2f4 100644
--- a/internal/jam/adminui/typeahead_test.go
+++ b/internal/jam/adminui/typeahead_test.go
@@ -32,12 +32,11 @@ func TestReferenceFieldsAreTypeaheads(t *testing.T) {
 			`name="role" data-ta="roles" data-ta-project="@form"`,
 			`name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials"`,
 		},
-		"/ui/roles": {
-			`name="project" data-ta="projects"`,
+		"/ui/projects/acme/roles": {
 			`name="kit" data-ta="kits"`,
 			`name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials"`,
 		},
-		"/ui/roles/acme/dev": {
+		"/ui/projects/acme/roles/dev": {
 			`name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials"`,
 			`name="addressing" data-ta="targets" data-ta-list data-ta-project="acme"`,
 			`name="kit" data-ta="kits"`,
@@ -45,8 +44,10 @@ func TestReferenceFieldsAreTypeaheads(t *testing.T) {
 		"/ui/destinations": {
 			`name="cred-name" data-ta="credentials"`,
 		},
-		"/ui/projects/acme": {
+		"/ui/projects/acme/escalation": {
 			`name="tiers" data-ta="targets" data-ta-list data-ta-project="acme"`,
+		},
+		"/ui/projects/acme/intercom": {
 			`name="service" data-ta="services"`,
 		},
 		"/ui/intercom": {
diff --git a/internal/jam/adminui/users_test.go b/internal/jam/adminui/users_test.go
index 1896126..c819eae 100644
--- a/internal/jam/adminui/users_test.go
+++ b/internal/jam/adminui/users_test.go
@@ -119,7 +119,7 @@ func TestProjectMembersSection(t *testing.T) {
 	store := seedProjects(t)
 	h := projHandler(store)
 	alice := userID(t, store, "alice")
-	body := get(t, h, "/ui/projects/acme").Body.String()
+	body := get(t, h, "/ui/projects/acme/members").Body.String()
 	for _, want := range []string{
 		`hx-post="/ui/projects/acme/members"`, `href="/ui/users/` + string(alice) + `"`,
 		"discord:dm-alice", `hx-delete="/ui/projects/acme/members/` + string(alice) + `"`,
diff --git a/internal/jam/adminui/writes_test.go b/internal/jam/adminui/writes_test.go
index 0f6d84d..83b98ec 100644
--- a/internal/jam/adminui/writes_test.go
+++ b/internal/jam/adminui/writes_test.go
@@ -267,8 +267,8 @@ func TestCreateRoleWithDestinationCredentials(t *testing.T) {
 		t.Fatalf("stored role = %+v", r)
 	}
 	// Credential names are references, not secret values: the table shows the mapping.
-	if !strings.Contains(rec.Body.String(), "known-cred") {
-		t.Errorf("roles table should show the credential mapping; got:\n%s", rec.Body.String())
+	if body := get(t, h, "/ui/projects/acme/roles").Body.String(); !strings.Contains(body, "known-cred") {
+		t.Errorf("roles table should show the credential mapping; got:\n%s", body)
 	}
 	for _, bad := range []string{"git=unknown-cred", "git="} {
 		if rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"x"}, "destinations": {bad}}); rec.Code != http.StatusBadRequest {
````

- [ ] **Step 2: Run to verify RED**

Run: `go test ./internal/jam/adminui/`
Expected: FAIL (≈9 tests) — no `/ui/projects/{p}/{section}` or nested role routes yet (404s), no `ptree` markup, `/ui/roles…` still 200 instead of 301, project writes answer with the whole project.

- [ ] **Step 3: Apply the code patch**

````diff
diff --git a/internal/jam/adminui/adminui.go b/internal/jam/adminui/adminui.go
index 23519ae..21f0b59 100644
--- a/internal/jam/adminui/adminui.go
+++ b/internal/jam/adminui/adminui.go
@@ -30,18 +30,17 @@ var pages = map[string]*template.Template{
 	"roster":       mustParse(navAgents, "roster.html"),
 	"users":        mustParse(navUsers, "users.html"),
 	"user":         mustParse(navUsers, "user.html"),
-	"roles":        mustParse(navProjects, "roles.html"),
 	"kits":         mustParse(navSpecs, "kits.html"),
 	"destinations": mustParse(navSpecs, "dest_fields.html", "destinations.html"),
-	"intercom":     mustParse(navIntercom, "intercom.html"),
+	"intercom":     mustParse(navIntercom, "squawks.html", "intercom.html"),
 	"session":      mustParse(navAgents, "session.html"),
-	"role":         mustParse(navProjects, "coves.html", "context_panel.html", "role.html"),
+	"role":         mustParse(navProjects, "coves.html", "context_panel.html", "projtree.html", "role.html"),
 	"destination":  mustParse(navSpecs, "dest_fields.html", "destination.html"),
 	"model-specs":  mustParse(navSpecs, "model_spec_fields.html", "model_specs.html"),
 	"model-spec":   mustParse(navSpecs, "model_spec_fields.html", "model_spec.html"),
 	"kit":          mustParse(navSpecs, "kit.html"),
 	"projects":     mustParse(navProjects, "projects.html"),
-	"project":      mustParse(navProjects, "coves.html", "context_panel.html", "project.html"),
+	"project":      mustParse(navProjects, "coves.html", "context_panel.html", "squawks.html", "projtree.html", "project.html"),
 	"studio":       mustParse(navAgents, "studio.html"),
 	"search":       mustParse(navNone, "search.html"),
 }
@@ -82,7 +81,7 @@ func mustParse(section navSection, names ...string) *template.Template {
 	}).ParseFS(files, paths...))
 }
 
-// covesData, rosterData and rolesData are the payloads of those pages and
+// covesData and rosterData are the payloads of those pages and
 // their swapped tables.
 func covesData(store jam.Store, img jam.ImageResolver, canEdit bool) map[string]any {
 	return map[string]any{"Coves": jam.CoveSummaries(store, img), "CanEdit": canEdit}
@@ -92,10 +91,6 @@ func rosterData(store jam.Store) map[string]any {
 	return map[string]any{"Actors": jam.RosterSummaries(store)}
 }
 
-func rolesData(store jam.Store, canRequest bool) map[string]any {
-	return map[string]any{"Roles": roleRows(store), "CanRequest": canRequest}
-}
-
 // funcs are the template helpers shared by every page.
 var funcs = template.FuncMap{
 	// ttl renders a role TTL compactly, or "—" when unset.
@@ -184,12 +179,14 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 		data["Title"] = "Actors"
 		render(w, "roster", data)
 	})
+	// The global roles list and role pages moved into the project tree.
 	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
-		data := rolesData(store, canEdit)
-		data["Title"] = "Roles"
-		render(w, "roles", data)
+		redirect(w, r, "/ui/projects")
 	})
 	mux.HandleFunc("GET /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
+		redirect(w, r, roleURL(r.PathValue("project"), r.PathValue("name")))
+	})
+	mux.HandleFunc("GET /ui/projects/{project}/roles/{name}", func(w http.ResponseWriter, r *http.Request) {
 		handleRoleDetail(w, r, store, sup, canEdit)
 	})
 	mux.HandleFunc("GET /ui/intercom", func(w http.ResponseWriter, r *http.Request) {
@@ -203,8 +200,8 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 	guardWrite := originGuard(o.trustedOrigins)
 	registerWrites(mux, store, log, sup, credExists, guardWrite)
 	registerRoleRequest(mux, store, log, sup, alloc, guardWrite)
-	registerProjects(mux, store, sup, log, guardWrite)
-	registerProjectEdits(mux, store, sup, log, guardWrite)
+	registerProjects(mux, store, sup, msgs, canEdit, log, guardWrite)
+	registerProjectEdits(mux, store, sup, msgs, log, guardWrite)
 	registerKits(mux, store, log, guardWrite)
 	registerDestinations(mux, store, log, credExists, guardWrite)
 	registerModelSpecs(mux, specUI{store: store, credExists: credExists, credNames: o.credNames, pool: o.poolConfigured}, log, guardWrite)
diff --git a/internal/jam/adminui/intercom.go b/internal/jam/adminui/intercom.go
index 20c9e44..a9d07e5 100644
--- a/internal/jam/adminui/intercom.go
+++ b/internal/jam/adminui/intercom.go
@@ -119,16 +119,59 @@ type squawkRow struct {
 	Plain     bool          // text/plain: shown literally, badged
 }
 
+// squawkFilter selects log entries; a zero field matches everything. Until
+// is exclusive.
+type squawkFilter struct {
+	Legacy       bool
+	Project      string
+	Participant  string
+	Q            string
+	Since, Until time.Time
+}
+
+// squawkTable is what the squawk-table template renders.
+type squawkTable struct {
+	Legacy   bool
+	Filtered bool
+	Rows     []squawkRow
+}
+
+// filterSquawks returns the log entries f selects, newest first, at most
+// limit of them (0 = all).
+func filterSquawks(msgs SquawkReader, f squawkFilter, limit int) squawkTable {
+	t := squawkTable{Legacy: f.Legacy, Filtered: f.Project != "" || f.Participant != "" || f.Q != "" || !f.Since.IsZero() || !f.Until.IsZero()}
+	needle := strings.ToLower(f.Q)
+	var kept []Logged
+	for _, m := range msgs.Squawks(f.Legacy) {
+		switch {
+		case f.Project != "" && m.Project != f.Project,
+			!f.Since.IsZero() && m.At.Before(f.Since),
+			!f.Until.IsZero() && !m.At.Before(f.Until),
+			f.Participant != "" && !matchesParticipant(m, f.Participant),
+			needle != "" && !strings.Contains(strings.ToLower(m.Body), needle):
+			continue
+		}
+		kept = append(kept, m)
+	}
+	sort.SliceStable(kept, func(i, j int) bool { return kept[i].At.After(kept[j].At) })
+	if limit > 0 && len(kept) > limit {
+		kept = kept[:limit]
+	}
+	t.Rows = make([]squawkRow, 0, len(kept))
+	for _, m := range kept {
+		t.Rows = append(t.Rows, toRow(m))
+	}
+	return t
+}
+
 // squawksData is the Intercom page payload. The filter fields are echoed back
 // into the form so a filtered view round-trips; RawQuery drives the Refresh
-// link; Filtered distinguishes the two empty states; Legacy selects the
-// frozen legacy log's tab.
+// link; the embedded table's Filtered distinguishes the two empty states and
+// Legacy selects the frozen legacy log's tab.
 type squawksData struct {
-	Title       string
-	Configured  bool
-	Legacy      bool
-	Filtered    bool
-	Rows        []squawkRow
+	Title      string
+	Configured bool
+	squawkTable
 	Project     string
 	Participant string
 	Q           string
@@ -144,7 +187,8 @@ type squawksData struct {
 // recipient). A nil reader means no log is configured.
 func handleIntercom(w http.ResponseWriter, r *http.Request, msgs SquawkReader) {
 	q := r.URL.Query()
-	data := squawksData{Title: "Intercom", Configured: msgs != nil, Legacy: q.Get("log") == "legacy"}
+	data := squawksData{Title: "Intercom", Configured: msgs != nil}
+	data.Legacy = q.Get("log") == "legacy"
 	if msgs == nil {
 		render(w, "intercom", data)
 		return
@@ -155,44 +199,28 @@ func handleIntercom(w http.ResponseWriter, r *http.Request, msgs SquawkReader) {
 	data.Since = strings.TrimSpace(q.Get("since"))
 	data.Until = strings.TrimSpace(q.Get("until"))
 	data.RawQuery = r.URL.RawQuery
-	data.Filtered = data.Project != "" || data.Participant != "" || data.Q != "" || data.Since != "" || data.Until != ""
 
 	// Date bounds are UTC day boundaries; an unparseable one is surfaced and
 	// left unbounded.
-	var since, until time.Time
+	f := squawkFilter{Legacy: data.Legacy, Project: data.Project, Participant: data.Participant, Q: data.Q}
 	if data.Since != "" {
 		if t, err := time.Parse(dateLayout, data.Since); err == nil {
-			since = t
+			f.Since = t
 		} else {
 			data.BadDate = true
 		}
 	}
 	if data.Until != "" {
 		if t, err := time.Parse(dateLayout, data.Until); err == nil {
-			until = t.AddDate(0, 0, 1) // inclusive day → exclusive next midnight
+			f.Until = t.AddDate(0, 0, 1) // inclusive day → exclusive next midnight
 		} else {
 			data.BadDate = true
 		}
 	}
-
-	needle := strings.ToLower(data.Q)
-	var kept []Logged
-	for _, m := range msgs.Squawks(data.Legacy) {
-		switch {
-		case data.Project != "" && m.Project != data.Project,
-			!since.IsZero() && m.At.Before(since),
-			!until.IsZero() && !m.At.Before(until),
-			data.Participant != "" && !matchesParticipant(m, data.Participant),
-			needle != "" && !strings.Contains(strings.ToLower(m.Body), needle):
-			continue
-		}
-		kept = append(kept, m)
-	}
-	sort.SliceStable(kept, func(i, j int) bool { return kept[i].At.After(kept[j].At) })
-	data.Rows = make([]squawkRow, 0, len(kept))
-	for _, m := range kept {
-		data.Rows = append(data.Rows, toRow(m))
-	}
+	data.squawkTable = filterSquawks(msgs, f, 0)
+	// A bad date still counts as filtering (the old view did): the "no
+	// match" empty state.
+	data.Filtered = data.Filtered || data.Since != "" || data.Until != ""
 	render(w, "intercom", data)
 }
 
diff --git a/internal/jam/adminui/nav.go b/internal/jam/adminui/nav.go
index 39daa51..adc0334 100644
--- a/internal/jam/adminui/nav.go
+++ b/internal/jam/adminui/nav.go
@@ -1,5 +1,7 @@
 package adminui
 
+import "net/http"
+
 // navSection is a top-nav section. Each page's template set is bound to one
 // (mustParse), and the layout highlights the nav item whose Section matches —
 // so a detail page highlights its section whatever its title.
@@ -32,3 +34,11 @@ var navItems = []navItem{
 	{navSpecs, "Specs", "/ui/kits"},
 	{navIntercom, "Intercom", "/ui/intercom"},
 }
+
+// redirect answers a moved GET page with 301 to target, keeping the query.
+func redirect(w http.ResponseWriter, r *http.Request, target string) {
+	if r.URL.RawQuery != "" {
+		target += "?" + r.URL.RawQuery
+	}
+	http.Redirect(w, r, target, http.StatusMovedPermanently)
+}
diff --git a/internal/jam/adminui/project_edit.go b/internal/jam/adminui/project_edit.go
index 1b0d4fd..02444b3 100644
--- a/internal/jam/adminui/project_edit.go
+++ b/internal/jam/adminui/project_edit.go
@@ -104,8 +104,10 @@ func memberDeliveryFromForm(r *http.Request) ([]jam.DeliveryProfile, error) {
 
 // registerProjectEdits mounts the project page's section writes. Each answers
 // with the re-rendered project body.
-func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
-	edit := func(what string, apply func(r *http.Request, project string) error) http.HandlerFunc {
+func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, msgs SquawkReader, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
+	// edit applies one project write and answers with the section it belongs
+	// to re-rendered, for the page's #project swap.
+	edit := func(what string, section projectSection, apply func(r *http.Request, project string) error) http.HandlerFunc {
 		return func(w http.ResponseWriter, r *http.Request) {
 			if !guardWrite(w, r) {
 				return
@@ -124,27 +126,27 @@ func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageReso
 				return
 			}
 			log.Info("ui project "+what, "operator", jam.OperatorID(r), "project", project)
-			d, ok := buildProjectDetail(store, img, project)
+			d, ok := buildProjectDetail(store, img, msgs, project, section)
 			if !ok {
 				renderError(w, http.StatusNotFound, "project no longer exists")
 				return
 			}
-			renderFragment(w, "project", "project-body", d)
+			renderFragment(w, "project", "project-"+string(section), d)
 		}
 	}
 
-	mux.HandleFunc("POST /ui/projects/{project}/context", edit("context set", func(r *http.Request, project string) error {
+	mux.HandleFunc("POST /ui/projects/{project}/context", edit("context set", sectionOverview, func(r *http.Request, project string) error {
 		b, err := parseContextForm(r)
 		if err != nil {
 			return err
 		}
 		return jam.SetProjectContextChecked(store, project, b)
 	}))
-	mux.HandleFunc("DELETE /ui/projects/{project}/context", edit("context cleared", func(r *http.Request, project string) error {
+	mux.HandleFunc("DELETE /ui/projects/{project}/context", edit("context cleared", sectionOverview, func(r *http.Request, project string) error {
 		return jam.SetProjectContextChecked(store, project, jam.ContextBody{})
 	}))
 
-	mux.HandleFunc("POST /ui/projects/{project}/members", edit("member put", func(r *http.Request, project string) error {
+	mux.HandleFunc("POST /ui/projects/{project}/members", edit("member put", sectionMembers, func(r *http.Request, project string) error {
 		uid, err := jam.ResolveRegistryRef(store, ident.User, strings.TrimSpace(r.FormValue("user")))
 		if err != nil {
 			return registryErr(err)
@@ -163,7 +165,7 @@ func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageReso
 		}
 		return registryErr(store.PutMembership(jam.Membership{ProjectID: p.ID, UserID: uid, Delivery: delivery}))
 	}))
-	mux.HandleFunc("DELETE /ui/projects/{project}/members/{user}", edit("member removed", func(r *http.Request, project string) error {
+	mux.HandleFunc("DELETE /ui/projects/{project}/members/{user}", edit("member removed", sectionMembers, func(r *http.Request, project string) error {
 		uid, err := jam.ResolveRegistryRef(store, ident.User, r.PathValue("user"))
 		if err != nil {
 			return registryErr(err)
@@ -172,7 +174,7 @@ func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageReso
 		return registryErr(store.RemoveMember(p.ID, uid))
 	}))
 
-	mux.HandleFunc("POST /ui/projects/{project}/channels", edit("room put", func(r *http.Request, project string) error {
+	mux.HandleFunc("POST /ui/projects/{project}/channels", edit("room put", sectionIntercom, func(r *http.Request, project string) error {
 		b := jam.RoomBody{
 			Name:       strings.TrimSpace(r.FormValue("name")),
 			Connection: strings.TrimSpace(r.FormValue("service")),
@@ -184,11 +186,11 @@ func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageReso
 		_, _, err := jam.PutRoom(store, project, b)
 		return registryErr(err)
 	}))
-	mux.HandleFunc("DELETE /ui/projects/{project}/channels/{name}", edit("room removed", func(r *http.Request, project string) error {
+	mux.HandleFunc("DELETE /ui/projects/{project}/channels/{name}", edit("room removed", sectionIntercom, func(r *http.Request, project string) error {
 		return registryErr(jam.RemoveRoom(store, project, r.PathValue("name")))
 	}))
 
-	mux.HandleFunc("POST /ui/projects/{project}/escalation", edit("escalation set", func(r *http.Request, project string) error {
+	mux.HandleFunc("POST /ui/projects/{project}/escalation", edit("escalation set", sectionEscalation, func(r *http.Request, project string) error {
 		var tiers []jam.EscalationTier
 		for _, l := range splitSpecLines(r.FormValue("tiers")) {
 			t, err := jam.ParseEscalationTierSpec(l)
@@ -202,11 +204,11 @@ func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageReso
 		}
 		return store.SetEscalationPolicy(project, strings.TrimSpace(r.FormValue("category")), tiers)
 	}))
-	mux.HandleFunc("DELETE /ui/projects/{project}/escalation", edit("escalation cleared", func(r *http.Request, project string) error {
+	mux.HandleFunc("DELETE /ui/projects/{project}/escalation", edit("escalation cleared", sectionEscalation, func(r *http.Request, project string) error {
 		return store.SetEscalationPolicy(project, r.URL.Query().Get("category"), nil)
 	}))
 
-	mux.HandleFunc("POST /ui/projects/{project}/chat-service", edit("chat service set", func(r *http.Request, project string) error {
+	mux.HandleFunc("POST /ui/projects/{project}/chat-service", edit("chat service set", sectionOverview, func(r *http.Request, project string) error {
 		return store.SetChatService(project, strings.TrimSpace(r.FormValue("service")))
 	}))
 }
diff --git a/internal/jam/adminui/projects.go b/internal/jam/adminui/projects.go
index 8f50df4..bfd0dee 100644
--- a/internal/jam/adminui/projects.go
+++ b/internal/jam/adminui/projects.go
@@ -110,14 +110,38 @@ func projectHolders(store jam.Store, project string) []projectHolder {
 	return out
 }
 
-// projectDetail is the project page payload.
+// crumb is one breadcrumb segment.
+type crumb struct{ Label, Href string }
+
+// projectCrumbs is the trail to a project section (and, on a role page, the
+// role): Projects / acme / Roles / dev.
+func projectCrumbs(project string, section projectSection, role string) []crumb {
+	out := []crumb{{"Projects", "/ui/projects"}, {project, projectURL(project)}}
+	for _, s := range projectSections {
+		if s.Section == section && section != sectionOverview {
+			out = append(out, crumb{s.Label, projectSectionURL(project, section)})
+		}
+	}
+	if role != "" {
+		out = append(out, crumb{role, roleURL(project, role)})
+	}
+	return out
+}
+
+// projectDetail is the payload of every project page: Section picks which
+// section's content renders inside the tree frame.
 type projectDetail struct {
 	Title        string
+	Section      projectSection
+	Tree         projectTree
+	Crumbs       []crumb
 	Project      jam.Project
 	Roles        []roleRow
 	Holders      []projectHolder
 	Coves        []jam.CoveSummary
+	LiveCoves    int
 	CanEdit      bool // always false: the page's studio table is read-only
+	CanRequest   bool // the Roles section offers Request (a supervisor runs)
 	Members      []memberRow
 	Rooms        []jam.RoomView
 	Escalation   []chainView // chains with at least one tier
@@ -128,14 +152,23 @@ type projectDetail struct {
 	NotFound     bool
 	NotFoundFor  string
 	Context      contextPanel // the project's session-context card
+	// The Intercom section's recent log: the newest squawks in this project.
+	LogConfigured bool
+	Squawks       squawkTable
 }
 
-func buildProjectDetail(store jam.Store, img jam.ImageResolver, name string) (projectDetail, bool) {
+// recentProjectSquawks caps the Intercom section's log; the full, filterable
+// log is the Intercom page.
+const recentProjectSquawks = 50
+
+func buildProjectDetail(store jam.Store, img jam.ImageResolver, msgs SquawkReader, name string, section projectSection) (projectDetail, bool) {
 	p, ok := store.GetProject(name)
 	if !ok || p.Status == jam.StatusRemoved {
 		return projectDetail{}, false
 	}
-	d := projectDetail{Title: "Projects", Project: p, Holders: projectHolders(store, name), InUseBy: projectRef(store, name)}
+	d := projectDetail{Title: name, Section: section, Project: p, Holders: projectHolders(store, name), InUseBy: projectRef(store, name)}
+	d.Tree = buildProjectTree(store, img, name, section, "")
+	d.Crumbs = projectCrumbs(name, section, "")
 	d.Context = newContextPanel("project", "/ui/projects/"+name+"/context", "project", p.Context, p.Resources, sessionctx.BudgetProject, true)
 	for _, r := range roleRows(store) {
 		if r.Project == name {
@@ -145,6 +178,9 @@ func buildProjectDetail(store jam.Store, img jam.ImageResolver, name string) (pr
 	for _, c := range jam.CoveSummaries(store, img) {
 		if orDefaultProject(c.Project) == name {
 			d.Coves = append(d.Coves, c)
+			if ph := jam.Phase(c.Phase); ph == jam.PhaseLive || ph == jam.PhaseRaising {
+				d.LiveCoves++
+			}
 		}
 	}
 	members := jam.MembersOf(store, p.ID)
@@ -165,6 +201,10 @@ func buildProjectDetail(store jam.Store, img jam.ImageResolver, name string) (pr
 	}
 	d.ChatService = chatServiceName(store, p)
 	d.ChatServices = chatServiceChoices(store, d.ChatService)
+	if section == sectionIntercom && msgs != nil {
+		d.LogConfigured = true
+		d.Squawks = filterSquawks(msgs, squawkFilter{Project: name}, recentProjectSquawks)
+	}
 	return d, true
 }
 
@@ -172,21 +212,29 @@ func projectTableData(store jam.Store) map[string]any {
 	return map[string]any{"Projects": projectRows(store)}
 }
 
-func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
+func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, msgs SquawkReader, canRequest bool, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
 	mux.HandleFunc("GET /ui/projects", func(w http.ResponseWriter, r *http.Request) {
 		data := projectTableData(store)
 		data["Title"] = "Projects"
 		render(w, "projects", data)
 	})
 
-	mux.HandleFunc("GET /ui/projects/{name}", func(w http.ResponseWriter, r *http.Request) {
-		d, ok := buildProjectDetail(store, img, r.PathValue("name"))
-		if !ok {
-			renderStatus(w, http.StatusNotFound, "project", projectDetail{Title: "Projects", NotFound: true, NotFoundFor: r.PathValue("name")})
-			return
+	page := func(section projectSection) http.HandlerFunc {
+		return func(w http.ResponseWriter, r *http.Request) {
+			name := r.PathValue("name")
+			d, ok := buildProjectDetail(store, img, msgs, name, section)
+			if !ok {
+				renderStatus(w, http.StatusNotFound, "project", projectDetail{Title: "Project not found", NotFound: true, NotFoundFor: name})
+				return
+			}
+			d.CanRequest = canRequest
+			render(w, "project", d)
 		}
-		render(w, "project", d)
-	})
+	}
+	mux.HandleFunc("GET /ui/projects/{name}", page(sectionOverview))
+	for _, s := range projectSections[1:] {
+		mux.HandleFunc("GET /ui/projects/{name}/"+string(s.Section), page(s.Section))
+	}
 
 	mux.HandleFunc("POST /ui/projects", func(w http.ResponseWriter, r *http.Request) {
 		if !guardWrite(w, r) {
diff --git a/internal/jam/adminui/projtree.go b/internal/jam/adminui/projtree.go
new file mode 100644
index 0000000..7ec4adc
--- /dev/null
+++ b/internal/jam/adminui/projtree.go
@@ -0,0 +1,115 @@
+package adminui
+
+import (
+	"net/url"
+	"strconv"
+
+	"github.com/aethons-tools/cove/internal/jam"
+)
+
+// projectSection is one node of a project's tree: its Overview page or one of
+// its sections. A role page sits under sectionRoles.
+type projectSection string
+
+const (
+	sectionOverview   projectSection = "overview"
+	sectionMembers    projectSection = "members"
+	sectionAgents     projectSection = "agents"
+	sectionRoles      projectSection = "roles"
+	sectionIntercom   projectSection = "intercom"
+	sectionEscalation projectSection = "escalation"
+)
+
+// projectSections is the tree's order, with each node's label.
+var projectSections = []struct {
+	Section projectSection
+	Label   string
+}{
+	{sectionOverview, "Overview"},
+	{sectionMembers, "Members"},
+	{sectionAgents, "Agents"},
+	{sectionRoles, "Roles"},
+	{sectionIntercom, "Intercom"},
+	{sectionEscalation, "Escalation"},
+}
+
+// projectSectionURL is the page path of a project's section ("" or overview:
+// the project page itself).
+func projectSectionURL(project string, s projectSection) string {
+	base := projectURL(project)
+	if s == "" || s == sectionOverview {
+		return base
+	}
+	return base + "/" + string(s)
+}
+
+// treeLeaf is one child under a tree node: a role, an agent or a room.
+type treeLeaf struct {
+	Label, Href, Phase string
+	Current            bool
+}
+
+// treeNode is one section in the tree with its children (none for leaf
+// sections). Open renders the branch expanded: it holds the current page.
+type treeNode struct {
+	Section  projectSection
+	Label    string
+	Href     string
+	Count    string // shown after the label, e.g. the live agent count
+	Current  bool   // this section's own page is the current page
+	Open     bool
+	Children []treeLeaf
+}
+
+// projectTree is the left-side navigation on every project page.
+type projectTree struct {
+	Project string
+	Href    string
+	Current string // the current node's label, for the narrow-screen summary
+	Nodes   []treeNode
+}
+
+// buildProjectTree lays out project's tree with current marked: section is the
+// page's section and role, on a role page, the role's name.
+func buildProjectTree(store jam.Store, img jam.ImageResolver, project string, section projectSection, role string) projectTree {
+	t := projectTree{Project: project, Href: projectURL(project)}
+	p, _ := store.GetProject(project)
+	for _, s := range projectSections {
+		n := treeNode{Section: s.Section, Label: s.Label, Href: projectSectionURL(project, s.Section)}
+		switch s.Section {
+		case sectionRoles:
+			for _, r := range store.ListRoles(project) {
+				cur := section == sectionRoles && role == r.Name
+				n.Children = append(n.Children, treeLeaf{Label: r.Name, Href: roleURL(project, r.Name), Current: cur})
+			}
+		case sectionAgents:
+			live := 0
+			for _, c := range jam.CoveSummaries(store, img) {
+				if orDefaultProject(c.Project) != project {
+					continue
+				}
+				if ph := jam.Phase(c.Phase); ph == jam.PhaseLive || ph == jam.PhaseRaising {
+					live++
+					n.Children = append(n.Children, treeLeaf{Label: c.ID, Href: "/ui/coves/" + url.PathEscape(c.ID), Phase: c.Phase})
+				}
+			}
+			if live > 0 {
+				n.Count = strconv.Itoa(live)
+			}
+		case sectionIntercom:
+			for _, rm := range jam.ListRooms(store, p) {
+				n.Children = append(n.Children, treeLeaf{Label: rm.Name, Href: "/ui/intercom?participant=" + url.QueryEscape(string(rm.ID))})
+			}
+		}
+		n.Current = s.Section == section && role == ""
+		n.Open = s.Section == section
+		if n.Open {
+			t.Current = s.Label
+			if role != "" {
+				t.Current += " ▸ " + role
+			}
+		}
+		t.Nodes = append(t.Nodes, n)
+	}
+	return t
+}
diff --git a/internal/jam/adminui/role_detail.go b/internal/jam/adminui/role_detail.go
index af881b1..40f26b0 100644
--- a/internal/jam/adminui/role_detail.go
+++ b/internal/jam/adminui/role_detail.go
@@ -44,6 +44,10 @@ type setting struct {
 // roleDetail is the role page's payload.
 type roleDetail struct {
 	Title, Project, Name string
+	Tree                 projectTree
+	Crumbs               []crumb
+	WriteBase            string // the role's write endpoints (roleWriteBase)
+	RolesHref            string // the project's Roles section (where a delete lands)
 	Role                 jam.Role
 	Dests                []destRow
 	EgressManaged        bool
@@ -65,7 +69,8 @@ func buildRoleDetail(store jam.Store, img jam.ImageResolver, project, name strin
 		return roleDetail{}, false
 	}
 	project = orDefaultProject(project)
-	d := roleDetail{Title: "Roles", Project: project, Name: name, Role: role, EgressManaged: role.Scope.Egress != nil, Form: newRoleForm(role)}
+	d := roleDetail{Title: name, Project: project, Name: name, Role: role, EgressManaged: role.Scope.Egress != nil, Form: newRoleForm(role),
+		WriteBase: roleWriteBase(project, name), RolesHref: projectSectionURL(project, sectionRoles)}
 	d.Context = newContextPanel("role", "/ui/roles/"+project+"/"+name+"/context", "role", role.Context, nil, sessionctx.BudgetRole, false)
 
 	dests := map[string]jam.Destination{}
@@ -138,17 +143,27 @@ func duration(label string, v time.Duration, unset string) setting {
 }
 
 // roleURL is the detail page path for a role.
+// roleURL is a role's page, under its project.
 func roleURL(project, name string) string {
+	return projectSectionURL(project, sectionRoles) + "/" + url.PathEscape(name)
+}
+
+// roleWriteBase is the prefix of a role's write endpoints, which keep their
+// pre-tree paths.
+func roleWriteBase(project, name string) string {
 	return "/ui/roles/" + url.PathEscape(orDefaultProject(project)) + "/" + url.PathEscape(name)
 }
 
 func handleRoleDetail(w http.ResponseWriter, r *http.Request, store jam.Store, img jam.ImageResolver, canRequest bool) {
-	d, ok := buildRoleDetail(store, img, r.PathValue("project"), r.PathValue("name"))
+	project, name := r.PathValue("project"), r.PathValue("name")
+	d, ok := buildRoleDetail(store, img, project, name)
 	if !ok {
-		renderStatus(w, http.StatusNotFound, "role", roleDetail{Title: "Roles", NotFound: true,
-			Project: r.PathValue("project"), Name: r.PathValue("name")})
+		renderStatus(w, http.StatusNotFound, "role", roleDetail{Title: "Role not found", NotFound: true,
+			Project: project, Name: name, RolesHref: projectSectionURL(project, sectionRoles)})
 		return
 	}
+	d.Tree = buildProjectTree(store, img, project, sectionRoles, name)
+	d.Crumbs = projectCrumbs(project, sectionRoles, name)
 	d.CanRequest = canRequest
 	render(w, "role", d)
 }
diff --git a/internal/jam/adminui/templates/intercom.html b/internal/jam/adminui/templates/intercom.html
index c868b26..6ab0bc9 100644
--- a/internal/jam/adminui/templates/intercom.html
+++ b/internal/jam/adminui/templates/intercom.html
@@ -24,33 +24,7 @@
   .tabs{margin:-6px 0 14px;display:flex;gap:6px}
   .tab{padding:4px 10px;border-radius:999px;color:var(--muted);text-decoration:none}
   .tab.sel{background:var(--accent-soft);color:var(--accent);font-weight:600}
-  #squawks td{vertical-align:top}
-  .reach{font-size:10.5px;font-weight:600;text-transform:uppercase;letter-spacing:.04em;padding:1px 6px;border-radius:999px;background:var(--idle-soft);color:var(--idle)}
-  .reach.ext{background:var(--accent-soft);color:var(--accent)}
-  td.body{max-width:560px;word-break:break-word}
-  td.body.plain{white-space:pre-wrap}
-  td.body.md > :first-child{margin-top:0} td.body.md > :last-child{margin-bottom:0}
-  td.body.md p, td.body.md ul, td.body.md ol, td.body.md pre{margin:0 0 .4em}
-  td.body.md pre{overflow-x:auto;background:var(--bg);padding:6px 8px;border-radius:6px}
-  td.body.md code{background:var(--idle-soft);padding:1px 4px;border-radius:4px}
 </style>
-<div class="card">
-<table id="squawks">
-  <thead><tr><th>Time</th><th>From</th><th>{{if .Legacy}}To{{else}}Channel{{end}}</th><th>Project</th><th>Body</th></tr></thead>
-  <tbody>
-    {{range .Rows}}
-      <tr>
-        <td><time>{{.At}}</time></td>
-        <td><span class="mono" title="{{.FromID}}">{{.From}}</span></td>
-        <td>{{if .ChannelID}}<a class="mono" href="/ui/intercom?participant={{.ChannelID}}" title="{{.ChannelID}}">{{.Channel}}</a>{{end}}{{range .To}}<div><span class="mono">{{.Target}}</span> <span class="reach{{if .External}} ext{{end}}">{{if .External}}external{{else}}internal{{end}}</span></div>{{end}}</td>
-        <td>{{.Project}}</td>
-        <td class="body {{if .Plain}}plain{{else}}md{{end}}">{{.Body}}{{if .Plain}} <span class="reach">text/plain</span>{{end}}</td>
-      </tr>
-    {{else}}
-      <tr><td colspan="5" class="empty">{{if .Filtered}}No squawks match.{{else}}No squawks logged yet.{{end}}</td></tr>
-    {{end}}
-  </tbody>
-</table>
-</div>
+{{template "squawk-table" .}}
 {{end}}
 {{end}}
diff --git a/internal/jam/adminui/templates/layout.html b/internal/jam/adminui/templates/layout.html
index eee44b9..8fe66a7 100644
--- a/internal/jam/adminui/templates/layout.html
+++ b/internal/jam/adminui/templates/layout.html
@@ -270,6 +270,10 @@
     window.addEventListener('scroll', taClose, true);
     window.addEventListener('resize', taClose);
 
+    // On a narrow screen a project's tree starts collapsed to its summary.
+    if (window.matchMedia && matchMedia('(max-width:720px)').matches) {
+      document.querySelectorAll('details.ptree-wrap').forEach(function(d){ d.open = false; });
+    }
     // "/" focuses search from anywhere except a text field.
     document.addEventListener('keydown', function(e){
       if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
diff --git a/internal/jam/adminui/templates/project.html b/internal/jam/adminui/templates/project.html
index 8a513c8..99552f2 100644
--- a/internal/jam/adminui/templates/project.html
+++ b/internal/jam/adminui/templates/project.html
@@ -7,7 +7,7 @@
   .crumbs{font-size:12.5px;color:var(--faint);margin-bottom:4px}
   .crumbs a{color:var(--muted);text-decoration:none}
   .page-head .spacer{flex:1}
-  .sections{display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:16px;align-items:start}
+  .sections{display:grid;grid-template-columns:repeat(auto-fit,minmax(380px,1fr));gap:16px;align-items:start}
   .sections .full{grid-column:1/-1}
   section.card{overflow:visible}
   section.card>header{display:flex;align-items:baseline;gap:10px;padding:12px 16px;border-bottom:1px solid var(--border);flex-wrap:wrap}
@@ -32,14 +32,32 @@
   details.row-edit form{padding:10px 0 0}
   .chip.unknown-target{background:var(--bad-soft);color:var(--bad)}
   .form-actions .left{margin-right:auto}
+  .tiles{display:grid;grid-template-columns:repeat(auto-fill,minmax(140px,1fr));gap:12px;margin-bottom:16px}
+  .tile{display:block;background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:12px 14px;text-decoration:none;color:var(--ink)}
+  .tile:hover{border-color:var(--faint)}
+  .tile b{display:block;font-size:22px;font-weight:600}
+  .tile span{color:var(--muted);font-size:12px}
   @media (max-width:720px){.sections{grid-template-columns:1fr}}
 </style>
-<div class="crumbs"><a href="/ui/projects">Projects</a></div>
-{{template "project-body" .}}
+<div class="pframe">
+{{template "project-tree" .Tree}}
+<div>
+{{template "crumbs" .Crumbs}}
+{{if eq .Section "members"}}{{template "project-members" .}}
+{{else if eq .Section "agents"}}{{template "project-agents" .}}
+{{else if eq .Section "roles"}}{{template "project-roles" .}}
+{{else if eq .Section "intercom"}}{{template "project-intercom" .}}
+{{else if eq .Section "escalation"}}{{template "project-escalation" .}}
+{{else}}{{template "project-overview" .}}{{end}}
+</div>
+</div>
 {{end}}
 {{end}}
 
-{{define "project-body"}}
+{{/* Each project-<section> template is one section's page body, wrapped in
+     #project: the section's writes answer with it re-rendered. */}}
+
+{{define "project-overview"}}
 {{$p := .Project.Name}}
 <div id="project">
 <div class="page-head">
@@ -53,32 +71,35 @@
       hx-on::after-request="if(event.detail.successful) location.href='/ui/projects'">Delete</button>{{end}}
 </div>
 
+<div class="tiles">
+  <a class="tile" href="{{.Tree.Href}}/members"><b>{{len .Members}}</b><span>members</span></a>
+  <a class="tile" href="{{.Tree.Href}}/agents"><b>{{.LiveCoves}}</b><span>agents live of {{len .Coves}}</span></a>
+  <a class="tile" href="{{.Tree.Href}}/roles"><b>{{len .Roles}}</b><span>roles</span></a>
+  <a class="tile" href="{{.Tree.Href}}/intercom"><b>{{len .Rooms}}</b><span>rooms</span></a>
+  <a class="tile" href="{{.Tree.Href}}/escalation"><b>{{len .Escalation}}</b><span>escalation chains</span></a>
+</div>
 <div class="sections">
   <section class="card">
-    <header><h2>Roles</h2><span class="sub"><a href="/ui/roles">add one on Roles</a></span></header>
-    <table>
-      <thead><tr><th>Role</th><th>Destinations</th><th>Kit</th></tr></thead>
-      <tbody>
-        {{range .Roles}}<tr><td><a class="mono" href="{{roleURL .Project .Name}}"><b>{{.Name}}</b></a></td>
-          <td>{{range .Destinations}}<span class="chip">{{.}}</span>{{else}}<span class="unset">—</span>{{end}}</td>
-          <td>{{if .Kit}}<a class="chip" href="{{kitURL .Kit}}">{{.Kit}}</a>{{else}}<span class="unset">default</span>{{end}}</td></tr>
-        {{else}}<tr><td colspan="3" class="empty">No roles.</td></tr>{{end}}
-      </tbody>
-    </table>
+    <header><h2>Chat service</h2><span class="sub">what backs human DMs</span></header>
+    <div class="body">
+      <form class="inline" hx-post="/ui/projects/{{$p}}/chat-service" hx-target="#project" hx-swap="outerHTML">
+        {{$cs := .ChatService}}
+        <select name="service" aria-label="Chat service">{{range .ChatServices}}<option value="{{.}}"{{if eq . $cs}} selected{{end}}>{{if .}}{{.}}{{else}}none — tracker @-mentions only{{end}}</option>{{end}}</select>
+        <button type="submit" class="small">Save</button>
+      </form>
+    </div>
   </section>
 
-  <section class="card">
-    <header><h2>Actors</h2><span class="sub">holding a grant into this project — manage on the <a href="/ui/actors">Actors</a> page</span></header>
-    <table>
-      <thead><tr><th>Actor</th><th>Roles</th></tr></thead>
-      <tbody>
-        {{range .Holders}}<tr><td class="mono">{{.ID}}</td><td>{{range .Roles}}<span class="chip">{{.}}</span>{{end}}</td></tr>
-        {{else}}<tr><td colspan="2" class="empty">No actors hold a grant here.</td></tr>{{end}}
-      </tbody>
-    </table>
-  </section>
+  {{template "context-panel" .Context}}
+</div>
+</div>
+{{end}}
 
-  <section class="card full">
+{{define "project-members"}}
+{{$p := .Project.Name}}
+<div id="project">
+<div class="page-head"><h1>Members</h1><span class="sub">the users agents in {{$p}} can address, and where they get DMs here</span></div>
+<section class="card">
     <header><h2>Members</h2><span class="sub">the <a href="/ui/users">users</a> studios in this project can address, and where they get DMs here</span></header>
     <table>
       <thead><tr><th>User</th><th>Handle</th><th>Delivery</th><th></th></tr></thead>
@@ -115,8 +136,71 @@
       </form>
     </details>
   </section>
+</div>
+{{end}}
 
-  <section class="card">
+{{define "project-agents"}}
+<div id="project">
+<div class="page-head"><h1>Agents</h1><span class="sub">studios raised in {{.Project.Name}}, and the actors holding a grant into it</span></div>
+<div class="sections">
+<section class="card full">
+    <header><h2>Studios</h2><span class="sub">this project's agents and their studios</span></header>
+    {{template "coves-tbl" .}}
+  </section>
+<section class="card">
+    <header><h2>Actors</h2><span class="sub">holding a grant into this project — manage on the <a href="/ui/actors">Actors</a> page</span></header>
+    <table>
+      <thead><tr><th>Actor</th><th>Roles</th></tr></thead>
+      <tbody>
+        {{range .Holders}}<tr><td class="mono">{{.ID}}</td><td>{{range .Roles}}<span class="chip">{{.}}</span>{{end}}</td></tr>
+        {{else}}<tr><td colspan="2" class="empty">No actors hold a grant here.</td></tr>{{end}}
+      </tbody>
+    </table>
+  </section>
+</div>
+</div>
+{{end}}
+
+{{define "project-roles"}}
+{{$p := .Project.Name}}
+<div id="project">
+<div class="page-head"><h1>Roles</h1><span class="sub">what a grant of each role lets an agent reach — open a role to edit it</span></div>
+<details class="panel">
+  <summary>New role</summary>
+  <form hx-post="/ui/roles" hx-swap="none">
+    <input type="hidden" name="project" value="{{$p}}">
+    <div class="grid">
+      <label>Role name <span class="req">required</span><input name="name" required></label>
+      <label>Destinations <span class="hint">comma-separated, e.g. git=git-pat,anthropic</span><input name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials" autocomplete="off" spellcheck="false"></label>
+      <label>TTL <span class="hint">seconds, optional</span><input name="ttl-seconds" type="number" min="0" inputmode="numeric"></label>
+      <label>Kit <span class="hint">optional</span><input name="kit" data-ta="kits" autocomplete="off" spellcheck="false"></label>
+      <label>Model-spec <span class="hint">optional; blank = claude-default</span><input name="model-spec" data-ta="model-specs" autocomplete="off" spellcheck="false"></label>
+    </div>
+    <div class="form-actions"><button type="submit" class="primary">Create role</button></div>
+  </form>
+</details>
+<div class="card">
+<table>
+  <thead><tr><th>Role</th><th>Destinations</th><th>TTL</th><th>Kit</th><th></th></tr></thead>
+  <tbody>
+    {{range .Roles}}<tr data-id="{{.Name}}"><td><a class="mono" href="{{roleURL .Project .Name}}"><b>{{.Name}}</b></a></td>
+      <td>{{$c := .Credentials}}{{range .Destinations}}<span class="chip">{{.}}{{with index $c .}} <span class="none">→</span> {{.}}{{end}}</span>{{else}}<span class="unset">—</span>{{end}}</td>
+      <td>{{ttl .TTL}}</td>
+      <td>{{if .Kit}}<a class="chip" href="{{kitURL .Kit}}">{{.Kit}}</a>{{else}}<span class="unset">default</span>{{end}}</td>
+      <td class="actions">{{if $.CanRequest}}<button class="primary small" hx-post="/ui/roles/{{.Project}}/{{.Name}}/request" hx-target="#flash" hx-swap="innerHTML"
+          title="Raise a personal session of this role for you">Request</button>{{end}}</td></tr>
+    {{else}}<tr><td colspan="5" class="empty">No roles.</td></tr>{{end}}
+  </tbody>
+</table>
+</div>
+</div>
+{{end}}
+
+{{define "project-intercom"}}
+{{$p := .Project.Name}}
+<div id="project">
+<div class="page-head"><h1>Intercom</h1><span class="sub">{{$p}}'s rooms and its recent messages</span><span class="spacer"></span><a class="btn" href="/ui/intercom?project={{$p}}">Full log</a></div>
+<section class="card">
     <header><h2>Rooms</h2></header>
     <table>
       <thead><tr><th>Name</th><th>Connection</th><th>Ref</th><th></th></tr></thead>
@@ -139,10 +223,17 @@
       </form>
     </details>
   </section>
+<h2>Recent messages</h2>
+{{if .LogConfigured}}{{template "squawk-table" .Squawks}}{{else}}<div class="banner">The message log is unavailable.</div>{{end}}
+</div>
+{{end}}
 
-  <section class="card">
-    <header><h2>Escalation</h2><span class="sub">who is pinged, in order, when a studio is waiting</span></header>
-    <div class="body">
+{{define "project-escalation"}}
+{{$p := .Project.Name}}
+<div id="project">
+<div class="page-head"><h1>Escalation</h1><span class="sub">who is pinged, in order, when an agent in {{$p}} is waiting</span></div>
+<section class="card">
+        <div class="body">
       {{range .Escalation}}
       <div class="chain">
         <h3>{{if .Category}}category <span class="mono">{{.Category}}</span>{{else}}default chain{{end}}</h3>
@@ -166,25 +257,6 @@
       </form>
     </details>
   </section>
-
-  <section class="card">
-    <header><h2>Chat service</h2><span class="sub">what backs human DMs</span></header>
-    <div class="body">
-      <form class="inline" hx-post="/ui/projects/{{$p}}/chat-service" hx-target="#project" hx-swap="outerHTML">
-        {{$cs := .ChatService}}
-        <select name="service" aria-label="Chat service">{{range .ChatServices}}<option value="{{.}}"{{if eq . $cs}} selected{{end}}>{{if .}}{{.}}{{else}}none — tracker @-mentions only{{end}}</option>{{end}}</select>
-        <button type="submit" class="small">Save</button>
-      </form>
-    </div>
-  </section>
-
-  {{template "context-panel" .Context}}
-
-  <section class="card full">
-    <header><h2>Studios</h2><span class="sub">running studios in this project</span></header>
-    {{template "coves-tbl" .}}
-  </section>
-</div>
 </div>
 {{end}}
 
diff --git a/internal/jam/adminui/templates/projtree.html b/internal/jam/adminui/templates/projtree.html
new file mode 100644
index 0000000..24007e2
--- /dev/null
+++ b/internal/jam/adminui/templates/projtree.html
@@ -0,0 +1,54 @@
+{{/* project-tree is the left-side navigation on every project page (a
+     projectTree). Branches are <details>: the one holding the current page
+     renders open. On a narrow screen the whole tree is a disclosure whose
+     summary names the current node (the layout script closes it there). */}}
+{{define "project-tree"}}
+<style>
+  .pframe{display:grid;grid-template-columns:200px minmax(0,1fr);gap:24px;align-items:start}
+  .ptree-wrap>summary{display:none}
+  .ptree{position:sticky;top:64px;font-size:13px}
+  .ptree a{display:block;color:var(--muted);text-decoration:none;padding:4px 8px;border-radius:7px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
+  .ptree a:hover{color:var(--ink);background:var(--surface-2)}
+  .ptree a[aria-current="page"]{color:var(--accent);background:var(--accent-soft);font-weight:600}
+  .ptree .root{font-weight:600;color:var(--ink);font-size:14px;margin-bottom:4px}
+  .ptree ul{list-style:none;margin:0;padding:0}
+  .ptree ul ul{padding-left:14px;border-left:1px solid var(--border);margin-left:12px}
+  .ptree details>summary{list-style:none;cursor:pointer;display:flex;align-items:center}
+  .ptree details>summary::-webkit-details-marker{display:none}
+  .ptree details>summary::before{content:"▸";width:12px;color:var(--faint);font-size:10px}
+  .ptree details[open]>summary::before{content:"▾"}
+  .ptree details>summary>a{flex:1}
+  .ptree li.leaf>a{margin-left:12px}
+  .ptree .count{color:var(--faint);font-weight:500}
+  .ptree .phase-dot{display:inline-block;width:6px;height:6px;border-radius:50%;background:var(--wait);margin-right:6px;vertical-align:middle}
+  .ptree .phase-dot.live{background:var(--live)}
+  @media (max-width:720px){
+    .pframe{grid-template-columns:1fr;gap:12px}
+    .ptree-wrap{background:var(--surface);border:1px solid var(--border);border-radius:10px;padding:6px}
+    .ptree-wrap>summary{display:block;cursor:pointer;padding:4px 8px;font-weight:600}
+    .ptree{position:static}
+  }
+</style>
+<details class="ptree-wrap" open>
+  <summary>{{.Project}} ▸ {{.Current}}</summary>
+  <nav class="ptree" aria-label="Project">
+    <a class="root" href="{{.Href}}">{{.Project}}</a>
+    <ul>
+      {{range .Nodes}}
+      {{if .Children}}
+      <li><details{{if .Open}} open{{end}}>
+        <summary><a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}{{if .Count}} <span class="count">({{.Count}})</span>{{end}}</a></summary>
+        <ul>{{range .Children}}<li><a class="mono" href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{if .Phase}}<span class="phase-dot{{if eq .Phase "live"}} live{{end}}"></span>{{end}}{{.Label}}</a></li>{{end}}</ul>
+      </details></li>
+      {{else}}
+      <li class="leaf"><a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}{{if .Count}} <span class="count">({{.Count}})</span>{{end}}</a></li>
+      {{end}}
+      {{end}}
+    </ul>
+  </nav>
+</details>
+{{end}}
+
+{{/* crumbs renders a breadcrumb trail: a list of crumb{Label, Href}; the last
+     one is the current page (no link). */}}
+{{define "crumbs"}}<div class="crumbs">{{range $i, $c := .}}{{if $i}} / {{end}}{{if $c.Href}}<a href="{{$c.Href}}">{{$c.Label}}</a>{{else}}<span>{{$c.Label}}</span>{{end}}{{end}}</div>{{end}}
diff --git a/internal/jam/adminui/templates/role.html b/internal/jam/adminui/templates/role.html
index e8282ff..9b7a58e 100644
--- a/internal/jam/adminui/templates/role.html
+++ b/internal/jam/adminui/templates/role.html
@@ -1,7 +1,7 @@
 {{define "content"}}
 {{if .NotFound}}
 <div class="page-head"><h1>Role not found</h1></div>
-<div class="banner">No role <span class="mono">{{.Name}}</span> in project <span class="mono">{{.Project}}</span>. <a href="/ui/roles">Back to roles</a></div>
+<div class="banner">No role <span class="mono">{{.Name}}</span> in project <span class="mono">{{.Project}}</span>. <a href="{{.RolesHref}}">Back to roles</a></div>
 {{else}}
 <style>
   .crumbs{font-size:12.5px;color:var(--faint);margin-bottom:4px}
@@ -31,15 +31,20 @@
   .form-actions .left{margin-right:auto}
   @media (max-width:720px){.sections{grid-template-columns:1fr}}
 </style>
-<div class="crumbs"><a href="/ui/projects">Projects</a> / <a href="{{projectURL .Project}}">{{.Project}}</a> / <a href="/ui/roles">Roles</a></div>
+<div class="pframe">
+{{template "project-tree" .Tree}}
+<div>
+{{template "crumbs" .Crumbs}}
 {{template "role-body" .}}
+</div>
+</div>
 {{end}}
 {{end}}
 
 {{/* role-body is the swappable part of the role page: every section write
      answers with it re-rendered. */}}
 {{define "role-body"}}
-{{$base := printf "/ui/roles/%s/%s" .Project .Name}}
+{{$base := .WriteBase}}
 <div id="role">
 <div class="page-head">
   <h1>{{.Name}}</h1>
@@ -54,7 +59,7 @@
       title="Raise a personal session of this role for you">Request session</button>{{end}}
   <button class="danger" hx-delete="{{$base}}" hx-swap="none"
       hx-confirm="Delete role {{.Name}} from {{.Project}}?"
-      hx-on::after-request="if(event.detail.successful) location.href='/ui/roles'">Delete</button>
+      hx-on::after-request="if(event.detail.successful) location.href='{{.RolesHref}}'">Delete</button>
 </div>
 
 <div class="sections">
diff --git a/internal/jam/adminui/templates/roles.html b/internal/jam/adminui/templates/roles.html
deleted file mode 100644
index b5122ed..0000000
--- a/internal/jam/adminui/templates/roles.html
+++ /dev/null
@@ -1,41 +0,0 @@
-{{define "content"}}
-<div class="page-head"><h1>Roles</h1><span class="sub">What a grant of each role lets an actor reach — open a role to edit it</span></div>
-<details class="panel">
-  <summary>New role</summary>
-  <form hx-post="/ui/roles" hx-target="#roles" hx-swap="outerHTML" data-reset>
-    <div class="grid">
-      <label>Role name <span class="req">required</span><input name="name" required></label>
-      <label>Project <span class="hint">or <a href="/ui/projects">create one</a></span>{{template "project-select"}}</label>
-      <label>Destinations <span class="hint">comma-separated, e.g. git=git-pat,anthropic</span><input name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials" autocomplete="off" spellcheck="false"></label>
-      <label>TTL <span class="hint">seconds, optional</span><input name="ttl-seconds" type="number" min="0" inputmode="numeric"></label>
-      <label>Kit <span class="hint">optional</span><input name="kit" data-ta="kits" autocomplete="off" spellcheck="false"></label>
-      <label>Model-spec <span class="hint">optional; blank = claude-default</span><input name="model-spec" data-ta="model-specs" autocomplete="off" spellcheck="false"></label>
-    </div>
-    <div class="form-actions"><button type="submit" class="primary">Create role</button></div>
-  </form>
-</details>
-{{template "roles-table" .}}
-{{end}}
-
-{{define "roles-table"}}
-<div class="card" id="roles">
-<table>
-  <thead><tr><th>Role</th><th>Project</th><th>Destinations</th><th>TTL</th><th>Kit</th><th></th></tr></thead>
-  <tbody>
-    {{range .Roles}}
-    <tr data-id="{{.Project}}/{{.Name}}">
-      <td><a class="mono" href="{{roleURL .Project .Name}}"><b>{{.Name}}</b></a></td><td><a href="{{projectURL .Project}}">{{.Project}}</a></td>
-      <td>{{$c := .Credentials}}{{range .Destinations}}<span class="chip">{{.}}{{with index $c .}} <span class="none">→</span> {{.}}{{end}}</span>{{else}}<span class="none">—</span>{{end}}</td>
-      <td>{{ttl .TTL}}</td>
-      <td>{{if .Kit}}<span class="chip">{{.Kit}}</span>{{else}}<span class="none">—</span>{{end}}</td>
-      <td class="actions">{{if $.CanRequest}}<button class="primary small" hx-post="/ui/roles/{{.Project}}/{{.Name}}/request" hx-target="#flash" hx-swap="innerHTML"
-              title="Raise a personal session of this role for you">Request</button>
-          {{end}}<button class="danger small" hx-delete="/ui/roles/{{.Project}}/{{.Name}}" hx-target="#roles" hx-swap="outerHTML"
-              hx-confirm="Delete role {{.Name}} from {{.Project}}?">Delete</button></td></tr>
-    {{else}}
-    <tr><td colspan="6" class="empty">No roles.</td></tr>
-    {{end}}
-  </tbody>
-</table>
-</div>
-{{end}}
diff --git a/internal/jam/adminui/templates/squawks.html b/internal/jam/adminui/templates/squawks.html
new file mode 100644
index 0000000..f27ae99
--- /dev/null
+++ b/internal/jam/adminui/templates/squawks.html
@@ -0,0 +1,33 @@
+{{/* squawk-table renders log rows newest first: .Rows ([]squawkRow), .Legacy
+     (recipients instead of a channel), .Filtered (which empty state). */}}
+{{define "squawk-table"}}
+<style>
+  #squawks td{vertical-align:top}
+  .reach{font-size:10.5px;font-weight:600;text-transform:uppercase;letter-spacing:.04em;padding:1px 6px;border-radius:999px;background:var(--idle-soft);color:var(--idle)}
+  .reach.ext{background:var(--accent-soft);color:var(--accent)}
+  td.body{max-width:560px;word-break:break-word}
+  td.body.plain{white-space:pre-wrap}
+  td.body.md > :first-child{margin-top:0} td.body.md > :last-child{margin-bottom:0}
+  td.body.md p, td.body.md ul, td.body.md ol, td.body.md pre{margin:0 0 .4em}
+  td.body.md pre{overflow-x:auto;background:var(--bg);padding:6px 8px;border-radius:6px}
+  td.body.md code{background:var(--idle-soft);padding:1px 4px;border-radius:4px}
+</style>
+<div class="card">
+<table id="squawks">
+  <thead><tr><th>Time</th><th>From</th><th>{{if .Legacy}}To{{else}}Channel{{end}}</th><th>Project</th><th>Body</th></tr></thead>
+  <tbody>
+    {{range .Rows}}
+      <tr>
+        <td><time>{{.At}}</time></td>
+        <td><span class="mono" title="{{.FromID}}">{{.From}}</span></td>
+        <td>{{if .ChannelID}}<a class="mono" href="/ui/intercom?participant={{.ChannelID}}" title="{{.ChannelID}}">{{.Channel}}</a>{{end}}{{range .To}}<div><span class="mono">{{.Target}}</span> <span class="reach{{if .External}} ext{{end}}">{{if .External}}external{{else}}internal{{end}}</span></div>{{end}}</td>
+        <td>{{.Project}}</td>
+        <td class="body {{if .Plain}}plain{{else}}md{{end}}">{{.Body}}{{if .Plain}} <span class="reach">text/plain</span>{{end}}</td>
+      </tr>
+    {{else}}
+      <tr><td colspan="5" class="empty">{{if .Filtered}}No squawks match.{{else}}No squawks logged yet.{{end}}</td></tr>
+    {{end}}
+  </tbody>
+</table>
+</div>
+{{end}}
diff --git a/internal/jam/adminui/writes.go b/internal/jam/adminui/writes.go
index a5b158a..d46fcaa 100644
--- a/internal/jam/adminui/writes.go
+++ b/internal/jam/adminui/writes.go
@@ -189,9 +189,10 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), msg)
 			return
 		}
+		// The new role's page is where it is edited; htmx follows the redirect.
 		w.Header().Set("HX-Redirect", roleURL(project, name))
 		log.Info("ui role created", "operator", jam.OperatorID(r), "project", orDefaultProject(project), "role", name)
-		renderFragment(w, "roles", "roles-table", rolesData(store, sup != nil))
+		w.WriteHeader(http.StatusOK)
 	})
 
 	mux.HandleFunc("POST /ui/actors/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
@@ -244,7 +245,8 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			return
 		}
 		log.Info("ui role removed", "operator", jam.OperatorID(r), "project", project, "role", name)
-		renderFragment(w, "roles", "roles-table", rolesData(store, sup != nil))
+		// The role page navigates to its project's roles on success.
+		w.WriteHeader(http.StatusOK)
 	})
 
 	mux.HandleFunc("POST /ui/coves", func(w http.ResponseWriter, r *http.Request) {
````

- [ ] **Step 4: Run to verify GREEN**

Run: `go test ./internal/jam/adminui/ && go vet ./internal/jam/adminui/ && gofmt -l internal/jam/adminui && just lint`
Expected: `ok`, no gofmt output, lint clean.

- [ ] **Step 5: Smoke the pages (no browser in the sandbox)**

Run: `go test ./internal/jam/adminui/ -run 'TestProjectTreeMarksCurrent|TestProjectPage|TestMovedPagesRedirect' -v`
Expected: PASS. Visual check happens on the user's `just dev-watch` (see Task 4).

- [ ] **Step 6: Commit**

```bash
git add -A internal/jam/adminui
git commit -m "feat(adminui): project tree with a page per section; roles move under their project"
```

---

### Task 3: Docs

**Files:**
- Create: `docs/usage/jam/ui-projects.md` (project tree, sections, role pages, `/ui/roles` redirects), `docs/usage/jam/ui-editing.md` (the UI's write surface, split out of `ui.md`, which was over the 200-line leaf budget)
- Modify: `docs/usage/jam/ui.md` (top nav paragraph; Projects bullet; Editing → pointer), `ui-pages.md` (project and role sections → pointer; frontmatter), `INDEX.md` (rows for the two new leaves; ui-pages row), `projects.md`, `escalation.md`, `personal-sessions.md` (inbound links)

- [ ] **Step 1: Apply the docs patch** (use the docs-author skill to review it — it moves content, it does not duplicate it)

````diff
diff --git a/docs/usage/jam/INDEX.md b/docs/usage/jam/INDEX.md
index bdf4c73..a6db488 100644
--- a/docs/usage/jam/INDEX.md
+++ b/docs/usage/jam/INDEX.md
@@ -54,7 +54,9 @@ five pillars), see the design history:
 | [standing-state.md](standing-state.md) | You need to know what survives a standing session's restart or upgrade, where its conversation and workspace live (labeled volumes), why it did or didn't resume, or which volumes Jam may delete. |
 | [requisitioner.md](requisitioner.md) | You are enabling Jam's always-on intake — polling a tracker (Linear) and raising a managed studio per ready ticket — or tuning its concurrency cap / poll interval. |
 | [ui.md](ui.md) | You want to watch a running Jam in a browser — the live studios and the roster/roles/kits/destinations — or do the roster day-job (enroll/revoke, roles, grants), edit kits/destinations/model-specs, or raise/tear down a managed studio, from the browser instead of the CLI. |
-| [ui-pages.md](ui-pages.md) | You are viewing or editing one project, user, studio, role, destination, model-spec or kit in the admin UI — a project's members/channels/escalation, a user's logins/OIDC/accounts, a studio's runtime/session/squawks, its scope, egress, allocation, standing sessions, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
+| [ui-editing.md](ui-editing.md) | You want to change something from the admin UI instead of the CLI — enroll/revoke, grants, roles, raise/tear down a studio, Request a personal session, edit kits/destinations/model-specs — or a UI write was refused. |
+| [ui-projects.md](ui-projects.md) | You are viewing or editing one project in the admin UI — its tree of sections (members, agents, roles, rooms and messages, escalation, chat service, context) — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles link. |
+| [ui-pages.md](ui-pages.md) | You are viewing or editing one user, studio, destination, model-spec or kit in the admin UI — a user's logins/OIDC/accounts, a studio's runtime/session/squawks, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
 | [session-events.md](session-events.md) | You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream, its storage/retention config, redaction, and the export API. |
 | [intercom.md](intercom.md) | You want a raised studio's agent to read/send comments on its own ticket (the brokered intercom MCP), or you're wiring the `/squawks` endpoint + its `cove-master mcp` delivery, wake-on (`runtime.wake`), or running the intercom without a Requisitioner. |
 | [turn-end.md](turn-end.md) | You want a ticket session to report its ticket's state or a session to end itself, to be woken at a time or on a schedule (optionally gated by a check), or to be woken (or torn down) after sitting idle; or you need to know why a studio shows `holding`, what a woken agent is told about why it woke, or how Jam decides a session whose turn ended may be woken, paused, or torn down. |
diff --git a/docs/usage/jam/escalation.md b/docs/usage/jam/escalation.md
index fccdc41..cb99d57 100644
--- a/docs/usage/jam/escalation.md
+++ b/docs/usage/jam/escalation.md
@@ -142,7 +142,7 @@ at-jam project escalation list  <project>
 at-jam project escalation clear <project> [--category <name>]
 ```
 
-The admin UI's project page edits the same chains ([ui-pages.md](ui-pages.md#project-pages)).
+The admin UI's project Escalation page edits the same chains ([ui-projects.md](ui-projects.md#sections)).
 
 - `set` **replaces** the whole ordered policy for one chain. Each `--tier` is
   `comma,separated,targets@duration` — a comma-separated list of `user:<name>`
diff --git a/docs/usage/jam/personal-sessions.md b/docs/usage/jam/personal-sessions.md
index fb74280..df2e2a7 100644
--- a/docs/usage/jam/personal-sessions.md
+++ b/docs/usage/jam/personal-sessions.md
@@ -173,7 +173,7 @@ at-jam session release ses_01j9q3x8f2k7m4n6p0r2s5t8v1
 - **request** grants a slot, then raises the studio with you as its owner, and
   prints only the session id (a new `ses_…` id for each request). The admin UI's role
   **Request** action does the same from the browser (see
-  [ui.md](ui.md#runtime-studios)). The prompt file is
+  [ui-editing.md](ui-editing.md#runtime-studios)). The prompt file is
   read on the host and sent in the request body. It never goes on argv. Unlike
   `studio raise`, no identity token or launch secret is returned.
 - **list** shows only **your** personal sessions in the project: id, role,
diff --git a/docs/usage/jam/projects.md b/docs/usage/jam/projects.md
index 056478b..f982dba 100644
--- a/docs/usage/jam/projects.md
+++ b/docs/usage/jam/projects.md
@@ -29,7 +29,7 @@ at-jam project rm acme       # refused while a role or grant references it
 
 Each is an admin-API client; for its target and auth flags see
 [operators.md](operators.md). The admin UI's Projects tab does the same
-([ui-pages.md](ui-pages.md#project-pages)). A project's goals and resources for its
+([ui-projects.md](ui-projects.md)). A project's goals and resources for its
 sessions are set with `at-jam context --project` ([session-context-authoring.md](session-context-authoring.md)).
 
 ## Existence is enforced
diff --git a/docs/usage/jam/ui-editing.md b/docs/usage/jam/ui-editing.md
new file mode 100644
index 0000000..5e3c2f5
--- /dev/null
+++ b/docs/usage/jam/ui-editing.md
@@ -0,0 +1,102 @@
+---
+summary: What the Jam admin UI can change — enroll/revoke actors, roles and grants, raising and tearing down studios and requesting a personal session, and the kit/destination/model-spec registry — with the type-ahead fields, the write banner, and the gate/CSRF/audit rules every write obeys.
+read_when: You want to change something from the Jam admin UI instead of the CLI — enroll or revoke an actor, add a grant, create a role, raise or tear down a studio, request a personal session, edit a kit/destination/model-spec — or a UI write was refused and you want to know why.
+owns: the admin UI's write surface — enroll/revoke/grant/role create-delete, the type-ahead reference fields, create panels and the write banner, the CSRF origin check and audit logging, runtime raise/teardown and Request, and the config-plane editing pointers
+prereqs: ui.md for reaching the UI and its sections; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive
+tier: leaf
+updated: 2026-10-07
+---
+
+# Editing from the admin UI
+
+Beyond viewing, the [admin UI](ui.md) can do the roster day-job, raise and tear
+down studios, and edit the config registry. Every write obeys the same gate as
+the views, is audit-logged against the operator, and is refused unless it comes
+from the UI itself.
+
+## Roster and roles
+
+Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
+verbs in [roster.md](roster.md):
+
+- **Enroll** an actor (id, project, role, optional destination overrides).
+  The identity token is shown **once**, right after enrolling — copy it then; it
+  is never shown again, stored in a list, or logged. For the full connection
+  snippet (env vars / git config), use the CLI `at-jam enroll`.
+- **Revoke** an actor, **create/delete** a role (and edit it on its
+  [role page](ui-projects.md#role-pages)), and **add/remove** a grant.
+  On the Actors page each actor's grants are chips (`project/role`, with a ×
+  to remove; hover for the effective destinations), and **+ Grant** on the
+  actor's row opens its add-grant form.
+- Destination fields (role, enroll/grant overrides) take the CLI's
+  `name=credential` syntax ([roster.md](roster.md#roles)); an unknown credential
+  or a mapping for a destination not in scope is rejected. Credential *names*
+  are references, not secrets, so the UI shows them (a project's Roles renders
+  `git → git-pat`); credential *values* never appear.
+- Every field that names another entity is a **type-ahead**: projects, roles
+  (of the project in the same form), kits, destinations and — after `=` in a
+  destinations list — credentials, roster targets (`user:`/`channel:` in
+  addressing and escalation tiers), Intercom participants, and chat services.
+  In list fields it completes the entry under the cursor. ↑/↓ move, Enter or
+  Tab accept, Esc closes. Suggestions guide but don't restrict: the server
+  still validates, so a glob like `user:*` is fine and an unknown project is
+  refused (a project must exist first — [projects.md](projects.md)). Project
+  fields start at `default`. Credential suggestions are the names `at-jam serve`
+  is configured with (names only, never values).
+
+Create forms sit in collapsed **+ Add …** panels above each table. The
+outcome of a write shows in a banner at the top of the page: a refused write
+(validation error, conflict, CSRF refusal) appears as a dismissible error with
+the server's message, rather than failing silently.
+
+Every change obeys the same gate as the views (loopback, or an off-loopback
+session with `require-scope`) and is recorded in Jam's audit log against the
+operator who made it. Destructive actions ask for confirmation. State-changing
+requests are refused unless they originate from the Jam UI itself (an
+Origin/Referer check, plus any exact origins listed in
+[`ui-origins`](serve.md)), so another site can't drive them through your browser.
+
+The kit registry and destinations are also editable from here — see
+[Config plane (kits, destinations, model-specs)](#config-plane-kits-destinations-model-specs) below.
+Raising and tearing down studios is editable from the UI when a runtime
+supervisor is configured — see [Runtime (studios)](#runtime-studios) below.
+
+### Runtime (studios)
+
+When Jam is configured with a runtime supervisor (`runtime:` in the serve
+config — see [coves.md](coves.md)), the Studios page can also:
+
+- **Raise a managed studio** — id, role, optional project/unit and a workload
+  prompt. Jam handles the studio's identity token and launch secret internally;
+  they are never shown in the browser (use the CLI `at-jam studio raise` for
+  manual wiring).
+- **Tear down a studio** (confirmed).
+
+A project's Roles section and each role page gain a **Request** action: it raises a
+[personal session](personal-sessions.md) of that role **for you**, with the
+prompt `Squawk me (user:<your name>) and we will get to work.`, so the
+session opens the conversation with you on the intercom. You must be signed in
+(`/ui/auth/login`) as a login linked to a member of the role's project.
+As anonymous loopback `local`, the action asks you to sign in. Admission,
+delivery checks, and errors are exactly those of `at-jam session request`, and
+the outcome (the new session id, or the refusal) shows in the page's banner.
+
+Without a runtime supervisor, the Studios page is view-only. Setting a studio's
+activity is not a UI action — that is reported by the studio itself. These actions
+obey the same gate, CSRF, and audit-logging as the roster edits above.
+
+### Config plane (kits, destinations, model-specs)
+
+- **Kits** — create a kit (name + studio-kit YAML, validated like `kit push`)
+  and delete an unused one; each kit's page shows its versions, diffs them,
+  pins one, and pushes new versions — see [ui-pages.md](ui-pages.md#kit-pages).
+- **Destinations** — create one (every field, including client env, git
+  routing and the session note) and remove one; each destination's page shows and
+  edits it — see [ui-pages.md](ui-pages.md#destination-pages).
+- **Model-specs** — create, edit and delete one, validated exactly like
+  `at-jam model-spec` — see [ui-pages.md](ui-pages.md#model-spec-pages).
+
+A kit config references credentials by name only (no secret values), and a
+destination's `cred-name` (or a model-spec's principal) is a reference, not a secret — the UI shows the name
+but never a credential value. These actions obey the same
+gate, CSRF, and audit-logging as the other edits.
diff --git a/docs/usage/jam/ui-pages.md b/docs/usage/jam/ui-pages.md
index 452ad76..65c18a2 100644
--- a/docs/usage/jam/ui-pages.md
+++ b/docs/usage/jam/ui-pages.md
@@ -1,7 +1,7 @@
 ---
-summary: The Jam admin UI's per-entity pages — a project's page (/ui/projects/<name>), a studio's page (/ui/coves/<id>), a role's page (/ui/roles/<project>/<name>), a destination's page (/ui/destinations/<name>), a model-spec's page (/ui/model-specs/<name>) and a kit's page (/ui/kits/<name>) — what each shows and how editing them works.
-read_when: You are viewing or editing a project, user, studio, role, destination, model-spec or kit in the Jam admin UI — a project's members, rooms, escalation or chat service; a user's logins, OIDC identities or accounts; a studio's runtime, waiting/escalation state, session streams or squawks; a role's scope, egress, allocation or standing sessions; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
-owns: the project, user, studio, role, destination, model-spec and kit detail pages (what they show, their edit forms incl. project members/rooms/escalation/chat-service editing, the users list, create-only list forms, connector-conflict flags, kit version rail/diff/push)
+summary: The Jam admin UI's per-entity pages outside a project — a user's page, a studio's page (/ui/coves/<id>), a destination's page (/ui/destinations/<name>), a model-spec's page (/ui/model-specs/<name>) and a kit's page (/ui/kits/<name>) — what each shows and how editing them works.
+read_when: You are viewing or editing a user, studio, destination, model-spec or kit in the Jam admin UI — a user's logins, OIDC identities or accounts; a studio's runtime, waiting/escalation state, session streams or squawks; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
+owns: the user, studio, destination, model-spec and kit detail pages (what they show, their edit forms, the users list, create-only list forms, connector-conflict flags, kit version rail/diff/push)
 prereqs: ui.md for reaching the UI, the write banner, and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles; connector.md for destination env/git; kits.md for the StudioKit schema and versioning
 tier: leaf
 updated: 2026-10-07
@@ -15,35 +15,10 @@ existing name is refused with "edit it on its page") and then open the new
 object's page, where every field is pre-filled — so an edit can't silently drop
 a field the form didn't show.
 
-## Project pages
-
-Each project name (in the Projects table, a role's breadcrumb, or a table cell)
-links to `/ui/projects/<name>`, the "everything in this project" view: its
-roles (linked, with destinations and kit), the actors holding a grant into it
-and which roles they hold, its members (linked to their user page, with handle
-and delivery) and rooms (service, ref), its escalation policy (the default chain and each category's chain, as
-ordered tiers of targets with their wait), its chat service, and its running
-studios.
-
-Members, rooms, escalation and chat service are edited in place; each write
-answers with the re-rendered page:
-
-- **Members** — **Add member** (a user, and delivery: one `service:address` per
-  line) and, per member, **Edit** delivery and **Remove**.
-- **Rooms** — **Add room** (name; connection: a connection name, or a service
-  for its connection; ref; an existing name is rebound) and **Remove**.
-- **Escalation** — edit the default chain or any category's chain as one
-  `targets@timeout` per line (the CLI's `--tier`; the first line is tier 0, the
-  timeout a positive duration), add a category chain, or **Clear** one. A target
-  that names nobody on this project's roster (e.g. after removing that human) is
-  flagged red — flagged, not blocked.
-- **Chat service** — none (tracker @-mentions only) or `discord`.
-
-**Delete** (here and in the table) is disabled while a member, a role, a session not yet gone or an actor's
-grant still references the project, and names what does — the same rule as
-`project rm` ([projects.md](projects.md)).
-**Rename** (the page head, except for `default`) renames the project in place
-and opens its new URL ([projects.md](projects.md)).
+## Project and role pages
+
+A project's pages (its tree, sections and role pages) are in
+[ui-projects.md](ui-projects.md).
 
 ## User pages
 
@@ -79,44 +54,13 @@ session or squawks is a 404.
 
 ## Session context cards
 
-The role page, the project page and the dashboard each carry a **Session context**
+The [role page, the project Overview](ui-projects.md) and the dashboard each carry a **Session context**
 card for that layer (role, project, Jam-wide): the core, its size as a session
 receives it against the budget (a project's includes the resources pointer), the
 leaves and, for projects, the resources. **Edit session context** is one YAML box in
 the [`at-jam context` format](session-context-authoring.md) with bodies inline;
 saving runs the same checks as the API, and **Clear** removes the layer.
 
-## Role pages
-
-Each role name (in the Roles table, a roster grant chip, or a studio row) links
-to its page, `/ui/roles/<project>/<name>`, which shows and edits the whole
-role, one section at a time — each with a pre-filled **Edit** form that saves
-only that section:
-
-- **Scope** — destinations with the credential the broker injects for each
-  (the role's mapping, or the destination's default), addressing, TTL, kit and
-  [model-spec](model-specs.md#binding-a-role) binding (blank = `claude-default`,
-  shown in the page head; the new-role form takes one too).
-  The destinations field uses the `name=credential` syntax; a bare name uses
-  the destination's default credential. Empty addressing means the role can't
-  squawk anyone.
-- **Egress** — the role's domain list, or "kit default" when it sets none.
-  Saving sets a policy (an empty list allows nothing beyond the sealed base and
-  the kit's infra domains); **Reset to kit default** removes it. Running
-  studios pick up the change on the supervisor's next reconcile.
-- **Allocation** — session caps and the personal-session idle ladder.
-  Durations take `30m`/`1h30m` (or bare seconds); a blank field is unset, and
-  the page says what applies when unset.
-- **Standing sessions** — declare, [upgrade](standing-sessions.md#upgrading-a-standing-session), [reset](standing-sessions.md#reset) and dismiss;
-  each shows its studio's phase, flagged **image stale** per [coves.md](coves.md#the-studio-verbs) (its Upgrade button highlighted) and any pending upgrade; a queued upgrade or pending reset flashes as accepted.
-- **Holders** and **Studios** — the actors granted the role (marked where the
-  grant overrides the scope; grants are managed on the Actors page) and the role's
-  running studios.
-
-**Request session** and **Delete** are on the page header. These writes share
-one lock with the JSON admin API's role, egress and standing routes, so an edit
-here and a CLI change can't overwrite each other.
-
 ## Destination pages
 
 Each destination name in the Destinations table links to
diff --git a/docs/usage/jam/ui-projects.md b/docs/usage/jam/ui-projects.md
new file mode 100644
index 0000000..46ff2f4
--- /dev/null
+++ b/docs/usage/jam/ui-projects.md
@@ -0,0 +1,102 @@
+---
+summary: The admin UI's project pages — the left-side project tree (Overview, Members, Agents, Roles, Intercom, Escalation) with one URL per section under /ui/projects/<name>, what each section shows and edits, and the role page at /ui/projects/<project>/roles/<name>.
+read_when: You are viewing or editing one project in the Jam admin UI — its members, agents, roles, rooms and recent messages, escalation chains, chat service or session context — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles link.
+owns: the project tree and its section pages (/ui/projects/<name>[/members|agents|roles|intercom|escalation]), creating a role from a project, the role page (/ui/projects/<project>/roles/<name>), and the /ui/roles redirects
+prereqs: ui.md for reaching the UI, the top nav, the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles and grants
+tier: leaf
+updated: 2026-10-07
+---
+
+# Admin UI project pages
+
+A project's pages share a **left-side tree** — the project, then **Overview ·
+Members · Agents · Roles · Intercom · Escalation** — beside the selected page.
+Each node is its own URL, so it can be linked and bookmarked, and the
+breadcrumb above the content follows the path (`Projects / acme / Roles / dev`,
+every segment a link). Everything scoped to one project lives here; the top
+nav's **Projects** section stays highlighted throughout.
+
+| Node | URL |
+|---|---|
+| Overview | `/ui/projects/<name>` |
+| Members | `/ui/projects/<name>/members` |
+| Agents | `/ui/projects/<name>/agents` |
+| Roles | `/ui/projects/<name>/roles`, each role `…/roles/<role>` |
+| Intercom | `/ui/projects/<name>/intercom` |
+| Escalation | `/ui/projects/<name>/escalation` |
+
+**The tree.** Roles, Agents and Intercom expand to their children — every role;
+the project's live and raising agents (the label counts them); its rooms. The
+branch holding the current page renders open and its node highlighted; the
+others expand on click (plain `<details>`, no script needed). On a narrow
+screen the tree collapses to one line naming the current node (`acme ▸ Roles ▸
+dev`); tap it to open the tree.
+
+**Old links.** `/ui/roles` redirects (301) to `/ui/projects`, and
+`/ui/roles/<project>/<role>` to the role's page here. The write endpoints keep
+their paths.
+
+## Sections
+
+Each section's edits are in place: a write answers with that section
+re-rendered.
+
+- **Overview** — counts (members, agents live of all, roles, rooms, escalation
+  chains), each linking to its section; the **chat service** (none — tracker
+  @-mentions only — or `discord`); the project's **Session context** card
+  ([ui-pages.md](ui-pages.md#session-context-cards)). **Rename** (except
+  `default`) renames the project and opens its new URL; **Delete** is disabled
+  while a member, a role, a session not yet gone or an actor's grant still
+  references the project, and names what does — the same rule as `project rm`
+  ([projects.md](projects.md)).
+- **Members** — the users agents here can address (linked to their user page,
+  with handle and delivery). **Add member** (a user, and delivery: one
+  `service:address` per line); per member, **Edit** delivery and **Remove**.
+- **Agents** — the project's studios (the shared studio table) and the actors
+  holding a grant into it, with the roles they hold.
+- **Roles** — the project's roles (linked, with destinations, TTL and kit) and,
+  with a runtime supervisor, a **Request** per role
+  ([ui-editing.md](ui-editing.md#runtime-studios)). **New role** creates one in this project
+  (the project comes from the page) and opens its page.
+- **Intercom** — the project's rooms (service, ref): **Add room** (name;
+  connection: a connection name, or a service for its connection; ref; an
+  existing name is rebound) and **Remove**. Below them, the project's newest 50
+  messages from the channel log, with **Full log** opening the
+  [Intercom](ui.md#intercom) page filtered to the project.
+- **Escalation** — the default chain and each category's chain, as ordered
+  tiers of targets with their wait ([escalation.md](escalation.md)). Edit a
+  chain as one `targets@timeout` per line (the CLI's `--tier`; the first line is
+  tier 0, the timeout a positive duration), add a category chain, or **Clear**
+  one. A target that names nobody on this project's roster is flagged red —
+  flagged, not blocked.
+
+## Role pages
+
+Each role name (in the tree, a project's Roles, a grant chip, or a studio row)
+links to its page, `/ui/projects/<project>/roles/<name>`, which shows and edits the whole
+role, one section at a time — each with a pre-filled **Edit** form that saves
+only that section:
+
+- **Scope** — destinations with the credential the broker injects for each
+  (the role's mapping, or the destination's default), addressing, TTL, kit and
+  [model-spec](model-specs.md#binding-a-role) binding (blank = `claude-default`,
+  shown in the page head; the new-role form takes one too).
+  The destinations field uses the `name=credential` syntax; a bare name uses
+  the destination's default credential. Empty addressing means the role can't
+  squawk anyone.
+- **Egress** — the role's domain list, or "kit default" when it sets none.
+  Saving sets a policy (an empty list allows nothing beyond the sealed base and
+  the kit's infra domains); **Reset to kit default** removes it. Running
+  studios pick up the change on the supervisor's next reconcile.
+- **Allocation** — session caps and the personal-session idle ladder.
+  Durations take `30m`/`1h30m` (or bare seconds); a blank field is unset, and
+  the page says what applies when unset.
+- **Standing sessions** — declare, [upgrade](standing-sessions.md#upgrading-a-standing-session), [reset](standing-sessions.md#reset) and dismiss;
+  each shows its studio's phase, flagged **image stale** per [coves.md](coves.md#the-studio-verbs) (its Upgrade button highlighted) and any pending upgrade; a queued upgrade or pending reset flashes as accepted.
+- **Holders** and **Studios** — the actors granted the role (marked where the
+  grant overrides the scope; grants are managed on the Actors page, under Agents) and the role's
+  running studios.
+
+**Request session** and **Delete** (which returns to the project's Roles) are on the page header. These writes share
+one lock with the JSON admin API's role, egress and standing routes, so an edit
+here and a CLI change can't overwrite each other.
diff --git a/docs/usage/jam/ui.md b/docs/usage/jam/ui.md
index f5a58ae..7bb5bd2 100644
--- a/docs/usage/jam/ui.md
+++ b/docs/usage/jam/ui.md
@@ -1,10 +1,10 @@
 ---
 summary: The Jam admin UI — a server-rendered web view of the live studios, the durable squawk Log, and the control-plane roster/roles/kits/destinations, served by `at-jam serve`; reachable on loopback always, and off-loopback via browser OIDC login. Beyond viewing, it can do the roster day-job (enroll/revoke actors, roles, grants), edit the kit registry, destinations and model-specs, and, with a runtime supervisor configured, raise/tear down managed studios and request a personal session of a role.
 read_when: You want to watch a running Jam in a browser — the live studio fleet, the squawk Log, and the roster/roles/kits/destinations — or do the roster day-job, edit kits/destinations/model-specs, or raise/tear down a managed studio from the browser, without running admin CLI verbs, or you are configuring browser login for it.
-owns: the `/ui/coves/{id}/session` timeline page; the `/ui/` observability + roster/kit/destination/model-spec-editing + runtime studio raise/teardown surface (what it shows, what it can mutate, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
+owns: the `/ui/coves/{id}/session` timeline page; the `/ui/` observability surface (the top nav and its sections, what each list shows, search, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
 prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
 tier: leaf
-updated: 2026-10-06
+updated: 2026-10-07
 ---
 
 # The Jam admin UI (`/ui/`)
@@ -17,7 +17,11 @@ there):
 http://127.0.0.1:8081/ui/
 ```
 
-It renders:
+The top nav has six sections — **Dashboard · Projects · Users · Agents ·
+Specs · Intercom** — and a page highlights its section, so a detail page
+highlights the list it belongs to (a role page: Projects). **Agents** opens the
+studio list (and holds the Actors page); **Specs** groups kits, destinations
+and model-specs. It renders:
 
 - **Dashboard** (`/ui/`) — summary tiles (live / raising / lost-or-terminating /
   idled studios, and counts of projects, actors, roles, kits, destinations),
@@ -36,13 +40,13 @@ It renders:
   Session event streams are not searched.
 - **Projects** (`/ui/projects`) — every project with its roles, actors,
   studios, roster size and chat service; create one, or delete one nothing
-  references. Each project's page is the "everything in this project" view —
-  see [ui-pages.md](ui-pages.md#project-pages).
+  references. Each project opens on a tree of its sections — members, agents,
+  roles, rooms and messages, escalation — see [ui-projects.md](ui-projects.md).
 - **Studios** (`/ui/coves`) — every managed studio's id, project/role, unit, phase,
   activity, connector and image status ([coves.md](coves.md#the-studio-verbs)), lease holder, raised-at, last-seen. The table **auto-refreshes every
   3 seconds** (htmx polling); no page reload. View-only unless a runtime
   supervisor is configured, in which case it can also raise and tear down
-  studios — see [Runtime (studios)](#runtime-studios) below and
+  studios — see [Runtime (studios)](ui-editing.md#runtime-studios) below and
   [coves.md](coves.md). Each id opens the studio's page (runtime, waiting and
   escalation state, session streams, squawks — see
   [ui-pages.md](ui-pages.md#studio-pages)); **timeline** next to it opens the
@@ -50,9 +54,9 @@ It renders:
 - **Intercom** (`/ui/intercom`) — a read-only, filterable, newest-first table of
   the channel log, with the frozen legacy log on a Legacy tab. See
   [Intercom](#intercom) below.
-- **Users / Actors / Roles / Kits / Destinations / Model-specs** — the control-plane objects as
-  tables, all editable from here — see [Editing](#editing-day-job-mutations)
-  below.
+- **Users / Actors / Kits / Destinations / Model-specs** — the control-plane
+  objects as tables, all editable from here; roles live in their project — see
+  [ui-editing.md](ui-editing.md).
 
 Every table has a fixed order — studios and actors by id; roles by project,
 then name; kits and destinations by name; squawks newest-first — so rows don't
@@ -80,7 +84,7 @@ the fail-closed rule in [serve.md](serve.md#exposing-the-admin-api-fail-closed))
   UI refuses it as a possible DNS-rebinding attempt.
   A loopback viewer is the anonymous operator `local`, unless they have signed
   in via `/ui/auth/login`: a valid session is used even on loopback, so the UI
-  knows *who* you are (the [role Request](#runtime-studios) action needs this).
+  knows *who* you are (the [role Request](ui-editing.md#runtime-studios) action needs this).
   A missing or expired session falls back to `local` without a login redirect.
   For UI development, [`dev-identity`](serve.md) makes loopback requests act as
   a chosen user on `/ui` and `/me` with no login at all.
@@ -186,89 +190,8 @@ behind **show progress events**. It updates live over SSE from `/ui/coves/{id}/s
 live; reconnects resume via `Last-Event-ID`). Storage, retention,
 and sensitivity: [session-events.md](session-events.md).
 
-## Editing (day-job mutations)
+## Editing
 
-Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
-verbs in [roster.md](roster.md):
-
-- **Enroll** an actor (id, project, role, optional destination overrides).
-  The identity token is shown **once**, right after enrolling — copy it then; it
-  is never shown again, stored in a list, or logged. For the full connection
-  snippet (env vars / git config), use the CLI `at-jam enroll`.
-- **Revoke** an actor, **create/delete** a role (and edit it on its
-  [role page](ui-pages.md#role-pages)), and **add/remove** a grant.
-  On the Actors page each actor's grants are chips (`project/role`, with a ×
-  to remove; hover for the effective destinations), and **+ Grant** on the
-  actor's row opens its add-grant form.
-- Destination fields (role, enroll/grant overrides) take the CLI's
-  `name=credential` syntax ([roster.md](roster.md#roles)); an unknown credential
-  or a mapping for a destination not in scope is rejected. Credential *names*
-  are references, not secrets, so the UI shows them (the Roles table renders
-  `git → git-pat`); credential *values* never appear.
-- Every field that names another entity is a **type-ahead**: projects, roles
-  (of the project in the same form), kits, destinations and — after `=` in a
-  destinations list — credentials, roster targets (`user:`/`channel:` in
-  addressing and escalation tiers), Intercom participants, and chat services.
-  In list fields it completes the entry under the cursor. ↑/↓ move, Enter or
-  Tab accept, Esc closes. Suggestions guide but don't restrict: the server
-  still validates, so a glob like `user:*` is fine and an unknown project is
-  refused (a project must exist first — [projects.md](projects.md)). Project
-  fields start at `default`. Credential suggestions are the names `at-jam serve`
-  is configured with (names only, never values).
-
-Create forms sit in collapsed **+ Add …** panels above each table. The
-outcome of a write shows in a banner at the top of the page: a refused write
-(validation error, conflict, CSRF refusal) appears as a dismissible error with
-the server's message, rather than failing silently.
-
-Every change obeys the same gate as the views (loopback, or an off-loopback
-session with `require-scope`) and is recorded in Jam's audit log against the
-operator who made it. Destructive actions ask for confirmation. State-changing
-requests are refused unless they originate from the Jam UI itself (an
-Origin/Referer check, plus any exact origins listed in
-[`ui-origins`](serve.md)), so another site can't drive them through your browser.
-
-The kit registry and destinations are also editable from here — see
-[Config plane (kits, destinations, model-specs)](#config-plane-kits-destinations-model-specs) below.
-Raising and tearing down studios is editable from the UI when a runtime
-supervisor is configured — see [Runtime (studios)](#runtime-studios) below.
-
-### Runtime (studios)
-
-When Jam is configured with a runtime supervisor (`runtime:` in the serve
-config — see [coves.md](coves.md)), the Studios page can also:
-
-- **Raise a managed studio** — id, role, optional project/unit and a workload
-  prompt. Jam handles the studio's identity token and launch secret internally;
-  they are never shown in the browser (use the CLI `at-jam studio raise` for
-  manual wiring).
-- **Tear down a studio** (confirmed).
-
-The Roles page gains a **Request** action per role: it raises a
-[personal session](personal-sessions.md) of that role **for you**, with the
-prompt `Squawk me (user:<your name>) and we will get to work.`, so the
-session opens the conversation with you on the intercom. You must be signed in
-(`/ui/auth/login`) as a login linked to a member of the role's project.
-As anonymous loopback `local`, the action asks you to sign in. Admission,
-delivery checks, and errors are exactly those of `at-jam session request`, and
-the outcome (the new session id, or the refusal) shows in the page's banner.
-
-Without a runtime supervisor, the Studios page is view-only. Setting a studio's
-activity is not a UI action — that is reported by the studio itself. These actions
-obey the same gate, CSRF, and audit-logging as the roster edits above.
-
-### Config plane (kits, destinations, model-specs)
-
-- **Kits** — create a kit (name + studio-kit YAML, validated like `kit push`)
-  and delete an unused one; each kit's page shows its versions, diffs them,
-  pins one, and pushes new versions — see [ui-pages.md](ui-pages.md#kit-pages).
-- **Destinations** — create one (every field, including client env, git
-  routing and the session note) and remove one; each destination's page shows and
-  edits it — see [ui-pages.md](ui-pages.md#destination-pages).
-- **Model-specs** — create, edit and delete one, validated exactly like
-  `at-jam model-spec` — see [ui-pages.md](ui-pages.md#model-spec-pages).
-
-A kit config references credentials by name only (no secret values), and a
-destination's `cred-name` (or a model-spec's principal) is a reference, not a secret — the UI shows the name
-but never a credential value. These actions obey the same
-gate, CSRF, and audit-logging as the other edits.
+The UI's writes — enrolling and granting, roles, raising and tearing down
+studios, **Request**, and the kit/destination/model-spec registry — with their
+gate, CSRF and audit rules, are in [ui-editing.md](ui-editing.md).
````

- [ ] **Step 2: Audit**

Run the **docs-audit** skill (`python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs`).
Expected: no ERROR or WARN for `usage/jam/ui*.md`, `usage/jam/INDEX.md`, or the three relinked docs. (Pre-existing findings elsewhere — `coves.md`/`serve.md` size, superpowers frontmatter — are out of scope.) `ui.md` ≤ 200 lines.

- [ ] **Step 3: Commit**

```bash
git add docs/usage/jam
git commit -m "docs(jam): project tree pages; split UI editing out of ui.md"
```

---

### Task 4: Full verification

- [ ] `just test` — all green.
- [ ] `just lint` — clean.
- [ ] `git diff main --stat` — only the files in File Structure (+ the spec and this plan).
- [ ] Ask the user to eyeball on `just dev-watch`: the tree on `/ui/projects/<p>` (open branch, highlighted node, counts), a role page, a narrow window (tree collapses to `acme ▸ …`), and that `/ui/roles/<p>/<r>` bookmarks land on the role page.
