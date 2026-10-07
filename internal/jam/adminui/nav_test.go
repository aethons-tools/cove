package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// The top nav is six sections, in order, and a page highlights its section
// whatever its title: a detail page highlights its list's section.
func TestTopNavSections(t *testing.T) {
	h := projHandler(seedProjects(t))
	body := get(t, h, "/ui/").Body.String()
	nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
	last := -1
	for _, label := range []string{">Dashboard<", ">Projects<", ">Users<", ">Agents<", ">Specs<", ">Intercom<"} {
		i := strings.Index(nav, label)
		if i < 0 || i < last {
			t.Fatalf("nav order: %q missing or out of order in\n%s", label, nav)
		}
		last = i
	}
	for _, gone := range []string{">Roles<", ">Actors<", ">Studios<", ">Kits<"} {
		if strings.Contains(nav, gone) {
			t.Errorf("nav still has %s", gone)
		}
	}
	for path, section := range map[string]string{
		"/ui/projects/acme/roles/dev": "Projects",
		"/ui/projects/acme/members":   "Projects",
		"/ui/coves":                   "Agents",
		"/ui/actors":                  "Agents",
		"/ui/kits":                    "Specs",
		"/ui/model-specs":             "Specs",
		"/ui/users":                   "Users",
	} {
		body := get(t, h, path).Body.String()
		nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")]
		if n := strings.Count(nav, `aria-current="page"`); n != 1 || !strings.Contains(nav, `aria-current="page">`+section+"<") {
			t.Errorf("%s: want only %s current in the top nav, got %d marked", path, section, n)
		}
	}
	// search belongs to no section
	if body := get(t, h, "/ui/search?q=acme").Body.String(); strings.Contains(body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")], `aria-current`) {
		t.Errorf("search should highlight no section")
	}
}

// Sections that group several list pages show a sub-tab strip naming them,
// the current page's tab marked; other sections show none.
func TestSectionSubTabs(t *testing.T) {
	h := projHandler(seedProjects(t))
	agents := []string{`href="/ui/coves"`, `href="/ui/actors"`}
	specs := []string{`href="/ui/kits"`, `href="/ui/destinations"`, `href="/ui/model-specs"`}
	for path, c := range map[string]struct {
		tabs    []string
		current string
	}{
		"/ui/coves":        {agents, "Studios"},
		"/ui/actors":       {agents, "Actors"},
		"/ui/kits":         {specs, "Kits"},
		"/ui/destinations": {specs, "Destinations"},
		"/ui/model-specs":  {specs, "Model-specs"},
	} {
		body := get(t, h, path).Body.String()
		i := strings.Index(body, `<nav class="subtabs"`)
		if i < 0 {
			t.Errorf("%s: no sub-tab strip", path)
			continue
		}
		strip := body[i : i+strings.Index(body[i:], "</nav>")]
		for _, tab := range c.tabs {
			if !strings.Contains(strip, tab) {
				t.Errorf("%s: strip missing %s:\n%s", path, tab, strip)
			}
		}
		if n := strings.Count(strip, `aria-current="page"`); n != 1 || !strings.Contains(strip, `aria-current="page">`+c.current+"<") {
			t.Errorf("%s: want only %s current in the strip:\n%s", path, c.current, strip)
		}
	}
	for _, path := range []string{"/ui/", "/ui/users", "/ui/projects/acme"} {
		if strings.Contains(get(t, h, path).Body.String(), `class="subtabs"`) {
			t.Errorf("%s should have no sub-tab strip", path)
		}
	}
}

// The top bar's nav rules are scoped to it: an unscoped nav rule would also
// lay out the project tree's and the sub-tab strip's <nav>.
func TestTopNavStylesAreScoped(t *testing.T) {
	body := get(t, projHandler(seedProjects(t)), "/ui/projects/acme").Body.String()
	if regexp.MustCompile(`(?m)^\s*nav[\s{a\[:]`).MatchString(body) {
		t.Error("layout has an unscoped nav rule")
	}
	if !strings.Contains(body, ".topbar nav{display:flex") {
		t.Error("top nav rules should be scoped to .topbar")
	}
}

// Moved pages answer 301 to their new home, keeping the query string.
func TestMovedPagesRedirect(t *testing.T) {
	h := projHandler(seedProjects(t))
	for from, to := range map[string]string{
		"/ui/roles":          "/ui/projects",
		"/ui/roles/acme/dev": "/ui/projects/acme/roles/dev",
		"/ui/roles?x=1":      "/ui/projects?x=1",
	} {
		rec := get(t, h, from)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
			t.Errorf("GET %s = %d → %q, want 301 → %q", from, rec.Code, rec.Header().Get("Location"), to)
		}
	}
}

// The tree opens the branch holding the current page and marks its node; the
// breadcrumb follows the path, every segment a link.
func TestProjectTreeMarksCurrent(t *testing.T) {
	h := projHandler(seedProjects(t))
	tree := func(body string) string {
		return body[strings.Index(body, `class="ptree"`):strings.Index(body, "</nav>\n</details>")]
	}
	body := get(t, h, "/ui/projects/acme/roles/dev").Body.String()
	tr := tree(body)
	if !strings.Contains(tr, `<a class="mono" href="/ui/projects/acme/roles/dev" aria-current="page">dev</a>`) {
		t.Errorf("role page: its leaf should be current:\n%s", tr)
	}
	if strings.Count(tr, "<details open>") != 1 || !strings.Contains(tr, `<details open>
        <summary><a href="/ui/projects/acme/roles">Roles`) {
		t.Errorf("role page: only the Roles branch should be open:\n%s", tr)
	}
	if !strings.Contains(body, `<summary>acme ▸ Roles ▸ dev</summary>`) {
		t.Errorf("narrow-screen summary should name the current node")
	}
	for _, crumb := range []string{`<a href="/ui/projects">Projects</a>`, `<a href="/ui/projects/acme">acme</a>`,
		`<a href="/ui/projects/acme/roles">Roles</a>`, `<a href="/ui/projects/acme/roles/dev">dev</a>`} {
		if !strings.Contains(body[strings.Index(body, `class="crumbs"`):], crumb) {
			t.Errorf("role page crumbs missing %s", crumb)
		}
	}

	tr = tree(get(t, h, "/ui/projects/acme/members").Body.String())
	if !strings.Contains(tr, `<a href="/ui/projects/acme/members" aria-current="page">Members</a>`) || strings.Contains(tr, "<details open>") {
		t.Errorf("members page: Members current, no branch open:\n%s", tr)
	}
	tr = tree(get(t, h, "/ui/projects/acme").Body.String())
	if !strings.Contains(tr, `<a href="/ui/projects/acme" aria-current="page">Overview</a>`) {
		t.Errorf("project page: Overview current:\n%s", tr)
	}
	if !strings.Contains(tr, `>Agents <span class="count">(1)</span>`) {
		t.Errorf("Agents should count acme's one live agent:\n%s", tr)
	}
}

// A section's write answers with that section re-rendered for its #project
// swap — not the whole project.
func TestProjectEditsAnswerWithTheirSection(t *testing.T) {
	h := projHandler(seedProjects(t))
	for _, c := range []struct {
		method, path string
		form         url.Values
		section      string
	}{
		{http.MethodPost, "/ui/projects/acme/context", url.Values{"yaml": {"core: C\n"}}, "<h1>acme</h1>"},
		{http.MethodDelete, "/ui/projects/acme/context", nil, "<h1>acme</h1>"},
		{http.MethodPost, "/ui/projects/acme/chat-service", url.Values{"service": {""}}, "<h1>acme</h1>"},
		{http.MethodPost, "/ui/projects/acme/members", url.Values{"user": {"alice"}, "add": {"1"}}, "<h1>Members</h1>"},
		{http.MethodDelete, "/ui/projects/acme/members/alice", nil, "<h1>Members</h1>"},
		{http.MethodPost, "/ui/projects/acme/channels", url.Values{"name": {"ops"}, "service": {"discord"}, "ref": {"chan-ops"}}, "<h1>Intercom</h1>"},
		{http.MethodDelete, "/ui/projects/acme/channels/ops", nil, "<h1>Intercom</h1>"},
		{http.MethodPost, "/ui/projects/acme/escalation", url.Values{"category": {"infra"}, "tiers": {"human:alice@10m"}}, "<h1>Escalation</h1>"},
		{http.MethodDelete, "/ui/projects/acme/escalation?category=deploy", nil, "<h1>Escalation</h1>"},
	} {
		var rec *httptest.ResponseRecorder
		if c.method == http.MethodPost {
			rec = post(t, h, c.path, c.form)
		} else {
			rec = del(t, h, c.path)
		}
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(body), `<div id="project">`) || !strings.Contains(body, c.section) {
			t.Errorf("%s %s = %d, want its section %s:\n%s", c.method, c.path, rec.Code, c.section, body)
		}
		// the section, plus the tree only as an out-of-band swap
		if strings.Contains(body, "<html") || strings.Count(body, `class="ptree"`) != 1 || !strings.Contains(body, `id="ptree" hx-swap-oob="true"`) {
			t.Errorf("%s %s should answer with the section and an out-of-band tree", c.method, c.path)
		}
	}
}

// A room write refreshes the tree (out of band): its Intercom branch lists the
// project's rooms.
func TestRoomWritesRefreshTheTree(t *testing.T) {
	h := projHandler(seedProjects(t))
	tree := func(body string) string {
		i := strings.Index(body, `id="ptree"`)
		if i < 0 {
			t.Fatalf("no out-of-band tree:\n%s", body)
		}
		return body[i:]
	}
	if got := tree(post(t, h, "/ui/projects/acme/channels", url.Values{"name": {"ops"}, "service": {"discord"}, "ref": {"chan-ops"}}).Body.String()); !strings.Contains(got, ">ops<") {
		t.Errorf("tree after adding ops lacks it:\n%s", got)
	}
	if got := tree(del(t, h, "/ui/projects/acme/channels/eng").Body.String()); strings.Contains(got, ">eng<") || !strings.Contains(got, ">ops<") {
		t.Errorf("tree after removing eng:\n%s", got)
	}
}

// A role is created from its project's Roles page (the project comes from the
// page) and the response sends the browser to the new role's page.
func TestCreateRoleFromProject(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	page := get(t, h, "/ui/projects/acme/roles").Body.String()
	if !strings.Contains(page, `<form hx-post="/ui/roles" hx-swap="none">`) || strings.Contains(page, `data-ta="projects"`) {
		t.Errorf("roles section should post the role with its project fixed:\n%s", page)
	}
	rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"qa"}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/projects/acme/roles/qa" {
		t.Fatalf("create = %d → %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	if _, ok := store.GetRole("acme", "qa"); !ok {
		t.Error("role not created")
	}
}

// The Intercom section lists the project's rooms above its recent messages,
// linking the full, filtered log.
func TestProjectIntercomSection(t *testing.T) {
	store := jam.NewMemStore()
	for _, p := range []string{"acme", "beta"} {
		if err := store.CreateProject(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := store.LookupName(ident.User, "alice")
	acme, _ := store.GetProject("acme")
	beta, _ := store.GetProject("beta")
	eng, err := store.CreateChannel(jam.Channel{ProjectID: acme.ID, Kind: jam.SourceRoom, Key: "eng", Label: "eng"})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := store.CreateChannel(jam.Channel{ProjectID: beta.ID, Kind: jam.SourceRoom, Key: "ops", Label: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	lg := intercom.NewMemLog(nil)
	ic := jam.NewIntercom(store, func() (ident.ID, bool) { return "", false }, lg, nil, nil)
	for _, c := range []struct {
		ch   jam.Channel
		body string
	}{{eng, "deploy started"}, {ops, "beta status"}} {
		if _, err := ic.PostTrusted(c.ch, intercom.Squawk{From: alice, Body: c.body}); err != nil {
			t.Fatal(err)
		}
	}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, adminui.NewSquawkReader(store, ic, lg, nil))
	body := get(t, h, "/ui/projects/acme/intercom").Body.String()
	for _, want := range []string{"<h1>Intercom</h1>", "deploy started", `href="/ui/intercom?project=acme"`, `hx-post="/ui/projects/acme/channels"`} {
		if !strings.Contains(body, want) {
			t.Errorf("intercom section missing %q", want)
		}
	}
	if strings.Contains(body, "beta status") {
		t.Error("intercom section shows another project's message")
	}
	// no log configured: the rooms still render, the log says so
	body = get(t, projHandler(store), "/ui/projects/acme/intercom").Body.String()
	if !strings.Contains(body, "The message log is unavailable.") || !strings.Contains(body, "Add room") {
		t.Errorf("intercom section without a log:\n%s", body)
	}
}
