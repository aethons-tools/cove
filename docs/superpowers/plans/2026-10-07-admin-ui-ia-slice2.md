# Admin UI IA — Slice 2 (Agents) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship slice 2 of [the admin UI IA spec](../specs/2026-10-07-admin-ui-ia-design.md) (§3, §5, §7.2): an **Agents** list that merges enrolled actors and studios, an **agent page** that is the identity (grants, revoke) plus its studio and session, and redirects from the retired `/ui/coves…` and `/ui/actors` pages.

**Architecture:** An agent is keyed by id: the union of `jam.RosterSummaries` (actors) and `jam.CoveSummaries` (studios), with a derived **kind** (`agentKind`: standing / personal from the session, ticket = ephemeral with a unit, manual = ephemeral without, enrolled = no studio). `agents.go` builds the list (sorted by id, optional phase filter matching the dashboard tiles) and registers the list route and the redirects; `agent.go` (the former `studio.go`) builds the agent page, adding the actor's grants. The roster template goes away; grant/revoke/teardown controls live on the agent page and answer with no body (the page reloads or navigates). The dashboard's studio table, which used to poll the Studios page, polls the dashboard itself.

**Tech Stack:** Go 1.26 `net/http`, `html/template`, htmx; hermetic `httptest` tests.

## Global Constraints

- Top nav: Agents links `/ui/agents`; Agents has no sub-tabs (Specs keeps Kits · Destinations · Model-specs).
- Kinds, exactly: `standing`, `personal`, `ticket`, `manual`, `enrolled`.
- Phase filter values: `live`, `raising`, `idled`, `attention` (= lost or terminating); anything else shows all; the polled table keeps the filter.
- List order: by id. One row per id (an enrolled studio is one row).
- Redirects (301, query kept): `/ui/coves` and `/ui/actors` → `/ui/agents`; `/ui/coves/{id}` → `/ui/agents/{id}`; `/ui/coves/{id}/session` → `/ui/agents/{id}/session`.
- Session events: `/ui/agents/{id}/session/events`, and the old `/ui/coves/{id}/session/events` still serves (open timelines).
- Write endpoints keep their paths (`/ui/coves`, `/ui/coves/{id}`, `/ui/enrollments…`, `/ui/actors/{id}/grants…`). Raise answers with the agents table; revoke, grant add/remove and teardown answer `200` with no body.
- Secrets: the agent page and list never render a token hash or launch-secret hash (existing no-leak tests keep covering both pages).
- Docs: `ui.md` stays ≤ 200 lines; use docs-author/docs-audit.

## How to apply the code in this plan

As in slice 1: the code was prototyped and verified before this plan was written; each patch applies cleanly (`git apply --whitespace=error`) from `main` at this plan's commit. Apply the **test patch** (RED), then the **code patch** (GREEN), read the diff, commit. If a patch does not apply, stop and report — don't hand-merge.

## File Structure

| File | Responsibility |
|---|---|
| `internal/jam/adminui/agents.go` (new) | `agentURL`, `agentKind`, `agentPhases`/`agentFilters`, `agentRow`/`agentRows`, `agentsData`, `registerAgents` (list + redirects) |
| `internal/jam/adminui/agent.go` (was `studio.go`) | `agentDetail` (+ `Actor`, `HasActor`, derived `Kind`), `buildAgentDetail`, `registerAgent` (`GET /ui/agents/{id}`) |
| `internal/jam/adminui/templates/agents.html` (new) | list page: Raise + Enroll panels, phase filter strip, polled `agents-table`, `enroll-result` |
| `internal/jam/adminui/templates/agent.html` (was `studio.html`) | Identity section (grants, + Grant, expiry), Revoke, agent breadcrumbs/links |
| `internal/jam/adminui/templates/coves.html` | only the shared `coves-tbl` (links to agent pages) and the dashboard's polled `coves-table` |
| `internal/jam/adminui/templates/roster.html` | **deleted** |
| `internal/jam/adminui/adminui.go`, `nav.go`, `session.go`, `writes.go`, `search.go`, `projtree.go` | page map, dashboard poll fragment, nav, session routes, write responses, agent links |
| `templates/session.html`, `project.html`, `role.html`, `dashboard.html` | agent links and wording |
| `internal/jam/adminui/agents_test.go` (new) + existing tests | kinds, merge, order, filters, redirects, identity, holder links, dashboard poll |
| `docs/usage/jam/ui.md`, `ui-pages.md`, `ui-editing.md`, `ui-projects.md`, `session-events.md`, `coves.md`, `renamed-from-harbor.md`, `INDEX.md` | docs |

---

### Task 1: Agents list, agent page, redirects

**Interfaces:**
- Consumes (slice 1): `navSection`/`mustParse`, `redirect`, `projectURL`, `roleURL`, `crumbs` styling conventions.
- Produces: `func agentURL(id string) string` (template func `agentURL`); `func agentKind(inst jam.Instance, running bool) string`; `func agentRows(store jam.Store, img jam.ImageResolver, phase string) []agentRow`; `func newAgentsData(store jam.Store, img jam.ImageResolver, phase string, canEdit bool) agentsData`; `func buildAgentDetail(store, msgs, sess, id string, canEdit bool) (agentDetail, bool)`; pages `agents`, `agent`; templates `agents-table`, `enroll-result`, `coves-table` (dashboard poll, `hx-get="/ui/"`).

Behavior notes (read before reviewing the diff):
- `GET /ui/` with `HX-Request: true` answers only the dashboard's studio table (its poll target moved from the retired Studios page).
- The list's Teardown re-fetches the table with `htmx.ajax`; the agent page's Teardown reloads, Revoke navigates to `/ui/agents`, grant add/remove reload.
- An agent page 404s only when nothing is known: no actor, no studio, no session streams, no squawks.

- [ ] **Step 1: Apply the test patch**

````diff
diff --git a/internal/jam/adminui/adminui_test.go b/internal/jam/adminui/adminui_test.go
index ac61dbb..ae9186c 100644
--- a/internal/jam/adminui/adminui_test.go
+++ b/internal/jam/adminui/adminui_test.go
@@ -70,12 +70,12 @@ func seedCove(t *testing.T, store jam.Store) {
 func TestCovesFullPage(t *testing.T) {
 	store := newStore(t)
 	seedCove(t, store)
-	rec := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/coves")
+	rec := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/agents")
 	if rec.Code != http.StatusOK {
-		t.Fatalf("GET /ui/coves = %d, want 200", rec.Code)
+		t.Fatalf("GET /ui/agents = %d, want 200", rec.Code)
 	}
 	body := rec.Body.String()
-	for _, want := range []string{"<nav", `id="coves"`, "spider-9", "live", "running", `hx-trigger="every 3s"`} {
+	for _, want := range []string{"<nav", `id="agents"`, "spider-9", "live", "running", `hx-trigger="every 3s"`} {
 		if !strings.Contains(body, want) {
 			t.Errorf("full page missing %q", want)
 		}
@@ -86,11 +86,11 @@ func TestCovesFragment(t *testing.T) {
 	store := newStore(t)
 	seedCove(t, store)
 	rec := httptest.NewRecorder()
-	req := httptest.NewRequest(http.MethodGet, "/ui/coves", nil)
+	req := httptest.NewRequest(http.MethodGet, "/ui/agents", nil)
 	req.Header.Set("HX-Request", "true")
 	adminui.Handler(store, testLogger(), nil, nil, anyCred, nil).ServeHTTP(rec, req)
 	body := rec.Body.String()
-	if !strings.Contains(body, `id="coves"`) || !strings.Contains(body, "spider-9") {
+	if !strings.Contains(body, `id="agents"`) || !strings.Contains(body, "spider-9") {
 		t.Errorf("fragment missing table/row; got:\n%s", body)
 	}
 	if strings.Contains(body, "<nav") || strings.Contains(body, "<html") {
@@ -107,7 +107,7 @@ func TestCovesNoSecretLeak(t *testing.T) {
 	}); err != nil {
 		t.Fatal(err)
 	}
-	rec := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/coves")
+	rec := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/agents")
 	if strings.Contains(rec.Body.String(), "SECRET-HASH-XYZ") {
 		t.Error("cove view leaked the launch-secret hash")
 	}
@@ -122,10 +122,18 @@ func TestRosterView(t *testing.T) {
 	if err := store.AddActor(jam.Actor{ID: "spider-2", TokenHash: "HASH-NOPE", Grants: []jam.Grant{{Project: "acme", Role: "worker"}}}); err != nil {
 		t.Fatal(err)
 	}
-	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/actors").Body.String()
-	for _, want := range []string{"spider-2", "worker", "anthropic"} {
+	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
+	body := get(t, h, "/ui/agents").Body.String()
+	for _, want := range []string{`href="/ui/agents/spider-2"`, "enrolled", `href="/ui/projects/acme/roles/worker"`} {
 		if !strings.Contains(body, want) {
-			t.Errorf("roster view missing %q", want)
+			t.Errorf("agents list missing %q", want)
+		}
+	}
+	// the agent page shows its grants with their effective destinations
+	body = get(t, h, "/ui/agents/spider-2").Body.String()
+	for _, want := range []string{"acme/worker", "destinations: anthropic"} {
+		if !strings.Contains(body, want) {
+			t.Errorf("agent page missing %q", want)
 		}
 	}
 }
@@ -135,7 +143,8 @@ func TestRosterViewNoSecretLeak(t *testing.T) {
 	if err := store.AddActor(jam.Actor{ID: "spider-2", TokenHash: "HASH-NOPE"}); err != nil {
 		t.Fatal(err)
 	}
-	if strings.Contains(get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/actors").Body.String(), "HASH-NOPE") {
+	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
+	if strings.Contains(get(t, h, "/ui/agents").Body.String()+get(t, h, "/ui/agents/spider-2").Body.String(), "HASH-NOPE") {
 		t.Error("roster view leaked a token hash")
 	}
 }
diff --git a/internal/jam/adminui/agents_test.go b/internal/jam/adminui/agents_test.go
new file mode 100644
index 0000000..36fcf11
--- /dev/null
+++ b/internal/jam/adminui/agents_test.go
@@ -0,0 +1,179 @@
+package adminui_test
+
+import (
+	"net/http"
+	"net/http/httptest"
+	"strings"
+	"testing"
+
+	"github.com/aethons-tools/cove/internal/jam"
+	"github.com/aethons-tools/cove/internal/jam/adminui"
+)
+
+// seedAgents: one agent of each kind — standing, personal, ticket, manual
+// (studios), enrolled (an actor with no studio) — and one studio whose actor
+// is also enrolled (merged into one row).
+func seedAgents(t *testing.T) jam.Store {
+	t.Helper()
+	store := newStore(t)
+	mustCreateProject(t, store, "acme")
+	for _, r := range []string{"dev", "ops"} {
+		if err := store.PutRole("acme", jam.Role{Name: r}); err != nil {
+			t.Fatal(err)
+		}
+	}
+	for _, i := range []jam.Instance{
+		{ActorID: "s-standing", Project: "acme", Role: "dev", Name: "nightly", SessionKind: "standing", Phase: jam.PhaseLive},
+		{ActorID: "s-personal", Project: "acme", Role: "dev", SessionKind: "personal", Owner: "alice", Phase: jam.PhaseIdled},
+		{ActorID: "s-ticket", Project: "acme", Role: "dev", Unit: "COV-7", Phase: jam.PhaseRaising},
+		{ActorID: "s-manual", Project: "acme", Role: "ops", Name: "scratch", Phase: jam.PhaseLost},
+	} {
+		if err := store.PutInstance(i); err != nil {
+			t.Fatal(err)
+		}
+	}
+	for _, a := range []jam.Actor{
+		{ID: "e-only", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "ops"}}},
+		{ID: "s-ticket", TokenHash: "h2", Grants: []jam.Grant{{Project: "acme", Role: "dev"}}},
+	} {
+		if err := store.AddActor(a); err != nil {
+			t.Fatal(err)
+		}
+	}
+	return store
+}
+
+func agentRow(t *testing.T, body, id string) string {
+	t.Helper()
+	i := strings.Index(body, `<tr data-id="`+id+`">`)
+	if i < 0 {
+		t.Fatalf("no row for %s in:\n%s", id, body)
+	}
+	return body[i : i+strings.Index(body[i:], "</tr>")]
+}
+
+// The list merges actors and studios by id, in id order, and names each
+// agent's kind.
+func TestAgentsListMergesAndKinds(t *testing.T) {
+	body := get(t, adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil), "/ui/agents").Body.String()
+	for id, kind := range map[string]string{
+		"s-standing": "standing", "s-personal": "personal", "s-ticket": "ticket", "s-manual": "manual", "e-only": "enrolled",
+	} {
+		if row := agentRow(t, body, id); !strings.Contains(row, `<span class="chip">`+kind+`</span>`) {
+			t.Errorf("%s: want kind %s in\n%s", id, kind, row)
+		}
+	}
+	if n := strings.Count(body, `<tr data-id="s-ticket">`); n != 1 {
+		t.Errorf("an enrolled studio is one row, got %d", n)
+	}
+	if row := agentRow(t, body, "e-only"); !strings.Contains(row, "no studio") || !strings.Contains(row, `href="/ui/projects/acme/roles/ops"`) {
+		t.Errorf("enrolled-only row: %s", row)
+	}
+	last := -1
+	for _, id := range []string{"e-only", "s-manual", "s-personal", "s-standing", "s-ticket"} {
+		i := strings.Index(body, `<tr data-id="`+id+`">`)
+		if i < last {
+			t.Errorf("rows not in id order at %s", id)
+		}
+		last = i
+	}
+}
+
+// ?phase= filters the list like the dashboard tiles count, and the polled
+// table keeps the filter; an unknown filter shows everything.
+func TestAgentsPhaseFilter(t *testing.T) {
+	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
+	for phase, want := range map[string][]string{
+		"live":      {"s-standing"},
+		"raising":   {"s-ticket"},
+		"idled":     {"s-personal"},
+		"attention": {"s-manual"},
+		"bogus":     {"s-standing", "s-ticket", "s-personal", "s-manual", "e-only"},
+	} {
+		body := get(t, h, "/ui/agents?phase="+phase).Body.String()
+		if n := strings.Count(body, `<tr data-id="`); n != len(want) {
+			t.Errorf("phase=%s: %d rows, want %d", phase, n, len(want))
+		}
+		for _, id := range want {
+			if !strings.Contains(body, `<tr data-id="`+id+`">`) {
+				t.Errorf("phase=%s: missing %s", phase, id)
+			}
+		}
+	}
+	body := get(t, h, "/ui/agents?phase=live").Body.String()
+	if !strings.Contains(body, `hx-get="/ui/agents?phase=live" hx-trigger="every 3s"`) || !strings.Contains(body, `class="tab sel" href="/ui/agents?phase=live"`) {
+		t.Errorf("the poll and the filter strip should keep phase=live")
+	}
+}
+
+// The retired studio and actor pages answer 301 to their agent pages.
+func TestAgentRedirects(t *testing.T) {
+	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
+	for from, to := range map[string]string{
+		"/ui/coves":                    "/ui/agents",
+		"/ui/actors":                   "/ui/agents",
+		"/ui/coves/s-ticket":           "/ui/agents/s-ticket",
+		"/ui/coves/s-ticket/session?x": "/ui/agents/s-ticket/session?x",
+	} {
+		rec := get(t, h, from)
+		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
+			t.Errorf("GET %s = %d → %q, want 301 → %q", from, rec.Code, rec.Header().Get("Location"), to)
+		}
+	}
+}
+
+// An agent's page shows its identity — grants with their controls and
+// Revoke — beside its studio; an enrolled-only agent has a page too.
+func TestAgentPageIdentity(t *testing.T) {
+	h := adminui.Handler(seedAgents(t), testLogger(), &jam.Supervisor{}, nil, anyCred, nil)
+	body := get(t, h, "/ui/agents/s-ticket").Body.String()
+	for _, want := range []string{
+		`<h1 class="mono">s-ticket</h1>`, `<span class="chip">ticket</span>`,
+		"<h2>Identity</h2>", `href="/ui/projects/acme/roles/dev"`,
+		`hx-delete="/ui/actors/s-ticket/grants/acme/dev"`, `hx-post="/ui/actors/s-ticket/grants"`,
+		`hx-delete="/ui/enrollments/s-ticket"`, `hx-delete="/ui/coves/s-ticket"`,
+		`<a href="/ui/agents">Agents</a> / <a href="/ui/agents/s-ticket">s-ticket</a>`,
+	} {
+		if !strings.Contains(body, want) {
+			t.Errorf("agent page missing %q", want)
+		}
+	}
+	rec := get(t, h, "/ui/agents/e-only")
+	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No studio is running for this agent.") || !strings.Contains(rec.Body.String(), `<span class="chip">enrolled</span>`) {
+		t.Errorf("enrolled-only agent = %d:\n%s", rec.Code, rec.Body.String())
+	}
+	if strings.Contains(rec.Body.String(), `hx-delete="/ui/coves/e-only"`) {
+		t.Error("no studio, no Teardown")
+	}
+	// a studio without an enrolled actor offers no Revoke
+	if body := get(t, h, "/ui/agents/s-manual").Body.String(); strings.Contains(body, `hx-delete="/ui/enrollments/`) || !strings.Contains(body, "Not enrolled") {
+		t.Error("an unenrolled studio should say so and offer no Revoke")
+	}
+}
+
+// Holders on role and project pages link to their agent pages.
+func TestHoldersLinkAgents(t *testing.T) {
+	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
+	if body := get(t, h, "/ui/projects/acme/roles/ops").Body.String(); !strings.Contains(body, `<a href="/ui/agents/e-only"`) {
+		t.Error("role holders should link their agent pages")
+	}
+	if body := get(t, h, "/ui/projects/acme/agents").Body.String(); !strings.Contains(body, `<a class="mono" href="/ui/agents/e-only">e-only</a>`) {
+		t.Error("project identities should link their agent pages")
+	}
+}
+
+// The dashboard's studio table polls the dashboard, which answers an htmx
+// request with just the table.
+func TestDashboardPollsItself(t *testing.T) {
+	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
+	if body := get(t, h, "/ui/").Body.String(); !strings.Contains(body, `id="coves" hx-get="/ui/" hx-trigger="every 3s"`) {
+		t.Errorf("dashboard studio table should poll /ui/")
+	}
+	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
+	req.Header.Set("HX-Request", "true")
+	rec := httptest.NewRecorder()
+	h.ServeHTTP(rec, req)
+	if body := rec.Body.String(); !strings.HasPrefix(strings.TrimSpace(body), `<div class="card" id="coves"`) || strings.Contains(body, "<nav") || !strings.Contains(body, "s-standing") {
+		t.Errorf("poll fragment:\n%s", body)
+	}
+}
diff --git a/internal/jam/adminui/coves_write_test.go b/internal/jam/adminui/coves_write_test.go
index b26d2c0..4748e67 100644
--- a/internal/jam/adminui/coves_write_test.go
+++ b/internal/jam/adminui/coves_write_test.go
@@ -94,7 +94,7 @@ func TestRaiseCoveNoRuntime503(t *testing.T) {
 		t.Fatalf("raise with no runtime = %d, want 503", rec.Code)
 	}
 	// And the Coves page hides the raise form.
-	page := get(t, h, "/ui/coves").Body.String()
+	page := get(t, h, "/ui/agents").Body.String()
 	if strings.Contains(page, `hx-post="/ui/coves"`) {
 		t.Error("read-only Coves page must not render the raise form")
 	}
@@ -148,7 +148,7 @@ func TestCovesControlsRenderWithSupervisor(t *testing.T) {
 	h := adminui.Handler(store, testLogger(), newSup(t, store), nil, anyCred, nil)
 
 	// Raise form is present on the Coves page.
-	page := get(t, h, "/ui/coves").Body.String()
+	page := get(t, h, "/ui/agents").Body.String()
 	if !strings.Contains(page, `hx-post="/ui/coves"`) {
 		t.Error("Coves page with a supervisor must render the raise form")
 	}
@@ -157,7 +157,7 @@ func TestCovesControlsRenderWithSupervisor(t *testing.T) {
 	if rec := covePost(t, h, "/ui/coves", url.Values{"id": {"cove-3"}, "project": {"acme"}, "role": {"worker"}}); rec.Code != http.StatusOK {
 		t.Fatalf("setup raise = %d", rec.Code)
 	}
-	page = get(t, h, "/ui/coves").Body.String()
+	page = get(t, h, "/ui/agents").Body.String()
 	if !strings.Contains(page, `hx-delete="/ui/coves/`) {
 		t.Errorf("Coves page with a live cove must render the Teardown button; got:\n%s", page)
 	}
@@ -198,7 +198,7 @@ func TestCovesConnectorStaleIsFlagged(t *testing.T) {
 	if err := sup.RecordConnector(jam.ResolveSession(store, "cove-s"), "not-the-current-fingerprint"); err != nil {
 		t.Fatal(err)
 	}
-	page := get(t, h, "/ui/coves").Body.String()
+	page := get(t, h, "/ui/agents").Body.String()
 	if !strings.Contains(page, `<span class="pill phase-raising">stale</span>`) {
 		t.Fatalf("stale connector not flagged; page:\n%s", page)
 	}
diff --git a/internal/jam/adminui/image_test.go b/internal/jam/adminui/image_test.go
index b0bea35..66828fb 100644
--- a/internal/jam/adminui/image_test.go
+++ b/internal/jam/adminui/image_test.go
@@ -49,7 +49,7 @@ func TestImageStaleIsFlagged(t *testing.T) {
 	}
 	h := adminui.Handler(store, testLogger(), sup, nil, anyCred, nil)
 
-	page := get(t, h, "/ui/coves").Body.String()
+	page := get(t, h, "/ui/agents").Body.String()
 	if !strings.Contains(page, "<th>Image</th>") || !strings.Contains(page, "<td>ok</td>") {
 		t.Fatalf("fresh image not shown ok; page:\n%s", page)
 	}
@@ -58,7 +58,7 @@ func TestImageStaleIsFlagged(t *testing.T) {
 	}
 
 	asm = "a2" // a Jam-side rebuild: every raised image is now stale
-	page = get(t, h, "/ui/coves").Body.String()
+	page = get(t, h, "/ui/agents").Body.String()
 	if !strings.Contains(page, `title="raised on an older image than its role would run now">stale</span>`) {
 		t.Fatalf("stale image not flagged; page:\n%s", page)
 	}
diff --git a/internal/jam/adminui/intercom_test.go b/internal/jam/adminui/intercom_test.go
index 205f3be..0f54e33 100644
--- a/internal/jam/adminui/intercom_test.go
+++ b/internal/jam/adminui/intercom_test.go
@@ -81,7 +81,7 @@ func TestSquawksNotConfigured(t *testing.T) {
 }
 
 func TestSquawksNavLinkPresentOnOtherPages(t *testing.T) {
-	body := get(t, squawkHandler(t, newIntercomLog(t)), "/ui/coves").Body.String()
+	body := get(t, squawkHandler(t, newIntercomLog(t)), "/ui/agents").Body.String()
 	if !strings.Contains(body, `href="/ui/intercom"`) {
 		t.Errorf("nav should link to /ui/intercom; got:\n%s", body)
 	}
diff --git a/internal/jam/adminui/nav_test.go b/internal/jam/adminui/nav_test.go
index fb0739c..f8fb6a1 100644
--- a/internal/jam/adminui/nav_test.go
+++ b/internal/jam/adminui/nav_test.go
@@ -36,8 +36,8 @@ func TestTopNavSections(t *testing.T) {
 	for path, section := range map[string]string{
 		"/ui/projects/acme/roles/dev": "Projects",
 		"/ui/projects/acme/members":   "Projects",
-		"/ui/coves":                   "Agents",
-		"/ui/actors":                  "Agents",
+		"/ui/agents":                  "Agents",
+		"/ui/agents/studio-acme":      "Agents",
 		"/ui/kits":                    "Specs",
 		"/ui/model-specs":             "Specs",
 		"/ui/users":                   "Users",
@@ -58,14 +58,11 @@ func TestTopNavSections(t *testing.T) {
 // the current page's tab marked; other sections show none.
 func TestSectionSubTabs(t *testing.T) {
 	h := projHandler(seedProjects(t))
-	agents := []string{`href="/ui/coves"`, `href="/ui/actors"`}
 	specs := []string{`href="/ui/kits"`, `href="/ui/destinations"`, `href="/ui/model-specs"`}
 	for path, c := range map[string]struct {
 		tabs    []string
 		current string
 	}{
-		"/ui/coves":        {agents, "Studios"},
-		"/ui/actors":       {agents, "Actors"},
 		"/ui/kits":         {specs, "Kits"},
 		"/ui/destinations": {specs, "Destinations"},
 		"/ui/model-specs":  {specs, "Model-specs"},
diff --git a/internal/jam/adminui/polish_test.go b/internal/jam/adminui/polish_test.go
index bca3ba0..d52d7b0 100644
--- a/internal/jam/adminui/polish_test.go
+++ b/internal/jam/adminui/polish_test.go
@@ -14,8 +14,8 @@ func TestNavMarksCurrentPage(t *testing.T) {
 	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
 	for path, href := range map[string]string{
 		"/ui/":             `href="/ui/"`,
-		"/ui/actors":       `href="/ui/coves"`, // Agents
-		"/ui/kits":         `href="/ui/kits"`,  // Specs
+		"/ui/agents":       `href="/ui/agents"`,
+		"/ui/kits":         `href="/ui/kits"`, // Specs
 		"/ui/destinations": `href="/ui/kits"`,
 		"/ui/users":        `href="/ui/users"`,
 		"/ui/projects":     `href="/ui/projects"`,
@@ -56,14 +56,14 @@ func TestDashboardShowsStatTiles(t *testing.T) {
 func TestPhasePill(t *testing.T) {
 	store := newStore(t)
 	seedCove(t, store)
-	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/coves").Body.String()
+	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/agents").Body.String()
 	if !strings.Contains(body, `class="pill phase-live"`) {
 		t.Errorf("studios table should render the phase as a pill; got:\n%s", body)
 	}
 }
 
-// The roster renders one collapsed add-grant form per actor and grants as
-// removable chips, not a full form row under every grant table.
+// An agent's page renders its grants as removable chips and one collapsed
+// add-grant form.
 func TestRosterPerActorGrantForm(t *testing.T) {
 	store := newStore(t)
 	mustCreateProject(t, store, "acme")
@@ -75,12 +75,12 @@ func TestRosterPerActorGrantForm(t *testing.T) {
 			t.Fatal(err)
 		}
 	}
-	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/actors").Body.String()
-	if n := strings.Count(body, `hx-post="/ui/actors/`); n != 2 {
-		t.Errorf("want one add-grant form per actor (2), got %d", n)
+	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/agents/a-1").Body.String()
+	if n := strings.Count(body, `hx-post="/ui/actors/a-1/grants"`); n != 1 {
+		t.Errorf("want one add-grant form on the agent's page, got %d", n)
 	}
-	if n := strings.Count(body, `<details class="grant-add"`); n != 2 {
-		t.Errorf("add-grant forms should be collapsed <details>, got %d", n)
+	if n := strings.Count(body, `<details class="grant-add"`); n != 1 {
+		t.Errorf("the add-grant form should be a collapsed <details>, got %d", n)
 	}
 	if !strings.Contains(body, `hx-delete="/ui/actors/a-1/grants/acme/worker"`) {
 		t.Errorf("grant chip should carry its remove action")
diff --git a/internal/jam/adminui/projects_test.go b/internal/jam/adminui/projects_test.go
index baebf70..a749b3a 100644
--- a/internal/jam/adminui/projects_test.go
+++ b/internal/jam/adminui/projects_test.go
@@ -68,7 +68,7 @@ func projHandler(store jam.Store) http.Handler {
 
 func TestProjectsTabFollowsDashboard(t *testing.T) {
 	body := get(t, projHandler(seedProjects(t)), "/ui/projects").Body.String()
-	dash, proj, studios := strings.Index(body, `href="/ui/"`), strings.Index(body, `href="/ui/projects"`), strings.Index(body, `href="/ui/coves"`)
+	dash, proj, studios := strings.Index(body, `href="/ui/"`), strings.Index(body, `href="/ui/projects"`), strings.Index(body, `href="/ui/agents"`)
 	if !(dash >= 0 && dash < proj && proj < studios) {
 		t.Errorf("nav order: dashboard@%d projects@%d studios@%d", dash, proj, studios)
 	}
@@ -161,7 +161,7 @@ func TestProjectPage(t *testing.T) {
 		}
 		// the section itself (the tree lists studio-acme on every page)
 		if strings.HasSuffix(path, "/agents") {
-			if sec := body[strings.Index(body, `<div id="project">`):]; !strings.Contains(sec, `href="/ui/coves/studio-acme"`) {
+			if sec := body[strings.Index(body, `<div id="project">`):]; !strings.Contains(sec, `href="/ui/agents/studio-acme"`) {
 				t.Errorf("%s: studios table missing studio-acme", path)
 			}
 		}
@@ -186,16 +186,15 @@ func TestProjectPageNotFound(t *testing.T) {
 // project, so a typo can't land anything.
 func TestProjectPickers(t *testing.T) {
 	h := projHandler(seedProjects(t))
-	for _, path := range []string{"/ui/actors", "/ui/coves"} {
+	for _, path := range []string{"/ui/agents", "/ui/agents/a1"} {
 		body := get(t, h, path).Body.String()
 		if !strings.Contains(body, `<input name="project" data-ta="projects" value="default"`) || strings.Contains(body, `<select name="project"`) {
 			t.Errorf("%s: project should be a type-ahead prefilled with default", path)
 		}
 	}
-	// the roster's per-actor add-grant form, re-rendered after a write, keeps it
-	rec := post(t, h, "/ui/actors/a1/grants", url.Values{"project": {"acme"}, "role": {"ops"}})
-	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="project" data-ta="projects"`) {
-		t.Errorf("roster fragment after add-grant lost the project type-ahead: %d", rec.Code)
+	// the agent page's add-grant form posts the typed project
+	if rec := post(t, h, "/ui/actors/a1/grants", url.Values{"project": {"acme"}, "role": {"ops"}}); rec.Code != http.StatusOK {
+		t.Errorf("add-grant = %d", rec.Code)
 	}
 	// an unknown project is still refused
 	if rec := post(t, h, "/ui/roles", url.Values{"project": {"nope"}, "name": {"r"}}); rec.Code != http.StatusNotFound {
diff --git a/internal/jam/adminui/role_detail_test.go b/internal/jam/adminui/role_detail_test.go
index 2261630..f405719 100644
--- a/internal/jam/adminui/role_detail_test.go
+++ b/internal/jam/adminui/role_detail_test.go
@@ -139,7 +139,7 @@ func TestRoleLinksFromRolesAndRoster(t *testing.T) {
 	if !strings.Contains(roles, "git-pat") {
 		t.Errorf("roles table should show the credential mapping")
 	}
-	roster := get(t, h, "/ui/actors").Body.String()
+	roster := get(t, h, "/ui/agents").Body.String()
 	if !strings.Contains(roster, `href="/ui/projects/acme/roles/review"`) {
 		t.Errorf("roster grant chips should link to the role")
 	}
diff --git a/internal/jam/adminui/search_test.go b/internal/jam/adminui/search_test.go
index 500bbff..aad245f 100644
--- a/internal/jam/adminui/search_test.go
+++ b/internal/jam/adminui/search_test.go
@@ -60,7 +60,7 @@ func TestSearchFindsEveryKind(t *testing.T) {
 	for _, want := range []string{
 		`href="/ui/projects/zephyr-labs"`,           // project by name
 		`href="/ui/projects/acme/roles/zephyr-dev"`, // role by name
-		`href="/ui/coves/studio-7"`,                 // studio by role
+		`href="/ui/agents/studio-7"`,                // studio by role
 		"bot-<mark>zephyr</mark>",                   // actor by id
 		"human:zoe",                                 // human by handle
 		"channel:ops",                               // channel by ref
@@ -94,7 +94,7 @@ func TestSearchLiveFragment(t *testing.T) {
 func TestSearchExactMatchJumps(t *testing.T) {
 	h := searchFixture(t)
 	for q, want := range map[string]string{
-		"studio-7":        "/ui/coves/studio-7",
+		"studio-7":        "/ui/agents/studio-7",
 		"web":             "/ui/kits/web",
 		"acme/zephyr-dev": "/ui/projects/acme/roles/zephyr-dev",
 	} {
diff --git a/internal/jam/adminui/session_test.go b/internal/jam/adminui/session_test.go
index 1787f09..a1c3068 100644
--- a/internal/jam/adminui/session_test.go
+++ b/internal/jam/adminui/session_test.go
@@ -43,15 +43,15 @@ func sse(t *testing.T, h http.Handler, path, lastID string) (stop func() string)
 func TestSessionPageRenders(t *testing.T) {
 	h, _, _, ing := sessionUI(t)
 	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{"type":"system","subtype":"init"}`))
-	rec := get(t, h, "/ui/coves/w1/session")
-	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/ui/coves/w1/session/events?stream="+sessSID) {
+	rec := get(t, h, "/ui/agents/w1/session")
+	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/ui/agents/w1/session/events?stream="+sessSID) {
 		t.Fatalf("%d %s", rec.Code, rec.Body.String())
 	}
 }
 
 func TestSessionPageNotConfigured(t *testing.T) {
 	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
-	if rec := get(t, h, "/ui/coves/w1/session"); !strings.Contains(rec.Body.String(), "not configured") {
+	if rec := get(t, h, "/ui/agents/w1/session"); !strings.Contains(rec.Body.String(), "not configured") {
 		t.Fatalf("%s", rec.Body.String())
 	}
 }
@@ -59,7 +59,7 @@ func TestSessionPageNotConfigured(t *testing.T) {
 func TestSessionSSENoStore(t *testing.T) {
 	h, _, _, _ := sessionUI(t)
 	ctx, cancel := context.WithCancel(context.Background())
-	req := httptest.NewRequest("GET", "/ui/coves/w1/session/events", nil).WithContext(ctx)
+	req := httptest.NewRequest("GET", "/ui/agents/w1/session/events", nil).WithContext(ctx)
 	rec := httptest.NewRecorder()
 	done := make(chan struct{})
 	go func() { h.ServeHTTP(rec, req); close(done) }()
@@ -74,7 +74,7 @@ func TestSessionSSENoStore(t *testing.T) {
 func TestSessionSSEBackfillThenLive(t *testing.T) {
 	h, _, _, ing := sessionUI(t)
 	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`))
-	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, "")
+	stop := sse(t, h, "/ui/agents/w1/session/events?stream="+sessSID, "")
 	time.Sleep(100 * time.Millisecond)
 	ing.Append("w1", sessionevents.Stamp{}, sessIn(2, `{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`))
 	time.Sleep(100 * time.Millisecond)
@@ -92,7 +92,7 @@ func TestSessionSSEResumesFromLastEventID(t *testing.T) {
 	for i := uint64(1); i <= 3; i++ {
 		ing.Append("w1", sessionevents.Stamp{}, sessIn(i, `{"type":"system","subtype":"init"}`))
 	}
-	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, sessSID+":2")
+	stop := sse(t, h, "/ui/agents/w1/session/events?stream="+sessSID, sessSID+":2")
 	time.Sleep(100 * time.Millisecond)
 	body := stop()
 	if strings.Contains(body, "id: "+sessSID+":1\n") || strings.Contains(body, "id: "+sessSID+":2\n") || !strings.Contains(body, "id: "+sessSID+":3\n") {
@@ -104,7 +104,7 @@ func TestSessionSSESubscribeBeforeBackfill(t *testing.T) {
 	// Events appended while the handler is starting must appear exactly once.
 	h, _, _, ing := sessionUI(t)
 	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{}`))
-	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, "")
+	stop := sse(t, h, "/ui/agents/w1/session/events?stream="+sessSID, "")
 	for i := uint64(2); i <= 50; i++ {
 		ing.Append("w1", sessionevents.Stamp{}, sessIn(i, `{}`))
 	}
@@ -121,7 +121,7 @@ func TestSessionSSESubscribeBeforeBackfill(t *testing.T) {
 func TestSessionRendersAgentOutputInert(t *testing.T) {
 	h, _, _, ing := sessionUI(t)
 	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{"type":"user","message":{"content":[{"type":"tool_result","content":"<script>alert(1)</script>","is_error":true}]}}`))
-	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, "")
+	stop := sse(t, h, "/ui/agents/w1/session/events?stream="+sessSID, "")
 	time.Sleep(100 * time.Millisecond)
 	body := stop()
 	if strings.Contains(body, "<script>alert") {
@@ -137,7 +137,7 @@ func TestSessionSSEFramingCRSafe(t *testing.T) {
 	inj := `hi\r\rid: ` + sessSID + `:999\revent: totals\rretry: 1\rdata: pwned\r`
 	ing.Append("w1", sessionevents.Stamp{}, sessIn(1, `{"type":"assistant","message":{"content":[{"type":"text","text":"`+inj+`"}]}}`))
 	ing.Append("w1", sessionevents.Stamp{}, sessIn(2, `{"type":"user","message":{"content":[{"type":"tool_result","content":"`+inj+`"}]}}`))
-	stop := sse(t, h, "/ui/coves/w1/session/events?stream="+sessSID, "")
+	stop := sse(t, h, "/ui/agents/w1/session/events?stream="+sessSID, "")
 	time.Sleep(100 * time.Millisecond)
 	body := stop()
 	// Split exactly as an SSE parser does: CRLF, CR and LF are all terminators.
diff --git a/internal/jam/adminui/studio_page_test.go b/internal/jam/adminui/studio_page_test.go
index a391d77..d384aab 100644
--- a/internal/jam/adminui/studio_page_test.go
+++ b/internal/jam/adminui/studio_page_test.go
@@ -50,7 +50,7 @@ func studioFixture(t *testing.T, sup *jam.Supervisor) http.Handler {
 }
 
 func TestStudioPageShowsRuntime(t *testing.T) {
-	rec := get(t, studioFixture(t, &jam.Supervisor{}), "/ui/coves/sess-1")
+	rec := get(t, studioFixture(t, &jam.Supervisor{}), "/ui/agents/sess-1")
 	if rec.Code != http.StatusOK {
 		t.Fatalf("studio page = %d", rec.Code)
 	}
@@ -75,9 +75,9 @@ func TestStudioPageShowsRuntime(t *testing.T) {
 }
 
 func TestStudioPageSessionAndSquawks(t *testing.T) {
-	body := get(t, studioFixture(t, nil), "/ui/coves/sess-1").Body.String()
+	body := get(t, studioFixture(t, nil), "/ui/agents/sess-1").Body.String()
 	for _, want := range []string{
-		fmt.Sprintf(`href="/ui/coves/sess-1/session?stream=%s"`, sessSID), "2 event(s)",
+		fmt.Sprintf(`href="/ui/agents/sess-1/session?stream=%s"`, sessSID), "2 event(s)",
 		"need a decision", "go ahead",
 		`href="/ui/intercom?participant=sess-1"`,
 	} {
@@ -103,19 +103,20 @@ func TestStudioPageGoneStillShowsAudit(t *testing.T) {
 	l := newIntercomLog(t, intercom.LegacySquawk{From: actor("old-1"), To: []intercom.Target{human("alice")}, Body: "last words", At: time.Now(), Project: "acme"})
 	st := sessionevents.NewMemStore()
 	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, l, adminui.WithSessions(st, sessionevents.NewHub()))
-	rec := get(t, h, "/ui/coves/old-1")
+	rec := get(t, h, "/ui/agents/old-1")
 	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not running") || !strings.Contains(rec.Body.String(), "last words") {
 		t.Fatalf("gone studio = %d: %s", rec.Code, rec.Body.String())
 	}
-	if rec := get(t, h, "/ui/coves/never-was"); rec.Code != http.StatusNotFound {
+	if rec := get(t, h, "/ui/agents/never-was"); rec.Code != http.StatusNotFound {
 		t.Errorf("unknown id = %d, want 404", rec.Code)
 	}
 }
 
 func TestStudiosTableLinksStudioPage(t *testing.T) {
-	body := get(t, studioFixture(t, nil), "/ui/coves").Body.String()
-	if !strings.Contains(body, `href="/ui/coves/sess-1"`) || !strings.Contains(body, `href="/ui/coves/sess-1/session"`) {
-		t.Errorf("studios row should link the studio page and its timeline")
+	// the dashboard's studio table links each studio's agent page and timeline
+	body := get(t, studioFixture(t, nil), "/ui/").Body.String()
+	if !strings.Contains(body, `href="/ui/agents/sess-1"`) || !strings.Contains(body, `href="/ui/agents/sess-1/session"`) {
+		t.Errorf("studios row should link the agent page and its timeline")
 	}
 }
 
@@ -146,7 +147,7 @@ func TestStudioPageChannelLogSquawks(t *testing.T) {
 		t.Fatal(err)
 	}
 	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, adminui.NewSquawkReader(store, ic, lg, nil))
-	if body := get(t, h, "/ui/coves/sess-2").Body.String(); !strings.Contains(body, "a reply on the ticket") || !strings.Contains(body, "COV-1 · ticket") {
+	if body := get(t, h, "/ui/agents/sess-2").Body.String(); !strings.Contains(body, "a reply on the ticket") || !strings.Contains(body, "COV-1 · ticket") {
 		t.Errorf("studio page lacks its ticket's squawk:\n%s", body)
 	}
 }
diff --git a/internal/jam/adminui/studio_text_test.go b/internal/jam/adminui/studio_text_test.go
index a6f23af..93f38f2 100644
--- a/internal/jam/adminui/studio_text_test.go
+++ b/internal/jam/adminui/studio_text_test.go
@@ -14,12 +14,12 @@ func TestUISaysStudioAndJam(t *testing.T) {
 	store := newStore(t)
 	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
 
-	page := get(t, h, "/ui/coves")
+	page := get(t, h, "/ui/agents")
 	if page.Code != http.StatusOK {
 		t.Fatalf("GET /ui/coves = %d", page.Code)
 	}
 	body := page.Body.String()
-	for _, want := range []string{"<title>Jam — Studios</title>", "<h1>Studios</h1>", `<a href="/ui/coves" aria-current="page">Agents</a>`, "No studios."} {
+	for _, want := range []string{"<title>Jam — Agents</title>", "<h1>Agents</h1>", `<a href="/ui/agents" aria-current="page">Agents</a>`, "No agents."} {
 		if !strings.Contains(body, want) {
 			t.Errorf("studios page missing %q; got:\n%s", want, body)
 		}
diff --git a/internal/jam/adminui/typeahead_test.go b/internal/jam/adminui/typeahead_test.go
index f3aa2f4..ebca753 100644
--- a/internal/jam/adminui/typeahead_test.go
+++ b/internal/jam/adminui/typeahead_test.go
@@ -23,11 +23,7 @@ func TestReferenceFieldsAreTypeaheads(t *testing.T) {
 	log := newIntercomLog(t, intercom.LegacySquawk{From: human("alice"), To: []intercom.Target{actor("studio-acme")}, Body: "hi", At: time.Now(), Project: "acme"})
 	h := adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, log)
 	for page, wants := range map[string][]string{
-		"/ui/coves": {
-			`name="project" data-ta="projects"`,
-			`name="role" data-ta="roles" data-ta-project="@form"`,
-		},
-		"/ui/actors": {
+		"/ui/agents": { // the Raise and Enroll forms
 			`name="project" data-ta="projects"`,
 			`name="role" data-ta="roles" data-ta-project="@form"`,
 			`name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials"`,
diff --git a/internal/jam/adminui/users_test.go b/internal/jam/adminui/users_test.go
index c819eae..1e01b2a 100644
--- a/internal/jam/adminui/users_test.go
+++ b/internal/jam/adminui/users_test.go
@@ -160,7 +160,7 @@ func TestProjectMembersSection(t *testing.T) {
 
 func TestActorsPageReplacesRoster(t *testing.T) {
 	h := projHandler(seedProjects(t))
-	if rec := get(t, h, "/ui/actors"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `aria-current="page">Agents`) {
+	if rec := get(t, h, "/ui/agents"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `aria-current="page">Agents`) {
 		t.Fatalf("actors page = %d", rec.Code)
 	}
 	if rec := get(t, h, "/ui/roster"); rec.Code != http.StatusNotFound {
diff --git a/internal/jam/adminui/writes_test.go b/internal/jam/adminui/writes_test.go
index 83b98ec..363f356 100644
--- a/internal/jam/adminui/writes_test.go
+++ b/internal/jam/adminui/writes_test.go
@@ -61,7 +61,7 @@ func TestEnrollCreatesActorAndShowsTokenOnce(t *testing.T) {
 		t.Fatalf("captured token looks too short to be real: %q", token)
 	}
 	// ...but never appears on the roster afterward.
-	roster := get(t, h, "/ui/actors").Body.String()
+	roster := get(t, h, "/ui/agents").Body.String()
 	if !strings.Contains(roster, "spider-1") {
 		t.Error("roster should list the new actor")
 	}
````

- [ ] **Step 2: RED** — `go test ./internal/jam/adminui/` FAILS (≈14 tests: `/ui/agents` 404s, old pages not redirecting, no Identity section, dashboard not polling itself).

- [ ] **Step 3: Apply the code patch** (it renames `studio.go`→`agent.go` and `templates/studio.html`→`templates/agent.html` and deletes `templates/roster.html`)

````diff
diff --git a/internal/jam/adminui/adminui.go b/internal/jam/adminui/adminui.go
index d4a3f08..a035cb2 100644
--- a/internal/jam/adminui/adminui.go
+++ b/internal/jam/adminui/adminui.go
@@ -27,14 +27,13 @@ var files embed.FS
 // highlights; mustParseTab also names the section sub-tab it sits under.
 var pages = map[string]*template.Template{
 	"dashboard":    mustParse(navDashboard, "coves.html", "context_panel.html", "dashboard.html"),
-	"coves":        mustParseTab(navAgents, "/ui/coves", "coves.html"),
-	"roster":       mustParseTab(navAgents, "/ui/actors", "roster.html"),
+	"agents":       mustParse(navAgents, "agents.html"),
 	"users":        mustParse(navUsers, "users.html"),
 	"user":         mustParse(navUsers, "user.html"),
 	"kits":         mustParseTab(navSpecs, "/ui/kits", "kits.html"),
 	"destinations": mustParseTab(navSpecs, "/ui/destinations", "dest_fields.html", "destinations.html"),
 	"intercom":     mustParse(navIntercom, "squawks.html", "intercom.html"),
-	"session":      mustParseTab(navAgents, "/ui/coves", "session.html"),
+	"session":      mustParse(navAgents, "session.html"),
 	"role":         mustParse(navProjects, "coves.html", "context_panel.html", "projtree.html", "role.html"),
 	"destination":  mustParseTab(navSpecs, "/ui/destinations", "dest_fields.html", "destination.html"),
 	"model-specs":  mustParseTab(navSpecs, "/ui/model-specs", "model_spec_fields.html", "model_specs.html"),
@@ -42,7 +41,7 @@ var pages = map[string]*template.Template{
 	"kit":          mustParseTab(navSpecs, "/ui/kits", "kit.html"),
 	"projects":     mustParse(navProjects, "projects.html"),
 	"project":      mustParse(navProjects, "coves.html", "context_panel.html", "squawks.html", "projtree.html", "project.html"),
-	"studio":       mustParseTab(navAgents, "/ui/coves", "studio.html"),
+	"agent":        mustParse(navAgents, "agent.html"),
 	"search":       mustParse(navNone, "search.html"),
 }
 
@@ -89,16 +88,6 @@ func mustParseTab(section navSection, tab string, names ...string) *template.Tem
 	}).ParseFS(files, paths...))
 }
 
-// covesData and rosterData are the payloads of those pages and
-// their swapped tables.
-func covesData(store jam.Store, img jam.ImageResolver, canEdit bool) map[string]any {
-	return map[string]any{"Coves": jam.CoveSummaries(store, img), "CanEdit": canEdit}
-}
-
-func rosterData(store jam.Store) map[string]any {
-	return map[string]any{"Actors": jam.RosterSummaries(store)}
-}
-
 // funcs are the template helpers shared by every page.
 var funcs = template.FuncMap{
 	// ttl renders a role TTL compactly, or "—" when unset.
@@ -109,6 +98,7 @@ var funcs = template.FuncMap{
 		return fmtDur(d)
 	},
 	"roleURL":    roleURL,
+	"agentURL":   agentURL,
 	"navItems":   func() []navItem { return navItems },
 	"hl":         highlight,
 	"stylesheet": func() string { return uiassets.StylesheetHref("/ui/static/") },
@@ -136,7 +126,7 @@ type options struct {
 	poolConfigured bool
 }
 
-// WithSessions enables the live session-event timeline (/ui/coves/{id}/session).
+// WithSessions enables the live session-event timeline (/ui/agents/{id}/session).
 func WithSessions(store sessionevents.Store, hub *sessionevents.Hub) Option {
 	return func(o *options) { o.sessStore, o.sessHub = store, hub }
 }
@@ -164,6 +154,10 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 	mux.Handle("GET /ui/static/", uiassets.Handler("/ui/static/"))
 
 	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
+		if r.Header.Get("HX-Request") == "true" { // the studio table's poll
+			renderFragment(w, "dashboard", "coves-table", map[string]any{"Coves": jam.CoveSummaries(store, sup)})
+			return
+		}
 		render(w, "dashboard", map[string]any{
 			"Title":      "Dashboard",
 			"Coves":      jam.CoveSummaries(store, sup),
@@ -172,21 +166,7 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 		})
 	})
 
-	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) {
-		data := covesData(store, sup, canEdit)
-		data["Title"] = "Studios"
-		if r.Header.Get("HX-Request") == "true" {
-			renderFragment(w, "coves", "coves-table", data)
-			return
-		}
-		render(w, "coves", data)
-	})
-
-	mux.HandleFunc("GET /ui/actors", func(w http.ResponseWriter, r *http.Request) {
-		data := rosterData(store)
-		data["Title"] = "Actors"
-		render(w, "roster", data)
-	})
+	registerAgents(mux, store, sup, canEdit)
 	// The global roles list and role pages moved into the project tree.
 	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
 		redirect(w, r, "/ui/projects")
@@ -201,7 +181,7 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 		handleIntercom(w, r, msgs)
 	})
 	registerSession(mux, o.sessStore, o.sessHub)
-	registerStudio(mux, store, msgs, o.sessStore, canEdit)
+	registerAgent(mux, store, msgs, o.sessStore, canEdit)
 	registerSearch(mux, store, msgs)
 	registerSuggest(mux, store, o.credNames)
 
diff --git a/internal/jam/adminui/studio.go b/internal/jam/adminui/agent.go
similarity index 60%
rename from internal/jam/adminui/studio.go
rename to internal/jam/adminui/agent.go
index e66d57d..c6560c6 100644
--- a/internal/jam/adminui/studio.go
+++ b/internal/jam/adminui/agent.go
@@ -10,22 +10,27 @@ import (
 	"github.com/aethons-tools/cove/internal/jam/sessionevents"
 )
 
-// studioSquawkLimit caps how many squawks the studio page shows; the Intercom
+// studioSquawkLimit caps how many squawks the agent page shows; the Intercom
 // page (linked, pre-filtered) has the rest.
 const studioSquawkLimit = 50
 
-// studioDetail is the studio page payload. Inst is the registry record while
-// the studio runs; a torn-down studio is gone from the registry, so Running is
-// false and only its audit trail (session streams, squawks) is shown.
-type studioDetail struct {
+// agentDetail is the agent page payload: the identity (an enrolled actor and
+// its grants), its studio while one runs, and its session's audit trail. Inst
+// is the registry record while the studio runs; a torn-down studio is gone
+// from the registry, so Running is false and only its audit trail (session
+// streams, squawks) is shown.
+type agentDetail struct {
 	Title    string
 	ID       string
 	Running  bool
 	Inst     jam.Instance
 	CanEdit  bool
-	Kind     string // ephemeral | personal | standing
+	Kind     string // standing | personal | ticket | manual | enrolled (agentKind)
 	EgressID string // short egress fingerprint
 
+	Actor    jam.ActorSummary // the enrolled identity, when HasActor
+	HasActor bool
+
 	SessionsEnabled bool
 	Streams         []sessionevents.StreamInfo
 
@@ -74,41 +79,43 @@ func studioSquawks(store jam.Store, msgs SquawkReader, id string) ([]squawkRow,
 	return rows, more
 }
 
-// buildStudioDetail gathers a studio's page; false when nothing at all is
-// known about id (not running, no session streams, no squawks).
-func buildStudioDetail(store jam.Store, msgs SquawkReader, sess sessionevents.Store, id string, canEdit bool) (studioDetail, bool) {
+// buildAgentDetail gathers an agent's page; false when nothing at all is
+// known about id (no actor, not running, no session streams, no squawks).
+func buildAgentDetail(store jam.Store, msgs SquawkReader, sess sessionevents.Store, id string, canEdit bool) (agentDetail, bool) {
 	participant := id // the session: its squawks in the channel log, and as actor:<id> in the legacy log
-	d := studioDetail{
-		Title: "Studios", ID: id, CanEdit: canEdit,
+	d := agentDetail{
+		Title: id, ID: id, CanEdit: canEdit,
 		SessionsEnabled: sess != nil, SquawksConfigured: msgs != nil,
 		IntercomURL: "/ui/intercom?participant=" + url.QueryEscape(participant),
 	}
 	d.Inst, d.Running = store.GetInstance(id)
 	d.Inst.Project = jam.ProjectName(store, d.Inst.Project) // the page names it
+	d.Kind = agentKind(d.Inst, d.Running)
 	if d.Running {
-		d.Kind = d.Inst.SessionKind
-		if d.Kind == "" {
-			d.Kind = "ephemeral"
-		}
 		d.EgressID = shortDigest(d.Inst.Egress)
 	}
+	for _, a := range jam.RosterSummaries(store) {
+		if a.ID == id {
+			d.Actor, d.HasActor = a, true
+		}
+	}
 	if sess != nil {
 		d.Streams, _ = sess.Streams(id)
 	}
 	if msgs != nil {
 		d.Squawks, d.SquawksMore = studioSquawks(store, msgs, id)
 	}
-	return d, d.Running || len(d.Streams) > 0 || len(d.Squawks) > 0
+	return d, d.HasActor || d.Running || len(d.Streams) > 0 || len(d.Squawks) > 0
 }
 
-func registerStudio(mux *http.ServeMux, store jam.Store, msgs SquawkReader, sess sessionevents.Store, canEdit bool) {
-	mux.HandleFunc("GET /ui/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
-		d, ok := buildStudioDetail(store, msgs, sess, r.PathValue("id"), canEdit)
+func registerAgent(mux *http.ServeMux, store jam.Store, msgs SquawkReader, sess sessionevents.Store, canEdit bool) {
+	mux.HandleFunc("GET /ui/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
+		d, ok := buildAgentDetail(store, msgs, sess, r.PathValue("id"), canEdit)
 		if !ok {
 			d.NotFound = true
-			renderStatus(w, http.StatusNotFound, "studio", d)
+			renderStatus(w, http.StatusNotFound, "agent", d)
 			return
 		}
-		render(w, "studio", d)
+		render(w, "agent", d)
 	})
 }
diff --git a/internal/jam/adminui/agents.go b/internal/jam/adminui/agents.go
new file mode 100644
index 0000000..705924f
--- /dev/null
+++ b/internal/jam/adminui/agents.go
@@ -0,0 +1,135 @@
+package adminui
+
+import (
+	"net/http"
+	"net/url"
+	"slices"
+	"time"
+
+	"github.com/aethons-tools/cove/internal/jam"
+)
+
+// agentURL is an agent's page.
+func agentURL(id string) string { return "/ui/agents/" + url.PathEscape(id) }
+
+// agentKind is how an agent came to be: a standing or personal session, a
+// ticket's session (an ephemeral session with a unit), a manual raise, or —
+// with no studio — an identity enrolled by hand.
+func agentKind(inst jam.Instance, running bool) string {
+	if !running {
+		return "enrolled"
+	}
+	switch inst.SessionKind {
+	case "standing", "personal":
+		return inst.SessionKind
+	}
+	if inst.Unit != "" {
+		return "ticket"
+	}
+	return "manual"
+}
+
+// agentPhases are the list's filters, matching the dashboard's studio tiles.
+var agentPhases = map[string][]jam.Phase{
+	"live":      {jam.PhaseLive},
+	"raising":   {jam.PhaseRaising},
+	"idled":     {jam.PhaseIdled},
+	"attention": {jam.PhaseLost, jam.PhaseTerminating},
+}
+
+// agentFilters is the filter strip, in order.
+var agentFilters = []struct{ Key, Label string }{
+	{"", "All"}, {"live", "Live"}, {"raising", "Raising"}, {"idled", "Idled"}, {"attention", "Lost / terminating"},
+}
+
+// agentRow is one row of the agents list: an actor, its studio, or both.
+type agentRow struct {
+	ID, Name, Kind  string
+	Project, Role   string
+	Unit            string
+	Phase, Activity string
+	Connector       string
+	Image           string
+	LastSeen        time.Time
+	Grants          []jam.GrantSummary
+	HasStudio       bool
+}
+
+// agentRows lists every agent — each enrolled actor and each studio, merged by
+// id, sorted by id — keeping only those in phase's filter when one is given.
+func agentRows(store jam.Store, img jam.ImageResolver, phase string) []agentRow {
+	byID := map[string]*agentRow{}
+	for _, a := range jam.RosterSummaries(store) {
+		byID[a.ID] = &agentRow{ID: a.ID, Kind: "enrolled", Grants: a.Grants}
+		if len(a.Grants) > 0 {
+			byID[a.ID].Project, byID[a.ID].Role = a.Grants[0].Project, a.Grants[0].Role
+		}
+	}
+	for _, c := range jam.CoveSummaries(store, img) {
+		row := byID[c.ID]
+		if row == nil {
+			row = &agentRow{ID: c.ID}
+			byID[c.ID] = row
+		}
+		inst, _ := store.GetInstance(c.ID)
+		row.Name, row.Kind, row.HasStudio = c.Name, agentKind(inst, true), true
+		row.Project, row.Role, row.Unit = c.Project, c.Role, c.Unit
+		row.Phase, row.Activity, row.Image, row.LastSeen = c.Phase, c.Activity, c.Image, c.LastSeen
+		row.Connector = c.Connector
+	}
+	want := agentPhases[phase]
+	out := make([]agentRow, 0, len(byID))
+	for _, r := range byID {
+		if want != nil && !slices.Contains(want, jam.Phase(r.Phase)) {
+			continue
+		}
+		out = append(out, *r)
+	}
+	slices.SortFunc(out, func(a, b agentRow) int {
+		switch {
+		case a.ID < b.ID:
+			return -1
+		case a.ID > b.ID:
+			return 1
+		}
+		return 0
+	})
+	return out
+}
+
+// agentsData is the agents page and its polled table.
+type agentsData struct {
+	Title   string
+	Phase   string // the active filter ("" = all)
+	Filters []struct{ Key, Label string }
+	Agents  []agentRow
+	CanEdit bool // a runtime supervisor: raise and teardown
+}
+
+func newAgentsData(store jam.Store, img jam.ImageResolver, phase string, canEdit bool) agentsData {
+	if _, ok := agentPhases[phase]; !ok {
+		phase = ""
+	}
+	return agentsData{Title: "Agents", Phase: phase, Filters: agentFilters, Agents: agentRows(store, img, phase), CanEdit: canEdit}
+}
+
+func registerAgents(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, canEdit bool) {
+	mux.HandleFunc("GET /ui/agents", func(w http.ResponseWriter, r *http.Request) {
+		data := newAgentsData(store, img, r.URL.Query().Get("phase"), canEdit)
+		if r.Header.Get("HX-Request") == "true" {
+			renderFragment(w, "agents", "agents-table", data)
+			return
+		}
+		render(w, "agents", data)
+	})
+	// The studio and actor lists and the studio page became the agents list
+	// and the agent page.
+	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/ui/agents") })
+	mux.HandleFunc("GET /ui/actors", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/ui/agents") })
+	mux.HandleFunc("GET /ui/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
+		redirect(w, r, agentURL(r.PathValue("id")))
+	})
+	mux.HandleFunc("GET /ui/coves/{id}/session", func(w http.ResponseWriter, r *http.Request) {
+		redirect(w, r, agentURL(r.PathValue("id"))+"/session")
+	})
+}
diff --git a/internal/jam/adminui/nav.go b/internal/jam/adminui/nav.go
index e57a12c..d5f5dff 100644
--- a/internal/jam/adminui/nav.go
+++ b/internal/jam/adminui/nav.go
@@ -24,13 +24,13 @@ type navItem struct {
 	Href    string
 }
 
-// navItems is the top nav, in order. Agents and Specs land on today's studio
-// and kit lists until their own pages exist.
+// navItems is the top nav, in order. Specs lands on the kit list until its
+// own page exists.
 var navItems = []navItem{
 	{navDashboard, "Dashboard", "/ui/"},
 	{navProjects, "Projects", "/ui/projects"},
 	{navUsers, "Users", "/ui/users"},
-	{navAgents, "Agents", "/ui/coves"},
+	{navAgents, "Agents", "/ui/agents"},
 	{navSpecs, "Specs", "/ui/kits"},
 	{navIntercom, "Intercom", "/ui/intercom"},
 }
@@ -45,8 +45,7 @@ type subTab struct {
 // shown under the top bar on each of the section's pages. Each tab's Href is
 // also its key: a page names the tab it belongs to (mustParseTab).
 var navSubTabs = map[navSection][]subTab{
-	navAgents: {{Label: "Studios", Href: "/ui/coves"}, {Label: "Actors", Href: "/ui/actors"}},
-	navSpecs:  {{Label: "Kits", Href: "/ui/kits"}, {Label: "Destinations", Href: "/ui/destinations"}, {Label: "Model-specs", Href: "/ui/model-specs"}},
+	navSpecs: {{Label: "Kits", Href: "/ui/kits"}, {Label: "Destinations", Href: "/ui/destinations"}, {Label: "Model-specs", Href: "/ui/model-specs"}},
 }
 
 // subTabsFor is section's sub-tabs with tab (an Href) marked current; none for
diff --git a/internal/jam/adminui/projtree.go b/internal/jam/adminui/projtree.go
index 1f59b7d..a3e5c11 100644
--- a/internal/jam/adminui/projtree.go
+++ b/internal/jam/adminui/projtree.go
@@ -91,7 +91,7 @@ func buildProjectTree(store jam.Store, img jam.ImageResolver, project string, se
 				}
 				if ph := jam.Phase(c.Phase); ph == jam.PhaseLive || ph == jam.PhaseRaising {
 					live++
-					n.Children = append(n.Children, treeLeaf{Label: c.ID, Href: "/ui/coves/" + url.PathEscape(c.ID), Phase: c.Phase})
+					n.Children = append(n.Children, treeLeaf{Label: c.ID, Href: agentURL(c.ID), Phase: c.Phase})
 				}
 			}
 			if live > 0 {
diff --git a/internal/jam/adminui/search.go b/internal/jam/adminui/search.go
index b7693a9..d2987a8 100644
--- a/internal/jam/adminui/search.go
+++ b/internal/jam/adminui/search.go
@@ -145,7 +145,7 @@ func search(store jam.Store, msgs SquawkReader, q string) searchData {
 			if i.Owner != "" {
 				sub += " · owner " + i.Owner
 			}
-			studios.add(searchHit{Title: i.ActorID, Sub: sub, URL: "/ui/coves/" + url.PathEscape(i.ActorID), Exact: m.exact(i.ActorID)})
+			studios.add(searchHit{Title: i.ActorID, Sub: sub, URL: agentURL(i.ActorID), Exact: m.exact(i.ActorID)})
 		}
 	}
 
@@ -156,7 +156,7 @@ func search(store jam.Store, msgs SquawkReader, q string) searchData {
 			grants[i] = jam.ProjectName(store, g.Project) + "/" + g.Role
 		}
 		if m.any(append([]string{a.ID}, grants...)...) {
-			actors.add(searchHit{Title: a.ID, Sub: "grants: " + strings.Join(grants, ", "), URL: "/ui/actors", Exact: m.exact(a.ID)})
+			actors.add(searchHit{Title: a.ID, Sub: "grants: " + strings.Join(grants, ", "), URL: agentURL(a.ID), Exact: m.exact(a.ID)})
 		}
 	}
 
diff --git a/internal/jam/adminui/session.go b/internal/jam/adminui/session.go
index 827bfcd..2921d9b 100644
--- a/internal/jam/adminui/session.go
+++ b/internal/jam/adminui/session.go
@@ -19,8 +19,8 @@ const (
 )
 
 func registerSession(mux *http.ServeMux, store sessionevents.Store, hub *sessionevents.Hub) {
-	mux.HandleFunc("GET /ui/coves/{id}/session", func(w http.ResponseWriter, r *http.Request) {
-		data := map[string]any{"Title": "Studios", "ActorID": r.PathValue("id"), "Enabled": store != nil}
+	mux.HandleFunc("GET /ui/agents/{id}/session", func(w http.ResponseWriter, r *http.Request) {
+		data := map[string]any{"Title": r.PathValue("id") + " session", "ActorID": r.PathValue("id"), "Enabled": store != nil}
 		if store != nil {
 			streams, _ := store.Streams(r.PathValue("id"))
 			selected := r.URL.Query().Get("stream")
@@ -31,13 +31,16 @@ func registerSession(mux *http.ServeMux, store sessionevents.Store, hub *session
 		}
 		render(w, "session", data)
 	})
-	mux.HandleFunc("GET /ui/coves/{id}/session/events", func(w http.ResponseWriter, r *http.Request) {
+	events := func(w http.ResponseWriter, r *http.Request) {
 		if store == nil || hub == nil {
 			w.WriteHeader(http.StatusNoContent)
 			return
 		}
 		serveSessionEvents(w, r, store, hub)
-	})
+	}
+	mux.HandleFunc("GET /ui/agents/{id}/session/events", events)
+	// The pre-agents path, for timelines already open in a browser.
+	mux.HandleFunc("GET /ui/coves/{id}/session/events", events)
 }
 
 type eventView struct {
diff --git a/internal/jam/adminui/templates/studio.html b/internal/jam/adminui/templates/agent.html
similarity index 66%
rename from internal/jam/adminui/templates/studio.html
rename to internal/jam/adminui/templates/agent.html
index afe1255..f406c5a 100644
--- a/internal/jam/adminui/templates/studio.html
+++ b/internal/jam/adminui/templates/agent.html
@@ -1,7 +1,7 @@
 {{define "content"}}
 {{if .NotFound}}
-<div class="page-head"><h1>Studio not found</h1></div>
-<div class="banner">Nothing is known about studio <span class="mono">{{.ID}}</span> — it isn't running and has no session or squawks. <a href="/ui/coves">Back to studios</a></div>
+<div class="page-head"><h1>Agent not found</h1></div>
+<div class="banner">Nothing is known about agent <span class="mono">{{.ID}}</span> — it isn't enrolled, isn't running and has no session or squawks. <a href="/ui/agents">Back to agents</a></div>
 {{else}}
 <style>
   .crumbs{font-size:12.5px;color:var(--faint);margin-bottom:4px}
@@ -30,31 +30,66 @@
   td.body.md pre{overflow-x:auto;background:var(--bg);padding:6px 8px;border-radius:6px}
   @media (max-width:720px){.sections{grid-template-columns:1fr}}
 </style>
-<div class="crumbs"><a href="/ui/coves">Studios</a></div>
+<div class="crumbs"><a href="/ui/agents">Agents</a> / <a href="{{agentURL .ID}}">{{.ID}}</a></div>
 <div class="page-head">
   <h1 class="mono">{{.ID}}</h1>
   {{if .Running}}
   {{template "phase" .Inst.Phase}}
   <div class="facts">
     {{if .Inst.Activity}}<span>{{.Inst.Activity}}</span>{{end}}
-    <span class="chip">{{.Kind}}{{if .Inst.Owner}} · {{.Inst.Owner}}{{end}}{{if .Inst.Name}} · {{.Inst.Name}}{{end}}</span>
+    <span class="chip">{{$.Kind}}{{if .Inst.Owner}} · {{.Inst.Owner}}{{end}}{{if .Inst.Name}} · {{.Inst.Name}}{{end}}</span>
     <a class="chip" href="{{projectURL .Inst.Project}}">{{.Inst.Project}}</a>
     <a class="chip" href="{{roleURL .Inst.Project .Inst.Role}}">{{.Inst.Role}}</a>
     {{if .Inst.Unit}}<span class="mono">{{.Inst.Unit}}</span>{{end}}
   </div>
-  {{end}}
+  {{else}}<span class="chip">{{.Kind}}</span>{{end}}
   <span class="spacer"></span>
-  {{if .SessionsEnabled}}<a class="btn" href="/ui/coves/{{.ID}}/session">Open live timeline</a>{{end}}
+  {{if .SessionsEnabled}}<a class="btn" href="{{agentURL .ID}}/session">Open live timeline</a>{{end}}
   {{if and .Running .CanEdit}}<button class="danger" hx-delete="/ui/coves/{{.ID}}" hx-swap="none"
       hx-confirm="Tear down {{.ID}}?"
       hx-on::after-request="if(event.detail.successful) location.reload()">Teardown</button>{{end}}
+  {{if .HasActor}}<button class="danger" hx-delete="/ui/enrollments/{{.ID}}" hx-swap="none"
+      hx-confirm="Revoke {{.ID}}? This invalidates its token."
+      hx-on::after-request="if(event.detail.successful) location.href='/ui/agents'">Revoke</button>{{end}}
 </div>
 
 {{if not .Running}}
-<div class="banner">This studio is not running — it was torn down and left the registry. Its session and squawks remain below as an audit trail.</div>
+<div class="banner">{{if .HasActor}}No studio is running for this agent.{{else}}This agent's studio is not running — it was torn down and left the registry.{{end}} Its session and squawks, if any, remain below as an audit trail.</div>
 {{end}}
 
 <div class="sections">
+  <section class="card full">
+    <header><h2>Identity</h2><span class="sub">the grants this agent's token carries</span></header>
+    <div class="body">
+    {{if .HasActor}}{{$id := .ID}}
+      <div>
+      {{range .Actor.Grants}}
+        <span class="chip" title="{{if .Destinations}}destinations: {{range $i, $d := .Destinations}}{{if $i}}, {{end}}{{$d}}{{end}}{{else}}no destinations{{end}}"><a href="{{roleURL .Project .Role}}" style="color:inherit">{{.Project}}/{{.Role}}</a>
+          <button type="button" aria-label="Remove grant" hx-delete="/ui/actors/{{$id}}/grants/{{.Project}}/{{.Role}}" hx-swap="none"
+            hx-confirm="Remove {{.Role}} grant on {{.Project}} from {{$id}}?" hx-on::after-request="if(event.detail.successful) location.reload()">×</button></span>
+      {{else}}<span class="unset">no grants</span>{{end}}
+      </div>
+      <dl class="kv" style="margin-top:10px"><dt>Token expiry</dt><dd>{{if .Actor.Expiry.IsZero}}<span class="unset">none</span>{{else}}<time title="{{.Actor.Expiry.Format "2006-01-02 15:04:05 MST"}}">{{.Actor.Expiry.Format "2006-01-02"}}</time>{{end}}</dd></dl>
+      <details class="grant-add">
+        <summary>+ Grant</summary>
+        <form hx-post="/ui/actors/{{$id}}/grants" hx-swap="none" hx-on::after-request="if(event.detail.successful) location.reload()">
+          <input name="role" data-ta="roles" data-ta-project="@form" placeholder="role" required autocomplete="off" aria-label="Role">
+          {{template "project-select"}}
+          <input name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials" placeholder="overrides, e.g. git=git-pat" autocomplete="off" spellcheck="false" aria-label="Destination overrides">
+          <button type="submit" class="primary small">Add</button>
+        </form>
+      </details>
+<style>
+  details.grant-add{display:inline-block;vertical-align:middle}
+  details.grant-add>summary{display:inline-block;cursor:pointer;list-style:none;font-size:12px;font-weight:600;color:var(--accent);padding:2px 6px;border-radius:6px}
+  details.grant-add>summary::-webkit-details-marker{display:none}
+  details.grant-add[open]{display:block;margin-top:8px}
+  details.grant-add form{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px}
+  details.grant-add input,details.grant-add select{width:auto;flex:1 1 140px;padding:5px 8px}
+</style>
+    {{else}}<span class="unset">Not enrolled — no identity token or grants (its studio's token was released, or it was revoked).</span>{{end}}
+    </div>
+  </section>
   {{if .Running}}{{with .Inst}}
   <section class="card">
     <header><h2>Runtime</h2></header>
@@ -99,7 +134,7 @@
       <thead><tr><th>Stream</th><th>Started</th><th>Last event</th><th>Events</th></tr></thead>
       <tbody>
         {{range .Streams}}<tr>
-          <td><a class="mono" href="/ui/coves/{{$.ID}}/session?stream={{.StreamID}}">{{slice .StreamID 0 12}}</a></td>
+          <td><a class="mono" href="{{agentURL $.ID}}/session?stream={{.StreamID}}">{{slice .StreamID 0 12}}</a></td>
           <td><time title="{{.FirstAt.Format "2006-01-02 15:04:05 MST"}}">{{.FirstAt.Format "Jan 2 15:04"}}</time></td>
           <td><time title="{{.LastAt.Format "2006-01-02 15:04:05 MST"}}">{{.LastAt.Format "Jan 2 15:04:05"}}</time></td>
           <td>{{.Events}} event(s)</td></tr>
diff --git a/internal/jam/adminui/templates/agents.html b/internal/jam/adminui/templates/agents.html
new file mode 100644
index 0000000..b381d0e
--- /dev/null
+++ b/internal/jam/adminui/templates/agents.html
@@ -0,0 +1,78 @@
+{{define "content"}}
+<div class="page-head"><h1>Agents</h1><span class="sub">every agent identity and its studio — refreshes every 3s</span></div>
+{{if .CanEdit}}
+<details class="panel">
+  <summary>Raise agent</summary>
+  <form hx-post="/ui/coves" hx-target="#agents" hx-swap="outerHTML" data-reset>
+    <div class="grid">
+      <label>Label <span class="req">required</span><span class="hint">its name; the agent gets a session id</span><input name="id" required autocomplete="off"></label>
+      <label>Role <span class="req">required</span><input name="role" data-ta="roles" data-ta-project="@form" required autocomplete="off"></label>
+      <label>Project <span class="hint">or <a href="/ui/projects">create one</a></span>{{template "project-select"}}</label>
+      <label>Unit <span class="hint">e.g. a ticket id</span><input name="unit"></label>
+      <label class="wide">Workload prompt <span class="hint">optional</span><textarea name="prompt"></textarea></label>
+    </div>
+    <div class="form-actions"><button type="submit" class="primary">Raise</button></div>
+  </form>
+</details>
+{{end}}
+<details class="panel">
+  <summary>Enroll agent</summary>
+  <form hx-post="/ui/enrollments" hx-target="#agents-panel" hx-swap="innerHTML" data-reset>
+    <div class="grid">
+      <label>Agent id <span class="req">required</span><input name="id" required autocomplete="off"></label>
+      <label>Role <span class="req">required</span><input name="role" data-ta="roles" data-ta-project="@form" required autocomplete="off"></label>
+      <label>Project <span class="hint">or <a href="/ui/projects">create one</a></span>{{template "project-select"}}</label>
+      <label>Destination overrides <span class="hint">optional, e.g. git=git-pat</span><input name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials" autocomplete="off" spellcheck="false"></label>
+    </div>
+    <div class="form-actions"><button type="submit" class="primary">Enroll</button></div>
+  </form>
+</details>
+<div id="agents-panel"></div>
+<div class="filters">{{range .Filters}}<a class="tab{{if eq .Key $.Phase}} sel{{end}}" href="/ui/agents{{if .Key}}?phase={{.Key}}{{end}}">{{.Label}}</a>{{end}}</div>
+<style>
+  .filters{margin:0 0 12px;display:flex;gap:6px;flex-wrap:wrap}
+  .filters .tab{padding:4px 10px;border-radius:999px;color:var(--muted);text-decoration:none;font-size:12.5px}
+  .filters .tab.sel{background:var(--accent-soft);color:var(--accent);font-weight:600}
+</style>
+{{template "agents-table" .}}
+{{end}}
+
+{{/* agents-table is the polled list: one row per agent (an actor, its studio,
+     or both), keeping the page's phase filter across polls. */}}
+{{define "agents-table"}}
+<div class="card" id="agents" hx-get="/ui/agents{{if .Phase}}?phase={{.Phase}}{{end}}" hx-trigger="every 3s" hx-swap="outerHTML">
+<table>
+  <thead><tr><th>Agent</th><th>Kind</th><th>Project</th><th>Role</th><th>Phase</th><th>Activity</th><th>Connector</th><th>Image</th><th>Last seen</th>{{if .CanEdit}}<th></th>{{end}}</tr></thead>
+  <tbody>
+    {{range .Agents}}
+    <tr data-id="{{.ID}}">
+      <td><a class="mono" href="{{agentURL .ID}}"><b>{{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</b></a>{{if .Name}} <span class="sub mono">{{.ID}}</span>{{end}}{{if .Unit}} <span class="sub mono">{{.Unit}}</span>{{end}}</td>
+      <td><span class="chip">{{.Kind}}</span></td>
+      <td>{{if .Project}}<a href="{{projectURL .Project}}">{{.Project}}</a>{{else}}<span class="none">—</span>{{end}}</td>
+      <td>{{if .Role}}<a href="{{roleURL .Project .Role}}">{{.Role}}</a>{{if gt (len .Grants) 1}} <span class="sub">+{{len (slice .Grants 1)}}</span>{{end}}{{else}}<span class="none">—</span>{{end}}</td>
+      <td>{{if .HasStudio}}{{template "phase" .Phase}}{{else}}<span class="none">no studio</span>{{end}}</td>
+      <td>{{if .Activity}}{{.Activity}}{{else}}<span class="none">—</span>{{end}}</td>
+      <td>{{if eq .Connector "stale"}}<span class="pill phase-raising">stale</span>{{else if eq .Connector "error"}}<span class="pill phase-lost">error</span>{{else if eq .Connector "unknown" ""}}<span class="none">{{or .Connector "—"}}</span>{{else}}{{.Connector}}{{end}}</td>
+      <td>{{if eq .Image "stale"}}<span class="pill phase-raising" title="raised on an older image than its role would run now">stale</span>{{else if eq .Image "ok"}}ok{{else}}<span class="none">{{or .Image "—"}}</span>{{end}}</td>
+      <td>{{if .LastSeen.IsZero}}<span class="none">—</span>{{else}}<time title="{{.LastSeen.Format "2006-01-02 15:04:05 MST"}}">{{.LastSeen.Format "Jan 2 15:04:05"}}</time>{{end}}</td>
+      {{if $.CanEdit}}<td class="actions">{{if .HasStudio}}<button class="danger small" hx-delete="/ui/coves/{{.ID}}" hx-swap="none"
+          hx-confirm="Tear down {{.ID}}?" hx-on::after-request="if(event.detail.successful) htmx.ajax('GET', location.pathname + location.search, {target: '#agents', swap: 'outerHTML'})">Teardown</button>{{end}}</td>{{end}}
+    </tr>
+    {{else}}
+    <tr><td colspan="{{if .CanEdit}}10{{else}}9{{end}}" class="empty">{{if .Phase}}No agents in this phase.{{else}}No agents.{{end}}</td></tr>
+    {{end}}
+  </tbody>
+</table>
+</div>
+{{end}}
+
+{{define "enroll-result"}}
+<div class="banner token-panel">
+  <div style="flex:1;min-width:0">
+    <p style="margin:0 0 6px"><strong>Enrolled {{.ID}}.</strong> Copy this identity token now — it is not shown again.</p>
+    <div id="enroll-token" class="mono" style="word-break:break-all;padding:8px 10px;background:var(--bg);border:1px solid var(--border);border-radius:8px"><code>{{.Token}}</code></div>
+  </div>
+  <button type="button" class="primary small" data-copy="#enroll-token">Copy</button>
+</div>
+<div hx-get="/ui/agents" hx-trigger="load" hx-target="#agents" hx-swap="outerHTML"></div>
+{{end}}
diff --git a/internal/jam/adminui/templates/coves.html b/internal/jam/adminui/templates/coves.html
index ead3196..da1c527 100644
--- a/internal/jam/adminui/templates/coves.html
+++ b/internal/jam/adminui/templates/coves.html
@@ -1,25 +1,10 @@
-{{define "content"}}
-<div class="page-head"><h1>Studios</h1><span class="sub">Refreshes every 3s</span></div>
-{{if .CanEdit}}
-<details class="panel">
-  <summary>Raise studio</summary>
-  <form hx-post="/ui/coves" hx-target="#coves" hx-swap="outerHTML" data-reset>
-    <div class="grid">
-      <label>Studio id <span class="req">required</span><input name="id" required autocomplete="off"></label>
-      <label>Role <span class="req">required</span><input name="role" data-ta="roles" data-ta-project="@form" required autocomplete="off"></label>
-      <label>Project <span class="hint">or <a href="/ui/projects">create one</a></span>{{template "project-select"}}</label>
-      <label>Unit <span class="hint">e.g. a ticket id</span><input name="unit"></label>
-      <label class="wide">Workload prompt <span class="hint">optional</span><textarea name="prompt"></textarea></label>
-    </div>
-    <div class="form-actions"><button type="submit" class="primary">Raise studio</button></div>
-  </form>
-</details>
-{{end}}
-{{template "coves-table" .}}
-{{end}}
+{{/* coves.html holds the shared studio table (coves-tbl) the dashboard,
+     project and role pages embed; the agents list is agents.html. */}}
 
+{{/* coves-table is the dashboard's polled studio table: it re-fetches the
+     dashboard, which answers an htmx request with just this fragment. */}}
 {{define "coves-table"}}
-<div class="card" id="coves" hx-get="/ui/coves" hx-trigger="every 3s" hx-swap="outerHTML">
+<div class="card" id="coves" hx-get="/ui/" hx-trigger="every 3s" hx-swap="outerHTML">
 {{template "coves-tbl" .}}
 </div>
 {{end}}
@@ -33,7 +18,7 @@
   <tbody>
     {{range .Coves}}
     <tr data-id="{{.ID}}">
-      <td><a class="mono" href="/ui/coves/{{.ID}}"><b>{{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</b></a>{{if .Name}} <span class="sub mono">{{.ID}}</span>{{end}} <a class="sub" href="/ui/coves/{{.ID}}/session" title="Live session timeline">timeline</a></td>
+      <td><a class="mono" href="{{agentURL .ID}}"><b>{{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</b></a>{{if .Name}} <span class="sub mono">{{.ID}}</span>{{end}} <a class="sub" href="{{agentURL .ID}}/session" title="Live session timeline">timeline</a></td>
       <td>{{template "phase" .Phase}}</td>
       <td>{{if .Activity}}{{.Activity}}{{else}}<span class="none">—</span>{{end}}</td>
       <td>{{if eq .Connector "stale"}}<span class="pill phase-raising">stale</span>{{else if eq .Connector "error"}}<span class="pill phase-lost">error</span>{{else if eq .Connector "unknown" ""}}<span class="none">{{or .Connector "—"}}</span>{{else}}{{.Connector}}{{end}}</td>
@@ -43,8 +28,8 @@
       <td>{{if .LeaseHolder}}<span class="mono">{{.LeaseHolder}}</span>{{else}}<span class="none">—</span>{{end}}</td>
       <td><time title="{{.RaisedAt.Format "2006-01-02 15:04:05 MST"}}">{{.RaisedAt.Format "Jan 2 15:04"}}</time></td>
       <td>{{if .LastSeen.IsZero}}<span class="none">—</span>{{else}}<time title="{{.LastSeen.Format "2006-01-02 15:04:05 MST"}}">{{.LastSeen.Format "15:04:05"}}</time>{{end}}</td>
-      {{if $.CanEdit}}<td class="actions"><button class="danger small" hx-delete="/ui/coves/{{.ID}}" hx-target="#coves" hx-swap="outerHTML"
-          hx-confirm="Tear down {{.ID}}?">Teardown</button></td>{{end}}
+      {{if $.CanEdit}}<td class="actions"><button class="danger small" hx-delete="/ui/coves/{{.ID}}" hx-swap="none"
+          hx-confirm="Tear down {{.ID}}?" hx-on::after-request="if(event.detail.successful) location.reload()">Teardown</button></td>{{end}}
     </tr>
     {{else}}
     <tr><td colspan="{{if .CanEdit}}12{{else}}11{{end}}" class="empty">No studios.</td></tr>
diff --git a/internal/jam/adminui/templates/dashboard.html b/internal/jam/adminui/templates/dashboard.html
index 06dcb23..b8582d2 100644
--- a/internal/jam/adminui/templates/dashboard.html
+++ b/internal/jam/adminui/templates/dashboard.html
@@ -16,7 +16,7 @@
   <a class="tile{{if .Attention}} bad{{end}}" href="/ui/coves" data-stat="attention"><b>{{.Attention}}</b><span>Lost / terminating</span></a>
   <a class="tile" href="/ui/coves" data-stat="idled"><b>{{.Idled}}</b><span>Idled</span></a>
   <a class="tile" href="/ui/projects" data-stat="projects"><b>{{.Projects}}</b><span>Projects</span></a>
-  <a class="tile" href="/ui/actors" data-stat="actors"><b>{{.Actors}}</b><span>Actors</span></a>
+  <a class="tile" href="/ui/agents" data-stat="actors"><b>{{.Actors}}</b><span>Actors</span></a>
   <a class="tile" href="/ui/roles" data-stat="roles"><b>{{.Roles}}</b><span>Roles</span></a>
   <a class="tile" href="/ui/kits" data-stat="kits"><b>{{.Kits}}</b><span>Kits</span></a>
   <a class="tile" href="/ui/destinations" data-stat="destinations"><b>{{.Destinations}}</b><span>Destinations</span></a>
diff --git a/internal/jam/adminui/templates/project.html b/internal/jam/adminui/templates/project.html
index 99552f2..44ddff0 100644
--- a/internal/jam/adminui/templates/project.html
+++ b/internal/jam/adminui/templates/project.html
@@ -148,11 +148,11 @@
     {{template "coves-tbl" .}}
   </section>
 <section class="card">
-    <header><h2>Actors</h2><span class="sub">holding a grant into this project — manage on the <a href="/ui/actors">Actors</a> page</span></header>
+    <header><h2>Identities</h2><span class="sub">agents holding a grant into this project — open one to manage its grants</span></header>
     <table>
-      <thead><tr><th>Actor</th><th>Roles</th></tr></thead>
+      <thead><tr><th>Agent</th><th>Roles</th></tr></thead>
       <tbody>
-        {{range .Holders}}<tr><td class="mono">{{.ID}}</td><td>{{range .Roles}}<span class="chip">{{.}}</span>{{end}}</td></tr>
+        {{range .Holders}}<tr><td><a class="mono" href="{{agentURL .ID}}">{{.ID}}</a></td><td>{{range .Roles}}<span class="chip">{{.}}</span>{{end}}</td></tr>
         {{else}}<tr><td colspan="2" class="empty">No actors hold a grant here.</td></tr>{{end}}
       </tbody>
     </table>
diff --git a/internal/jam/adminui/templates/role.html b/internal/jam/adminui/templates/role.html
index 9b7a58e..eee5b1a 100644
--- a/internal/jam/adminui/templates/role.html
+++ b/internal/jam/adminui/templates/role.html
@@ -185,9 +185,9 @@
   </section>
 
   <section class="card">
-    <header><h2>Holders</h2><span class="sub">actors granted this role — manage grants on the <a href="/ui/actors">Actors</a> page</span></header>
+    <header><h2>Holders</h2><span class="sub">agents granted this role — open one to manage its grants</span></header>
     <div class="body">
-      {{range .Holders}}<span class="chip">{{.ID}}{{if .Override}} <span class="unset" title="This grant overrides part of the role's scope">override</span>{{end}}</span>{{else}}<span class="unset">No actors hold this role.</span>{{end}}
+      {{range .Holders}}<span class="chip"><a href="{{agentURL .ID}}" style="color:inherit">{{.ID}}</a>{{if .Override}} <span class="unset" title="This grant overrides part of the role's scope">override</span>{{end}}</span>{{else}}<span class="unset">No actors hold this role.</span>{{end}}
     </div>
   </section>
 
diff --git a/internal/jam/adminui/templates/roster.html b/internal/jam/adminui/templates/roster.html
deleted file mode 100644
index a270bb0..0000000
--- a/internal/jam/adminui/templates/roster.html
+++ /dev/null
@@ -1,73 +0,0 @@
-{{define "content"}}
-<div class="page-head"><h1>Actors</h1><span class="sub">Actors and the role grants they hold</span></div>
-<details class="panel">
-  <summary>Enroll actor</summary>
-  <form hx-post="/ui/enrollments" hx-target="#roster-panel" hx-swap="innerHTML" data-reset>
-    <div class="grid">
-      <label>Actor id <span class="req">required</span><input name="id" required autocomplete="off"></label>
-      <label>Role <span class="req">required</span><input name="role" data-ta="roles" data-ta-project="@form" required autocomplete="off"></label>
-      <label>Project <span class="hint">or <a href="/ui/projects">create one</a></span>{{template "project-select"}}</label>
-      <label>Destination overrides <span class="hint">optional, e.g. git=git-pat</span><input name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials" autocomplete="off" spellcheck="false"></label>
-    </div>
-    <div class="form-actions"><button type="submit" class="primary">Enroll</button></div>
-  </form>
-</details>
-<div id="roster-panel"></div>
-{{template "roster-table" .}}
-{{end}}
-
-{{define "roster-table"}}
-<div class="card" id="roster">
-<table>
-  <thead><tr><th>Actor</th><th>Expiry</th><th>Grants</th><th></th></tr></thead>
-  <tbody>
-    {{range .Actors}}
-      {{$id := .ID}}
-      <tr data-id="{{$id}}">
-        <td>{{template "entity" $id}}</td>
-        <td>{{if .Expiry.IsZero}}<span class="none">—</span>{{else}}<time title="{{.Expiry.Format "2006-01-02 15:04:05 MST"}}">{{.Expiry.Format "2006-01-02"}}</time>{{end}}</td>
-        <td>
-          {{range .Grants}}
-            <span class="chip" title="{{if .Destinations}}destinations: {{range $i, $d := .Destinations}}{{if $i}}, {{end}}{{$d}}{{end}}{{else}}no destinations{{end}}"><a href="{{roleURL .Project .Role}}" style="color:inherit">{{.Project}}/{{.Role}}</a>
-              <button type="button" aria-label="Remove grant" hx-delete="/ui/actors/{{$id}}/grants/{{.Project}}/{{.Role}}" hx-target="#roster" hx-swap="outerHTML"
-                hx-confirm="Remove {{.Role}} grant on {{.Project}} from {{$id}}?">×</button></span>
-          {{else}}<span class="none">no grants</span>{{end}}
-          <details class="grant-add">
-            <summary>+ Grant</summary>
-            <form hx-post="/ui/actors/{{$id}}/grants" hx-target="#roster" hx-swap="outerHTML">
-              <input name="role" data-ta="roles" data-ta-project="@form" placeholder="role" required autocomplete="off" aria-label="Role">
-              {{template "project-select"}}
-              <input name="destinations" data-ta="destinations" data-ta-list data-ta-eq="credentials" placeholder="overrides, e.g. git=git-pat" autocomplete="off" spellcheck="false" aria-label="Destination overrides">
-              <button type="submit" class="primary small">Add</button>
-            </form>
-          </details>
-        </td>
-        <td class="actions"><button class="danger small" hx-delete="/ui/enrollments/{{$id}}" hx-target="#roster" hx-swap="outerHTML"
-            hx-confirm="Revoke {{$id}}? This invalidates its token.">Revoke</button></td>
-      </tr>
-    {{else}}
-      <tr><td colspan="4" class="empty">No actors enrolled.</td></tr>
-    {{end}}
-  </tbody>
-</table>
-<style>
-  details.grant-add{display:inline-block;vertical-align:middle}
-  details.grant-add>summary{display:inline-block;cursor:pointer;list-style:none;font-size:12px;font-weight:600;color:var(--accent);padding:2px 6px;border-radius:6px}
-  details.grant-add>summary::-webkit-details-marker{display:none}
-  details.grant-add[open]{display:block;margin-top:8px}
-  details.grant-add form{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px}
-  details.grant-add input,details.grant-add select{width:auto;flex:1 1 140px;padding:5px 8px}
-</style>
-</div>
-{{end}}
-
-{{define "enroll-result"}}
-<div class="banner token-panel">
-  <div style="flex:1;min-width:0">
-    <p style="margin:0 0 6px"><strong>Enrolled {{.ID}}.</strong> Copy this identity token now — it is not shown again.</p>
-    <div id="enroll-token" class="mono" style="word-break:break-all;padding:8px 10px;background:var(--bg);border:1px solid var(--border);border-radius:8px"><code>{{.Token}}</code></div>
-  </div>
-  <button type="button" class="primary small" data-copy="#enroll-token">Copy</button>
-</div>
-<div hx-get="/ui/actors" hx-trigger="load" hx-select="#roster" hx-target="#roster" hx-swap="outerHTML"></div>
-{{end}}
diff --git a/internal/jam/adminui/templates/session.html b/internal/jam/adminui/templates/session.html
index bb6f42a..548f8b2 100644
--- a/internal/jam/adminui/templates/session.html
+++ b/internal/jam/adminui/templates/session.html
@@ -30,13 +30,13 @@
   .ev details>summary{cursor:pointer;color:var(--accent);font-weight:600;user-select:none;width:max-content}
   .ev pre{white-space:pre-wrap;overflow-wrap:anywhere;margin:6px 0 2px;padding:8px 10px;background:var(--surface-2);border:1px solid var(--border);border-radius:8px;font:12px/1.5 "IBM Plex Mono",ui-monospace,monospace;max-height:480px;overflow:auto}
 </style>
-<div class="crumbs"><a href="/ui/coves">Studios</a> / session</div>
+<div class="crumbs"><a href="/ui/agents">Agents</a> / <a href="{{agentURL .ActorID}}">{{.ActorID}}</a> / <a href="{{agentURL .ActorID}}/session">session</a></div>
 <div class="page-head">
   <h1 class="mono">{{.ActorID}}</h1>
   {{if .Enabled}}<div id="totals" class="facts"><span class="none">loading…</span></div>{{end}}
 </div>
 {{if not .Enabled}}
-<div class="banner">Session events are not configured on this Jam. <a href="/ui/coves">Back to studios</a></div>
+<div class="banner">Session events are not configured on this Jam. <a href="{{agentURL .ActorID}}">Back to the agent</a></div>
 {{else}}
 <section class="card">
   <header>
@@ -49,7 +49,7 @@
     <span class="spacer"></span>
     <label class="check"><input type="checkbox" id="show-progress"> show progress events</label>
   </header>
-  <div id="events" data-src="/ui/coves/{{.ActorID}}/session/events?stream={{.Stream}}"></div>
+  <div id="events" data-src="{{agentURL .ActorID}}/session/events?stream={{.Stream}}"></div>
 </section>
 <script>
   document.getElementById("show-progress").addEventListener("change", e =>
diff --git a/internal/jam/adminui/writes.go b/internal/jam/adminui/writes.go
index d46fcaa..68f95db 100644
--- a/internal/jam/adminui/writes.go
+++ b/internal/jam/adminui/writes.go
@@ -115,7 +115,7 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			return
 		}
 		log.Info("ui enrolled", "operator", jam.OperatorID(r), "id", id, "project", project, "role", role)
-		renderFragment(w, "roster", "enroll-result", map[string]any{"ID": id, "Token": token})
+		renderFragment(w, "agents", "enroll-result", map[string]any{"ID": id, "Token": token})
 	})
 
 	mux.HandleFunc("DELETE /ui/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
@@ -128,7 +128,8 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			return
 		}
 		log.Info("ui revoked", "operator", jam.OperatorID(r), "id", id)
-		renderFragment(w, "roster", "roster-table", rosterData(store))
+		// The agent page navigates to the agents list on success.
+		w.WriteHeader(http.StatusOK)
 	})
 
 	mux.HandleFunc("POST /ui/roles", func(w http.ResponseWriter, r *http.Request) {
@@ -220,7 +221,8 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			return
 		}
 		log.Info("ui grant added", "operator", jam.OperatorID(r), "id", r.PathValue("id"), "project", orDefaultProject(project), "role", role)
-		renderFragment(w, "roster", "roster-table", rosterData(store))
+		// The agent page reloads on success.
+		w.WriteHeader(http.StatusOK)
 	})
 
 	mux.HandleFunc("DELETE /ui/actors/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
@@ -232,7 +234,8 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			return
 		}
 		log.Info("ui grant removed", "operator", jam.OperatorID(r), "id", r.PathValue("id"), "project", r.PathValue("project"), "role", r.PathValue("role"))
-		renderFragment(w, "roster", "roster-table", rosterData(store))
+		// The agent page reloads on success.
+		w.WriteHeader(http.StatusOK)
 	})
 
 	mux.HandleFunc("DELETE /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
@@ -283,7 +286,7 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			return
 		}
 		log.Info("ui cove raised", "operator", jam.OperatorID(r), "id", sid, "label", id, "project", orDefaultProject(project), "role", role)
-		renderFragment(w, "coves", "coves-table", covesData(store, sup, true))
+		renderFragment(w, "agents", "agents-table", newAgentsData(store, sup, "", true))
 	})
 
 	mux.HandleFunc("DELETE /ui/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
@@ -300,7 +303,8 @@ func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *
 			return
 		}
 		log.Info("ui cove torn down", "operator", jam.OperatorID(r), "id", id)
-		renderFragment(w, "coves", "coves-table", covesData(store, sup, true))
+		// Pages that offer Teardown reload on success.
+		w.WriteHeader(http.StatusOK)
 	})
 
 }
````

- [ ] **Step 4: GREEN** — `go test ./internal/jam/adminui/ && go vet ./internal/jam/adminui/ && gofmt -l internal/jam/adminui && just lint` all clean.

- [ ] **Step 5: Commit** — `git add -A internal/jam/adminui && git commit -m "feat(adminui): Agents — one list and one page per agent identity"`

---

### Task 2: Docs

- [ ] **Step 1: Apply the docs patch** (review with the docs-author skill)

````diff
diff --git a/docs/usage/jam/INDEX.md b/docs/usage/jam/INDEX.md
index d3a5565..5fd7e9d 100644
--- a/docs/usage/jam/INDEX.md
+++ b/docs/usage/jam/INDEX.md
@@ -56,7 +56,7 @@ five pillars), see the design history:
 | [ui.md](ui.md) | You want to watch a running Jam in a browser — the live studios, the squawk Log, a session timeline, the roster/roles/kits/destinations — find your way around the UI (nav, sub-tabs, search), use /me/, or configure browser login. To change something, see ui-editing.md. |
 | [ui-editing.md](ui-editing.md) | You want to change something from the admin UI instead of the CLI — enroll/revoke, grants, roles, raise/tear down a studio, Request a personal session, edit kits/destinations/model-specs — or a UI write was refused. |
 | [ui-projects.md](ui-projects.md) | You are viewing or editing one project in the admin UI — its tree of sections (members, agents, roles, rooms and messages, escalation, chat service, context) — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles link. |
-| [ui-pages.md](ui-pages.md) | You are viewing or editing one user, studio, destination, model-spec or kit in the admin UI — a user's logins/OIDC/accounts, a studio's runtime/session/squawks, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
+| [ui-pages.md](ui-pages.md) | You are viewing or editing one user, agent, destination, model-spec or kit in the admin UI — a user's logins/OIDC/accounts, an agent's grants and its studio's runtime/session/squawks, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
 | [session-events.md](session-events.md) | You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream, its storage/retention config, redaction, and the export API. |
 | [intercom.md](intercom.md) | You want a raised studio's agent to read/send comments on its own ticket (the brokered intercom MCP), or you're wiring the `/squawks` endpoint + its `cove-master mcp` delivery, wake-on (`runtime.wake`), or running the intercom without a Requisitioner. |
 | [turn-end.md](turn-end.md) | You want a ticket session to report its ticket's state or a session to end itself, to be woken at a time or on a schedule (optionally gated by a check), or to be woken (or torn down) after sitting idle; or you need to know why a studio shows `holding`, what a woken agent is told about why it woke, or how Jam decides a session whose turn ended may be woken, paused, or torn down. |
diff --git a/docs/usage/jam/coves.md b/docs/usage/jam/coves.md
index 1b6a1c5..df4d7ef 100644
--- a/docs/usage/jam/coves.md
+++ b/docs/usage/jam/coves.md
@@ -4,7 +4,7 @@ read_when: You are raising or tearing down a managed studio through Jam, inspect
 owns: the operator-facing managed-cove runtime story — the Instance registry (Phase vs Activity, leases), the `studio` verbs (formerly `cove`), the `runtime:` serve-config block, the Attach stream, and the `cove-master` client that dials it
 prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a studio is raised for
 tier: leaf
-updated: 2026-10-06
+updated: 2026-10-07
 ---
 
 # Managed studios (the supervisor)
@@ -61,8 +61,8 @@ at-jam studio teardown --id spider-42
   host-side, never on argv) — required by the real launcher; see below.
 - `studio status` reports the studio's activity; `--activity done` triggers teardown.
 - `studio teardown` tears the studio down and revokes its identity (idempotent).
-- `connector` (in `studio list` and the Studios table) is `ok` when the studio's latest agent episode (agent process) started with its role's current connector, `stale` when a destination or grant changed since (it refreshes at the next episode — a wake delivered into a live episode does not refresh it), `unknown` when it never reported (an image built before the per-spawn refresh — re-raise it), `error` when Jam cannot compute the current connector to compare against (the studio's actor is no longer on the roster, or its role's destinations conflict). Because the git route is part of the fingerprint, a studio whose git-config rewrite failed reports the route actually in effect, so it shows `stale` until the rewrite lands on a later episode.
-- `image` (in `studio list`, [`standing list`](standing-sessions.md#declaring-one), the Studios table and the role page's standing rows) is `ok` when the studio runs the image a raise for its role would run now, and `stale` when that image has changed since the raise. The image changes when the role's kit changes a build-affecting field, its [model-spec](model-specs.md) harness changes (CLI version, plugins), Jam's own build inputs change (a new at-jam binary, Jam host or launcher key — the launcher's assembly fingerprint), or the role stops raising a kit at all. Each Instance records the tag it was raised on (`image_tag`) and its kit ref (`kit`); the current tag is resolved by the same code a raise uses, cached per kit version and harness. Without both tags (a studio raised before tags were recorded, or a launcher that cannot name images) Jam compares the recorded kit: a different kit or build digest is `stale`, the same one `unknown`. It is also `unknown` when the role's current kit or model-spec does not resolve; the listing never fails on it, and the reason is logged at debug level (once per role per minute). Terminating, lost and gone studios show `-`. A stale studio keeps its old image until it is torn down and raised again; a standing one is moved to the current image with [`standing upgrade`](standing-sessions.md#upgrading-a-standing-session). `GET /admin/coves?project=&role=` lists one role's studios.
+- `connector` (in `studio list` and the admin UI Agents list) is `ok` when the studio's latest agent episode (agent process) started with its role's current connector, `stale` when a destination or grant changed since (it refreshes at the next episode — a wake delivered into a live episode does not refresh it), `unknown` when it never reported (an image built before the per-spawn refresh — re-raise it), `error` when Jam cannot compute the current connector to compare against (the studio's actor is no longer on the roster, or its role's destinations conflict). Because the git route is part of the fingerprint, a studio whose git-config rewrite failed reports the route actually in effect, so it shows `stale` until the rewrite lands on a later episode.
+- `image` (in `studio list`, [`standing list`](standing-sessions.md#declaring-one), the admin UI Agents list and the role page's standing rows) is `ok` when the studio runs the image a raise for its role would run now, and `stale` when that image has changed since the raise. The image changes when the role's kit changes a build-affecting field, its [model-spec](model-specs.md) harness changes (CLI version, plugins), Jam's own build inputs change (a new at-jam binary, Jam host or launcher key — the launcher's assembly fingerprint), or the role stops raising a kit at all. Each Instance records the tag it was raised on (`image_tag`) and its kit ref (`kit`); the current tag is resolved by the same code a raise uses, cached per kit version and harness. Without both tags (a studio raised before tags were recorded, or a launcher that cannot name images) Jam compares the recorded kit: a different kit or build digest is `stale`, the same one `unknown`. It is also `unknown` when the role's current kit or model-spec does not resolve; the listing never fails on it, and the reason is logged at debug level (once per role per minute). Terminating, lost and gone studios show `-`. A stale studio keeps its old image until it is torn down and raised again; a standing one is moved to the current image with [`standing upgrade`](standing-sessions.md#upgrading-a-standing-session). `GET /admin/coves?project=&role=` lists one role's studios.
 
 ### Raising a real managed studio
 
diff --git a/docs/usage/jam/renamed-from-harbor.md b/docs/usage/jam/renamed-from-harbor.md
index 9d04fd2..1754723 100644
--- a/docs/usage/jam/renamed-from-harbor.md
+++ b/docs/usage/jam/renamed-from-harbor.md
@@ -4,7 +4,7 @@ read_when: You have a kit, serve config, script, environment or bookmark that st
 owns: the Harbor → Jam old→new name table, which old names are deprecated aliases, and the alias removal policy
 prereqs: none
 tier: leaf
-updated: 2026-09-27
+updated: 2026-10-07
 ---
 
 # Renamed from Harbor
@@ -24,7 +24,7 @@ process) that names the old and new name and points here. The aliases are
 | kit `config.yml` `harbor:` block | `jam:` | Yes, with a warning. Setting both is a validation error. |
 | serve config `runtime.launcher.harbor-host` | `runtime.launcher.jam-host` | Yes, with a warning. Setting both is an error. |
 | serve config `runtime.dispatcher` (the resident dispatcher) | `runtime.requisitioner` (the Requisitioner) | Yes, with a warning. Setting both is an error. The standalone `at-dispatch` keeps its name. |
-| `at-jam cove raise\|list\|status\|teardown` | `at-jam studio raise\|list\|status\|teardown` | Yes, with a warning. The entity is called a **Studio** in the CLI, the admin UI and the docs; ids, admin API routes (`/admin/coves…`, `/ui/coves`), JSON fields, `cove-master` and `.at-cove/` keep "cove". |
+| `at-jam cove raise\|list\|status\|teardown` | `at-jam studio raise\|list\|status\|teardown` | Yes, with a warning. The entity is called a **Studio** in the CLI, the admin UI and the docs; ids, admin API routes (`/admin/coves…`), JSON fields, `cove-master` and `.at-cove/` keep "cove". |
 | admin UI cookies `harbor_session`, `harbor_oauth_*` | `jam_session`, `jam_oauth_*` | No. **Admin UI users log in again once** after upgrading; the old cookies are simply ignored. |
 | broker basic-auth realm `harbor` | `jam` | No. Git picks its credential helper by URL, not realm, so coves are unaffected. |
 | `HARBOR_TEST_POSTGRES_DSN`; CI and dev database/user/password `harbor` | `JAM_TEST_POSTGRES_DSN`; `jam` | No (test infrastructure). The dev compose project is now `jam-dev`, so an old `harbor-dev` volume is not reused — `just dev-up` starts fresh. |
diff --git a/docs/usage/jam/session-events.md b/docs/usage/jam/session-events.md
index 5c9924a..367e3f8 100644
--- a/docs/usage/jam/session-events.md
+++ b/docs/usage/jam/session-events.md
@@ -89,9 +89,9 @@ recorded before they existed), and the extracted `type`, `subtype`,
 
 ## UI
 
-The browser timeline lives at `/ui/coves/{id}/session`; see
+The browser timeline lives at `/ui/agents/{id}/session`; see
 [ui.md](ui.md#session-timeline). The studio's page lists its streams
-([ui-pages.md](ui-pages.md#studio-pages)). `/me` shows participants only a
+([ui-pages.md](ui-pages.md#agent-pages)). `/me` shows participants only a
 derived status per session, never events — see
 [intercom-ui.md](intercom-ui.md#session-status).
 
diff --git a/docs/usage/jam/ui-editing.md b/docs/usage/jam/ui-editing.md
index 5e3c2f5..5bc51f5 100644
--- a/docs/usage/jam/ui-editing.md
+++ b/docs/usage/jam/ui-editing.md
@@ -19,15 +19,15 @@ from the UI itself.
 Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
 verbs in [roster.md](roster.md):
 
-- **Enroll** an actor (id, project, role, optional destination overrides).
+- **Enroll** an agent on the Agents list (id, project, role, optional destination overrides).
   The identity token is shown **once**, right after enrolling — copy it then; it
   is never shown again, stored in a list, or logged. For the full connection
   snippet (env vars / git config), use the CLI `at-jam enroll`.
-- **Revoke** an actor, **create/delete** a role (and edit it on its
-  [role page](ui-projects.md#role-pages)), and **add/remove** a grant.
-  On the Actors page each actor's grants are chips (`project/role`, with a ×
-  to remove; hover for the effective destinations), and **+ Grant** on the
-  actor's row opens its add-grant form.
+- **Revoke** an agent, **create/delete** a role (and edit it on its
+  [role page](ui-projects.md#role-pages)), and **add/remove** a grant. On an
+  [agent's page](ui-pages.md#agent-pages) its grants are chips (`project/role`,
+  with a × to remove; hover for the effective destinations), **+ Grant** opens
+  its add-grant form, and **Revoke** is in the header.
 - Destination fields (role, enroll/grant overrides) take the CLI's
   `name=credential` syntax ([roster.md](roster.md#roles)); an unknown credential
   or a mapping for a destination not in scope is rejected. Credential *names*
@@ -64,13 +64,13 @@ supervisor is configured — see [Runtime (studios)](#runtime-studios) below.
 ### Runtime (studios)
 
 When Jam is configured with a runtime supervisor (`runtime:` in the serve
-config — see [coves.md](coves.md)), the Studios page can also:
+config — see [coves.md](coves.md)), the Agents list can also:
 
-- **Raise a managed studio** — id, role, optional project/unit and a workload
+- **Raise a managed studio** — a label, role, optional project/unit and a workload
   prompt. Jam handles the studio's identity token and launch secret internally;
   they are never shown in the browser (use the CLI `at-jam studio raise` for
   manual wiring).
-- **Tear down a studio** (confirmed).
+- **Tear down a studio** (confirmed) — from its row or its agent's page.
 
 A project's Roles section and each role page gain a **Request** action: it raises a
 [personal session](personal-sessions.md) of that role **for you**, with the
@@ -81,7 +81,7 @@ As anonymous loopback `local`, the action asks you to sign in. Admission,
 delivery checks, and errors are exactly those of `at-jam session request`, and
 the outcome (the new session id, or the refusal) shows in the page's banner.
 
-Without a runtime supervisor, the Studios page is view-only. Setting a studio's
+Without a runtime supervisor, studios are view-only. Setting a studio's
 activity is not a UI action — that is reported by the studio itself. These actions
 obey the same gate, CSRF, and audit-logging as the roster edits above.
 
diff --git a/docs/usage/jam/ui-pages.md b/docs/usage/jam/ui-pages.md
index 4690b77..524ef63 100644
--- a/docs/usage/jam/ui-pages.md
+++ b/docs/usage/jam/ui-pages.md
@@ -1,7 +1,7 @@
 ---
-summary: The Jam admin UI's per-entity pages outside a project — a user's page, a studio's page (/ui/coves/<id>), a destination's page (/ui/destinations/<name>), a model-spec's page (/ui/model-specs/<name>) and a kit's page (/ui/kits/<name>) — what each shows and how editing them works.
-read_when: You are viewing or editing a user, studio, destination, model-spec or kit in the Jam admin UI — a user's logins, OIDC identities or accounts; a studio's runtime, waiting/escalation state, session streams or squawks; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
-owns: the user, studio, destination, model-spec and kit detail pages (what they show, their edit forms, the users list, create-only list forms, connector-conflict flags, kit version rail/diff/push)
+summary: The Jam admin UI's per-entity pages outside a project — a user's page, an agent's page (/ui/agents/<id>: identity, grants, studio, session, squawks), a destination's page (/ui/destinations/<name>), a model-spec's page (/ui/model-specs/<name>) and a kit's page (/ui/kits/<name>) — what each shows and how editing them works.
+read_when: You are viewing or editing a user, studio, destination, model-spec or kit in the Jam admin UI — a user's logins, OIDC identities or accounts; an agent's grants, studio runtime, waiting/escalation state, session streams or squawks; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
+owns: the user, agent, destination, model-spec and kit detail pages (what they show, their edit forms, the users list, create-only list forms, connector-conflict flags, kit version rail/diff/push)
 prereqs: ui.md for reaching the UI and the top nav; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles; connector.md for destination env/git; kits.md for the StudioKit schema and versioning
 tier: leaf
 updated: 2026-10-07
@@ -26,14 +26,18 @@ A project's pages (its tree, sections and role pages) are in
 logins and OIDC identities, adds or unlinks accounts, and removes it (rename and
 remove are refused while the user owns a live personal session).
 
-## Studio pages
+## Agent pages
 
-Each studio id in the Studios table opens `/ui/coves/<id>`, the hub for one
-studio. The header shows its phase, activity, kind (ephemeral, personal with
-its owner, or standing with its name), project, role and unit (linked), with
-**Open live timeline** (the [session timeline](ui.md#session-timeline)) and,
-when a runtime supervisor is configured, **Teardown**.
+Each agent id (in the Agents list, a studio table, a role's holders, a
+project's identities, or search) opens `/ui/agents/<id>`, the page for one
+agent identity. The header shows its kind, phase and activity, standing name or
+personal owner, project, role and unit (linked), with **Open live timeline**
+(the [session timeline](ui.md#session-timeline)), **Teardown** when it has a
+studio and a runtime supervisor runs, and **Revoke** when it is enrolled.
 
+- **Identity** — its grants as `project/role` chips (× removes one; hover for
+  the effective destinations), **+ Grant**, and its token expiry; or "not
+  enrolled" for a studio whose identity is gone.
 - **Runtime** — raised and last seen, lease holder, backend and location.
 - **Waiting & escalation** — whether it is waiting and since when (wake-on
   resumes it on a reply past its wait seq), the open escalation (which tier was
diff --git a/docs/usage/jam/ui-projects.md b/docs/usage/jam/ui-projects.md
index 6daec7c..b3eb165 100644
--- a/docs/usage/jam/ui-projects.md
+++ b/docs/usage/jam/ui-projects.md
@@ -94,8 +94,8 @@ only that section:
   the page says what applies when unset.
 - **Standing sessions** — declare, [upgrade](standing-sessions.md#upgrading-a-standing-session), [reset](standing-sessions.md#reset) and dismiss;
   each shows its studio's phase, flagged **image stale** per [coves.md](coves.md#the-studio-verbs) (its Upgrade button highlighted) and any pending upgrade; a queued upgrade or pending reset flashes as accepted.
-- **Holders** and **Studios** — the actors granted the role (marked where the
-  grant overrides the scope; grants are managed on the Actors page, under Agents) and the role's
+- **Holders** and **Studios** — the agents granted the role (linked; marked where the
+  grant overrides the scope; grants are managed on the agent's page) and the role's
   running studios.
 
 **Request session** and **Delete** (which returns to the project's Roles) are on the page header. These writes share
diff --git a/docs/usage/jam/ui.md b/docs/usage/jam/ui.md
index 794d8b2..c66df51 100644
--- a/docs/usage/jam/ui.md
+++ b/docs/usage/jam/ui.md
@@ -1,7 +1,7 @@
 ---
 summary: The Jam admin UI — a server-rendered web view of the live studios, the durable squawk Log, and the control-plane roster/roles/kits/destinations, served by `at-jam serve`; reachable on loopback always, and off-loopback via browser OIDC login. Covers the top nav and its sub-tabs, the list pages, search, the Intercom log, the session timeline and the participant /me/ surface; what the UI can change is in ui-editing.md.
 read_when: You want to watch a running Jam in a browser — the live studio fleet, the squawk Log, a session timeline, and the roster/roles/kits/destinations — find your way around the UI (nav, sub-tabs, search), use the participant /me/ page, or configure browser login for it. To change something from the UI, read ui-editing.md instead.
-owns: the `/ui/coves/{id}/session` timeline page; the `/ui/` observability surface (the top nav and its sections, what each list shows, search, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
+owns: the `/ui/agents/{id}/session` timeline page; the `/ui/` observability surface (the top nav and its sections, what each list shows, search, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
 prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
 tier: leaf
 updated: 2026-10-07
@@ -19,10 +19,9 @@ http://127.0.0.1:8081/ui/
 
 The top nav has six sections — **Dashboard · Projects · Users · Agents ·
 Specs · Intercom** — and a page highlights its section, so a detail page
-highlights the list it belongs to (a role page: Projects). **Agents** and
-**Specs** each group several list pages under a sub-tab strip: Agents holds
-**Studios · Actors**, Specs holds **Kits · Destinations · Model-specs** (their
-detail pages show the strip too). It renders:
+highlights the list it belongs to (a role page: Projects). **Specs** groups
+**Kits · Destinations · Model-specs** under a sub-tab strip (their detail pages
+show it too). It renders:
 
 - **Dashboard** (`/ui/`) — summary tiles (live / raising / lost-or-terminating /
   idled studios, and counts of projects, actors, roles, kits, destinations),
@@ -43,25 +42,25 @@ detail pages show the strip too). It renders:
   studios, roster size and chat service; create one, or delete one nothing
   references. Each project opens on a tree of its sections — members, agents,
   roles, rooms and messages, escalation — see [ui-projects.md](ui-projects.md).
-- **Studios** (`/ui/coves`) — every managed studio's id, project/role, unit, phase,
-  activity, connector and image status ([coves.md](coves.md#the-studio-verbs)), lease holder, raised-at, last-seen. The table **auto-refreshes every
-  3 seconds** (htmx polling); no page reload. View-only unless a runtime
-  supervisor is configured, in which case it can also raise and tear down
-  studios — see [Runtime (studios)](ui-editing.md#runtime-studios) and
-  [coves.md](coves.md). Each id opens the studio's page (runtime, waiting and
-  escalation state, session streams, squawks — see
-  [ui-pages.md](ui-pages.md#studio-pages)); **timeline** next to it opens the
-  live session timeline.
+- **Agents** (`/ui/agents`) — each enrolled identity and each studio, one row
+  per id, with its **kind** (`standing`, `personal`, `ticket` — a session with
+  a unit, `manual`, or `enrolled` — no studio), project/role, phase, activity,
+  connector and image status ([coves.md](coves.md#the-studio-verbs)); it
+  **auto-refreshes every 3 seconds**. Phase filters
+  (`?phase=live|raising|idled|attention`) match the dashboard tiles. Enroll,
+  raise and teardown: [ui-editing.md](ui-editing.md). Each id opens the agent's
+  page ([ui-pages.md](ui-pages.md#agent-pages)); `/ui/coves…` and `/ui/actors`
+  redirect here.
 - **Intercom** (`/ui/intercom`) — a read-only, filterable, newest-first table of
   the channel log, with the frozen legacy log on a Legacy tab. See
   [Intercom](#intercom) below.
-- **Users / Actors / Kits / Destinations / Model-specs** — the control-plane
-  objects as tables, all editable from here; roles live in their project — see
+- **Users / Kits / Destinations / Model-specs** — the control-plane objects as
+  tables, all editable from here; roles live in their project — see
   [ui-editing.md](ui-editing.md).
 
-Every table has a fixed order — studios and actors by id; roles by project,
+Every table has a fixed order — agents and studios by id; roles by project,
 then name; kits and destinations by name; squawks newest-first — so rows don't
-shuffle across the Studios poll or after an edit. The order comes from the
+shuffle across a poll or after an edit. The order comes from the
 store, so the JSON admin API and CLI lists match it.
 
 **One look for `/ui` and `/me`.** Both UIs take their colors (light and dark,
@@ -183,12 +182,12 @@ reads the Log (still a full snapshot per load — pagination is a later phase).
 
 ## Session timeline
 
-`/ui/coves/{id}/session` (linked from the Studios table and the studio's page) shows a managed
+`/ui/agents/{id}/session` (linked from studio tables and the agent's page) shows a managed
 studio's agent session: a stream selector (current and past streams), header
 totals (turns = results answered, episodes, tool calls, tokens in/out, cost = last total per episode), and a flat event list, each
 event tagged with its turn (`tN`) (text, thinking, tool use/results expandable, results, gap and truncation
 markers), with a raw-JSON toggle. `system`/`thinking_tokens` events are hidden
-behind **show progress events**. It updates live over SSE from `/ui/coves/{id}/session/events` (backfill, then
+behind **show progress events**. It updates live over SSE from `/ui/agents/{id}/session/events` (backfill, then
 live; reconnects resume via `Last-Event-ID`). Storage, retention,
 and sensitivity: [session-events.md](session-events.md).
 
````

- [ ] **Step 2: Audit** — docs-audit: no ERROR/WARN for `usage/jam/ui*.md`, `session-events.md`, `coves.md` beyond its pre-existing size warning, `renamed-from-harbor.md`, `INDEX.md`; `wc -l docs/usage/jam/ui.md` ≤ 200.

- [ ] **Step 3: Commit** — `git add docs/usage/jam && git commit -m "docs(jam): the Agents list and agent pages"`

---

### Task 3: Full verification

- [ ] `just test` and `just lint` green; `git status --short` shows no tracked changes.
- [ ] Ask the user to eyeball on `just dev-watch`: `/ui/agents` (kinds, filters, poll, Raise/Enroll), an agent page (Identity grants +/−, Revoke, Teardown, timeline link), the dashboard's studio table still live-updating, and old `/ui/coves/<id>` links landing on agent pages.
