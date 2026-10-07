package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// Moved pages answer 301 to their new home, keeping the query string.
func TestMovedPagesRedirect(t *testing.T) {
	h := projHandler(seedProjects(t))
	for from, to := range map[string]string{
		"/ui/roles":               "/ui/",
		"/ui/roles/acme/dev":      "/ui/projects/acme/roles/dev",
		"/ui/roles?x=1":           "/ui/?x=1",
		"/ui/projects":            "/ui/",
		"/ui/projects/acme/roles": "/ui/projects/acme/agents",
	} {
		rec := get(t, h, from)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
			t.Errorf("GET %s = %d → %q, want 301 → %q", from, rec.Code, rec.Header().Get("Location"), to)
		}
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
		// the section only: no page chrome, rail or tabs
		if strings.Contains(body, "<html") || strings.Contains(body, `id="rail"`) || strings.Contains(body, `class="tabs"`) {
			t.Errorf("%s %s should answer with the section only", c.method, c.path)
		}
	}
}

// A role is created from its project's Roles page (the project comes from the
// page) and the response sends the browser to the new role's page.
func TestCreateRoleFromProject(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	page := get(t, h, "/ui/projects/acme/agents").Body.String()
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
