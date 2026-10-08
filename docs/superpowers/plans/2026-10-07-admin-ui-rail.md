# Admin UI scope rail + attention badges Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement [the scope rail spec](../specs/2026-10-07-admin-ui-rail-design.md): a permanent left rail (Jam, then every project, then + New project) whose selection decides the tab strip over the content, the project tree and top nav removed, and attention badges on rail entries, tabs and rows plus Needs attention cards — all from one `attention` function.

**Architecture:**
- **Frame per request.** `Handler` wraps its mux so every request carries a `frameSource` (store + image resolver) on its context. `render(w, r, page, data)` builds a `frame` from the page's `pageMeta` (scope kind, tab, Specs sub-tab) and the request: a project page's project and tab come from its path, and an agent page takes `?project=`. It then executes a **clone** of the page's master template set with `frame`, `attn` and `agentHref` bound as template funcs. Masters are only ever cloned, never executed (html/template refuses to clone an executed set). That leaves page payloads unchanged. Fragments render from a clone with default funcs.
- **`attention(store, img)`** (pure, `attention.go`) returns `[]attnItem{Kind, Scope, Tab, Subject, Href, Why}`:
  - Jam items first: kits whose current version doesn't parse, destinations in a connector conflict.
  - Then each project's: one item per broken or stale studio (at its worst), roles naming a missing kit or model-spec, escalation targets naming nobody.
  - `badgeOf` gives the count, the worst colour and the hover breakdown.
- **Rail.** A template in `layout.html`, re-fetched from `GET /ui/rail?scope=` every 3s. `scope` is `~` for Jam, a project name, or empty.

**Tech Stack:** Go 1.26 `net/http` + `html/template` (Clone/Funcs per request), htmx; hermetic `httptest` tests.

## Global Constraints

- Every existing page URL keeps working; write endpoints keep their paths; page payloads are unchanged.
- `/ui/projects` (and `/ui/roles`) → 301 `/ui/`; `+ New project` posts the existing `POST /ui/projects`, which answers `200` + `HX-Redirect` to the new project (no table fragment).
- Jam tabs: Dashboard `/ui/` · Agents `/ui/agents` · Users `/ui/users` · Specs `/ui/specs` · Intercom `/ui/intercom`. Project tabs: Overview · Members · Agents · Roles · Intercom · Escalation.
- Attention kinds and severities exactly as the spec table; **Jam's badge counts only Jam-scope items**; one item per studio.
- Badge markup: `<span class="badge[ red]" title="<breakdown>"><N></span>`, nothing when zero.
- Agent links from project pages carry `?project=<name>` (`agentHref`); the agent page then sits under that project (unknown project → Jam).
- Narrow screens (≤ 720px): the rail is a drawer toggled by the title bar's ☰.
- Docs: `ui.md` ≤ 200 lines (audit-counted); a new leaf `ui-attention.md` owns the attention model.

## How to apply

As in the IA slices: verified patches, applied with `git apply --whitespace=error` from `main` at this plan's commit. Test patch first (RED: 11 failures — no rail/tabs/badges/attention, `/ui/projects` not redirecting), then the code patch (GREEN), then docs.

---

### Task 1: Rail, scope tabs, attention and badges

**Files:** new `attention.go`, `templates/attention.html`, `frame_internal_test.go`, `attention_internal_test.go`; rewritten `nav.go`; `adminui.go` (pages → `page{t, meta}`, `mustParse(meta, …)`, `render`/`renderStatus` take `r`, `frameFuncs`, `/ui/rail`, Handler wrapper); every handler's `render` call gains `r`; `projects.go` (no tree; `/ui/projects` redirect; create/delete answers), `projtree.go` (sections only), `project_edit.go` (section-only answers), `role_detail.go`, `session.go` (event fragments from a once-made clone); templates `layout.html` (title bar ☰, rail, tabs, sub-tabs, `badge`, `attn-flag`, `crumbs`), `project.html`, `role.html`, `coves.html`, `agents.html`, `agent.html`, `dashboard.html`, `kits.html`, `destinations.html`; deleted `templates/projtree.html`, `templates/projects.html`; test updates across the package.

- [ ] **Step 1: Apply the test patch**

````diff
diff --git a/internal/jam/adminui/agents_test.go b/internal/jam/adminui/agents_test.go
index aa463c9..23a0a4f 100644
--- a/internal/jam/adminui/agents_test.go
+++ b/internal/jam/adminui/agents_test.go
@@ -154,10 +154,10 @@ func TestAgentPageIdentity(t *testing.T) {
 // Holders on role and project pages link to their agent pages.
 func TestHoldersLinkAgents(t *testing.T) {
 	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
-	if body := get(t, h, "/ui/projects/acme/roles/ops").Body.String(); !strings.Contains(body, `<a href="/ui/agents/e-only"`) {
+	if body := get(t, h, "/ui/projects/acme/roles/ops").Body.String(); !strings.Contains(body, `<a href="/ui/agents/e-only?project=acme"`) {
 		t.Error("role holders should link their agent pages")
 	}
-	if body := get(t, h, "/ui/projects/acme/agents").Body.String(); !strings.Contains(body, `<a class="mono" href="/ui/agents/e-only">e-only</a>`) {
+	if body := get(t, h, "/ui/projects/acme/agents").Body.String(); !strings.Contains(body, `<a class="mono" href="/ui/agents/e-only?project=acme">e-only</a>`) {
 		t.Error("project identities should link their agent pages")
 	}
 }
diff --git a/internal/jam/adminui/attention_internal_test.go b/internal/jam/adminui/attention_internal_test.go
new file mode 100644
index 0000000..5cbbb05
--- /dev/null
+++ b/internal/jam/adminui/attention_internal_test.go
@@ -0,0 +1,134 @@
+package adminui
+
+import (
+	"strings"
+	"testing"
+	"time"
+
+	"github.com/aethons-tools/cove/internal/jam"
+)
+
+// newImage reports one current image tag for every role, so a studio raised
+// on another tag is stale.
+type newImage struct{}
+
+func (newImage) CurrentImage(project, role string) (jam.CurrentImage, error) {
+	return jam.CurrentImage{HasKit: true, Tag: "img:new"}, nil
+}
+
+// attnFixture: project acme with one studio per trouble (lost, terminating,
+// egress failing, stale image, healthy), a role naming a missing kit and one
+// naming a missing model-spec, an escalation target naming nobody; Jam with
+// an unparseable kit and two destinations whose git routes conflict in one
+// role; project beta with nothing wrong.
+func attnFixture(t *testing.T) jam.Store {
+	t.Helper()
+	st := jam.NewMemStore()
+	for _, p := range []string{"acme", "beta"} {
+		if err := st.CreateProject(p); err != nil {
+			t.Fatal(err)
+		}
+	}
+	for _, i := range []jam.Instance{
+		{ActorID: "s-lost", Project: "acme", Role: "dev", Phase: jam.PhaseLost},
+		{ActorID: "s-term", Project: "acme", Role: "dev", Phase: jam.PhaseTerminating},
+		{ActorID: "s-egress", Project: "acme", Role: "dev", Phase: jam.PhaseLive, EgressFailures: 2},
+		{ActorID: "s-stale", Project: "acme", Role: "dev", Phase: jam.PhaseLive, ImageTag: "img:old"},
+		{ActorID: "s-ok", Project: "acme", Role: "dev", Phase: jam.PhaseLive, ImageTag: "img:new"},
+		{ActorID: "s-beta", Project: "beta", Role: "w", Phase: jam.PhaseLive, ImageTag: "img:new"},
+	} {
+		if err := st.PutInstance(i); err != nil {
+			t.Fatal(err)
+		}
+	}
+	if _, err := st.PushKit("bad", "listen: :443"); err != nil { // not a studio kit
+		t.Fatal(err)
+	}
+	for _, d := range []jam.Destination{
+		{Name: "git-a", Route: "/git-a/", Upstream: "https://github.com", Git: true},
+		{Name: "git-b", Route: "/git-b/", Upstream: "https://github.com", Git: true},
+	} {
+		if err := st.AddDestination(d); err != nil {
+			t.Fatal(err)
+		}
+	}
+	// A kit removed after a role bound it (RemoveKit doesn't check users).
+	if _, err := st.PushKit("ghost", "kind: studio\n"); err != nil {
+		t.Fatal(err)
+	}
+	for _, r := range []jam.Role{
+		{Name: "dev", Scope: jam.Scope{Destinations: []string{"git-a", "git-b"}}},
+		{Name: "nokit", Kit: "ghost"},
+		{Name: "nospec", ModelSpec: "ghost-spec"},
+	} {
+		if err := st.PutRole("acme", r); err != nil {
+			t.Fatal(err)
+		}
+	}
+	if err := st.RemoveKit("ghost"); err != nil {
+		t.Fatal(err)
+	}
+	if err := st.PutRole("beta", jam.Role{Name: "w"}); err != nil {
+		t.Fatal(err)
+	}
+	if err := st.SetEscalationPolicy("acme", "", []jam.EscalationTier{{Targets: []string{"user:ghost"}, Timeout: time.Minute}}); err != nil {
+		t.Fatal(err)
+	}
+	return st
+}
+
+func TestAttention(t *testing.T) {
+	items := attention(attnFixture(t), newImage{})
+	got := map[string]attnItem{}
+	for _, it := range items {
+		if _, dup := got[it.Scope+"|"+it.Tab+"|"+it.Subject]; dup {
+			t.Errorf("duplicate item %+v", it)
+		}
+		got[it.Scope+"|"+it.Tab+"|"+it.Subject] = it
+	}
+	for key, want := range map[string]struct {
+		kind attnKind
+		why  string
+	}{
+		"acme|agents|s-lost":                     {attnBroken, "lost"},
+		"acme|agents|s-term":                     {attnBroken, "terminating"},
+		"acme|agents|s-egress":                   {attnBroken, "egress re-apply failing (2)"},
+		"acme|agents|s-stale":                    {attnStale, "image stale"},
+		"acme|roles|acme/nokit":                  {attnConfig, "kit ghost not found"},
+		"acme|roles|acme/nospec":                 {attnConfig, "model-spec ghost-spec not found"},
+		"acme|escalation|escalation::user:ghost": {attnConfig, "user:ghost"},
+		"|specs|kit:bad":                         {attnConfig, "does not parse"},
+		"|specs|dest:git-a":                      {attnConfig, "conflicts"},
+		"|specs|dest:git-b":                      {attnConfig, "conflicts"},
+	} {
+		it, ok := got[key]
+		if !ok {
+			t.Errorf("missing %s", key)
+			continue
+		}
+		if it.Kind != want.kind || !strings.Contains(it.Why, want.why) || it.Href == "" {
+			t.Errorf("%s = %+v, want kind %s, why containing %q, an href", key, it, want.kind, want.why)
+		}
+		delete(got, key)
+	}
+	for key := range got {
+		t.Errorf("unexpected item %s", key) // s-ok, s-beta and beta are healthy
+	}
+	if items[0].Scope != "" || items[len(items)-1].Scope != "acme" {
+		t.Errorf("Jam's items come first, then each project's")
+	}
+}
+
+func TestBadgeOf(t *testing.T) {
+	if b := badgeOf(nil); b.N != 0 {
+		t.Errorf("no items: %+v", b)
+	}
+	b := badgeOf([]attnItem{{Kind: attnStale}, {Kind: attnConfig}, {Kind: attnStale}})
+	if b.N != 3 || b.Red || b.Title != "2 out of date · 1 config" {
+		t.Errorf("amber badge = %+v", b)
+	}
+	b = badgeOf([]attnItem{{Kind: attnStale}, {Kind: attnBroken}})
+	if b.N != 2 || !b.Red || b.Title != "1 broken · 1 out of date" {
+		t.Errorf("red badge = %+v", b)
+	}
+}
diff --git a/internal/jam/adminui/frame_internal_test.go b/internal/jam/adminui/frame_internal_test.go
new file mode 100644
index 0000000..3373ac7
--- /dev/null
+++ b/internal/jam/adminui/frame_internal_test.go
@@ -0,0 +1,149 @@
+package adminui
+
+import (
+	"io"
+	"log/slog"
+	"net/http"
+	"net/http/httptest"
+	"strings"
+	"testing"
+)
+
+func frameGet(t *testing.T, h http.Handler, path string) string {
+	t.Helper()
+	rec := httptest.NewRecorder()
+	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
+	if rec.Code != http.StatusOK {
+		t.Fatalf("GET %s = %d", path, rec.Code)
+	}
+	return rec.Body.String()
+}
+
+func between(s, from, to string) string {
+	i := strings.Index(s, from)
+	if i < 0 {
+		return ""
+	}
+	s = s[i:]
+	if j := strings.Index(s, to); j >= 0 {
+		return s[:j]
+	}
+	return s
+}
+
+func frameHandler(t *testing.T) http.Handler {
+	return Handler(attnFixture(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, func(string) bool { return true }, nil)
+}
+
+// The rail badges each project with its own items — red when any is broken —
+// and Jam with Jam's items only.
+func TestRailBadges(t *testing.T) {
+	rail := between(frameGet(t, frameHandler(t), "/ui/"), `<aside id="rail"`, "</aside>")
+	for _, want := range []string{
+		// Jam: kit bad + two conflicting destinations; never the projects' items
+		`<span class="name">◉ Jam</span> <span class="badge" title="3 config">3</span>`,
+		// acme: three broken studios, two roles and one escalation target (no
+		// image resolver here, so nothing is stale)
+		`<span class="name">acme</span> <span class="badge red" title="3 broken · 3 config">6</span>`,
+		// beta: nothing
+		`<span class="name">beta</span></a>`,
+	} {
+		if !strings.Contains(rail, want) {
+			t.Errorf("rail missing %s in\n%s", want, rail)
+		}
+	}
+}
+
+// Each tab shows its scope's items for that tab.
+func TestTabBadges(t *testing.T) {
+	h := frameHandler(t)
+	tabs := between(frameGet(t, h, "/ui/projects/acme"), `<nav class="tabs"`, "</nav>")
+	for _, want := range []string{
+		`>Agents <span class="badge red" title="3 broken">3</span>`,
+		`>Roles <span class="badge" title="2 config">2</span>`,
+		`>Escalation <span class="badge" title="1 config">1</span>`,
+		`>Members</a>`, `>Overview</a>`,
+	} {
+		if !strings.Contains(tabs, want) {
+			t.Errorf("acme tabs missing %s in\n%s", want, tabs)
+		}
+	}
+	tabs = between(frameGet(t, h, "/ui/"), `<nav class="tabs"`, "</nav>")
+	if !strings.Contains(tabs, `>Specs <span class="badge" title="3 config">3</span>`) || !strings.Contains(tabs, `>Agents</a>`) {
+		t.Errorf("Jam tabs: Specs carries Jam's items, Agents none:\n%s", tabs)
+	}
+}
+
+// The project Overview and the Dashboard list their scope's items.
+func TestNeedsAttentionCards(t *testing.T) {
+	h := frameHandler(t)
+	card := between(frameGet(t, h, "/ui/projects/acme"), "<h2>Needs attention</h2>", "</section>")
+	for _, want := range []string{"s-lost: lost", "role nokit: kit ghost not found", "user:ghost", `href="/ui/agents/s-lost?project=acme"`} {
+		if !strings.Contains(card, want) {
+			t.Errorf("acme card missing %q in\n%s", want, card)
+		}
+	}
+	card = between(frameGet(t, h, "/ui/"), "<h2>Needs attention</h2>", "</section>")
+	if !strings.Contains(card, "kit bad v1 does not parse") || strings.Contains(card, "s-lost") {
+		t.Errorf("dashboard card lists Jam's items only:\n%s", card)
+	}
+	if card := between(frameGet(t, h, "/ui/projects/beta"), "<h2>Needs attention</h2>", "</section>"); !strings.Contains(card, "Nothing needs attention.") {
+		t.Errorf("beta card:\n%s", card)
+	}
+}
+
+// Rows an item names carry its flag.
+func TestRowFlags(t *testing.T) {
+	h := frameHandler(t)
+	for path, want := range map[string]string{
+		"/ui/projects/acme/roles":  `<b>nokit</b></a> <span class="attn-flag" title="role nokit: kit ghost not found">⚠</span>`,
+		"/ui/agents":               `<span class="attn-flag red" title="s-lost: lost">⚠</span>`,
+		"/ui/kits":                 `<b>bad</b></a> <span class="attn-flag" title="kit bad v1 does not parse">⚠</span>`,
+		"/ui/projects/acme/agents": `<span class="attn-flag red" title="s-term: terminating">⚠</span>`,
+	} {
+		if body := frameGet(t, h, path); !strings.Contains(body, want) {
+			t.Errorf("%s missing flag %s", path, want)
+		}
+	}
+}
+
+// The rail polls its own fragment, keeping the selection.
+func TestRailFragment(t *testing.T) {
+	h := frameHandler(t)
+	if body := frameGet(t, h, "/ui/projects/acme/roles"); !strings.Contains(body, `hx-get="/ui/rail?scope=acme" hx-trigger="every 3s"`) {
+		t.Error("rail should poll with its scope")
+	}
+	body := frameGet(t, h, "/ui/rail?scope=acme")
+	if !strings.HasPrefix(strings.TrimSpace(body), `<aside id="rail"`) || strings.Contains(body, "<html") ||
+		!strings.Contains(body, `<a href="/ui/projects/acme" aria-current="page">`) {
+		t.Errorf("rail fragment:\n%s", body)
+	}
+	if body := frameGet(t, h, "/ui/rail?scope=~"); !strings.Contains(body, `<a class="jam" href="/ui/" aria-current="page">`) {
+		t.Errorf("rail fragment for Jam:\n%s", body)
+	}
+}
+
+// An agent page linked from a project keeps the project's scope.
+func TestAgentPageScope(t *testing.T) {
+	h := frameHandler(t)
+	body := frameGet(t, h, "/ui/agents/s-lost?project=acme")
+	if !strings.Contains(between(body, `<aside id="rail"`, "</aside>"), `<a href="/ui/projects/acme" aria-current="page">`) ||
+		!strings.Contains(between(body, `<nav class="tabs"`, "</nav>"), `href="/ui/projects/acme/agents" aria-current="page">Agents`) ||
+		!strings.Contains(body, `<a href="/ui/projects/acme/agents">Agents</a> / `) {
+		t.Errorf("agent page with ?project=acme should sit under acme's Agents")
+	}
+	body = frameGet(t, h, "/ui/agents/s-lost")
+	if !strings.Contains(between(body, `<nav class="tabs"`, "</nav>"), `href="/ui/agents" aria-current="page">Agents`) {
+		t.Errorf("agent page without a project sits under Jam's Agents")
+	}
+	if body := frameGet(t, h, "/ui/agents/s-lost?project=nope"); !strings.Contains(between(body, `<nav class="tabs"`, "</nav>"), `href="/ui/agents" aria-current="page">Agents`) {
+		t.Errorf("an unknown project falls back to Jam")
+	}
+}
+
+// Narrow screens open the rail from a button in the title bar.
+func TestRailDrawerButton(t *testing.T) {
+	if body := frameGet(t, frameHandler(t), "/ui/"); !strings.Contains(body, `class="railbtn"`) || !strings.Contains(body, "body.rail-open #rail{display:block}") {
+		t.Error("the title bar should carry the rail drawer button")
+	}
+}
diff --git a/internal/jam/adminui/nav_test.go b/internal/jam/adminui/nav_test.go
index f8fb6a1..7d3e182 100644
--- a/internal/jam/adminui/nav_test.go
+++ b/internal/jam/adminui/nav_test.go
@@ -4,7 +4,6 @@ import (
 	"net/http"
 	"net/http/httptest"
 	"net/url"
-	"regexp"
 	"strings"
 	"testing"
 
@@ -14,101 +13,14 @@ import (
 	"github.com/aethons-tools/cove/internal/jam/adminui"
 )
 
-// The top nav is six sections, in order, and a page highlights its section
-// whatever its title: a detail page highlights its list's section.
-func TestTopNavSections(t *testing.T) {
-	h := projHandler(seedProjects(t))
-	body := get(t, h, "/ui/").Body.String()
-	nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
-	last := -1
-	for _, label := range []string{">Dashboard<", ">Projects<", ">Users<", ">Agents<", ">Specs<", ">Intercom<"} {
-		i := strings.Index(nav, label)
-		if i < 0 || i < last {
-			t.Fatalf("nav order: %q missing or out of order in\n%s", label, nav)
-		}
-		last = i
-	}
-	for _, gone := range []string{">Roles<", ">Actors<", ">Studios<", ">Kits<"} {
-		if strings.Contains(nav, gone) {
-			t.Errorf("nav still has %s", gone)
-		}
-	}
-	for path, section := range map[string]string{
-		"/ui/projects/acme/roles/dev": "Projects",
-		"/ui/projects/acme/members":   "Projects",
-		"/ui/agents":                  "Agents",
-		"/ui/agents/studio-acme":      "Agents",
-		"/ui/kits":                    "Specs",
-		"/ui/model-specs":             "Specs",
-		"/ui/users":                   "Users",
-	} {
-		body := get(t, h, path).Body.String()
-		nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
-		if n := strings.Count(nav, `aria-current="page"`); n != 1 || !strings.Contains(nav, `aria-current="page">`+section+"<") {
-			t.Errorf("%s: want only %s current in the top nav, got %d marked", path, section, n)
-		}
-	}
-	// search belongs to no section
-	if body := get(t, h, "/ui/search?q=acme").Body.String(); strings.Contains(body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")], `aria-current`) {
-		t.Errorf("search should highlight no section")
-	}
-}
-
-// Sections that group several list pages show a sub-tab strip naming them,
-// the current page's tab marked; other sections show none.
-func TestSectionSubTabs(t *testing.T) {
-	h := projHandler(seedProjects(t))
-	specs := []string{`href="/ui/kits"`, `href="/ui/destinations"`, `href="/ui/model-specs"`}
-	for path, c := range map[string]struct {
-		tabs    []string
-		current string
-	}{
-		"/ui/kits":         {specs, "Kits"},
-		"/ui/destinations": {specs, "Destinations"},
-		"/ui/model-specs":  {specs, "Model-specs"},
-	} {
-		body := get(t, h, path).Body.String()
-		i := strings.Index(body, `<nav class="subtabs"`)
-		if i < 0 {
-			t.Errorf("%s: no sub-tab strip", path)
-			continue
-		}
-		strip := body[i : i+strings.Index(body[i:], "</nav>")]
-		for _, tab := range c.tabs {
-			if !strings.Contains(strip, tab) {
-				t.Errorf("%s: strip missing %s:\n%s", path, tab, strip)
-			}
-		}
-		if n := strings.Count(strip, `aria-current="page"`); n != 1 || !strings.Contains(strip, `aria-current="page">`+c.current+"<") {
-			t.Errorf("%s: want only %s current in the strip:\n%s", path, c.current, strip)
-		}
-	}
-	for _, path := range []string{"/ui/", "/ui/users", "/ui/projects/acme"} {
-		if strings.Contains(get(t, h, path).Body.String(), `class="subtabs"`) {
-			t.Errorf("%s should have no sub-tab strip", path)
-		}
-	}
-}
-
-// The top bar's nav rules are scoped to it: an unscoped nav rule would also
-// lay out the project tree's and the sub-tab strip's <nav>.
-func TestTopNavStylesAreScoped(t *testing.T) {
-	body := get(t, projHandler(seedProjects(t)), "/ui/projects/acme").Body.String()
-	if regexp.MustCompile(`(?m)^\s*nav[\s{a\[:]`).MatchString(body) {
-		t.Error("layout has an unscoped nav rule")
-	}
-	if !strings.Contains(body, ".topbar nav{display:flex") {
-		t.Error("top nav rules should be scoped to .topbar")
-	}
-}
-
 // Moved pages answer 301 to their new home, keeping the query string.
 func TestMovedPagesRedirect(t *testing.T) {
 	h := projHandler(seedProjects(t))
 	for from, to := range map[string]string{
-		"/ui/roles":          "/ui/projects",
+		"/ui/roles":          "/ui/",
 		"/ui/roles/acme/dev": "/ui/projects/acme/roles/dev",
-		"/ui/roles?x=1":      "/ui/projects?x=1",
+		"/ui/roles?x=1":      "/ui/?x=1",
+		"/ui/projects":       "/ui/",
 	} {
 		rec := get(t, h, from)
 		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
@@ -117,45 +29,6 @@ func TestMovedPagesRedirect(t *testing.T) {
 	}
 }
 
-// The tree opens the branch holding the current page and marks its node; the
-// breadcrumb follows the path, every segment a link.
-func TestProjectTreeMarksCurrent(t *testing.T) {
-	h := projHandler(seedProjects(t))
-	tree := func(body string) string {
-		return body[strings.Index(body, `class="ptree"`):strings.Index(body, "</nav>\n</details>")]
-	}
-	body := get(t, h, "/ui/projects/acme/roles/dev").Body.String()
-	tr := tree(body)
-	if !strings.Contains(tr, `<a class="mono" href="/ui/projects/acme/roles/dev" aria-current="page">dev</a>`) {
-		t.Errorf("role page: its leaf should be current:\n%s", tr)
-	}
-	if strings.Count(tr, "<details open>") != 1 || !strings.Contains(tr, `<details open>
-        <summary><a href="/ui/projects/acme/roles">Roles`) {
-		t.Errorf("role page: only the Roles branch should be open:\n%s", tr)
-	}
-	if !strings.Contains(body, `<summary>acme ▸ Roles ▸ dev</summary>`) {
-		t.Errorf("narrow-screen summary should name the current node")
-	}
-	for _, crumb := range []string{`<a href="/ui/projects">Projects</a>`, `<a href="/ui/projects/acme">acme</a>`,
-		`<a href="/ui/projects/acme/roles">Roles</a>`, `<a href="/ui/projects/acme/roles/dev">dev</a>`} {
-		if !strings.Contains(body[strings.Index(body, `class="crumbs"`):], crumb) {
-			t.Errorf("role page crumbs missing %s", crumb)
-		}
-	}
-
-	tr = tree(get(t, h, "/ui/projects/acme/members").Body.String())
-	if !strings.Contains(tr, `<a href="/ui/projects/acme/members" aria-current="page">Members</a>`) || strings.Contains(tr, "<details open>") {
-		t.Errorf("members page: Members current, no branch open:\n%s", tr)
-	}
-	tr = tree(get(t, h, "/ui/projects/acme").Body.String())
-	if !strings.Contains(tr, `<a href="/ui/projects/acme" aria-current="page">Overview</a>`) {
-		t.Errorf("project page: Overview current:\n%s", tr)
-	}
-	if !strings.Contains(tr, `>Agents <span class="count">(1)</span>`) {
-		t.Errorf("Agents should count acme's one live agent:\n%s", tr)
-	}
-}
-
 // A section's write answers with that section re-rendered for its #project
 // swap — not the whole project.
 func TestProjectEditsAnswerWithTheirSection(t *testing.T) {
@@ -185,32 +58,13 @@ func TestProjectEditsAnswerWithTheirSection(t *testing.T) {
 		if rec.Code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(body), `<div id="project">`) || !strings.Contains(body, c.section) {
 			t.Errorf("%s %s = %d, want its section %s:\n%s", c.method, c.path, rec.Code, c.section, body)
 		}
-		// the section, plus the tree only as an out-of-band swap
-		if strings.Contains(body, "<html") || strings.Count(body, `class="ptree"`) != 1 || !strings.Contains(body, `id="ptree" hx-swap-oob="true"`) {
-			t.Errorf("%s %s should answer with the section and an out-of-band tree", c.method, c.path)
+		// the section only: no page chrome, rail or tabs
+		if strings.Contains(body, "<html") || strings.Contains(body, `id="rail"`) || strings.Contains(body, `class="tabs"`) {
+			t.Errorf("%s %s should answer with the section only", c.method, c.path)
 		}
 	}
 }
 
-// A room write refreshes the tree (out of band): its Intercom branch lists the
-// project's rooms.
-func TestRoomWritesRefreshTheTree(t *testing.T) {
-	h := projHandler(seedProjects(t))
-	tree := func(body string) string {
-		i := strings.Index(body, `id="ptree"`)
-		if i < 0 {
-			t.Fatalf("no out-of-band tree:\n%s", body)
-		}
-		return body[i:]
-	}
-	if got := tree(post(t, h, "/ui/projects/acme/channels", url.Values{"name": {"ops"}, "service": {"discord"}, "ref": {"chan-ops"}}).Body.String()); !strings.Contains(got, ">ops<") {
-		t.Errorf("tree after adding ops lacks it:\n%s", got)
-	}
-	if got := tree(del(t, h, "/ui/projects/acme/channels/eng").Body.String()); strings.Contains(got, ">eng<") || !strings.Contains(got, ">ops<") {
-		t.Errorf("tree after removing eng:\n%s", got)
-	}
-}
-
 // A role is created from its project's Roles page (the project comes from the
 // page) and the response sends the browser to the new role's page.
 func TestCreateRoleFromProject(t *testing.T) {
diff --git a/internal/jam/adminui/polish_test.go b/internal/jam/adminui/polish_test.go
index 89f79e9..efb1f77 100644
--- a/internal/jam/adminui/polish_test.go
+++ b/internal/jam/adminui/polish_test.go
@@ -18,15 +18,16 @@ func TestNavMarksCurrentPage(t *testing.T) {
 		"/ui/kits":         `href="/ui/specs"`, // Specs
 		"/ui/destinations": `href="/ui/specs"`,
 		"/ui/users":        `href="/ui/users"`,
-		"/ui/projects":     `href="/ui/projects"`,
+		"/ui/intercom":     `href="/ui/intercom"`,
 	} {
 		body := get(t, h, path).Body.String()
-		nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")] // the top nav (sub-tabs mark their own)
-		if !strings.Contains(nav, href+` aria-current="page"`) {
-			t.Errorf("%s: nav should mark %s as current", path, href)
+		tabs := body[strings.Index(body, `<nav class="tabs"`):]
+		tabs = tabs[:strings.Index(tabs, "</nav>")] // Jam's tabs (sub-tabs mark their own)
+		if !strings.Contains(tabs, href+` aria-current="page"`) {
+			t.Errorf("%s: tabs should mark %s as current", path, href)
 		}
-		if n := strings.Count(nav, ` aria-current="page">`); n != 1 {
-			t.Errorf("%s: %d nav items marked current, want 1", path, n)
+		if n := strings.Count(tabs, ` aria-current="page">`); n != 1 {
+			t.Errorf("%s: %d tabs marked current, want 1", path, n)
 		}
 	}
 }
@@ -34,7 +35,7 @@ func TestNavMarksCurrentPage(t *testing.T) {
 // Every page carries the flash region and the htmx error hook, so a 4xx/5xx
 // write response is shown to the operator instead of silently dropped.
 func TestLayoutShipsFlashAndErrorHook(t *testing.T) {
-	body := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/projects").Body.String()
+	body := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/").Body.String()
 	for _, want := range []string{`id="flash"`, "htmx:responseError", `href="/ui/static/jam.css`} {
 		if !strings.Contains(body, want) {
 			t.Errorf("layout missing %q", want)
diff --git a/internal/jam/adminui/projects_test.go b/internal/jam/adminui/projects_test.go
index 73d27e5..6fa1ed4 100644
--- a/internal/jam/adminui/projects_test.go
+++ b/internal/jam/adminui/projects_test.go
@@ -66,34 +66,44 @@ func projHandler(store jam.Store) http.Handler {
 	return adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil)
 }
 
-func TestProjectsTabFollowsDashboard(t *testing.T) {
-	body := get(t, projHandler(seedProjects(t)), "/ui/projects").Body.String()
-	dash, proj, studios := strings.Index(body, `href="/ui/"`), strings.Index(body, `href="/ui/projects"`), strings.Index(body, `href="/ui/agents"`)
-	if !(dash >= 0 && dash < proj && proj < studios) {
-		t.Errorf("nav order: dashboard@%d projects@%d studios@%d", dash, proj, studios)
+// The rail lists Jam, then every project, on every page; a project page
+// selects its project, a Jam page selects Jam.
+func TestRailListsProjects(t *testing.T) {
+	h := projHandler(seedProjects(t))
+	for path, current := range map[string]string{
+		"/ui/":                        `<a class="jam" href="/ui/" aria-current="page">`,
+		"/ui/users":                   `<a class="jam" href="/ui/" aria-current="page">`,
+		"/ui/projects/acme":           `<a href="/ui/projects/acme" aria-current="page"><span class="name">acme</span>`,
+		"/ui/projects/beta/roles":     `<a href="/ui/projects/beta" aria-current="page"><span class="name">beta</span>`,
+		"/ui/projects/acme/roles/dev": `<a href="/ui/projects/acme" aria-current="page"><span class="name">acme</span>`,
+	} {
+		body := get(t, h, path).Body.String()
+		rail := body[strings.Index(body, `<aside id="rail"`):strings.Index(body, "</aside>")]
+		for _, want := range []string{`href="/ui/projects/acme"`, `href="/ui/projects/beta"`, `href="/ui/projects/default"`, current, `hx-post="/ui/projects"`} {
+			if !strings.Contains(rail, want) {
+				t.Errorf("%s: rail missing %s", path, want)
+			}
+		}
+		if n := strings.Count(rail, `aria-current="page"`); n != 1 {
+			t.Errorf("%s: %d rail entries current, want 1", path, n)
+		}
 	}
-	if !strings.Contains(body, `href="/ui/projects" aria-current="page">Projects`) {
-		t.Errorf("Projects tab should be current on /ui/projects")
+	// search selects nothing
+	body := get(t, h, "/ui/search?q=acme").Body.String()
+	if rail := body[strings.Index(body, `<aside id="rail"`):strings.Index(body, "</aside>")]; strings.Contains(rail, "aria-current") {
+		t.Error("search should select no scope")
 	}
 }
 
-func TestProjectsList(t *testing.T) {
-	body := get(t, projHandler(seedProjects(t)), "/ui/projects").Body.String()
-	for _, want := range []string{
-		`href="/ui/projects/acme"`, `href="/ui/projects/beta"`, `href="/ui/projects/default"`,
-		`data-id="acme" data-roles="2" data-actors="2" data-studios="1"`,
-		"discord",
-		`hx-delete="/ui/projects/beta"`, // empty: removable
-	} {
-		if !strings.Contains(body, want) {
-			t.Errorf("projects list missing %q", want)
-		}
-	}
-	if strings.Contains(body, `hx-delete="/ui/projects/acme"`) {
-		t.Errorf("acme is referenced; its delete should be disabled")
+// A project's delete is disabled while something references it, saying what.
+func TestProjectDeleteGuard(t *testing.T) {
+	h := projHandler(seedProjects(t))
+	if body := get(t, h, "/ui/projects/beta").Body.String(); !strings.Contains(body, `hx-delete="/ui/projects/beta"`) {
+		t.Errorf("beta is empty: removable")
 	}
-	if !strings.Contains(body, "member(s)") {
-		t.Errorf("a disabled delete should say what still references the project")
+	body := get(t, h, "/ui/projects/acme").Body.String()
+	if strings.Contains(body, `hx-delete="/ui/projects/acme"`) || !strings.Contains(body, "member(s)") {
+		t.Errorf("acme is referenced; its delete should be disabled and say why")
 	}
 }
 
@@ -129,17 +139,14 @@ func TestCreateAndDeleteProject(t *testing.T) {
 	}
 }
 
-// Every project page shows the tree; each section shows its own content and
+// Every project page shows the project's tabs; each section shows its own content and
 // nothing from another project.
 func TestProjectPage(t *testing.T) {
 	h := projHandler(seedProjects(t))
-	tree := []string{
-		`aria-current="page">Projects`, `class="ptree"`,
+	tabs := []string{
+		`<div class="scope-title">acme</div>`,
 		`href="/ui/projects/acme/members"`, `href="/ui/projects/acme/agents"`, `href="/ui/projects/acme/roles"`,
 		`href="/ui/projects/acme/intercom"`, `href="/ui/projects/acme/escalation"`,
-		`href="/ui/projects/acme/roles/dev"`, `href="/ui/projects/acme/roles/ops"`, // roles under Roles
-		"studio-acme", // its live agent under Agents
-		">eng<",       // its room under Intercom
 	}
 	for path, wants := range map[string][]string{
 		"/ui/projects/acme":            {"<h1>acme</h1>", "discord"},
@@ -154,14 +161,13 @@ func TestProjectPage(t *testing.T) {
 			t.Fatalf("%s = %d", path, rec.Code)
 		}
 		body := rec.Body.String()
-		for _, want := range append(wants, tree...) {
+		for _, want := range append(wants, tabs...) {
 			if !strings.Contains(body, want) {
 				t.Errorf("%s missing %q", path, want)
 			}
 		}
-		// the section itself (the tree lists studio-acme on every page)
 		if strings.HasSuffix(path, "/agents") {
-			if sec := body[strings.Index(body, `<div id="project">`):]; !strings.Contains(sec, `href="/ui/agents/studio-acme"`) {
+			if sec := body[strings.Index(body, `<div id="project">`):]; !strings.Contains(sec, `href="/ui/agents/studio-acme?project=acme"`) {
 				t.Errorf("%s: studios table missing studio-acme", path)
 			}
 		}
diff --git a/internal/jam/adminui/role_detail_test.go b/internal/jam/adminui/role_detail_test.go
index f405719..3e52f07 100644
--- a/internal/jam/adminui/role_detail_test.go
+++ b/internal/jam/adminui/role_detail_test.go
@@ -87,7 +87,8 @@ func TestRoleDetailShowsEverything(t *testing.T) {
 		"nightly", "run the nightly sweep",
 		"holder-plain", "holder-ovr", "override",
 		"studio-of-review",
-		`aria-current="page">Projects`, // a role lives in its project
+		`<a href="/ui/projects/acme" aria-current="page"><span class="name">acme</span>`, // its project is selected
+		`href="/ui/projects/acme/roles" aria-current="page">Roles`,                       // under Roles
 	} {
 		if !strings.Contains(body, want) {
 			t.Errorf("role detail missing %q", want)
diff --git a/internal/jam/adminui/search_test.go b/internal/jam/adminui/search_test.go
index 849ff21..d47d3a0 100644
--- a/internal/jam/adminui/search_test.go
+++ b/internal/jam/adminui/search_test.go
@@ -120,7 +120,7 @@ func TestSearchShortQueryAndNoMatches(t *testing.T) {
 }
 
 func TestSearchBoxOnEveryPage(t *testing.T) {
-	body := get(t, searchFixture(t), "/ui/projects").Body.String()
+	body := get(t, searchFixture(t), "/ui/").Body.String()
 	for _, want := range []string{`action="/ui/search"`, `id="q-top"`, `name="go" value="1"`} {
 		if !strings.Contains(body, want) {
 			t.Errorf("topbar search missing %q", want)
diff --git a/internal/jam/adminui/users_test.go b/internal/jam/adminui/users_test.go
index 1e01b2a..67b3ef9 100644
--- a/internal/jam/adminui/users_test.go
+++ b/internal/jam/adminui/users_test.go
@@ -193,6 +193,7 @@ func TestUserPageGuards(t *testing.T) {
 		t.Fatal(err)
 	}
 	body := get(t, h, "/ui/users/"+string(bob.ID)).Body.String()
+	body = body[strings.Index(body, `id="flash"`):] // the page content: the rail has its own New project form
 	if !strings.Contains(body, "removed") || strings.Contains(body, "hx-post=") || strings.Contains(body, "hx-delete=") {
 		t.Errorf("a removed user's page must be read-only:\n%s", body)
 	}
````

- [ ] **Step 2: RED** — `go test ./internal/jam/adminui/` fails (11 tests).

- [ ] **Step 3: Apply the code patch**

````diff
diff --git a/internal/jam/adminui/adminui.go b/internal/jam/adminui/adminui.go
index 70df988..d0563d0 100644
--- a/internal/jam/adminui/adminui.go
+++ b/internal/jam/adminui/adminui.go
@@ -11,6 +11,7 @@ import (
 	"html/template"
 	"log/slog"
 	"net/http"
+	"net/url"
 	"time"
 
 	"github.com/aethons-tools/cove/internal/jam"
@@ -21,28 +22,37 @@ import (
 //go:embed templates/*.html
 var files embed.FS
 
-// page holds one parsed template set (layout + that page's content). Each set's
-// full page is rendered via ExecuteTemplate(w, "layout", data). The first
-// argument is the page's top-nav section (see nav.go), which the layout
-// highlights; mustParseTab also names the section sub-tab it sits under.
-var pages = map[string]*template.Template{
-	"dashboard":    mustParse(navDashboard, "coves.html", "context_panel.html", "dashboard.html"),
-	"agents":       mustParse(navAgents, "agents.html"),
-	"users":        mustParse(navUsers, "users.html"),
-	"user":         mustParse(navUsers, "user.html"),
-	"kits":         mustParseTab(navSpecs, "/ui/kits", "kits.html"),
-	"destinations": mustParseTab(navSpecs, "/ui/destinations", "dest_fields.html", "destinations.html"),
-	"intercom":     mustParse(navIntercom, "squawks.html", "intercom.html"),
-	"session":      mustParse(navAgents, "session.html"),
-	"role":         mustParse(navProjects, "coves.html", "context_panel.html", "projtree.html", "role.html"),
-	"destination":  mustParseTab(navSpecs, "/ui/destinations", "dest_fields.html", "destination.html"),
-	"model-specs":  mustParseTab(navSpecs, "/ui/model-specs", "model_spec_fields.html", "model_specs.html"),
-	"model-spec":   mustParseTab(navSpecs, "/ui/model-specs", "model_spec_fields.html", "model_spec.html"),
-	"kit":          mustParseTab(navSpecs, "/ui/kits", "kit.html"),
-	"projects":     mustParse(navProjects, "projects.html"),
-	"project":      mustParse(navProjects, "coves.html", "context_panel.html", "squawks.html", "projtree.html", "project.html"),
-	"agent":        mustParse(navAgents, "agent.html"),
-	"search":       mustParse(navNone, "search.html"),
+// page is one parsed template set (layout + that page's content) and where the
+// page sits in the frame. The set is a master: each render executes a clone
+// with the request's frame bound (renderStatus), so the master never executes.
+type page struct {
+	t    *template.Template
+	meta pageMeta
+}
+
+var (
+	jam_      = func(tab string) pageMeta { return pageMeta{Kind: scopeJam, Tab: tab} }
+	specsPage = func(sub string) pageMeta { return pageMeta{Kind: scopeJam, Tab: "specs", SubTab: sub} }
+	projPage  = pageMeta{Kind: scopeProject}
+)
+
+var pages = map[string]page{
+	"dashboard":    mustParse(jam_("dashboard"), "attention.html", "coves.html", "context_panel.html", "dashboard.html"),
+	"agents":       mustParse(jam_("agents"), "agents.html"),
+	"users":        mustParse(jam_("users"), "users.html"),
+	"user":         mustParse(jam_("users"), "user.html"),
+	"kits":         mustParse(specsPage("/ui/kits"), "kits.html"),
+	"destinations": mustParse(specsPage("/ui/destinations"), "dest_fields.html", "destinations.html"),
+	"intercom":     mustParse(jam_("intercom"), "squawks.html", "intercom.html"),
+	"session":      mustParse(jam_("agents"), "session.html"),
+	"role":         mustParse(projPage, "coves.html", "context_panel.html", "role.html"),
+	"destination":  mustParse(specsPage("/ui/destinations"), "dest_fields.html", "destination.html"),
+	"model-specs":  mustParse(specsPage("/ui/model-specs"), "model_spec_fields.html", "model_specs.html"),
+	"model-spec":   mustParse(specsPage("/ui/model-specs"), "model_spec_fields.html", "model_spec.html"),
+	"kit":          mustParse(specsPage("/ui/kits"), "kit.html"),
+	"project":      mustParse(projPage, "attention.html", "coves.html", "context_panel.html", "squawks.html", "project.html"),
+	"agent":        mustParse(jam_("agents"), "agent.html"),
+	"search":       mustParse(pageMeta{}, "search.html"),
 }
 
 // roleRow is one project/role pair flattened for the roles table.
@@ -70,22 +80,13 @@ func roleRows(store jam.Store) []roleRow {
 	return out
 }
 
-func mustParse(section navSection, names ...string) *template.Template {
-	return mustParseTab(section, "", names...)
-}
-
-// mustParseTab is mustParse for a page under one of section's sub-tabs (tab is
-// that tab's Href; see navSubTabs).
-func mustParseTab(section navSection, tab string, names ...string) *template.Template {
+func mustParse(meta pageMeta, names ...string) page {
 	paths := make([]string, 0, len(names)+1)
 	paths = append(paths, "templates/layout.html")
 	for _, n := range names {
 		paths = append(paths, "templates/"+n)
 	}
-	return template.Must(template.New("").Funcs(funcs).Funcs(template.FuncMap{
-		"navSection": func() navSection { return section },
-		"subTabs":    func() []subTab { return subTabsFor(section, tab) },
-	}).ParseFS(files, paths...))
+	return page{t: template.Must(template.New("").Funcs(funcs).ParseFS(files, paths...)), meta: meta}
 }
 
 // funcs are the template helpers shared by every page.
@@ -97,9 +98,13 @@ var funcs = template.FuncMap{
 		}
 		return fmtDur(d)
 	},
-	"roleURL":    roleURL,
-	"agentURL":   agentURL,
-	"navItems":   func() []navItem { return navItems },
+	"roleURL":  roleURL,
+	"agentURL": agentURL,
+	// frame, attn and agentHref are bound per render (frameFuncs); these are
+	// the defaults a fragment renders with.
+	"frame":      func() frame { return frame{} },
+	"attn":       func(tab, subject string) *attnItem { return nil },
+	"agentHref":  func(id, suffix string) string { return agentURL(id) + suffix },
 	"hl":         highlight,
 	"stylesheet": func() string { return uiassets.StylesheetHref("/ui/static/") },
 	"destURL":    destURL,
@@ -158,7 +163,7 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 			renderFragment(w, "dashboard", "coves-table", map[string]any{"Coves": jam.CoveSummaries(store, sup)})
 			return
 		}
-		render(w, "dashboard", map[string]any{
+		render(w, r, "dashboard", map[string]any{
 			"Title":      "Dashboard",
 			"Coves":      jam.CoveSummaries(store, sup),
 			"Stats":      dashboardStats(store),
@@ -167,13 +172,17 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 	})
 
 	registerAgents(mux, store, sup, canEdit)
+	// The rail, re-fetched by its own poll so badges stay live on every page.
+	mux.HandleFunc("GET /ui/rail", func(w http.ResponseWriter, r *http.Request) {
+		renderFragment(w, "dashboard", "rail", buildRail(store, r.URL.Query().Get("scope"), attention(store, sup)))
+	})
 	// Specs has no page of its own: it opens on its first sub-tab.
 	mux.HandleFunc("GET /ui/specs", func(w http.ResponseWriter, r *http.Request) {
 		http.Redirect(w, r, "/ui/kits", http.StatusFound)
 	})
 	// The global roles list and role pages moved into the project tree.
 	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
-		redirect(w, r, "/ui/projects")
+		redirect(w, r, "/ui/")
 	})
 	mux.HandleFunc("GET /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
 		redirect(w, r, roleURL(r.PathValue("project"), r.PathValue("name")))
@@ -201,63 +210,88 @@ func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.S
 	registerJamContext(mux, store, log, guardWrite)
 	registerUsers(mux, store, log, guardWrite)
 
-	return mux
+	src := frameSource{store: store, img: sup}
+	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
+		mux.ServeHTTP(w, withFrameSource(r, src))
+	})
 }
 
-// render executes the named page's "layout" template.
-func render(w http.ResponseWriter, page string, data any) {
-	renderStatus(w, http.StatusOK, page, data)
+// render executes the named page's "layout" template inside its frame.
+func render(w http.ResponseWriter, r *http.Request, name string, data any) {
+	renderStatus(w, r, http.StatusOK, name, data)
 }
 
-// renderStatus is render with an explicit status code.
-func renderStatus(w http.ResponseWriter, status int, page string, data any) {
-	t, ok := pages[page]
+// renderStatus is render with an explicit status code. It executes a clone of
+// the page's master set with the request's frame bound to the frame, attn and
+// agentHref template funcs.
+func renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
+	p, ok := pages[name]
 	if !ok {
 		http.Error(w, "unknown page", http.StatusInternalServerError)
 		return
 	}
-	w.Header().Set("Content-Type", "text/html; charset=utf-8")
-	w.WriteHeader(status)
-	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
-		http.Error(w, err.Error(), http.StatusInternalServerError)
+	var f frame
+	if src, ok := frameSourceOf(r); ok {
+		f = frameFor(r, p.meta, src)
 	}
-}
-
-// namedFragment is one named template and its data, for renderFragments.
-type namedFragment struct {
-	tmpl string
-	data any
-}
-
-// renderFragments executes several named templates of one page, in order,
-// without the page chrome — a swap target followed by its out-of-band swaps.
-func renderFragments(w http.ResponseWriter, page string, fs ...namedFragment) {
-	t, ok := pages[page]
-	if !ok {
-		http.Error(w, "unknown page", http.StatusInternalServerError)
+	t, err := p.t.Clone()
+	if err != nil {
+		http.Error(w, err.Error(), http.StatusInternalServerError)
 		return
 	}
+	t.Funcs(frameFuncs(f))
 	var buf bytes.Buffer
-	for _, f := range fs {
-		if err := t.ExecuteTemplate(&buf, f.tmpl, f.data); err != nil {
-			http.Error(w, err.Error(), http.StatusInternalServerError)
-			return
-		}
+	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
+		http.Error(w, err.Error(), http.StatusInternalServerError)
+		return
 	}
 	w.Header().Set("Content-Type", "text/html; charset=utf-8")
+	w.WriteHeader(status)
 	_, _ = w.Write(buf.Bytes())
 }
 
+// frameFuncs binds f: frame returns it; attn finds the item a row is about
+// (tab and subject, over every scope); agentHref links an agent page, keeping
+// a project scope.
+func frameFuncs(f frame) template.FuncMap {
+	return template.FuncMap{
+		"frame": func() frame { return f },
+		"attn": func(tab, subject string) *attnItem {
+			for i := range f.All {
+				if f.All[i].Tab == tab && f.All[i].Subject == subject {
+					return &f.All[i]
+				}
+			}
+			return nil
+		},
+		"agentHref": func(id, suffix string) string {
+			h := agentURL(id) + suffix
+			if f.Kind == scopeProject {
+				h += "?project=" + url.QueryEscape(f.Project)
+			}
+			return h
+		},
+	}
+}
+
 // renderFragment executes a single named template (e.g. an htmx-swapped table)
-// without the page chrome.
-func renderFragment(w http.ResponseWriter, page, tmpl string, data any) {
-	t, ok := pages[page]
+// without the page chrome or frame.
+func renderFragment(w http.ResponseWriter, name, tmpl string, data any) {
+	p, ok := pages[name]
 	if !ok {
 		http.Error(w, "unknown page", http.StatusInternalServerError)
 		return
 	}
-	w.Header().Set("Content-Type", "text/html; charset=utf-8")
-	if err := t.ExecuteTemplate(w, tmpl, data); err != nil {
+	t, err := p.t.Clone()
+	if err != nil {
+		http.Error(w, err.Error(), http.StatusInternalServerError)
+		return
+	}
+	var buf bytes.Buffer
+	if err := t.ExecuteTemplate(&buf, tmpl, data); err != nil {
 		http.Error(w, err.Error(), http.StatusInternalServerError)
+		return
 	}
+	w.Header().Set("Content-Type", "text/html; charset=utf-8")
+	_, _ = w.Write(buf.Bytes())
 }
diff --git a/internal/jam/adminui/agent.go b/internal/jam/adminui/agent.go
index c4dc964..8581ed7 100644
--- a/internal/jam/adminui/agent.go
+++ b/internal/jam/adminui/agent.go
@@ -115,9 +115,9 @@ func registerAgent(mux *http.ServeMux, store jam.Store, msgs SquawkReader, sess
 		d, ok := buildAgentDetail(store, msgs, sess, r.PathValue("id"), canEdit)
 		if !ok {
 			d.NotFound = true
-			renderStatus(w, http.StatusNotFound, "agent", d)
+			renderStatus(w, r, http.StatusNotFound, "agent", d)
 			return
 		}
-		render(w, "agent", d)
+		render(w, r, "agent", d)
 	})
 }
diff --git a/internal/jam/adminui/agents.go b/internal/jam/adminui/agents.go
index 705924f..eea924b 100644
--- a/internal/jam/adminui/agents.go
+++ b/internal/jam/adminui/agents.go
@@ -120,7 +120,7 @@ func registerAgents(mux *http.ServeMux, store jam.Store, img jam.ImageResolver,
 			renderFragment(w, "agents", "agents-table", data)
 			return
 		}
-		render(w, "agents", data)
+		render(w, r, "agents", data)
 	})
 	// The studio and actor lists and the studio page became the agents list
 	// and the agent page.
diff --git a/internal/jam/adminui/attention.go b/internal/jam/adminui/attention.go
new file mode 100644
index 0000000..9a63866
--- /dev/null
+++ b/internal/jam/adminui/attention.go
@@ -0,0 +1,178 @@
+package adminui
+
+import (
+	"fmt"
+	"maps"
+	"net/url"
+	"slices"
+	"strings"
+
+	"github.com/aethons-tools/cove/internal/jam"
+	"github.com/aethons-tools/cove/internal/studio"
+)
+
+// attnKind is what kind of trouble an attention item is. broken is red; stale
+// and config are amber.
+type attnKind string
+
+const (
+	attnBroken attnKind = "broken" // a studio is lost, terminating or failing egress re-apply
+	attnStale  attnKind = "stale"  // a studio runs an out-of-date image or connector
+	attnConfig attnKind = "config" // configuration that names something that isn't there
+)
+
+// attnItem is one thing that needs an operator's attention. Scope is "" for
+// Jam, else the project's name; Tab is the scope tab that owns it; Subject
+// keys it for row flags (an agent id, "project/role", "kit:<name>",
+// "dest:<name>", "escalation:<category>:<target>").
+type attnItem struct {
+	Kind    attnKind
+	Scope   string
+	Tab     string
+	Subject string
+	Href    string
+	Why     string
+}
+
+// attention lists everything that needs attention, Jam's items first, then
+// each project's in name order — the one source for the rail, tab and row
+// badges and the Needs attention cards.
+func attention(store jam.Store, img jam.ImageResolver) []attnItem {
+	var out []attnItem
+
+	// Jam: kits whose current version doesn't parse; destinations in a
+	// connector conflict.
+	for _, k := range store.ListKits() {
+		if _, err := studio.ParseStudioKit([]byte(k.Versions[k.Current])); err != nil {
+			out = append(out, attnItem{Kind: attnConfig, Tab: "specs", Subject: "kit:" + k.Name, Href: kitURL(k.Name),
+				Why: fmt.Sprintf("kit %s v%d does not parse", k.Name, k.Current)})
+		}
+	}
+	for _, d := range store.ListDestinations() {
+		if dd, ok := buildDestDetail(store, d.Name); ok && len(dd.Conflicts) > 0 {
+			out = append(out, attnItem{Kind: attnConfig, Tab: "specs", Subject: "dest:" + d.Name, Href: destURL(d.Name),
+				Why: "destination " + d.Name + " conflicts with another in " + strings.Join(dd.ConflictRoles(), ", ")})
+		}
+	}
+
+	// Projects: studios (one item per agent, its worst trouble), roles naming
+	// a missing kit or model-spec, escalation targets naming nobody.
+	byProject := map[string][]attnItem{}
+	for _, c := range jam.CoveSummaries(store, img) {
+		p := orDefaultProject(c.Project)
+		inst, _ := store.GetInstance(c.ID)
+		var broken, stale []string
+		switch jam.Phase(c.Phase) {
+		case jam.PhaseLost, jam.PhaseTerminating:
+			broken = append(broken, c.Phase)
+		}
+		if inst.EgressFailures > 0 {
+			broken = append(broken, fmt.Sprintf("egress re-apply failing (%d)", inst.EgressFailures))
+		}
+		if c.Image == "stale" {
+			stale = append(stale, "image stale")
+		}
+		if c.Connector == "stale" {
+			stale = append(stale, "connector stale")
+		}
+		item := attnItem{Scope: p, Tab: "agents", Subject: c.ID, Href: agentURL(c.ID) + "?project=" + url.QueryEscape(p)}
+		switch {
+		case len(broken) > 0:
+			item.Kind, item.Why = attnBroken, c.ID+": "+strings.Join(append(broken, stale...), ", ")
+		case len(stale) > 0:
+			item.Kind, item.Why = attnStale, c.ID+": "+strings.Join(stale, ", ")
+		default:
+			continue
+		}
+		byProject[p] = append(byProject[p], item)
+	}
+	for _, p := range store.ListProjects() {
+		for _, r := range store.ListRoles(p) {
+			var why []string
+			if r.Kit != "" {
+				if _, ok := store.GetKit(r.Kit); !ok {
+					why = append(why, "kit "+r.Kit+" not found")
+				}
+			}
+			if r.ModelSpec != "" {
+				if _, ok := store.GetModelSpec(r.ModelSpec); !ok {
+					why = append(why, "model-spec "+r.ModelSpec+" not found")
+				}
+			}
+			if why != nil {
+				byProject[p] = append(byProject[p], attnItem{Kind: attnConfig, Scope: p, Tab: "roles", Subject: p + "/" + r.Name,
+					Href: roleURL(p, r.Name), Why: "role " + r.Name + ": " + strings.Join(why, ", ")})
+			}
+		}
+		proj, ok := store.GetProject(p)
+		if !ok {
+			continue
+		}
+		known := knownTargets(jam.MembersOf(store, proj.ID), jam.ListRooms(store, proj))
+		chains := map[string][]jam.EscalationTier{"": proj.Escalation}
+		maps.Copy(chains, proj.EscalationByCategory)
+		for _, cat := range slices.Sorted(maps.Keys(chains)) {
+			for _, t := range chain(cat, chains[cat], known).Tiers {
+				for _, tg := range t.Targets {
+					if !tg.Unknown {
+						continue
+					}
+					where := "default chain"
+					if cat != "" {
+						where = "chain " + cat
+					}
+					byProject[p] = append(byProject[p], attnItem{Kind: attnConfig, Scope: p, Tab: "escalation",
+						Subject: "escalation:" + cat + ":" + tg.Text, Href: projectSectionURL(p, sectionEscalation),
+						Why: "escalation target " + tg.Text + " (" + where + ") names nobody here"})
+				}
+			}
+		}
+	}
+	for _, p := range slices.Sorted(maps.Keys(byProject)) {
+		out = append(out, byProject[p]...)
+	}
+	return out
+}
+
+// attnBadge is a count of items with the worst kind's color and a breakdown
+// for its hover title ("1 broken · 2 out of date").
+type attnBadge struct {
+	N     int
+	Red   bool
+	Title string
+}
+
+func badgeOf(items []attnItem) attnBadge {
+	var broken, stale, config int
+	for _, it := range items {
+		switch it.Kind {
+		case attnBroken:
+			broken++
+		case attnStale:
+			stale++
+		case attnConfig:
+			config++
+		}
+	}
+	var parts []string
+	for _, p := range []struct {
+		n    int
+		what string
+	}{{broken, "broken"}, {stale, "out of date"}, {config, "config"}} {
+		if p.n > 0 {
+			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
+		}
+	}
+	return attnBadge{N: len(items), Red: broken > 0, Title: strings.Join(parts, " · ")}
+}
+
+// inScope keeps the items of one scope ("" = Jam), optionally of one tab.
+func inScope(items []attnItem, scope, tab string) []attnItem {
+	var out []attnItem
+	for _, it := range items {
+		if it.Scope == scope && (tab == "" || it.Tab == tab) {
+			out = append(out, it)
+		}
+	}
+	return out
+}
diff --git a/internal/jam/adminui/dest_detail.go b/internal/jam/adminui/dest_detail.go
index 5242668..edb6556 100644
--- a/internal/jam/adminui/dest_detail.go
+++ b/internal/jam/adminui/dest_detail.go
@@ -232,16 +232,16 @@ func registerDestinations(mux *http.ServeMux, store jam.Store, log *slog.Logger,
 	mux.HandleFunc("GET /ui/destinations", func(w http.ResponseWriter, r *http.Request) {
 		data := destTableData(store)
 		data["Title"] = "Destinations"
-		render(w, "destinations", data)
+		render(w, r, "destinations", data)
 	})
 
 	mux.HandleFunc("GET /ui/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
 		d, ok := buildDestDetail(store, r.PathValue("name"))
 		if !ok {
-			renderStatus(w, http.StatusNotFound, "destination", destDetail{Title: "Destinations", NotFound: true, NotFoundFor: r.PathValue("name")})
+			renderStatus(w, r, http.StatusNotFound, "destination", destDetail{Title: "Destinations", NotFound: true, NotFoundFor: r.PathValue("name")})
 			return
 		}
-		render(w, "destination", d)
+		render(w, r, "destination", d)
 	})
 
 	// Create only: an existing destination is edited on its page, where every
diff --git a/internal/jam/adminui/intercom.go b/internal/jam/adminui/intercom.go
index 6c17ff3..29f9bd3 100644
--- a/internal/jam/adminui/intercom.go
+++ b/internal/jam/adminui/intercom.go
@@ -207,7 +207,7 @@ func handleIntercom(w http.ResponseWriter, r *http.Request, msgs SquawkReader) {
 	data := squawksData{Title: "Intercom", Configured: msgs != nil}
 	data.Legacy = q.Get("log") == "legacy"
 	if msgs == nil {
-		render(w, "intercom", data)
+		render(w, r, "intercom", data)
 		return
 	}
 	data.Project = strings.TrimSpace(q.Get("project"))
@@ -238,7 +238,7 @@ func handleIntercom(w http.ResponseWriter, r *http.Request, msgs SquawkReader) {
 	// A bad date still counts as filtering (the old view did): the "no
 	// match" empty state.
 	data.Filtered = data.Filtered || data.Since != "" || data.Until != ""
-	render(w, "intercom", data)
+	render(w, r, "intercom", data)
 }
 
 // matchesParticipant reports whether p is m's author or channel (a legacy
diff --git a/internal/jam/adminui/kits.go b/internal/jam/adminui/kits.go
index eae70d6..3c8335d 100644
--- a/internal/jam/adminui/kits.go
+++ b/internal/jam/adminui/kits.go
@@ -44,7 +44,7 @@ func registerKits(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWr
 	mux.HandleFunc("GET /ui/kits", func(w http.ResponseWriter, r *http.Request) {
 		data := kitTableData(store)
 		data["Title"] = "Kits"
-		render(w, "kits", data)
+		render(w, r, "kits", data)
 	})
 
 	mux.HandleFunc("GET /ui/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
@@ -52,10 +52,10 @@ func registerKits(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWr
 		diff, _ := strconv.Atoi(r.URL.Query().Get("diff"))
 		d, ok := buildKitDetail(store, r.PathValue("name"), view, diff)
 		if !ok {
-			renderStatus(w, http.StatusNotFound, "kit", kitDetail{Title: "Kits", NotFound: true, NotFoundFor: r.PathValue("name")})
+			renderStatus(w, r, http.StatusNotFound, "kit", kitDetail{Title: "Kits", NotFound: true, NotFoundFor: r.PathValue("name")})
 			return
 		}
-		render(w, "kit", d)
+		render(w, r, "kit", d)
 	})
 
 	// New kit: create only — further versions are pushed on the kit's page.
diff --git a/internal/jam/adminui/model_specs.go b/internal/jam/adminui/model_specs.go
index 33b32e4..b078a67 100644
--- a/internal/jam/adminui/model_specs.go
+++ b/internal/jam/adminui/model_specs.go
@@ -284,16 +284,16 @@ func registerModelSpecs(mux *http.ServeMux, u specUI, log *slog.Logger, guardWri
 	mux.HandleFunc("GET /ui/model-specs", func(w http.ResponseWriter, r *http.Request) {
 		data := u.tableData()
 		data["Title"] = "Model-specs"
-		render(w, "model-specs", data)
+		render(w, r, "model-specs", data)
 	})
 
 	mux.HandleFunc("GET /ui/model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
 		m, ok := u.store.GetModelSpec(r.PathValue("name"))
 		if !ok {
-			renderStatus(w, http.StatusNotFound, "model-spec", specDetail{Title: "Model-specs", NotFound: true, NotFoundFor: r.PathValue("name")})
+			renderStatus(w, r, http.StatusNotFound, "model-spec", specDetail{Title: "Model-specs", NotFound: true, NotFoundFor: r.PathValue("name")})
 			return
 		}
-		render(w, "model-spec", u.detail(m))
+		render(w, r, "model-spec", u.detail(m))
 	})
 
 	// Create only: an existing model-spec is edited on its page.
diff --git a/internal/jam/adminui/nav.go b/internal/jam/adminui/nav.go
index 304df76..c66988b 100644
--- a/internal/jam/adminui/nav.go
+++ b/internal/jam/adminui/nav.go
@@ -1,63 +1,177 @@
 package adminui
 
-import "net/http"
+import (
+	"context"
+	"net/http"
+	"net/url"
+	"strings"
 
-// navSection is a top-nav section. Each page's template set is bound to one
-// (mustParse), and the layout highlights the nav item whose Section matches —
-// so a detail page highlights its section whatever its title.
-type navSection string
+	"github.com/aethons-tools/cove/internal/jam"
+)
+
+// scopeKind is what a page belongs to in the rail: Jam, one project, or —
+// search — neither.
+type scopeKind int
 
 const (
-	navNone      navSection = "" // highlights nothing (search)
-	navDashboard navSection = "dashboard"
-	navProjects  navSection = "projects"
-	navUsers     navSection = "users"
-	navAgents    navSection = "agents"
-	navSpecs     navSection = "specs"
-	navIntercom  navSection = "intercom"
+	scopeNone scopeKind = iota
+	scopeJam
+	scopeProject
 )
 
-// navItem is one top-nav link.
-type navItem struct {
-	Section navSection
-	Label   string
-	Href    string
+// pageMeta places a page in the frame: its scope kind, the scope tab it sits
+// under, and — for Specs — the sub-tab (an Href). A project page's project and
+// tab come from its URL (frameFor).
+type pageMeta struct {
+	Kind   scopeKind
+	Tab    string
+	SubTab string
 }
 
-// navItems is the top nav, in order. Specs lands on its first sub-tab (Kits).
-var navItems = []navItem{
-	{navDashboard, "Dashboard", "/ui/"},
-	{navProjects, "Projects", "/ui/projects"},
-	{navUsers, "Users", "/ui/users"},
-	{navAgents, "Agents", "/ui/agents"},
-	{navSpecs, "Specs", "/ui/specs"},
-	{navIntercom, "Intercom", "/ui/intercom"},
+// tabDef is one tab of a scope.
+type tabDef struct{ Key, Label, Href string }
+
+// jamTabs are Jam's tabs, in order.
+var jamTabs = []tabDef{
+	{"dashboard", "Dashboard", "/ui/"},
+	{"agents", "Agents", "/ui/agents"},
+	{"users", "Users", "/ui/users"},
+	{"specs", "Specs", "/ui/specs"},
+	{"intercom", "Intercom", "/ui/intercom"},
 }
 
-// subTab is one sub-tab of a section; Current marks the page's own.
+// subTab is one sub-tab of a tab; Current marks the page's own.
 type subTab struct {
 	Label, Href string
 	Current     bool
 }
 
-// navSubTabs are the sub-tabs of the sections that group several list pages,
-// shown under the top bar on each of the section's pages. Each tab's Href is
-// also its key: a page names the tab it belongs to (mustParseTab).
-var navSubTabs = map[navSection][]subTab{
-	navSpecs: {{Label: "Kits", Href: "/ui/kits"}, {Label: "Destinations", Href: "/ui/destinations"}, {Label: "Model-specs", Href: "/ui/model-specs"}},
+// specsSubTabs are the Specs tab's sub-tabs; each Href is also its key.
+var specsSubTabs = []subTab{{Label: "Kits", Href: "/ui/kits"}, {Label: "Destinations", Href: "/ui/destinations"}, {Label: "Model-specs", Href: "/ui/model-specs"}}
+
+// tabView is one rendered tab.
+type tabView struct {
+	Label, Href string
+	Current     bool
+	Badge       attnBadge
+}
+
+// railEntry is one rail row: Jam or a project.
+type railEntry struct {
+	Name, Href string
+	Current    bool
+	Badge      attnBadge
+}
+
+// railView is the rail. Scope is the current scope as the rail's poll query
+// carries it: "" none, "~" Jam, else a project name.
+type railView struct {
+	Jam      railEntry
+	Projects []railEntry
+	Scope    string
+}
+
+// frame is everything around a page's content: the rail, the scope's title
+// and tabs (with their badges), the Specs sub-tabs, and the attention items —
+// All of them (row flags) and the scope's own (Needs attention).
+type frame struct {
+	Kind    scopeKind
+	Project string // the project scope's name
+	Title   string // the scope's title
+	Tabs    []tabView
+	SubTabs []subTab
+	Rail    railView
+	All     []attnItem
+	Items   []attnItem
+}
+
+// railScopeJam is the rail poll's scope value for Jam.
+const railScopeJam = "~"
+
+// buildRail lays out the rail with scope ("" none, railScopeJam, or a project)
+// current; items are every attention item.
+func buildRail(store jam.Store, scope string, items []attnItem) railView {
+	rv := railView{Scope: scope, Jam: railEntry{Name: "Jam", Href: "/ui/", Current: scope == railScopeJam, Badge: badgeOf(inScope(items, "", ""))}}
+	for _, p := range store.ListProjects() {
+		rv.Projects = append(rv.Projects, railEntry{Name: p, Href: projectURL(p), Current: scope == p, Badge: badgeOf(inScope(items, p, ""))})
+	}
+	return rv
 }
 
-// subTabsFor is section's sub-tabs with tab (an Href) marked current; none for
-// a section without sub-tabs or a page that names no tab.
-func subTabsFor(section navSection, tab string) []subTab {
-	if tab == "" {
-		return nil
+// frameFor builds a page's frame from its meta and request. A project page
+// names its project in the URL (/ui/projects/{name}[/section] or
+// /ui/projects/{project}/roles/{name}); an agent page is in a project's scope
+// when linked with ?project=.
+func frameFor(r *http.Request, meta pageMeta, src frameSource) frame {
+	items := attention(src.store, src.img)
+	f := frame{Kind: meta.Kind, All: items}
+	if meta.Kind == scopeJam && meta.Tab == "agents" {
+		if p := r.URL.Query().Get("project"); p != "" {
+			if _, ok := src.store.GetProject(p); ok {
+				f.Kind, f.Project = scopeProject, p
+			}
+		}
+	}
+	tab := meta.Tab
+	if meta.Kind == scopeProject {
+		f.Project, tab = projectFromPath(r.URL.Path)
+	}
+	switch f.Kind {
+	case scopeJam:
+		f.Title, f.Items = "Jam", inScope(items, "", "")
+		for _, t := range jamTabs {
+			f.Tabs = append(f.Tabs, tabView{Label: t.Label, Href: t.Href, Current: t.Key == tab, Badge: badgeOf(inScope(items, "", t.Key))})
+		}
+		f.Rail = buildRail(src.store, railScopeJam, items)
+	case scopeProject:
+		f.Title, f.Items = f.Project, inScope(items, f.Project, "")
+		for _, s := range projectSections {
+			key := string(s.Section)
+			f.Tabs = append(f.Tabs, tabView{Label: s.Label, Href: projectSectionURL(f.Project, s.Section), Current: key == tab,
+				Badge: badgeOf(inScope(items, f.Project, key))})
+		}
+		f.Rail = buildRail(src.store, f.Project, items)
+	default:
+		f.Rail = buildRail(src.store, "", items)
 	}
-	tabs := append([]subTab(nil), navSubTabs[section]...)
-	for i := range tabs {
-		tabs[i].Current = tabs[i].Href == tab
+	if meta.SubTab != "" {
+		for _, s := range specsSubTabs {
+			s.Current = s.Href == meta.SubTab
+			f.SubTabs = append(f.SubTabs, s)
+		}
+	}
+	return f
+}
+
+// projectFromPath reads a project page's project and tab from its path:
+// /ui/projects/{p} (overview), /ui/projects/{p}/{section}, and
+// /ui/projects/{p}/roles/{role} (roles).
+func projectFromPath(path string) (project, tab string) {
+	parts := strings.Split(strings.TrimPrefix(path, "/ui/projects/"), "/")
+	project, _ = url.PathUnescape(parts[0])
+	tab = string(sectionOverview)
+	if len(parts) > 1 && parts[1] != "" {
+		tab = parts[1]
 	}
-	return tabs
+	return project, tab
+}
+
+// frameSource is what frameFor reads, carried on each request's context by
+// Handler so the renderer can build the frame.
+type frameSource struct {
+	store jam.Store
+	img   jam.ImageResolver
+}
+
+type frameKey struct{}
+
+func withFrameSource(r *http.Request, src frameSource) *http.Request {
+	return r.WithContext(context.WithValue(r.Context(), frameKey{}, src))
+}
+
+func frameSourceOf(r *http.Request) (frameSource, bool) {
+	src, ok := r.Context().Value(frameKey{}).(frameSource)
+	return src, ok
 }
 
 // redirect answers a moved GET page with 301 to target, keeping the query.
diff --git a/internal/jam/adminui/project_edit.go b/internal/jam/adminui/project_edit.go
index e37821c..e661db5 100644
--- a/internal/jam/adminui/project_edit.go
+++ b/internal/jam/adminui/project_edit.go
@@ -132,8 +132,7 @@ func registerProjectEdits(mux *http.ServeMux, store jam.Store, img jam.ImageReso
 				renderError(w, http.StatusNotFound, "project no longer exists")
 				return
 			}
-			d.Tree.OOB = true
-			renderFragments(w, "project", namedFragment{"project-" + string(section), d}, namedFragment{"project-tree-nav", d.Tree})
+			renderFragment(w, "project", "project-"+string(section), d)
 		}
 	}
 
diff --git a/internal/jam/adminui/projects.go b/internal/jam/adminui/projects.go
index 7f09a8e..d82956a 100644
--- a/internal/jam/adminui/projects.go
+++ b/internal/jam/adminui/projects.go
@@ -28,15 +28,6 @@ func projectChoices(store jam.Store) []string {
 	return out
 }
 
-// projectRow is one projects-table row.
-type projectRow struct {
-	Name                   string
-	Roles, Actors, Studios int
-	Members, Channels      int
-	ChatService            string
-	InUseBy                string // what blocks removal; "" when removable
-}
-
 // projectRef is the first thing (deterministically) that keeps project from
 // being removed: a member, a standing-session entry, a role in it, a session
 // not yet gone, or an actor's grant into it. Mirrors the store's refusal.
@@ -71,23 +62,6 @@ func projectRef(store jam.Store, project string) string {
 	return ""
 }
 
-func projectRows(store jam.Store) []projectRow {
-	studios := map[string]int{}
-	for _, c := range jam.CoveSummaries(store, nil) {
-		studios[orDefaultProject(c.Project)]++
-	}
-	var out []projectRow
-	for _, name := range store.ListProjects() {
-		p, _ := store.GetProject(name)
-		out = append(out, projectRow{
-			Name: name, Roles: len(store.ListRoles(name)), Actors: len(projectHolders(store, name)),
-			Studios: studios[name], Members: len(store.ListMembers(p.ID)), Channels: len(store.ListChannels(p.ID, jam.SourceRoom)),
-			ChatService: chatServiceName(store, p), InUseBy: projectRef(store, name),
-		})
-	}
-	return out
-}
-
 // projectHolder is one actor with grants into the project.
 type projectHolder struct {
 	ID    string
@@ -113,27 +87,20 @@ func projectHolders(store jam.Store, project string) []projectHolder {
 // crumb is one breadcrumb segment.
 type crumb struct{ Label, Href string }
 
-// projectCrumbs is the trail to a project section (and, on a role page, the
-// role): Projects / acme / Roles / dev.
+// projectCrumbs is the trail below a project's tab: on a role page, Roles /
+// dev; none on a tab's own page (the tab strip names it).
 func projectCrumbs(project string, section projectSection, role string) []crumb {
-	out := []crumb{{"Projects", "/ui/projects"}, {project, projectURL(project)}}
-	for _, s := range projectSections {
-		if s.Section == section && section != sectionOverview {
-			out = append(out, crumb{s.Label, projectSectionURL(project, section)})
-		}
-	}
-	if role != "" {
-		out = append(out, crumb{role, roleURL(project, role)})
+	if role == "" {
+		return nil
 	}
-	return out
+	return []crumb{{"Roles", projectSectionURL(project, sectionRoles)}, {role, roleURL(project, role)}}
 }
 
 // projectDetail is the payload of every project page: Section picks which
-// section's content renders inside the tree frame.
+// section's content renders under the project's tabs.
 type projectDetail struct {
 	Title        string
 	Section      projectSection
-	Tree         projectTree
 	Crumbs       []crumb
 	Project      jam.Project
 	Roles        []roleRow
@@ -168,7 +135,6 @@ func buildProjectDetail(store jam.Store, img jam.ImageResolver, msgs SquawkReade
 		return projectDetail{}, false
 	}
 	d := projectDetail{Title: name, Section: section, Project: p, Holders: projectHolders(store, name), InUseBy: projectRef(store, name)}
-	d.Tree = buildProjectTree(store, img, name, section, "")
 	d.Crumbs = projectCrumbs(name, section, "")
 	d.Context = newContextPanel("project", "/ui/projects/"+name+"/context", "project", p.Context, p.Resources, sessionctx.BudgetProject, true)
 	for _, r := range roleRows(store) {
@@ -217,15 +183,10 @@ func buildProjectDetail(store jam.Store, img jam.ImageResolver, msgs SquawkReade
 	return d, true
 }
 
-func projectTableData(store jam.Store) map[string]any {
-	return map[string]any{"Projects": projectRows(store)}
-}
-
 func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, msgs SquawkReader, canRequest bool, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
+	// The rail is the project list.
 	mux.HandleFunc("GET /ui/projects", func(w http.ResponseWriter, r *http.Request) {
-		data := projectTableData(store)
-		data["Title"] = "Projects"
-		render(w, "projects", data)
+		redirect(w, r, "/ui/")
 	})
 
 	page := func(section projectSection) http.HandlerFunc {
@@ -233,11 +194,11 @@ func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver
 			name := r.PathValue("name")
 			d, ok := buildProjectDetail(store, img, msgs, name, section)
 			if !ok {
-				renderStatus(w, http.StatusNotFound, "project", projectDetail{Title: "Project not found", NotFound: true, NotFoundFor: name})
+				renderStatus(w, r, http.StatusNotFound, "project", projectDetail{Title: "Project not found", NotFound: true, NotFoundFor: name})
 				return
 			}
 			d.CanRequest = canRequest
-			render(w, "project", d)
+			render(w, r, "project", d)
 		}
 	}
 	mux.HandleFunc("GET /ui/projects/{name}", page(sectionOverview))
@@ -259,8 +220,9 @@ func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver
 			return
 		}
 		log.Info("ui project created", "operator", jam.OperatorID(r), "project", name)
+		// The new project's page; htmx follows the redirect.
 		w.Header().Set("HX-Redirect", projectURL(name))
-		renderFragment(w, "projects", "projects-table", projectTableData(store))
+		w.WriteHeader(http.StatusOK)
 	})
 
 	mux.HandleFunc("POST /ui/projects/{name}/rename", func(w http.ResponseWriter, r *http.Request) {
@@ -291,7 +253,8 @@ func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver
 			return
 		}
 		log.Info("ui project removed", "operator", jam.OperatorID(r), "project", name)
-		renderFragment(w, "projects", "projects-table", projectTableData(store))
+		// The project page navigates to Jam on success.
+		w.WriteHeader(http.StatusOK)
 	})
 }
 
diff --git a/internal/jam/adminui/projtree.go b/internal/jam/adminui/projtree.go
index a3e5c11..594bb35 100644
--- a/internal/jam/adminui/projtree.go
+++ b/internal/jam/adminui/projtree.go
@@ -1,14 +1,7 @@
 package adminui
 
-import (
-	"net/url"
-	"strconv"
-
-	"github.com/aethons-tools/cove/internal/jam"
-)
-
-// projectSection is one node of a project's tree: its Overview page or one of
-// its sections. A role page sits under sectionRoles.
+// projectSection is one of a project's tabs: its Overview page or one of its
+// sections. A role page sits under sectionRoles.
 type projectSection string
 
 const (
@@ -20,7 +13,7 @@ const (
 	sectionEscalation projectSection = "escalation"
 )
 
-// projectSections is the tree's order, with each node's label.
+// projectSections is a project's tab order, with each tab's label.
 var projectSections = []struct {
 	Section projectSection
 	Label   string
@@ -42,75 +35,3 @@ func projectSectionURL(project string, s projectSection) string {
 	}
 	return base + "/" + string(s)
 }
-
-// treeLeaf is one child under a tree node: a role, an agent or a room.
-type treeLeaf struct {
-	Label, Href, Phase string
-	Current            bool
-}
-
-// treeNode is one section in the tree with its children (none for leaf
-// sections). Open renders the branch expanded: it holds the current page.
-type treeNode struct {
-	Section  projectSection
-	Label    string
-	Href     string
-	Count    string // shown after the label, e.g. the live agent count
-	Current  bool   // this section's own page is the current page
-	Open     bool
-	Children []treeLeaf
-}
-
-// projectTree is the left-side navigation on every project page.
-type projectTree struct {
-	Project string
-	Href    string
-	Current string // the current node's label, for the narrow-screen summary
-	Nodes   []treeNode
-	OOB     bool // rendered as an out-of-band swap of #ptree (after a write)
-}
-
-// buildProjectTree lays out project's tree with current marked: section is the
-// page's section and role, on a role page, the role's name.
-func buildProjectTree(store jam.Store, img jam.ImageResolver, project string, section projectSection, role string) projectTree {
-	t := projectTree{Project: project, Href: projectURL(project)}
-	p, _ := store.GetProject(project)
-	for _, s := range projectSections {
-		n := treeNode{Section: s.Section, Label: s.Label, Href: projectSectionURL(project, s.Section)}
-		switch s.Section {
-		case sectionRoles:
-			for _, r := range store.ListRoles(project) {
-				cur := section == sectionRoles && role == r.Name
-				n.Children = append(n.Children, treeLeaf{Label: r.Name, Href: roleURL(project, r.Name), Current: cur})
-			}
-		case sectionAgents:
-			live := 0
-			for _, c := range jam.CoveSummaries(store, img) {
-				if orDefaultProject(c.Project) != project {
-					continue
-				}
-				if ph := jam.Phase(c.Phase); ph == jam.PhaseLive || ph == jam.PhaseRaising {
-					live++
-					n.Children = append(n.Children, treeLeaf{Label: c.ID, Href: agentURL(c.ID), Phase: c.Phase})
-				}
-			}
-			if live > 0 {
-				n.Count = strconv.Itoa(live)
-			}
-		case sectionIntercom:
-			for _, rm := range jam.ListRooms(store, p) {
-				n.Children = append(n.Children, treeLeaf{Label: rm.Name, Href: "/ui/intercom?participant=" + url.QueryEscape(string(rm.ID))})
-			}
-		}
-		n.Current = s.Section == section && role == ""
-		n.Open = s.Section == section
-		if n.Open {
-			t.Current = s.Label
-			if role != "" {
-				t.Current += " ▸ " + role
-			}
-		}
-		t.Nodes = append(t.Nodes, n)
-	}
-	return t
-}
diff --git a/internal/jam/adminui/role_detail.go b/internal/jam/adminui/role_detail.go
index 02f1515..6a043b5 100644
--- a/internal/jam/adminui/role_detail.go
+++ b/internal/jam/adminui/role_detail.go
@@ -44,7 +44,6 @@ type setting struct {
 // roleDetail is the role page's payload.
 type roleDetail struct {
 	Title, Project, Name string
-	Tree                 projectTree
 	Crumbs               []crumb
 	WriteBase            string // the role's write endpoints (roleWriteBase)
 	RolesHref            string // the project's Roles section (where a delete lands)
@@ -157,12 +156,11 @@ func handleRoleDetail(w http.ResponseWriter, r *http.Request, store jam.Store, i
 	project, name := r.PathValue("project"), r.PathValue("name")
 	d, ok := buildRoleDetail(store, img, project, name)
 	if !ok {
-		renderStatus(w, http.StatusNotFound, "role", roleDetail{Title: "Role not found", NotFound: true,
+		renderStatus(w, r, http.StatusNotFound, "role", roleDetail{Title: "Role not found", NotFound: true,
 			Project: project, Name: name, RolesHref: projectSectionURL(project, sectionRoles)})
 		return
 	}
-	d.Tree = buildProjectTree(store, img, project, sectionRoles, name)
 	d.Crumbs = projectCrumbs(project, sectionRoles, name)
 	d.CanRequest = canRequest
-	render(w, "role", d)
+	render(w, r, "role", d)
 }
diff --git a/internal/jam/adminui/search.go b/internal/jam/adminui/search.go
index 09fafab..6af5f5f 100644
--- a/internal/jam/adminui/search.go
+++ b/internal/jam/adminui/search.go
@@ -299,7 +299,7 @@ func registerSearch(mux *http.ServeMux, store jam.Store, msgs SquawkReader) {
 			renderFragment(w, "search", "search-results", d)
 			return
 		}
-		render(w, "search", d)
+		render(w, r, "search", d)
 	})
 }
 
diff --git a/internal/jam/adminui/session.go b/internal/jam/adminui/session.go
index 2921d9b..114fda4 100644
--- a/internal/jam/adminui/session.go
+++ b/internal/jam/adminui/session.go
@@ -4,9 +4,11 @@ import (
 	"bytes"
 	"encoding/json"
 	"fmt"
+	"html/template"
 	"net/http"
 	"strconv"
 	"strings"
+	"sync"
 	"time"
 	"unicode/utf8"
 
@@ -29,7 +31,7 @@ func registerSession(mux *http.ServeMux, store sessionevents.Store, hub *session
 			}
 			data["Streams"], data["Stream"] = streams, selected
 		}
-		render(w, "session", data)
+		render(w, r, "session", data)
 	})
 	events := func(w http.ResponseWriter, r *http.Request) {
 		if store == nil || hub == nil {
@@ -211,9 +213,15 @@ func sseWrite(w http.ResponseWriter, event, id, data string) {
 	_, _ = w.Write([]byte(b.String()))
 }
 
+// sessionFragments is a clone of the session page's set for rendering event
+// fragments (the master set is only ever cloned, never executed).
+var sessionFragments = sync.OnceValue(func() *template.Template {
+	return template.Must(pages["session"].t.Clone())
+})
+
 func fragment(name string, data any) string {
 	var b bytes.Buffer
-	if err := pages["session"].ExecuteTemplate(&b, name, data); err != nil {
+	if err := sessionFragments().ExecuteTemplate(&b, name, data); err != nil {
 		return ""
 	}
 	// html/template leaves CR raw; keep it visible to the operator as an entity
diff --git a/internal/jam/adminui/templates/agent.html b/internal/jam/adminui/templates/agent.html
index 811b4da..28db7c9 100644
--- a/internal/jam/adminui/templates/agent.html
+++ b/internal/jam/adminui/templates/agent.html
@@ -30,7 +30,7 @@
   td.body.md pre{overflow-x:auto;background:var(--bg);padding:6px 8px;border-radius:6px}
   @media (max-width:720px){.sections{grid-template-columns:1fr}}
 </style>
-<div class="crumbs"><a href="/ui/agents">Agents</a> / <a href="{{agentURL .ID}}">{{.ID}}</a></div>
+{{$f := frame}}<div class="crumbs"><a href="{{if $f.Project}}{{projectURL $f.Project}}/agents{{else}}/ui/agents{{end}}">Agents</a> / <a href="{{agentHref .ID ""}}">{{.ID}}</a></div>
 <div class="page-head">
   <h1 class="mono">{{.ID}}</h1>
   {{if .Running}}
diff --git a/internal/jam/adminui/templates/agents.html b/internal/jam/adminui/templates/agents.html
index 2c12e9f..03fb685 100644
--- a/internal/jam/adminui/templates/agents.html
+++ b/internal/jam/adminui/templates/agents.html
@@ -46,7 +46,7 @@
   <tbody>
     {{range .Agents}}
     <tr data-id="{{.ID}}">
-      <td><a class="mono" href="{{agentURL .ID}}"><b>{{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</b></a>{{if .Name}} <span class="sub mono">{{.ID}}</span>{{end}}{{if .Unit}} <span class="sub mono">{{.Unit}}</span>{{end}}</td>
+      <td><a class="mono" href="{{agentURL .ID}}"><b>{{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</b></a>{{if .Name}} <span class="sub mono">{{.ID}}</span>{{end}}{{if .Unit}} <span class="sub mono">{{.Unit}}</span>{{end}}{{template "attn-flag" (attn "agents" .ID)}}</td>
       <td><span class="chip">{{.Kind}}</span></td>
       <td>{{if .Project}}<a href="{{projectURL .Project}}">{{.Project}}</a>{{else}}<span class="none">—</span>{{end}}</td>
       <td>{{if .Role}}<a href="{{roleURL .Project .Role}}">{{.Role}}</a>{{if gt (len .Grants) 1}} <span class="sub">+{{len (slice .Grants 1)}}</span>{{end}}{{else}}<span class="none">—</span>{{end}}</td>
diff --git a/internal/jam/adminui/templates/attention.html b/internal/jam/adminui/templates/attention.html
new file mode 100644
index 0000000..a00f644
--- /dev/null
+++ b/internal/jam/adminui/templates/attention.html
@@ -0,0 +1,19 @@
+{{/* attention-card lists a scope's attention items (frame.Items): each with
+     its why, linked to where to fix it. */}}
+{{define "attention-card"}}
+<section class="card full attn-card">
+  <header><h2>Needs attention</h2>{{with .}}<span class="sub">{{len .}} item(s)</span>{{end}}</header>
+  <div class="body">
+    {{range .}}<div class="attn-row"><span class="attn-flag{{if eq .Kind "broken"}} red{{end}}">⚠</span> <a href="{{.Href}}">{{.Why}}</a> <span class="chip">{{if eq .Kind "broken"}}broken{{else if eq .Kind "stale"}}out of date{{else}}config{{end}}</span></div>
+    {{else}}<span class="unset">Nothing needs attention.</span>{{end}}
+  </div>
+</section>
+<style>
+  .attn-card{overflow:visible}
+  .attn-card>header{display:flex;align-items:baseline;gap:10px;padding:12px 16px;border-bottom:1px solid var(--border)}
+  .attn-card>header h2{margin:0}
+  .attn-card>.body{padding:10px 16px}
+  .attn-row{padding:4px 0}
+  .attn-card .unset{color:var(--faint)}
+</style>
+{{end}}
diff --git a/internal/jam/adminui/templates/coves.html b/internal/jam/adminui/templates/coves.html
index da1c527..23e1902 100644
--- a/internal/jam/adminui/templates/coves.html
+++ b/internal/jam/adminui/templates/coves.html
@@ -18,7 +18,7 @@
   <tbody>
     {{range .Coves}}
     <tr data-id="{{.ID}}">
-      <td><a class="mono" href="{{agentURL .ID}}"><b>{{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</b></a>{{if .Name}} <span class="sub mono">{{.ID}}</span>{{end}} <a class="sub" href="{{agentURL .ID}}/session" title="Live session timeline">timeline</a></td>
+      <td><a class="mono" href="{{agentHref .ID ""}}"><b>{{if .Name}}{{.Name}}{{else}}{{.ID}}{{end}}</b></a>{{if .Name}} <span class="sub mono">{{.ID}}</span>{{end}}{{template "attn-flag" (attn "agents" .ID)}} <a class="sub" href="{{agentHref .ID "/session"}}" title="Live session timeline">timeline</a></td>
       <td>{{template "phase" .Phase}}</td>
       <td>{{if .Activity}}{{.Activity}}{{else}}<span class="none">—</span>{{end}}</td>
       <td>{{if eq .Connector "stale"}}<span class="pill phase-raising">stale</span>{{else if eq .Connector "error"}}<span class="pill phase-lost">error</span>{{else if eq .Connector "unknown" ""}}<span class="none">{{or .Connector "—"}}</span>{{else}}{{.Connector}}{{end}}</td>
diff --git a/internal/jam/adminui/templates/dashboard.html b/internal/jam/adminui/templates/dashboard.html
index 0454676..957fbcb 100644
--- a/internal/jam/adminui/templates/dashboard.html
+++ b/internal/jam/adminui/templates/dashboard.html
@@ -21,6 +21,7 @@
   <a class="tile" href="/ui/specs" data-stat="specs"><b>{{.Specs}}</b><span>Specs</span></a>
 </div>
 {{end}}
+{{template "attention-card" frame.Items}}
 {{template "context-panel" .JamContext}}
 <h2>Studios</h2>
 {{template "coves-table" .}}
diff --git a/internal/jam/adminui/templates/destinations.html b/internal/jam/adminui/templates/destinations.html
index ff63738..9b995b6 100644
--- a/internal/jam/adminui/templates/destinations.html
+++ b/internal/jam/adminui/templates/destinations.html
@@ -17,7 +17,7 @@
   <tbody>
     {{range .Destinations}}
     <tr data-id="{{.Name}}" data-used-by="{{.UsedBy}}">
-      <td><a class="mono" href="{{destURL .Name}}"><b>{{.Name}}</b></a></td>
+      <td><a class="mono" href="{{destURL .Name}}"><b>{{.Name}}</b></a>{{template "attn-flag" (attn "specs" (printf "dest:%s" .Name))}}</td>
       <td><span class="mono">{{.Route}}</span></td>
       <td><span class="mono">{{.Upstream}}</span></td>
       <td>{{if or .IdentityIn .Apply}}<span class="mono">{{.IdentityIn}} → {{.Apply}}</span>{{else}}<span class="none">—</span>{{end}}</td>
diff --git a/internal/jam/adminui/templates/kits.html b/internal/jam/adminui/templates/kits.html
index 9121892..e7bc0ea 100644
--- a/internal/jam/adminui/templates/kits.html
+++ b/internal/jam/adminui/templates/kits.html
@@ -21,7 +21,7 @@
   <tbody>
     {{range .Kits}}
     <tr data-id="{{.Name}}" data-used-by="{{.UsedBy}}">
-      <td><a class="mono" href="{{kitURL .Name}}"><b>{{.Name}}</b></a></td>
+      <td><a class="mono" href="{{kitURL .Name}}"><b>{{.Name}}</b></a>{{template "attn-flag" (attn "specs" (printf "kit:%s" .Name))}}</td>
       <td><span class="chip accent">v{{.Current}}</span> <span class="none">of {{.Count}}</span></td>
       <td>{{if .Invalid}}<span class="pill phase-lost" title="The current version is not a valid studio kit">invalid</span>{{else}}{{.Base}}{{end}}</td>
       <td>{{if .Invalid}}<span class="none">—</span>{{else}}{{.Egress}} domain(s){{end}}</td>
diff --git a/internal/jam/adminui/templates/layout.html b/internal/jam/adminui/templates/layout.html
index 29f2eea..c7e76dc 100644
--- a/internal/jam/adminui/templates/layout.html
+++ b/internal/jam/adminui/templates/layout.html
@@ -19,10 +19,37 @@
     .brand{display:flex;align-items:baseline;gap:8px;font-weight:600;white-space:nowrap;padding:12px 0}
     .brand .dot{width:9px;height:9px;border-radius:50%;background:var(--accent);align-self:center}
     .brand small{color:var(--faint);font-weight:500;letter-spacing:.04em;text-transform:uppercase;font-size:11px}
-    .topbar nav{display:flex;gap:2px}
-    .topbar nav a{color:var(--muted);text-decoration:none;padding:14px 10px 12px;border-bottom:2px solid transparent;white-space:nowrap;font-weight:500}
-    .topbar nav a:hover{color:var(--ink)}
-    .topbar nav a[aria-current="page"]{color:var(--accent);border-bottom-color:var(--accent)}
+    .railbtn{display:none;background:none;border:0;font-size:18px;line-height:1;padding:10px 2px;color:var(--ink);cursor:pointer}
+
+    /* Rail: Jam, then every project; the selection decides the tabs. */
+    .shell{display:grid;grid-template-columns:200px minmax(0,1fr);min-height:calc(100vh - 49px)}
+    #rail{border-right:1px solid var(--border);background:var(--surface);padding:12px 8px;position:sticky;top:49px;align-self:start;max-height:calc(100vh - 49px);overflow-y:auto}
+    #rail ul{list-style:none;margin:0;padding:0}
+    #rail a{display:flex;align-items:center;gap:8px;padding:6px 10px;border-radius:8px;color:var(--muted);text-decoration:none;font-weight:500;white-space:nowrap;overflow:hidden}
+    #rail a span.name{flex:1;overflow:hidden;text-overflow:ellipsis}
+    #rail a:hover{color:var(--ink);background:var(--surface-2)}
+    #rail a[aria-current="page"]{color:var(--accent);background:var(--accent-soft);font-weight:600}
+    #rail .jam{font-weight:600;color:var(--ink)}
+    #rail hr{border:0;border-top:1px solid var(--border);margin:8px 4px}
+    #rail .rail-label{font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:var(--faint);padding:4px 10px;font-weight:600}
+    #rail details.newproj{margin-top:8px}
+    #rail details.newproj>summary{list-style:none;cursor:pointer;padding:6px 10px;color:var(--accent);font-weight:600;font-size:12.5px}
+    #rail details.newproj>summary::-webkit-details-marker{display:none}
+    #rail details.newproj form{display:flex;gap:6px;padding:4px 6px}
+    #rail details.newproj input{padding:4px 8px}
+    .badge{display:inline-flex;align-items:center;justify-content:center;min-width:18px;height:18px;padding:0 5px;border-radius:999px;font-size:11px;font-weight:700;background:var(--wait-soft);color:var(--wait)}
+    .badge.red{background:var(--bad-soft);color:var(--bad)}
+
+    /* Scope head: the selected scope's title and tabs. */
+    .scope-title{font-size:12px;text-transform:uppercase;letter-spacing:.06em;color:var(--faint);font-weight:600;margin-bottom:2px}
+    nav.tabs{display:flex;gap:2px;border-bottom:1px solid var(--border);margin-bottom:16px;overflow-x:auto}
+    nav.tabs a{display:inline-flex;align-items:center;gap:6px;color:var(--muted);text-decoration:none;padding:9px 10px 8px;border-bottom:2px solid transparent;white-space:nowrap;font-weight:500}
+    nav.tabs a:hover{color:var(--ink)}
+    nav.tabs a[aria-current="page"]{color:var(--accent);border-bottom-color:var(--accent)}
+    .crumbs{font-size:12.5px;color:var(--faint);margin-bottom:4px}
+    .crumbs a{color:var(--muted);text-decoration:none}
+    .attn-flag{color:var(--wait);font-weight:700;cursor:help}
+    .attn-flag.red{color:var(--bad)}
     /* A section's sub-tabs (Agents, Specs): its list pages. */
     .subtabs{display:flex;gap:4px;flex-wrap:wrap;margin-bottom:16px}
     .subtabs a{color:var(--muted);text-decoration:none;padding:5px 12px;border-radius:999px;font-size:13px;font-weight:500;white-space:nowrap}
@@ -30,7 +57,7 @@
     .subtabs a[aria-current="page"]{color:var(--accent);background:var(--accent-soft);font-weight:600}
     .topsearch{margin-left:auto;padding:8px 0}
     .topsearch input{width:200px;padding:5px 10px;font-size:12.5px;border-radius:999px}
-    main{max-width:1200px;margin:0 auto;padding:22px 20px 60px}
+    main{max-width:1200px;width:100%;margin:0 auto;padding:18px 20px 60px}
     .page-head{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-bottom:16px}
     h1{font-size:20px;margin:0;font-weight:600}
     h2{font-size:13px;margin:26px 0 10px;text-transform:uppercase;letter-spacing:.06em;color:var(--faint);font-weight:600}
@@ -105,26 +132,38 @@
     .ta-menu .ta-opt{padding:5px 9px;border-radius:7px;cursor:pointer;font:12.5px "IBM Plex Mono",ui-monospace,monospace;color:var(--ink);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
     .ta-menu .ta-opt[aria-selected="true"]{background:var(--accent-soft);color:var(--accent)}
 
-    @media (max-width:720px){main{padding:16px}.topbar{gap:12px;padding:0 16px}th,td{padding:8px 10px}}
+    @media (max-width:720px){
+      main{padding:16px}.topbar{gap:12px;padding:0 16px}th,td{padding:8px 10px}
+      .railbtn{display:block}
+      .shell{grid-template-columns:1fr}
+      #rail{display:none;position:fixed;top:49px;left:0;bottom:0;width:240px;max-height:none;z-index:30;box-shadow:0 10px 28px rgba(0,0,0,.18)}
+      body.rail-open #rail{display:block}
+    }
   </style>
 </head>
 <body>
   <header class="topbar">
+    <button type="button" class="railbtn" aria-label="Projects" onclick="document.body.classList.toggle('rail-open')">☰</button>
     <span class="brand"><span class="dot"></span>Jam <small>Admin</small></span>
-    <nav>
-      {{- $s := navSection}}
-      {{- range navItems}}
-      <a href="{{.Href}}"{{if eq .Section $s}} aria-current="page"{{end}}>{{.Label}}</a>
-      {{- end}}
-    </nav>
     <form class="topsearch" action="/ui/search" method="get" role="search">
       <input id="q-top" name="q" placeholder="Search  /" aria-label="Search" autocomplete="off">
       <input type="hidden" name="go" value="1">
     </form>
   </header>
+  <div class="shell">
+  {{- $f := frame}}
+  {{template "rail" $f.Rail}}
   <main>
-    {{- with subTabs}}
-    <nav class="subtabs" aria-label="Section">
+    {{- with $f.Tabs}}
+    <div class="scope-title">{{$f.Title}}</div>
+    <nav class="tabs" aria-label="{{$f.Title}}">
+      {{- range .}}
+      <a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}{{template "badge" .Badge}}</a>
+      {{- end}}
+    </nav>
+    {{- end}}
+    {{- with $f.SubTabs}}
+    <nav class="subtabs" aria-label="Specs">
       {{- range .}}
       <a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}</a>
       {{- end}}
@@ -133,6 +172,7 @@
     <div id="flash" aria-live="polite"></div>
     {{template "content" .}}
   </main>
+  </div>
   <script>
   (function(){
     var flash = document.getElementById('flash');
@@ -282,20 +322,12 @@
     window.addEventListener('scroll', taClose, true);
     window.addEventListener('resize', taClose);
 
-    // On a narrow screen a project's tree starts collapsed to its summary;
-    // on a wide one (where the summary is hidden) it is always open — also
-    // after a resize or rotation, and after a write swaps it out of band.
-    var narrow = window.matchMedia && matchMedia('(max-width:720px)');
-    function syncTree(){
-      if (!narrow) return;
-      document.querySelectorAll('details.ptree-wrap').forEach(function(d){ d.open = !narrow.matches; });
-    }
-    syncTree();
-    if (narrow) {
-      if (narrow.addEventListener) narrow.addEventListener('change', syncTree); else narrow.addListener(syncTree);
-    }
-    document.body.addEventListener('htmx:oobAfterSwap', function(e){
-      if (e.detail && e.detail.target && e.detail.target.id === 'ptree') syncTree();
+    // The rail is a drawer on a narrow screen: following a link in it, or
+    // tapping outside it, closes it.
+    document.addEventListener('click', function(e){
+      if (!document.body.classList.contains('rail-open')) return;
+      if (e.target.closest('.railbtn')) return;
+      if (!e.target.closest('#rail') || e.target.closest('#rail a')) document.body.classList.remove('rail-open');
     });
     // "/" focuses search from anywhere except a text field.
     document.addEventListener('keydown', function(e){
@@ -318,6 +350,38 @@
 </body>
 </html>{{end}}
 
+{{/* rail is the permanent scope list (a railView), re-fetched every 3s so
+     its badges stay live; Scope keeps the selection across polls. */}}
+{{define "rail"}}
+<aside id="rail" hx-get="/ui/rail?scope={{.Scope}}" hx-trigger="every 3s" hx-swap="outerHTML">
+  <ul>
+    <li><a class="jam" href="{{.Jam.Href}}"{{if .Jam.Current}} aria-current="page"{{end}}><span class="name">◉ Jam</span>{{template "badge" .Jam.Badge}}</a></li>
+  </ul>
+  <hr>
+  <div class="rail-label">Projects</div>
+  <ul>
+    {{- range .Projects}}
+    <li><a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}><span class="name">{{.Name}}</span>{{template "badge" .Badge}}</a></li>
+    {{- end}}
+  </ul>
+  <details class="newproj">
+    <summary>+ New project</summary>
+    <form hx-post="/ui/projects" hx-swap="none"><input name="name" placeholder="name" aria-label="Project name" required autocomplete="off"><button type="submit" class="primary small">Create</button></form>
+  </details>
+</aside>
+{{end}}
+
+{{/* badge renders an attention badge (an attnBadge): its count, red when
+     anything in it is broken, the breakdown on hover; nothing when zero. */}}
+{{define "badge"}}{{if .N}} <span class="badge{{if .Red}} red{{end}}" title="{{.Title}}">{{.N}}</span>{{end}}{{end}}
+
+{{/* attn-flag marks a row an attention item names (an *attnItem or nil). */}}
+{{define "attn-flag"}}{{with .}} <span class="attn-flag{{if eq .Kind "broken"}} red{{end}}" title="{{.Why}}">⚠</span>{{end}}{{end}}
+
+{{/* crumbs renders a breadcrumb trail: a list of crumb{Label, Href}; the last
+     one is the current page (no link). */}}
+{{define "crumbs"}}<div class="crumbs">{{range $i, $c := .}}{{if $i}} / {{end}}{{if $c.Href}}<a href="{{$c.Href}}">{{$c.Label}}</a>{{else}}<span>{{$c.Label}}</span>{{end}}{{end}}</div>{{end}}
+
 {{define "phase"}}<span class="pill phase-{{.}}">{{.}}</span>{{end}}
 {{/* project-select is the project field: a type-ahead over every project,
      prefilled with default. */}}
diff --git a/internal/jam/adminui/templates/project.html b/internal/jam/adminui/templates/project.html
index 4efebfe..25deb84 100644
--- a/internal/jam/adminui/templates/project.html
+++ b/internal/jam/adminui/templates/project.html
@@ -1,7 +1,7 @@
 {{define "content"}}
 {{if .NotFound}}
 <div class="page-head"><h1>Project not found</h1></div>
-<div class="banner">No project <span class="mono">{{.NotFoundFor}}</span>. <a href="/ui/projects">Back to projects</a></div>
+<div class="banner">No project <span class="mono">{{.NotFoundFor}}</span>. <a href="/ui/">Back to Jam</a></div>
 {{else}}
 <style>
   .crumbs{font-size:12.5px;color:var(--faint);margin-bottom:4px}
@@ -39,18 +39,13 @@
   .tile span{color:var(--muted);font-size:12px}
   @media (max-width:720px){.sections{grid-template-columns:1fr}}
 </style>
-<div class="pframe">
-{{template "project-tree" .Tree}}
-<div>
-{{template "crumbs" .Crumbs}}
+{{with .Crumbs}}{{template "crumbs" .}}{{end}}
 {{if eq .Section "members"}}{{template "project-members" .}}
 {{else if eq .Section "agents"}}{{template "project-agents" .}}
 {{else if eq .Section "roles"}}{{template "project-roles" .}}
 {{else if eq .Section "intercom"}}{{template "project-intercom" .}}
 {{else if eq .Section "escalation"}}{{template "project-escalation" .}}
-{{else}}{{template "project-overview" .}}{{end}}
-</div>
-</div>
+{{else}}{{template "attention-card" frame.Items}}{{template "project-overview" .}}{{end}}
 {{end}}
 {{end}}
 
@@ -68,15 +63,15 @@
   {{if .InUseBy}}<button class="danger" disabled title="Still referenced by {{.InUseBy}} — remove its roles, sessions and grants first">Delete</button>
   {{else}}<button class="danger" hx-delete="/ui/projects/{{$p}}" hx-swap="none"
       hx-confirm="Delete project {{$p}}? Its roster, escalation and chat service go with it; its channels are archived (history stays readable) and its name is freed."
-      hx-on::after-request="if(event.detail.successful) location.href='/ui/projects'">Delete</button>{{end}}
+      hx-on::after-request="if(event.detail.successful) location.href='/ui/'">Delete</button>{{end}}
 </div>
 
 <div class="tiles">
-  <a class="tile" href="{{.Tree.Href}}/members"><b>{{len .Members}}</b><span>members</span></a>
-  <a class="tile" href="{{.Tree.Href}}/agents"><b>{{.LiveCoves}}</b><span>agents live of {{.Agents}}</span></a>
-  <a class="tile" href="{{.Tree.Href}}/roles"><b>{{len .Roles}}</b><span>roles</span></a>
-  <a class="tile" href="{{.Tree.Href}}/intercom"><b>{{len .Rooms}}</b><span>rooms</span></a>
-  <a class="tile" href="{{.Tree.Href}}/escalation"><b>{{len .Escalation}}</b><span>escalation chains</span></a>
+  <a class="tile" href="{{projectURL $p}}/members"><b>{{len .Members}}</b><span>members</span></a>
+  <a class="tile" href="{{projectURL $p}}/agents"><b>{{.LiveCoves}}</b><span>agents live of {{.Agents}}</span></a>
+  <a class="tile" href="{{projectURL $p}}/roles"><b>{{len .Roles}}</b><span>roles</span></a>
+  <a class="tile" href="{{projectURL $p}}/intercom"><b>{{len .Rooms}}</b><span>rooms</span></a>
+  <a class="tile" href="{{projectURL $p}}/escalation"><b>{{len .Escalation}}</b><span>escalation chains</span></a>
 </div>
 <div class="sections">
   <section class="card">
@@ -152,7 +147,7 @@
     <table>
       <thead><tr><th>Agent</th><th>Roles</th></tr></thead>
       <tbody>
-        {{range .Holders}}<tr><td><a class="mono" href="{{agentURL .ID}}">{{.ID}}</a></td><td>{{range .Roles}}<span class="chip">{{.}}</span>{{end}}</td></tr>
+        {{range .Holders}}<tr><td><a class="mono" href="{{agentHref .ID ""}}">{{.ID}}</a></td><td>{{range .Roles}}<span class="chip">{{.}}</span>{{end}}</td></tr>
         {{else}}<tr><td colspan="2" class="empty">No agents hold a grant here.</td></tr>{{end}}
       </tbody>
     </table>
@@ -183,7 +178,7 @@
 <table>
   <thead><tr><th>Role</th><th>Destinations</th><th>TTL</th><th>Kit</th><th></th></tr></thead>
   <tbody>
-    {{range .Roles}}<tr data-id="{{.Name}}"><td><a class="mono" href="{{roleURL .Project .Name}}"><b>{{.Name}}</b></a></td>
+    {{range .Roles}}<tr data-id="{{.Name}}"><td><a class="mono" href="{{roleURL .Project .Name}}"><b>{{.Name}}</b></a>{{template "attn-flag" (attn "roles" (printf "%s/%s" .Project .Name))}}</td>
       <td>{{$c := .Credentials}}{{range .Destinations}}<span class="chip">{{.}}{{with index $c .}} <span class="none">→</span> {{.}}{{end}}</span>{{else}}<span class="unset">—</span>{{end}}</td>
       <td>{{ttl .TTL}}</td>
       <td>{{if .Kit}}<a class="chip" href="{{kitURL .Kit}}">{{.Kit}}</a>{{else}}<span class="unset">default</span>{{end}}</td>
diff --git a/internal/jam/adminui/templates/projects.html b/internal/jam/adminui/templates/projects.html
deleted file mode 100644
index 6025a0f..0000000
--- a/internal/jam/adminui/templates/projects.html
+++ /dev/null
@@ -1,38 +0,0 @@
-{{define "content"}}
-<div class="page-head"><h1>Projects</h1><span class="sub">The top of the config tree — each owns its roles, grants, roster, escalation and chat service</span></div>
-<details class="panel">
-  <summary>New project</summary>
-  <form hx-post="/ui/projects" hx-target="#projects" hx-swap="outerHTML">
-    <div class="grid">
-      <label>Project name <span class="req">required</span><input name="name" required autocomplete="off"></label>
-    </div>
-    <div class="form-actions"><button type="submit" class="primary">Create project</button></div>
-  </form>
-</details>
-{{template "projects-table" .}}
-{{end}}
-
-{{define "projects-table"}}
-<div class="card" id="projects">
-<table>
-  <thead><tr><th>Project</th><th>Roles</th><th>Agents</th><th>Studios</th><th>Roster</th><th>Chat</th><th></th></tr></thead>
-  <tbody>
-    {{range .Projects}}
-    <tr data-id="{{.Name}}" data-roles="{{.Roles}}" data-actors="{{.Actors}}" data-studios="{{.Studios}}">
-      <td><a class="mono" href="{{projectURL .Name}}"><b>{{.Name}}</b></a></td>
-      <td>{{.Roles}}</td>
-      <td>{{.Actors}}</td>
-      <td>{{if .Studios}}{{.Studios}}{{else}}<span class="none">0</span>{{end}}</td>
-      <td>{{.Members}} member(s), {{.Channels}} room(s)</td>
-      <td>{{if .ChatService}}<span class="chip">{{.ChatService}}</span>{{else}}<span class="none">tracker only</span>{{end}}</td>
-      <td class="actions">{{if .InUseBy}}<button class="danger small" disabled title="Still referenced by {{.InUseBy}} — remove its roles, sessions and grants first">Delete</button>
-        {{else}}<button class="danger small" hx-delete="/ui/projects/{{.Name}}" hx-target="#projects" hx-swap="outerHTML"
-          hx-confirm="Delete project {{.Name}}? Its roster, escalation and chat service go with it; its channels are archived (history stays readable) and its name is freed.">Delete</button>{{end}}</td>
-    </tr>
-    {{else}}
-    <tr><td colspan="7" class="empty">No projects.</td></tr>
-    {{end}}
-  </tbody>
-</table>
-</div>
-{{end}}
diff --git a/internal/jam/adminui/templates/projtree.html b/internal/jam/adminui/templates/projtree.html
deleted file mode 100644
index eac1a9d..0000000
--- a/internal/jam/adminui/templates/projtree.html
+++ /dev/null
@@ -1,60 +0,0 @@
-{{/* project-tree is the left-side navigation on every project page (a
-     projectTree). Branches are <details>: the one holding the current page
-     renders open. On a narrow screen the whole tree is a disclosure whose
-     summary names the current node (the layout script closes it there).
-     project-tree-nav is the tree alone (#ptree): a project write sends it
-     back out of band (OOB set) so it follows rooms added or removed. */}}
-{{define "project-tree"}}
-<style>
-  .pframe{display:grid;grid-template-columns:200px minmax(0,1fr);gap:24px;align-items:start}
-  .ptree-wrap>summary{display:none}
-  .ptree{position:sticky;top:64px;font-size:13px}
-  .ptree a{display:block;color:var(--muted);text-decoration:none;padding:4px 8px;border-radius:7px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
-  .ptree a:hover{color:var(--ink);background:var(--surface-2)}
-  .ptree a[aria-current="page"]{color:var(--accent);background:var(--accent-soft);font-weight:600}
-  .ptree .root{font-weight:600;color:var(--ink);font-size:14px;margin-bottom:4px}
-  .ptree ul{list-style:none;margin:0;padding:0}
-  .ptree ul ul{padding-left:14px;border-left:1px solid var(--border);margin-left:12px}
-  .ptree details>summary{list-style:none;cursor:pointer;display:flex;align-items:center}
-  .ptree details>summary::-webkit-details-marker{display:none}
-  .ptree details>summary::before{content:"▸";width:12px;color:var(--faint);font-size:10px}
-  .ptree details[open]>summary::before{content:"▾"}
-  .ptree details>summary>a{flex:1}
-  .ptree li.leaf>a{margin-left:12px}
-  .ptree .count{color:var(--faint);font-weight:500}
-  .ptree .phase-dot{display:inline-block;width:6px;height:6px;border-radius:50%;background:var(--wait);margin-right:6px;vertical-align:middle}
-  .ptree .phase-dot.live{background:var(--live)}
-  @media (max-width:720px){
-    .pframe{grid-template-columns:1fr;gap:12px}
-    .ptree-wrap{background:var(--surface);border:1px solid var(--border);border-radius:10px;padding:6px}
-    .ptree-wrap>summary{display:block;cursor:pointer;padding:4px 8px;font-weight:600}
-    .ptree{position:static}
-  }
-</style>
-{{template "project-tree-nav" .}}
-{{end}}
-
-{{define "project-tree-nav"}}
-<details id="ptree"{{if .OOB}} hx-swap-oob="true"{{end}} class="ptree-wrap" open>
-  <summary>{{.Project}} ▸ {{.Current}}</summary>
-  <nav class="ptree" aria-label="Project">
-    <a class="root" href="{{.Href}}">{{.Project}}</a>
-    <ul>
-      {{range .Nodes}}
-      {{if .Children}}
-      <li><details{{if .Open}} open{{end}}>
-        <summary><a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}{{if .Count}} <span class="count">({{.Count}})</span>{{end}}</a></summary>
-        <ul>{{range .Children}}<li><a class="mono" href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{if .Phase}}<span class="phase-dot{{if eq .Phase "live"}} live{{end}}"></span>{{end}}{{.Label}}</a></li>{{end}}</ul>
-      </details></li>
-      {{else}}
-      <li class="leaf"><a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}{{if .Count}} <span class="count">({{.Count}})</span>{{end}}</a></li>
-      {{end}}
-      {{end}}
-    </ul>
-  </nav>
-</details>
-{{end}}
-
-{{/* crumbs renders a breadcrumb trail: a list of crumb{Label, Href}; the last
-     one is the current page (no link). */}}
-{{define "crumbs"}}<div class="crumbs">{{range $i, $c := .}}{{if $i}} / {{end}}{{if $c.Href}}<a href="{{$c.Href}}">{{$c.Label}}</a>{{else}}<span>{{$c.Label}}</span>{{end}}{{end}}</div>{{end}}
diff --git a/internal/jam/adminui/templates/role.html b/internal/jam/adminui/templates/role.html
index c3f3494..e597aa8 100644
--- a/internal/jam/adminui/templates/role.html
+++ b/internal/jam/adminui/templates/role.html
@@ -31,13 +31,8 @@
   .form-actions .left{margin-right:auto}
   @media (max-width:720px){.sections{grid-template-columns:1fr}}
 </style>
-<div class="pframe">
-{{template "project-tree" .Tree}}
-<div>
 {{template "crumbs" .Crumbs}}
 {{template "role-body" .}}
-</div>
-</div>
 {{end}}
 {{end}}
 
@@ -187,7 +182,7 @@
   <section class="card">
     <header><h2>Holders</h2><span class="sub">agents granted this role — open one to manage its grants</span></header>
     <div class="body">
-      {{range .Holders}}<span class="chip"><a href="{{agentURL .ID}}" style="color:inherit">{{.ID}}</a>{{if .Override}} <span class="unset" title="This grant overrides part of the role's scope">override</span>{{end}}</span>{{else}}<span class="unset">No agents hold this role.</span>{{end}}
+      {{range .Holders}}<span class="chip"><a href="{{agentHref .ID ""}}" style="color:inherit">{{.ID}}</a>{{if .Override}} <span class="unset" title="This grant overrides part of the role's scope">override</span>{{end}}</span>{{else}}<span class="unset">No agents hold this role.</span>{{end}}
     </div>
   </section>
 
diff --git a/internal/jam/adminui/users.go b/internal/jam/adminui/users.go
index 0f6b16a..a9a8b14 100644
--- a/internal/jam/adminui/users.go
+++ b/internal/jam/adminui/users.go
@@ -78,15 +78,15 @@ func registryErr(err error) error {
 
 func registerUsers(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
 	mux.HandleFunc("GET /ui/users", func(w http.ResponseWriter, r *http.Request) {
-		render(w, "users", usersData(store))
+		render(w, r, "users", usersData(store))
 	})
 	mux.HandleFunc("GET /ui/users/{user}", func(w http.ResponseWriter, r *http.Request) {
 		d, ok := buildUserDetail(store, r.PathValue("user"))
 		if !ok {
-			renderStatus(w, http.StatusNotFound, "user", userDetail{Title: "Users", NotFound: true, NotFoundFor: r.PathValue("user")})
+			renderStatus(w, r, http.StatusNotFound, "user", userDetail{Title: "Users", NotFound: true, NotFoundFor: r.PathValue("user")})
 			return
 		}
-		render(w, "user", d)
+		render(w, r, "user", d)
 	})
 
 	// form wraps a write: guard, parse, apply, log; apply's error carries the
````

- [ ] **Step 4: GREEN** — `go test ./internal/jam/adminui/ && go vet ./internal/jam/adminui/ && gofmt -l internal/jam/adminui && just lint` clean; `just test` green.

- [ ] **Step 5: Commit** — `git add -A internal/jam/adminui && git commit -m "feat(adminui): scope rail with attention badges; tabs replace the top nav and project tree"`

---

### Task 2: Docs

- [ ] **Step 1: Apply**

````diff
diff --git a/docs/usage/jam/INDEX.md b/docs/usage/jam/INDEX.md
index b436077..68a2e5a 100644
--- a/docs/usage/jam/INDEX.md
+++ b/docs/usage/jam/INDEX.md
@@ -55,7 +55,8 @@ five pillars), see the design history:
 | [requisitioner.md](requisitioner.md) | You are enabling Jam's always-on intake — polling a tracker (Linear) and raising a managed studio per ready ticket — or tuning its concurrency cap / poll interval. |
 | [ui.md](ui.md) | You want to watch a running Jam in a browser — the agents and their studios, the squawk Log, a session timeline, the projects/users/specs — find your way around the UI (nav, sub-tabs, dashboard, search), use /me/, or configure browser login. To change something, see ui-editing.md. |
 | [ui-editing.md](ui-editing.md) | You want to change something from the admin UI instead of the CLI — enroll/revoke agents, grants, roles, raise/tear down a studio, Request a personal session, edit kits/destinations/model-specs — or a UI write was refused. |
-| [ui-projects.md](ui-projects.md) | You are viewing or editing one project in the admin UI — its tree of sections (members, agents, roles, rooms and messages, escalation, chat service, context) — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles link. |
+| [ui-projects.md](ui-projects.md) | You are viewing or editing one project in the admin UI — its tabs (overview, members, agents, roles, rooms and messages, escalation, chat service, context) — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles or /ui/projects link. |
+| [ui-attention.md](ui-attention.md) | You see a red or amber badge in the admin UI's rail, on a tab or on a row and want to know what it counts and where to fix it. |
 | [ui-pages.md](ui-pages.md) | You are viewing or editing one user, agent, destination, model-spec or kit in the admin UI — a user's logins/OIDC/accounts, an agent's grants and its studio's runtime/session/squawks, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
 | [session-events.md](session-events.md) | You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream, its storage/retention config, redaction, and the export API. |
 | [intercom.md](intercom.md) | You want a raised studio's agent to read/send comments on its own ticket (the brokered intercom MCP), or you're wiring the `/squawks` endpoint + its `cove-master mcp` delivery, wake-on (`runtime.wake`), or running the intercom without a Requisitioner. |
diff --git a/docs/usage/jam/ui-attention.md b/docs/usage/jam/ui-attention.md
new file mode 100644
index 0000000..a8429ff
--- /dev/null
+++ b/docs/usage/jam/ui-attention.md
@@ -0,0 +1,34 @@
+---
+summary: The admin UI's attention badges — what counts as needing attention (broken studios, out-of-date studios, config that names something missing), which scope and tab each item belongs to, how the rail, tabs and rows show it, and the Needs attention cards.
+read_when: You see a red or amber badge in the admin UI's rail, on a tab or on a row and want to know what it counts and where to fix it, or you want to know why a project or Jam is (or isn't) flagged.
+owns: the attention model (kinds, severity, scope and tab of each item), the rail/tab/row badges and the Needs attention cards
+prereqs: ui.md for the rail and tabs; ui-projects.md for a project's tabs; coves.md for studio phases, image and connector status
+tier: leaf
+updated: 2026-10-07
+---
+
+# Attention badges
+
+The admin UI computes one list of things that need an operator's attention and
+shows it in three places: a **badge** on each rail entry, a badge on the tab
+that owns each item, and a **⚠** on the row it names. A badge is a count of
+items, **red** when any of them is broken, **amber** otherwise; hover for the
+breakdown (`1 broken · 2 out of date`). The rail refreshes every 3 seconds, so
+its badges stay live on every page.
+
+| Kind | Severity | Raised when | Scope · tab |
+|---|---|---|---|
+| broken | red | a studio is `lost` or `terminating`, or its egress re-apply is failing | its project · Agents |
+| out of date | amber | a studio runs a stale image or connector ([coves.md](coves.md#the-studio-verbs)) | its project · Agents |
+| config | amber | an escalation target names nobody on the project | the project · Escalation |
+| config | amber | a role's kit or model-spec no longer exists | the project · Roles |
+| config | amber | a destination is in a connector conflict ([ui-pages.md](ui-pages.md#destination-pages)) | Jam · Specs |
+| config | amber | a kit's current version does not parse | Jam · Specs |
+
+One studio counts once, at its worst (a lost studio on a stale image is one
+broken item). **Jam's badge counts only Jam's own items** — never the sum of its
+projects; each project's badge counts that project's.
+
+**Needs attention.** A project's Overview and Jam's Dashboard each list their
+scope's items, with why and a link to where to fix it, or say nothing needs
+attention. An agent linked from there opens under its project.
diff --git a/docs/usage/jam/ui-pages.md b/docs/usage/jam/ui-pages.md
index f8e97ef..4bb6bce 100644
--- a/docs/usage/jam/ui-pages.md
+++ b/docs/usage/jam/ui-pages.md
@@ -2,7 +2,7 @@
 summary: The Jam admin UI's per-entity pages outside a project — a user's page, an agent's page (/ui/agents/<id>: identity, grants, studio, session, squawks), a destination's page (/ui/destinations/<name>), a model-spec's page (/ui/model-specs/<name>) and a kit's page (/ui/kits/<name>) — what each shows and how editing them works.
 read_when: You are viewing or editing a user, agent, destination, model-spec or kit in the Jam admin UI — a user's logins, OIDC identities or accounts; an agent's grants, studio runtime, waiting/escalation state, session streams or squawks; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
 owns: the user, agent, destination, model-spec and kit detail pages (what they show, their edit forms, the users list, create-only list forms, connector-conflict flags, kit version rail/diff/push)
-prereqs: ui.md for reaching the UI and the top nav; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles; connector.md for destination env/git; kits.md for the StudioKit schema and versioning
+prereqs: ui.md for reaching the UI, the rail and tabs; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles; connector.md for destination env/git; kits.md for the StudioKit schema and versioning
 tier: leaf
 updated: 2026-10-07
 ---
@@ -17,7 +17,7 @@ a field the form didn't show.
 
 ## Project and role pages
 
-A project's pages (its tree, sections and role pages) are in
+A project's pages (its tabs and role pages) are in
 [ui-projects.md](ui-projects.md).
 
 ## User pages
@@ -30,7 +30,8 @@ remove are refused while the user owns a live personal session).
 
 Each agent id (in the Agents list, a studio table, a role's holders, a
 project's identities, or search) opens `/ui/agents/<id>`, the page for one
-agent identity. The header shows its kind, phase and activity, standing name or
+agent identity. It sits under Jam's Agents tab, or — linked from a project's
+pages (`?project=<name>`) — under that project's. The header shows its kind, phase and activity, standing name or
 personal owner, project, role and unit (linked), with **Open live timeline**
 (the [session timeline](ui.md#session-timeline); shown when session capture is
 configured), **Teardown** (returns to the Agents list) when it has a
diff --git a/docs/usage/jam/ui-projects.md b/docs/usage/jam/ui-projects.md
index b3eb165..7d183a2 100644
--- a/docs/usage/jam/ui-projects.md
+++ b/docs/usage/jam/ui-projects.md
@@ -1,22 +1,21 @@
 ---
-summary: The admin UI's project pages — the left-side project tree (Overview, Members, Agents, Roles, Intercom, Escalation) with one URL per section under /ui/projects/<name>, what each section shows and edits, and the role page at /ui/projects/<project>/roles/<name>.
+summary: The admin UI's project pages — a project selected in the rail, its tabs (Overview, Members, Agents, Roles, Intercom, Escalation) with one URL per tab under /ui/projects/<name>, what each tab shows and edits, and the role page at /ui/projects/<project>/roles/<name>.
 read_when: You are viewing or editing one project in the Jam admin UI — its members, agents, roles, rooms and recent messages, escalation chains, chat service or session context — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles link.
-owns: the project tree and its section pages (/ui/projects/<name>[/members|agents|roles|intercom|escalation]), creating a role from a project, the role page (/ui/projects/<project>/roles/<name>), and the /ui/roles redirects
-prereqs: ui.md for reaching the UI and the top nav; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles and grants
+owns: a project's tabs and their pages (/ui/projects/<name>[/members|agents|roles|intercom|escalation]), creating a role from a project, the role page (/ui/projects/<project>/roles/<name>), and the /ui/roles and /ui/projects redirects
+prereqs: ui.md for reaching the UI, the rail and tabs; ui-attention.md for the badges and Needs attention; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles and grants
 tier: leaf
 updated: 2026-10-07
 ---
 
 # Admin UI project pages
 
-A project's pages share a **left-side tree** — the project, then **Overview ·
-Members · Agents · Roles · Intercom · Escalation** — beside the selected page.
-Each node is its own URL, so it can be linked and bookmarked, and the
-breadcrumb above the content follows the path (`Projects / acme / Roles / dev`,
-every segment a link). Everything scoped to one project lives here; the top
-nav's **Projects** section stays highlighted throughout.
+Select a project in the [rail](ui.md) and its tabs — **Overview · Members ·
+Agents · Roles · Intercom · Escalation** — sit over its pages. Each tab is its
+own URL, so it can be linked and bookmarked; a role page sits under Roles with
+a `Roles / dev` breadcrumb. Everything scoped to one project lives here, and its
+tabs carry the project's [attention badges](ui-attention.md).
 
-| Node | URL |
+| Tab | URL |
 |---|---|
 | Overview | `/ui/projects/<name>` |
 | Members | `/ui/projects/<name>/members` |
@@ -25,25 +24,18 @@ nav's **Projects** section stays highlighted throughout.
 | Intercom | `/ui/projects/<name>/intercom` |
 | Escalation | `/ui/projects/<name>/escalation` |
 
-**The tree.** Roles, Agents and Intercom expand to their children — every role;
-the project's live and raising agents (the label counts them); its rooms. The
-branch holding the current page renders open and its node highlighted; the
-others expand on click (plain `<details>`, no script needed). On a narrow
-screen the tree collapses to one line naming the current node (`acme ▸ Roles ▸
-dev`); tap it to open the tree. Widening the window opens it again.
-
-**Old links.** `/ui/roles` redirects (301) to `/ui/projects`, and
-`/ui/roles/<project>/<role>` to the role's page here. The write endpoints keep
-their paths.
+**Old links.** `/ui/projects` and `/ui/roles` redirect (301) to `/ui/` (the
+rail is the project list), and `/ui/roles/<project>/<role>` to the role's page
+here. The write endpoints keep their paths. A new project is created from the
+rail's **+ New project**, which opens it.
 
 ## Sections
 
-Each section's edits are in place: a write answers with that section
-re-rendered, and the tree with it (so an added or removed room shows there at
-once).
+Each tab's edits are in place: a write answers with that tab's content
+re-rendered.
 
-- **Overview** — counts (members, agents live of all, roles, rooms, escalation
-  chains), each linking to its section; the **chat service** (none — tracker
+- **Overview** — the project's **Needs attention** card; counts (members,
+  agents live of all, roles, rooms, escalation chains), each linking to its tab; the **chat service** (none — tracker
   @-mentions only — or `discord`); the project's **Session context** card
   ([ui-pages.md](ui-pages.md#session-context-cards)). **Rename** (except
   `default`) renames the project and opens its new URL; **Delete** is disabled
@@ -73,7 +65,7 @@ once).
 
 ## Role pages
 
-Each role name (in the tree, a project's Roles, a grant chip, or a studio row)
+Each role name (in a project's Roles, a grant chip, or a studio row)
 links to its page, `/ui/projects/<project>/roles/<name>`, which shows and edits the whole
 role, one section at a time — each with a pre-filled **Edit** form that saves
 only that section:
diff --git a/docs/usage/jam/ui.md b/docs/usage/jam/ui.md
index ba236b9..08efb7a 100644
--- a/docs/usage/jam/ui.md
+++ b/docs/usage/jam/ui.md
@@ -1,7 +1,7 @@
 ---
-summary: The Jam admin UI — a server-rendered web view of the agents and their studios, the projects, users and specs, and the durable squawk Log, served by `at-jam serve`; reachable on loopback always, and off-loopback via browser OIDC login. Covers the top nav and its sub-tabs, the list pages, search, the Intercom log, the session timeline and the participant /me/ surface; what the UI can change is in ui-editing.md.
+summary: The Jam admin UI — a server-rendered web view of the agents and their studios, the projects, users and specs, and the durable squawk Log, served by `at-jam serve`; reachable on loopback always, and off-loopback via browser OIDC login. Covers the rail of scopes (Jam and each project), their tabs, the list pages, search, the Intercom log, the session timeline and the participant /me/ surface; what the UI can change is in ui-editing.md.
 read_when: You want to watch a running Jam in a browser — the agents and their studios, the squawk Log, a session timeline, the projects/users/specs — find your way around the UI (nav, sub-tabs, search), use the participant /me/ page, or configure browser login for it. To change something from the UI, read ui-editing.md instead.
-owns: the `/ui/agents/{id}/session` timeline page; the `/ui/` observability surface (the top nav and its sections, what each list shows, search, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
+owns: the `/ui/agents/{id}/session` timeline page; the `/ui/` observability surface (the rail, Jam's tabs and the Specs sub-tabs, what each list shows, search, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
 prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
 tier: leaf
 updated: 2026-10-07
@@ -17,16 +17,21 @@ there):
 http://127.0.0.1:8081/ui/
 ```
 
-The top nav has six sections — **Dashboard · Projects · Users · Agents ·
-Specs · Intercom** — and a page highlights its section, so a detail page
-highlights the list it belongs to (a role page: Projects). **Specs**
-(`/ui/specs`, opening on Kits) groups **Kits · Destinations · Model-specs**
-under a sub-tab strip (their detail pages show it too). It renders:
+A permanent **rail** on the left lists **Jam**, then every project, then **+
+New project**; the selection decides the **tabs** over the content. Jam's tabs
+are **Dashboard · Agents · Users · Specs · Intercom**; a project's are
+**Overview · Members · Agents · Roles · Intercom · Escalation**
+([ui-projects.md](ui-projects.md)). A detail page sits under its tab (a role
+page: its project's Roles; a kit: Jam's Specs) with a breadcrumb below it.
+**Specs** (`/ui/specs`, opening on Kits) groups **Kits · Destinations ·
+Model-specs** under a sub-tab strip. On a narrow screen the rail is a drawer
+(☰ in the title bar). Rail entries, tabs and rows carry **attention badges**
+([ui-attention.md](ui-attention.md)). Jam's pages:
 
 - **Dashboard** (`/ui/`) — summary tiles: live / raising / lost-or-terminating /
   idled agents, each opening the Agents list filtered to that phase, and counts
   of projects, agents, users and specs (kits + destinations + model-specs),
-  each opening its section; then the Jam-wide **Session context** card
+  each opening its section; Jam's **Needs attention** card; then the Jam-wide **Session context** card
   ([ui-pages.md](ui-pages.md#session-context-cards)), above the studio table.
 - **Search** — the box in the top bar (press `/` from anywhere) searches every
   page's objects at once: agents (id, unit, owner, standing name,
@@ -36,14 +41,9 @@ under a sub-tab strip (their detail pages show it too). It renders:
   users (name, logins, OIDC subject, account handles and ids) and rooms, and
   squawk bodies (newest 10; the rest via Intercom's `q=`).
   Matching is case-insensitive substring, at least 2 characters; results are
-  grouped and link to each object's page. **Enter** jumps straight to the page
-  when exactly one object's name is the whole query (e.g. an agent id or
-  `acme/dev`); otherwise it opens `/ui/search?q=…`, which updates as you type.
-  Session event streams are not searched.
-- **Projects** (`/ui/projects`) — every project with its roles, agents,
-  roster size and chat service; create one, or delete one nothing
-  references. Each project opens on a tree of its sections — members, agents,
-  roles, rooms and messages, escalation — see [ui-projects.md](ui-projects.md).
+  grouped and link to each object's page. **Enter** jumps to the page when one
+  object's name is the whole query (e.g. an agent id or `acme/dev`), else opens
+  `/ui/search?q=…`, which updates as you type. Session events aren't searched.
 - **Agents** (`/ui/agents`) — each enrolled identity and each studio, one row
   per id, with its **kind** (`standing`, `personal`, `ticket` — a session with
   a unit, `manual`, or `enrolled` — no studio), project/role, phase, activity,
````

- [ ] **Step 2: Audit** — no ERROR/WARN for `usage/jam/ui*.md`, `INDEX.md` (ignore duplicated-line WARNs against `docs/superpowers/plans/*`).

- [ ] **Step 3: Commit** — `git add docs/usage/jam && git commit -m "docs(jam): the scope rail, tabs and attention badges"`

---

### Task 3: Verify

- [ ] `just test`, `just lint` green; no tracked changes.
- [ ] Eyeball on `just dev-watch`: the rail on every page (selection, badges, + New project, 3s refresh), Jam and project tabs with badges, a role and an agent page's placement (agent via a project keeps the project), the Needs attention cards, ⚠ on rows, the drawer at narrow width.
