# Admin UI IA — Slice 3 (Specs + dead ends) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship slice 3 of [the admin UI IA spec](../specs/2026-10-07-admin-ui-ia-design.md) (§4 Specs, §6 dead ends, §7.3) and the terminology/duplication leftovers from the slice 1–2 reviews.

**Architecture:** Small, independent edits on the existing pages: a `/ui/specs` landing (302 to its first sub-tab, Kits) that the top nav now links; a role's kit links to the kit page; model-specs gain "Used by" (`specUses` — roles whose `ModelSpecName()` is the spec, the default spec also listing unbound roles) on the page and a count on the list; search merges the Studios and Actors groups into one **Agents** group (one hit per id) and gains a **Model-specs** group; the dashboard's phase tiles open `/ui/agents?phase=…` and its count tiles become Projects / Agents / Users / Specs (`stats` drops `Actors`, `Roles`, `Kits`, `Destinations` for `Agents`, `Users`, `Specs`).

**Tech Stack:** Go 1.26 `net/http`, `html/template`; hermetic `httptest` tests.

## Global Constraints

- `/ui/specs` answers **302** → `/ui/kits` (a landing, not a move); the top nav's Specs links `/ui/specs`.
- Dashboard tiles: `live|raising|attention|idled` link `/ui/agents?phase=<same>`; counts are Projects, Agents (actors ∪ studios by id), Users (live, not removed), Specs (kits + destinations + model-specs). No Roles or Actors tile.
- Search: one Agents hit per id; model-specs matched on name, type, model id and principal credential.
- Model-spec "Used by" reuses `kitUse` (`Implicit` = unbound role on the default spec).
- Docs: `ui.md` ≤ 200 lines (docs-audit counts the trailing newline); `ui-pages.md` owns the grant-chip mechanics, `ui-editing.md` links to it.

## How to apply

As in slices 1–2: verified patches, applied with `git apply --whitespace=error` from `main` at this plan's commit — the test patch (RED: here the package doesn't compile, because `stats_test.go` uses the new `stats` fields), then the code patch (GREEN). Stop and report if a patch doesn't apply.

---

### Task 1: Specs landing, role → kit link, model-spec used-by, search, dashboard tiles

**Files:** `adminui.go` (`/ui/specs`), `nav.go`, `stats.go`, `model_specs.go`, `search.go`, `templates/{dashboard,role,model_specs,model_spec,search}.html`; tests `specs_test.go` (new), `stats_test.go`, `adminui_test.go`, `polish_test.go`, `studio_text_test.go`.

**Interfaces:** Produces `func specUses(store jam.Store, name string) []kitUse`; `specDetail.Uses`; model-specs table data key `UsedBy map[string]int`; `stats{Studios, Live, Raising, Idled, Attention, Projects, Agents, Users, Specs int}`; search group keys `agents`, `model-specs` (replacing `studios`, `actors`).

- [ ] **Step 1: Apply the test patch**

````diff
diff --git a/internal/jam/adminui/adminui_test.go b/internal/jam/adminui/adminui_test.go
index ae9186c..a4a45bf 100644
--- a/internal/jam/adminui/adminui_test.go
+++ b/internal/jam/adminui/adminui_test.go
@@ -48,7 +48,7 @@ func TestDashboard(t *testing.T) {
 		t.Fatalf("GET /ui/ = %d, want 200", rec.Code)
 	}
 	body := rec.Body.String()
-	for _, want := range []string{`id="coves"`, "spider-9", `hx-trigger="every 3s"`, `data-stat="actors"><b>1</b>`} {
+	for _, want := range []string{`id="coves"`, "spider-9", `hx-trigger="every 3s"`, `data-stat="agents"><b>2</b>`} {
 		if !strings.Contains(body, want) {
 			t.Errorf("dashboard missing %q", want)
 		}
diff --git a/internal/jam/adminui/polish_test.go b/internal/jam/adminui/polish_test.go
index d52d7b0..89f79e9 100644
--- a/internal/jam/adminui/polish_test.go
+++ b/internal/jam/adminui/polish_test.go
@@ -15,8 +15,8 @@ func TestNavMarksCurrentPage(t *testing.T) {
 	for path, href := range map[string]string{
 		"/ui/":             `href="/ui/"`,
 		"/ui/agents":       `href="/ui/agents"`,
-		"/ui/kits":         `href="/ui/kits"`, // Specs
-		"/ui/destinations": `href="/ui/kits"`,
+		"/ui/kits":         `href="/ui/specs"`, // Specs
+		"/ui/destinations": `href="/ui/specs"`,
 		"/ui/users":        `href="/ui/users"`,
 		"/ui/projects":     `href="/ui/projects"`,
 	} {
@@ -46,7 +46,7 @@ func TestDashboardShowsStatTiles(t *testing.T) {
 	store := newStore(t)
 	seedCove(t, store)
 	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/").Body.String()
-	for _, want := range []string{`data-stat="live"`, `data-stat="attention"`, `data-stat="actors"`, `href="/ui/kits"`} {
+	for _, want := range []string{`data-stat="live"`, `data-stat="attention"`, `data-stat="agents"`, `href="/ui/specs"`} {
 		if !strings.Contains(body, want) {
 			t.Errorf("dashboard missing %q", want)
 		}
diff --git a/internal/jam/adminui/specs_test.go b/internal/jam/adminui/specs_test.go
new file mode 100644
index 0000000..3c39854
--- /dev/null
+++ b/internal/jam/adminui/specs_test.go
@@ -0,0 +1,116 @@
+package adminui_test
+
+import (
+	"net/http"
+	"strings"
+	"testing"
+
+	"github.com/aethons-tools/cove/internal/jam"
+	"github.com/aethons-tools/cove/internal/jam/adminui"
+)
+
+// seedSpecUse: model-spec "opus" bound by acme/dev (and the default spec); acme/ops binds none (so it
+// runs the default); a kit "web" bound by acme/dev.
+func seedSpecUse(t *testing.T) jam.Store {
+	t.Helper()
+	store := newStore(t)
+	mustCreateProject(t, store, "acme")
+	// "opus", and the default spec a serving Jam seeds (a bare store has none).
+	for _, name := range []string{"opus", jam.DefaultModelSpec} {
+		if err := store.PutModelSpec(jam.ModelSpec{Name: name, Type: jam.HarnessClaude, Version: "2.1.0",
+			Principal: jam.ModelPrincipal{Credential: "anth"}, Model: jam.ModelChoice{ID: "claude-" + name + "-5-5"}}); err != nil {
+			t.Fatal(err)
+		}
+	}
+	if _, err := store.PushKit("web", "kind: studio\n"); err != nil {
+		t.Fatal(err)
+	}
+	for _, r := range []jam.Role{{Name: "dev", ModelSpec: "opus", Kit: "web"}, {Name: "ops"}} {
+		if err := store.PutRole("acme", r); err != nil {
+			t.Fatal(err)
+		}
+	}
+	return store
+}
+
+// Specs opens on its first sub-tab.
+func TestSpecsLanding(t *testing.T) {
+	rec := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/specs")
+	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/kits" {
+		t.Fatalf("GET /ui/specs = %d → %q, want 302 → /ui/kits", rec.Code, rec.Header().Get("Location"))
+	}
+}
+
+// A role's kit links to the kit's page, not the kit list.
+func TestRoleKitLinksKitPage(t *testing.T) {
+	body := get(t, adminui.Handler(seedSpecUse(t), testLogger(), nil, nil, anyCred, nil), "/ui/projects/acme/roles/dev").Body.String()
+	if !strings.Contains(body, `<a class="chip accent" href="/ui/kits/web">web</a>`) {
+		t.Errorf("role page should link its kit's page")
+	}
+}
+
+// A model-spec's page lists the roles that run it — bound ones, and for the
+// default spec the unbound ones — and the list counts them.
+func TestModelSpecUsedBy(t *testing.T) {
+	store := seedSpecUse(t)
+	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
+	body := get(t, h, "/ui/model-specs/opus").Body.String()
+	if !strings.Contains(body, "<h2>Used by</h2>") || !strings.Contains(body, `href="/ui/projects/acme/roles/dev">acme/dev</a>`) || strings.Contains(body, "acme/ops") {
+		t.Errorf("opus used-by should list acme/dev only:\n%s", body)
+	}
+	body = get(t, h, "/ui/model-specs/"+jam.DefaultModelSpec).Body.String()
+	if !strings.Contains(body, `acme/ops <span class="unset">no model-spec set</span>`) || strings.Contains(body, "acme/dev") {
+		t.Errorf("the default spec's used-by should list the unbound acme/ops only:\n%s", body)
+	}
+	list := get(t, h, "/ui/model-specs").Body.String()
+	row := list[strings.Index(list, `<tr data-id="opus">`):]
+	row = row[:strings.Index(row, "</tr>")]
+	if !strings.Contains(row, "<td>1</td>") {
+		t.Errorf("model-specs list should count opus's one role:\n%s", row)
+	}
+}
+
+// Search covers model-specs and gives one Agents hit per id, whether it is a
+// studio, an enrolled actor or both.
+func TestSearchModelSpecsAndAgents(t *testing.T) {
+	store := seedSpecUse(t)
+	if err := store.PutInstance(jam.Instance{ActorID: "zz-agent", Project: "acme", Role: "dev", Phase: jam.PhaseLive}); err != nil {
+		t.Fatal(err)
+	}
+	if err := store.AddActor(jam.Actor{ID: "zz-agent", TokenHash: "h", Grants: []jam.Grant{{Project: "acme", Role: "dev"}}}); err != nil {
+		t.Fatal(err)
+	}
+	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
+	body := get(t, h, "/ui/search?q=zz-agent").Body.String()
+	if n := strings.Count(body, `href="/ui/agents/zz-agent"`); n != 1 {
+		t.Errorf("a studio that is also enrolled should be one search hit, got %d", n)
+	}
+	body = get(t, h, "/ui/search?q=claude-opus").Body.String()
+	if !strings.Contains(body, `href="/ui/model-specs/opus"`) {
+		t.Errorf("search should find a model-spec by its model:\n%s", body)
+	}
+	if rec := get(t, h, "/ui/search?go=1&q=opus"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ui/model-specs/opus" {
+		t.Errorf("an exact model-spec name should jump to it: %d → %q", rec.Code, rec.Header().Get("Location"))
+	}
+}
+
+// The dashboard's studio tiles open the Agents list filtered to their phase;
+// its count tiles follow the top nav.
+func TestDashboardTilesFollowSections(t *testing.T) {
+	body := get(t, adminui.Handler(seedSpecUse(t), testLogger(), nil, nil, anyCred, nil), "/ui/").Body.String()
+	for _, want := range []string{
+		`href="/ui/agents?phase=live" data-stat="live"`, `href="/ui/agents?phase=raising" data-stat="raising"`,
+		`href="/ui/agents?phase=attention" data-stat="attention"`, `href="/ui/agents?phase=idled" data-stat="idled"`,
+		`href="/ui/projects" data-stat="projects"`, `href="/ui/agents" data-stat="agents"`,
+		`href="/ui/users" data-stat="users"`, `href="/ui/specs" data-stat="specs"`,
+	} {
+		if !strings.Contains(body, want) {
+			t.Errorf("dashboard missing %s", want)
+		}
+	}
+	for _, gone := range []string{`data-stat="roles"`, `data-stat="actors"`, `href="/ui/roles"`, `href="/ui/coves"`} {
+		if strings.Contains(body, gone) {
+			t.Errorf("dashboard still has %s", gone)
+		}
+	}
+}
diff --git a/internal/jam/adminui/stats_test.go b/internal/jam/adminui/stats_test.go
index dfff4ad..5882fc0 100644
--- a/internal/jam/adminui/stats_test.go
+++ b/internal/jam/adminui/stats_test.go
@@ -31,7 +31,9 @@ func TestDashboardStatsCountsPhasesAndObjects(t *testing.T) {
 	}
 
 	got := dashboardStats(st)
-	want := stats{Live: 2, Raising: 1, Attention: 2, Idled: 1, Studios: 6, Projects: 1, Actors: 1, Roles: 1, Kits: 1}
+	// Agents: six studios plus actor x, which has none. Specs: kit k plus the
+	// built-in default model-spec.
+	want := stats{Live: 2, Raising: 1, Attention: 2, Idled: 1, Studios: 6, Projects: 1, Agents: 7, Specs: 1 + len(st.ListModelSpecs())}
 	if got != want {
 		t.Fatalf("dashboardStats = %+v, want %+v", got, want)
 	}
diff --git a/internal/jam/adminui/studio_text_test.go b/internal/jam/adminui/studio_text_test.go
index 93f38f2..49a8e8d 100644
--- a/internal/jam/adminui/studio_text_test.go
+++ b/internal/jam/adminui/studio_text_test.go
@@ -31,7 +31,7 @@ func TestUISaysStudioAndJam(t *testing.T) {
 	}
 
 	dash := get(t, h, "/ui/").Body.String()
-	if !strings.Contains(dash, "Live studios") || strings.Contains(dash, "Live coves") {
-		t.Errorf("dashboard should say Live studios; got:\n%s", dash)
+	if !strings.Contains(dash, "Live agents") || strings.Contains(dash, "Live coves") {
+		t.Errorf("dashboard should say Live agents; got:\n%s", dash)
 	}
 }
````

- [ ] **Step 2: RED** — `go test ./internal/jam/adminui/` fails to build (`unknown field Actors/Roles/Kits in struct literal of type stats`).

- [ ] **Step 3: Apply the code patch**

````diff
diff --git a/internal/jam/adminui/adminui.go b/internal/jam/adminui/adminui.go
index a035cb2..70df988 100644
--- a/internal/jam/adminui/adminui.go
+++ b/internal/jam/adminui/adminui.go
@@ -167,6 +167,10 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 	})
 
 	registerAgents(mux, store, sup, canEdit)
+	// Specs has no page of its own: it opens on its first sub-tab.
+	mux.HandleFunc("GET /ui/specs", func(w http.ResponseWriter, r *http.Request) {
+		http.Redirect(w, r, "/ui/kits", http.StatusFound)
+	})
 	// The global roles list and role pages moved into the project tree.
 	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
 		redirect(w, r, "/ui/projects")
diff --git a/internal/jam/adminui/model_specs.go b/internal/jam/adminui/model_specs.go
index 4250634..33b32e4 100644
--- a/internal/jam/adminui/model_specs.go
+++ b/internal/jam/adminui/model_specs.go
@@ -39,7 +39,8 @@ type specChoices struct {
 type specDetail struct {
 	Title       string
 	Spec        jam.ModelSpec
-	Provider    string // Spec.Claude.Provider, "" when no body
+	Uses        []kitUse // roles that run this model-spec (Implicit: unbound, so the default)
+	Provider    string   // Spec.Claude.Provider, "" when no body
 	Form        specForm
 	Choices     specChoices
 	NotFound    bool
@@ -73,7 +74,7 @@ func (u specUI) choices(current string) specChoices {
 }
 
 func (u specUI) detail(m jam.ModelSpec) specDetail {
-	d := specDetail{Title: "Model-specs", Spec: m, Choices: u.choices(m.Principal.Credential)}
+	d := specDetail{Title: "Model-specs", Spec: m, Choices: u.choices(m.Principal.Credential), Uses: specUses(u.store, m.Name)}
 	d.Form.Allow = strings.Join(m.Policy.Allow, "\n")
 	d.Form.Deny = strings.Join(m.Policy.Deny, "\n")
 	d.Form.Headers = formatHeaderRules(m.Principal.Headers)
@@ -94,9 +95,28 @@ func (u specUI) detail(m jam.ModelSpec) specDetail {
 	return d
 }
 
+// specUses lists the roles that run model-spec name: those bound to it and,
+// for the default spec, those bound to none.
+func specUses(store jam.Store, name string) []kitUse {
+	var out []kitUse
+	for _, p := range store.ListProjects() {
+		for _, r := range store.ListRoles(p) {
+			if r.ModelSpecName() == name {
+				out = append(out, kitUse{Project: p, Role: r.Name, Implicit: r.ModelSpec == ""})
+			}
+		}
+	}
+	return out
+}
+
 func (u specUI) tableData() map[string]any {
+	used := map[string]int{}
+	for _, m := range u.store.ListModelSpecs() {
+		used[m.Name] = len(specUses(u.store, m.Name))
+	}
 	return map[string]any{
-		"Specs": u.store.ListModelSpecs(),
+		"Specs":  u.store.ListModelSpecs(),
+		"UsedBy": used,
 		// New seeds the create form: claude on the anthropic provider.
 		"New": u.detail(jam.ModelSpec{Type: jam.HarnessClaude, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}),
 	}
diff --git a/internal/jam/adminui/nav.go b/internal/jam/adminui/nav.go
index d5f5dff..304df76 100644
--- a/internal/jam/adminui/nav.go
+++ b/internal/jam/adminui/nav.go
@@ -24,14 +24,13 @@ type navItem struct {
 	Href    string
 }
 
-// navItems is the top nav, in order. Specs lands on the kit list until its
-// own page exists.
+// navItems is the top nav, in order. Specs lands on its first sub-tab (Kits).
 var navItems = []navItem{
 	{navDashboard, "Dashboard", "/ui/"},
 	{navProjects, "Projects", "/ui/projects"},
 	{navUsers, "Users", "/ui/users"},
 	{navAgents, "Agents", "/ui/agents"},
-	{navSpecs, "Specs", "/ui/kits"},
+	{navSpecs, "Specs", "/ui/specs"},
 	{navIntercom, "Intercom", "/ui/intercom"},
 }
 
diff --git a/internal/jam/adminui/search.go b/internal/jam/adminui/search.go
index d2987a8..796824f 100644
--- a/internal/jam/adminui/search.go
+++ b/internal/jam/adminui/search.go
@@ -134,7 +134,9 @@ func search(store jam.Store, msgs SquawkReader, q string) searchData {
 		}
 	}
 
-	studios := searchGroup{Key: "studios", Name: "Studios"}
+	// Agents: one hit per id, whether it is a studio, an enrolled actor or both.
+	agents := searchGroup{Key: "agents", Name: "Agents"}
+	seen := map[string]bool{}
 	for _, i := range store.ListInstances() {
 		i.Project = jam.ProjectName(store, i.Project)
 		if m.any(i.ActorID, i.Unit, i.Owner, i.Name, i.Project+"/"+i.Role) {
@@ -145,18 +147,17 @@ func search(store jam.Store, msgs SquawkReader, q string) searchData {
 			if i.Owner != "" {
 				sub += " · owner " + i.Owner
 			}
-			studios.add(searchHit{Title: i.ActorID, Sub: sub, URL: agentURL(i.ActorID), Exact: m.exact(i.ActorID)})
+			seen[i.ActorID] = true
+			agents.add(searchHit{Title: i.ActorID, Sub: sub, URL: agentURL(i.ActorID), Exact: m.exact(i.ActorID)})
 		}
 	}
-
-	actors := searchGroup{Key: "actors", Name: "Actors"}
 	for _, a := range store.ListActors() {
 		grants := make([]string, len(a.Grants))
 		for i, g := range a.Grants {
 			grants[i] = jam.ProjectName(store, g.Project) + "/" + g.Role
 		}
-		if m.any(append([]string{a.ID}, grants...)...) {
-			actors.add(searchHit{Title: a.ID, Sub: "grants: " + strings.Join(grants, ", "), URL: agentURL(a.ID), Exact: m.exact(a.ID)})
+		if !seen[a.ID] && m.any(append([]string{a.ID}, grants...)...) {
+			agents.add(searchHit{Title: a.ID, Sub: "grants: " + strings.Join(grants, ", "), URL: agentURL(a.ID), Exact: m.exact(a.ID)})
 		}
 	}
 
@@ -183,6 +184,17 @@ func search(store jam.Store, msgs SquawkReader, q string) searchData {
 		}
 	}
 
+	specs := searchGroup{Key: "model-specs", Name: "Model-specs"}
+	for _, ms := range store.ListModelSpecs() {
+		if m.any(ms.Name, string(ms.Type), ms.Model.ID, ms.Principal.Credential) {
+			sub := string(ms.Type) + " · " + ms.Principal.Credential
+			if ms.Model.ID != "" {
+				sub += " · " + ms.Model.ID
+			}
+			specs.add(searchHit{Title: ms.Name, Sub: sub, URL: specURL(ms.Name), Exact: m.exact(ms.Name)})
+		}
+	}
+
 	squawks := searchGroup{Key: "squawks", Name: "Squawks", MoreURL: "/ui/intercom?q=" + url.QueryEscape(q)}
 	if msgs != nil {
 		var found []Logged
@@ -204,7 +216,7 @@ func search(store jam.Store, msgs SquawkReader, q string) searchData {
 		}
 	}
 
-	for _, g := range []searchGroup{studios, roles, projects, kits, dests, actors, users, channels, squawks} {
+	for _, g := range []searchGroup{agents, roles, projects, kits, dests, specs, users, channels, squawks} {
 		if g.Count > 0 {
 			d.Groups = append(d.Groups, g)
 			d.Total += g.Count
diff --git a/internal/jam/adminui/stats.go b/internal/jam/adminui/stats.go
index 6973e09..7997397 100644
--- a/internal/jam/adminui/stats.go
+++ b/internal/jam/adminui/stats.go
@@ -6,8 +6,8 @@ import "github.com/aethons-tools/cove/internal/jam"
 // need to look at: lost (reconciler declared dead) or terminating (teardown in
 // flight).
 type stats struct {
-	Studios, Live, Raising, Idled, Attention    int
-	Projects, Actors, Roles, Kits, Destinations int
+	Studios, Live, Raising, Idled, Attention int
+	Projects, Agents, Users, Specs           int // Specs: kits + destinations + model-specs
 }
 
 // dashboardStats counts the fleet by phase and the control-plane objects.
@@ -27,11 +27,12 @@ func dashboardStats(store jam.Store) stats {
 		}
 	}
 	s.Projects = len(store.ListProjects())
-	s.Actors = len(store.ListActors())
-	for _, p := range store.ListProjects() {
-		s.Roles += len(store.ListRoles(p))
+	s.Agents = len(agentRows(store, nil, ""))
+	for _, u := range store.ListUsers() {
+		if u.Status != jam.StatusRemoved {
+			s.Users++
+		}
 	}
-	s.Kits = len(store.ListKits())
-	s.Destinations = len(store.ListDestinations())
+	s.Specs = len(store.ListKits()) + len(store.ListDestinations()) + len(store.ListModelSpecs())
 	return s
 }
diff --git a/internal/jam/adminui/templates/dashboard.html b/internal/jam/adminui/templates/dashboard.html
index b8582d2..0454676 100644
--- a/internal/jam/adminui/templates/dashboard.html
+++ b/internal/jam/adminui/templates/dashboard.html
@@ -11,15 +11,14 @@
 </style>
 {{with .Stats}}
 <div class="tiles">
-  <a class="tile live" href="/ui/coves" data-stat="live"><b>{{.Live}}</b><span>Live studios</span></a>
-  <a class="tile{{if .Raising}} wait{{end}}" href="/ui/coves" data-stat="raising"><b>{{.Raising}}</b><span>Raising</span></a>
-  <a class="tile{{if .Attention}} bad{{end}}" href="/ui/coves" data-stat="attention"><b>{{.Attention}}</b><span>Lost / terminating</span></a>
-  <a class="tile" href="/ui/coves" data-stat="idled"><b>{{.Idled}}</b><span>Idled</span></a>
+  <a class="tile live" href="/ui/agents?phase=live" data-stat="live"><b>{{.Live}}</b><span>Live agents</span></a>
+  <a class="tile{{if .Raising}} wait{{end}}" href="/ui/agents?phase=raising" data-stat="raising"><b>{{.Raising}}</b><span>Raising</span></a>
+  <a class="tile{{if .Attention}} bad{{end}}" href="/ui/agents?phase=attention" data-stat="attention"><b>{{.Attention}}</b><span>Lost / terminating</span></a>
+  <a class="tile" href="/ui/agents?phase=idled" data-stat="idled"><b>{{.Idled}}</b><span>Idled</span></a>
   <a class="tile" href="/ui/projects" data-stat="projects"><b>{{.Projects}}</b><span>Projects</span></a>
-  <a class="tile" href="/ui/agents" data-stat="actors"><b>{{.Actors}}</b><span>Actors</span></a>
-  <a class="tile" href="/ui/roles" data-stat="roles"><b>{{.Roles}}</b><span>Roles</span></a>
-  <a class="tile" href="/ui/kits" data-stat="kits"><b>{{.Kits}}</b><span>Kits</span></a>
-  <a class="tile" href="/ui/destinations" data-stat="destinations"><b>{{.Destinations}}</b><span>Destinations</span></a>
+  <a class="tile" href="/ui/agents" data-stat="agents"><b>{{.Agents}}</b><span>Agents</span></a>
+  <a class="tile" href="/ui/users" data-stat="users"><b>{{.Users}}</b><span>Users</span></a>
+  <a class="tile" href="/ui/specs" data-stat="specs"><b>{{.Specs}}</b><span>Specs</span></a>
 </div>
 {{end}}
 {{template "context-panel" .JamContext}}
diff --git a/internal/jam/adminui/templates/model_spec.html b/internal/jam/adminui/templates/model_spec.html
index df731e2..7bf57a1 100644
--- a/internal/jam/adminui/templates/model_spec.html
+++ b/internal/jam/adminui/templates/model_spec.html
@@ -69,6 +69,13 @@
     </div>
   </section>
 
+  <section class="card">
+    <header><h2>Used by</h2><span class="sub">roles that run this model-spec</span></header>
+    <div class="body">
+      {{range .Uses}}<a class="chip" href="{{roleURL .Project .Role}}">{{.Project}}/{{.Role}}{{if .Implicit}} <span class="unset">no model-spec set</span>{{end}}</a>{{else}}<span class="unset">No role runs this model-spec.</span>{{end}}
+    </div>
+  </section>
+
   <section class="card full">
     <header><h2>Claude</h2><span class="sub">the per-harness body</span></header>
     {{with .Spec.Claude}}
diff --git a/internal/jam/adminui/templates/model_specs.html b/internal/jam/adminui/templates/model_specs.html
index 328fa8d..b4a2984 100644
--- a/internal/jam/adminui/templates/model_specs.html
+++ b/internal/jam/adminui/templates/model_specs.html
@@ -13,7 +13,7 @@
 {{define "model-specs-table"}}
 <div class="card" id="model-specs">
 <table>
-  <thead><tr><th>Name</th><th>Type</th><th>Version</th><th>Principal</th><th>Model</th><th>Policy</th><th>Provider</th><th></th></tr></thead>
+  <thead><tr><th>Name</th><th>Type</th><th>Version</th><th>Principal</th><th>Model</th><th>Policy</th><th>Provider</th><th>Used by</th><th></th></tr></thead>
   <tbody>
     {{range .Specs}}
     <tr data-id="{{.Name}}">
@@ -24,11 +24,12 @@
       <td>{{if .Model.ID}}<span class="mono">{{.Model.ID}}</span>{{if .Model.Effort}} <span class="chip">{{.Model.Effort}}</span>{{end}}{{else}}<span class="none">default</span>{{end}}</td>
       <td>{{if .Policy.Mode}}<span class="mono">{{.Policy.Mode}}</span>{{else}}<span class="none">default</span>{{end}}</td>
       <td>{{with .Claude}}<span class="mono">{{.Provider}}</span>{{else}}<span class="none">—</span>{{end}}</td>
+      <td>{{with index $.UsedBy .Name}}{{.}}{{else}}<span class="none">0</span>{{end}}</td>
       <td class="actions"><button class="danger small" hx-delete="/ui/model-specs/{{.Name}}" hx-target="#model-specs" hx-swap="outerHTML"
           hx-confirm="Delete model-spec {{.Name}}?">Delete</button></td>
     </tr>
     {{else}}
-    <tr><td colspan="8" class="empty">No model-specs.</td></tr>
+    <tr><td colspan="9" class="empty">No model-specs.</td></tr>
     {{end}}
   </tbody>
 </table>
diff --git a/internal/jam/adminui/templates/role.html b/internal/jam/adminui/templates/role.html
index eee5b1a..d48718b 100644
--- a/internal/jam/adminui/templates/role.html
+++ b/internal/jam/adminui/templates/role.html
@@ -50,7 +50,7 @@
   <h1>{{.Name}}</h1>
   <div class="facts">
     <a class="chip" href="{{projectURL .Project}}">{{.Project}}</a>
-    {{if .Role.Kit}}<span>kit <a class="chip accent" href="/ui/kits">{{.Role.Kit}}</a></span>{{else}}<span class="unset">no kit</span>{{end}}
+    {{if .Role.Kit}}<span>kit <a class="chip accent" href="{{kitURL .Role.Kit}}">{{.Role.Kit}}</a></span>{{else}}<span class="unset">no kit</span>{{end}}
     <span>model-spec <a class="chip" href="/ui/model-specs/{{.Role.ModelSpecName}}">{{.Role.ModelSpecName}}</a>{{if not .Role.ModelSpec}} <span class="unset">default</span>{{end}}</span>
     <span>TTL {{ttl .Role.Scope.TTL}}</span>
   </div>
diff --git a/internal/jam/adminui/templates/search.html b/internal/jam/adminui/templates/search.html
index 4354f42..a3c0b29 100644
--- a/internal/jam/adminui/templates/search.html
+++ b/internal/jam/adminui/templates/search.html
@@ -13,7 +13,7 @@
   .hit .s{display:block;color:var(--muted);font-size:12.5px;overflow-wrap:anywhere}
   mark{background:var(--accent-soft);color:var(--accent);border-radius:3px;padding:0 1px}
 </style>
-<div class="page-head"><h1>Search</h1><span class="sub">projects, studios, roles, actors, roster, kits, destinations and squawks</span></div>
+<div class="page-head"><h1>Search</h1><span class="sub">agents, roles, projects, kits, destinations, model-specs, users, rooms and squawks</span></div>
 <form class="search-big" action="/ui/search" method="get" role="search">
   <input id="q-page" name="q" value="{{.Q}}" autocomplete="off" autofocus placeholder="Search everything…"
     hx-get="/ui/search" hx-trigger="input changed delay:200ms, search" hx-target="#search-results" hx-swap="outerHTML" hx-push-url="true">
````

- [ ] **Step 4: GREEN** — `go test ./internal/jam/adminui/ && go vet ./internal/jam/adminui/ && gofmt -l internal/jam/adminui && just lint` clean.

- [ ] **Step 5: Commit** — `git add -A internal/jam/adminui && git commit -m "feat(adminui): Specs landing, model-spec used-by, agents in search, dashboard tiles open filtered agents"`

---

### Task 2: Docs

- [ ] **Step 1: Apply the docs patch**

````diff
diff --git a/docs/usage/jam/INDEX.md b/docs/usage/jam/INDEX.md
index 5fd7e9d..b436077 100644
--- a/docs/usage/jam/INDEX.md
+++ b/docs/usage/jam/INDEX.md
@@ -53,8 +53,8 @@ five pillars), see the design history:
 | [standing-sessions.md](standing-sessions.md) | You want a role to have a permanent, named agent running (a standing teammate) or want to remove, reset or upgrade one: `standing add\|list\|rm\|reset\|upgrade`, how Jam keeps one studio per name alive (restart with its conversation and workspace kept, backoff), upgrading a stale one, dismissal, admission, and how it messages people. |
 | [standing-state.md](standing-state.md) | You need to know what survives a standing session's restart or upgrade, where its conversation and workspace live (labeled volumes), why it did or didn't resume, or which volumes Jam may delete. |
 | [requisitioner.md](requisitioner.md) | You are enabling Jam's always-on intake — polling a tracker (Linear) and raising a managed studio per ready ticket — or tuning its concurrency cap / poll interval. |
-| [ui.md](ui.md) | You want to watch a running Jam in a browser — the live studios, the squawk Log, a session timeline, the roster/roles/kits/destinations — find your way around the UI (nav, sub-tabs, search), use /me/, or configure browser login. To change something, see ui-editing.md. |
-| [ui-editing.md](ui-editing.md) | You want to change something from the admin UI instead of the CLI — enroll/revoke, grants, roles, raise/tear down a studio, Request a personal session, edit kits/destinations/model-specs — or a UI write was refused. |
+| [ui.md](ui.md) | You want to watch a running Jam in a browser — the agents and their studios, the squawk Log, a session timeline, the projects/users/specs — find your way around the UI (nav, sub-tabs, dashboard, search), use /me/, or configure browser login. To change something, see ui-editing.md. |
+| [ui-editing.md](ui-editing.md) | You want to change something from the admin UI instead of the CLI — enroll/revoke agents, grants, roles, raise/tear down a studio, Request a personal session, edit kits/destinations/model-specs — or a UI write was refused. |
 | [ui-projects.md](ui-projects.md) | You are viewing or editing one project in the admin UI — its tree of sections (members, agents, roles, rooms and messages, escalation, chat service, context) — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles link. |
 | [ui-pages.md](ui-pages.md) | You are viewing or editing one user, agent, destination, model-spec or kit in the admin UI — a user's logins/OIDC/accounts, an agent's grants and its studio's runtime/session/squawks, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
 | [session-events.md](session-events.md) | You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream, its storage/retention config, redaction, and the export API. |
diff --git a/docs/usage/jam/ui-editing.md b/docs/usage/jam/ui-editing.md
index 5bc51f5..d338415 100644
--- a/docs/usage/jam/ui-editing.md
+++ b/docs/usage/jam/ui-editing.md
@@ -1,6 +1,6 @@
 ---
-summary: What the Jam admin UI can change — enroll/revoke actors, roles and grants, raising and tearing down studios and requesting a personal session, and the kit/destination/model-spec registry — with the type-ahead fields, the write banner, and the gate/CSRF/audit rules every write obeys.
-read_when: You want to change something from the Jam admin UI instead of the CLI — enroll or revoke an actor, add a grant, create a role, raise or tear down a studio, request a personal session, edit a kit/destination/model-spec — or a UI write was refused and you want to know why.
+summary: What the Jam admin UI can change — enroll/revoke agents, roles and grants, raising and tearing down studios and requesting a personal session, and the kit/destination/model-spec registry — with the type-ahead fields, the write banner, and the gate/CSRF/audit rules every write obeys.
+read_when: You want to change something from the Jam admin UI instead of the CLI — enroll or revoke an agent, add a grant, create a role, raise or tear down a studio, request a personal session, edit a kit/destination/model-spec — or a UI write was refused and you want to know why.
 owns: the admin UI's write surface — enroll/revoke/grant/role create-delete, the type-ahead reference fields, create panels and the write banner, the CSRF origin check and audit logging, runtime raise/teardown and Request, and the config-plane editing pointers
 prereqs: ui.md for reaching the UI and its sections; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive
 tier: leaf
@@ -23,11 +23,9 @@ verbs in [roster.md](roster.md):
   The identity token is shown **once**, right after enrolling — copy it then; it
   is never shown again, stored in a list, or logged. For the full connection
   snippet (env vars / git config), use the CLI `at-jam enroll`.
-- **Revoke** an agent, **create/delete** a role (and edit it on its
-  [role page](ui-projects.md#role-pages)), and **add/remove** a grant. On an
-  [agent's page](ui-pages.md#agent-pages) its grants are chips (`project/role`,
-  with a × to remove; hover for the effective destinations), **+ Grant** opens
-  its add-grant form, and **Revoke** is in the header.
+- **Revoke** an agent and **add/remove** its grants on its
+  [agent page](ui-pages.md#agent-pages); **create/delete** a role (and edit it
+  on its [role page](ui-projects.md#role-pages)).
 - Destination fields (role, enroll/grant overrides) take the CLI's
   `name=credential` syntax ([roster.md](roster.md#roles)); an unknown credential
   or a mapping for a destination not in scope is rejected. Credential *names*
diff --git a/docs/usage/jam/ui-pages.md b/docs/usage/jam/ui-pages.md
index a50e6c4..36ae999 100644
--- a/docs/usage/jam/ui-pages.md
+++ b/docs/usage/jam/ui-pages.md
@@ -103,8 +103,9 @@ git** is ticked; the form says so. **Delete** is on the page header.
 
 Each model-spec name in the Model-specs table (`/ui/model-specs`) links to
 `/ui/model-specs/<name>`. The table shows type, version (with any constraint as a chip), principal,
-model, policy mode and provider. The page shows the harness (type, version, constraint, principal
-credential *name* and header rules, model, effort, note), the policy (mode, allow/deny rules) and the
+model, policy mode, provider and how many roles use it. The page shows the harness (type, version, constraint, principal
+credential *name* and header rules, model, effort, note), the policy (mode, allow/deny rules), the
+roles that run it (**Used by** — for the default spec, also the roles bound to none) and the
 claude body (provider, provider-env keys, plugins, settings keys).
 
 **New model-spec** and **Edit model-spec** share one form: type and claude
diff --git a/docs/usage/jam/ui.md b/docs/usage/jam/ui.md
index c66df51..142c025 100644
--- a/docs/usage/jam/ui.md
+++ b/docs/usage/jam/ui.md
@@ -1,6 +1,6 @@
 ---
 summary: The Jam admin UI — a server-rendered web view of the live studios, the durable squawk Log, and the control-plane roster/roles/kits/destinations, served by `at-jam serve`; reachable on loopback always, and off-loopback via browser OIDC login. Covers the top nav and its sub-tabs, the list pages, search, the Intercom log, the session timeline and the participant /me/ surface; what the UI can change is in ui-editing.md.
-read_when: You want to watch a running Jam in a browser — the live studio fleet, the squawk Log, a session timeline, and the roster/roles/kits/destinations — find your way around the UI (nav, sub-tabs, search), use the participant /me/ page, or configure browser login for it. To change something from the UI, read ui-editing.md instead.
+read_when: You want to watch a running Jam in a browser — the agents and their studios, the squawk Log, a session timeline, the projects/users/specs — find your way around the UI (nav, sub-tabs, search), use the participant /me/ page, or configure browser login for it. To change something from the UI, read ui-editing.md instead.
 owns: the `/ui/agents/{id}/session` timeline page; the `/ui/` observability surface (the top nav and its sections, what each list shows, search, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
 prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
 tier: leaf
@@ -19,23 +19,25 @@ http://127.0.0.1:8081/ui/
 
 The top nav has six sections — **Dashboard · Projects · Users · Agents ·
 Specs · Intercom** — and a page highlights its section, so a detail page
-highlights the list it belongs to (a role page: Projects). **Specs** groups
-**Kits · Destinations · Model-specs** under a sub-tab strip (their detail pages
-show it too). It renders:
-
-- **Dashboard** (`/ui/`) — summary tiles (live / raising / lost-or-terminating /
-  idled studios, and counts of projects, actors, roles, kits, destinations),
-  each linking to its page, then the Jam-wide **Session context** card
+highlights the list it belongs to (a role page: Projects). **Specs**
+(`/ui/specs`, opening on Kits) groups **Kits · Destinations · Model-specs**
+under a sub-tab strip (their detail pages show it too). It renders:
+
+- **Dashboard** (`/ui/`) — summary tiles: live / raising / lost-or-terminating /
+  idled agents, each opening the Agents list filtered to that phase, and counts
+  of projects, agents, users and specs (kits + destinations + model-specs),
+  each opening its section; then the Jam-wide **Session context** card
   ([ui-pages.md](ui-pages.md#session-context-cards)), above the studio table.
 - **Search** — the box in the top bar (press `/` from anywhere) searches every
-  page's objects at once: studios (id, unit, owner, standing name,
-  project/role), roles (project/name, kit, destinations), projects, kits (name,
-  current prompt and egress), destinations (name, route, upstream, env keys),
-  actors (id, grants), users (name, logins, OIDC subject, account handles and
-  ids) and channels, and squawk bodies (newest 10; the rest via Intercom's `q=`).
+  page's objects at once: agents (id, unit, owner, standing name,
+  project/role, grants — one hit per id), roles (project/name, kit,
+  destinations), projects, kits (name, current prompt and egress), destinations
+  (name, route, upstream, env keys), model-specs (name, type, model, principal),
+  users (name, logins, OIDC subject, account handles and ids) and rooms, and
+  squawk bodies (newest 10; the rest via Intercom's `q=`).
   Matching is case-insensitive substring, at least 2 characters; results are
   grouped and link to each object's page. **Enter** jumps straight to the page
-  when exactly one object's name is the whole query (e.g. a studio id or
+  when exactly one object's name is the whole query (e.g. an agent id or
   `acme/dev`); otherwise it opens `/ui/search?q=…`, which updates as you type.
   Session event streams are not searched.
 - **Projects** (`/ui/projects`) — every project with its roles, actors,
@@ -58,10 +60,9 @@ show it too). It renders:
   tables, all editable from here; roles live in their project — see
   [ui-editing.md](ui-editing.md).
 
-Every table has a fixed order — agents and studios by id; roles by project,
-then name; kits and destinations by name; squawks newest-first — so rows don't
-shuffle across a poll or after an edit. The order comes from the
-store, so the JSON admin API and CLI lists match it.
+Every table has a fixed order — agents and studios by id; roles by project, then
+name; kits and destinations by name; squawks newest-first — so rows don't shuffle
+across a poll or an edit, and the JSON admin API and CLI lists match it.
 
 **One look for `/ui` and `/me`.** Both UIs take their colors (light and dark,
 following the OS setting) and typography from one stylesheet, `jam.css`, in
````

- [ ] **Step 2: Audit** — no ERROR/WARN for `usage/jam/ui*.md` or `INDEX.md` (ignore duplicated-line WARNs whose other file is a `docs/superpowers/plans/*.md`); `ui.md` within budget.

- [ ] **Step 3: Commit** — `git add docs/usage/jam && git commit -m "docs(jam): Specs landing, dashboard tiles, search and model-spec used-by"`

---

### Task 3: Full verification

- [ ] `just test`, `just lint` green; no tracked changes left.
- [ ] Eyeball on `just dev-watch`: dashboard tiles (filtered agents; Projects/Agents/Users/Specs), Specs nav → Kits, a role's kit chip → kit page, a model-spec's Used by, search for an agent id (one hit) and a model name.
