package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// seedProjects: acme (two roles, two holders, a studio, a full roster,
// escalation and chat service), beta (empty, removable), and default (one role).
func seedProjects(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	for _, p := range []string{"acme", "beta"} {
		mustCreateProject(t, store, p)
	}
	for _, r := range []struct{ project, name string }{{"acme", "dev"}, {"acme", "ops"}, {"default", "solo"}} {
		if err := store.PutRole(r.project, jam.Role{Name: r.name}); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []jam.Actor{
		{ID: "a1", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "dev"}}},
		{ID: "a2", TokenHash: "h2", Grants: []jam.Grant{{Project: "acme", Role: "ops"}, {Project: "default", Role: "solo"}}},
		{ID: "a3", TokenHash: "h3", Grants: []jam.Grant{{Project: "default", Role: "solo"}}},
	} {
		if err := store.AddActor(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutInstance(jam.Instance{ActorID: "studio-acme", Project: "acme", Role: "dev", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutInstance(jam.Instance{ActorID: "studio-solo", Project: "default", Role: "solo", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(store, "acme", jam.Human{
		Name: "alice", Handle: "alice-h", Login: "sub-alice",
		Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "dm-alice", UserID: "123456789"}},
		Identity: []jam.OIDCIdentity{{Issuer: "https://idp.example", Subject: "oidc-alice"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := putRoom(store, "acme", "eng", "discord", "chan-eng"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEscalationPolicy("acme", "", []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 30 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEscalationPolicy("acme", "deploy", []jam.EscalationTier{{Targets: []string{"human:alice", "channel:eng"}, Timeout: 10 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	return store
}

func projHandler(store jam.Store) http.Handler {
	return adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil)
}

// The rail lists Jam, then every project, on every page; a project page
// selects its project, a Jam page selects Jam.
func TestRailListsProjects(t *testing.T) {
	h := projHandler(seedProjects(t))
	for path, current := range map[string]string{
		"/ui/":                        `<a class="jam" href="/ui/" aria-current="page">`,
		"/ui/users":                   `<a class="jam" href="/ui/" aria-current="page">`,
		"/ui/projects/acme":           `<a href="/ui/projects/acme" aria-current="page"><span class="name">acme</span>`,
		"/ui/projects/beta/agents":    `<a href="/ui/projects/beta" aria-current="page"><span class="name">beta</span>`,
		"/ui/projects/acme/roles/dev": `<a href="/ui/projects/acme" aria-current="page"><span class="name">acme</span>`,
	} {
		body := get(t, h, path).Body.String()
		rail := body[strings.Index(body, `<aside id="rail"`):strings.Index(body, "</aside>")]
		for _, want := range []string{`href="/ui/projects/acme"`, `href="/ui/projects/beta"`, `href="/ui/projects/default"`, current, `hx-post="/ui/projects"`} {
			if !strings.Contains(rail, want) {
				t.Errorf("%s: rail missing %s", path, want)
			}
		}
		if n := strings.Count(rail, `aria-current="page"`); n != 1 {
			t.Errorf("%s: %d rail entries current, want 1", path, n)
		}
	}
	// search selects nothing
	body := get(t, h, "/ui/search?q=acme").Body.String()
	if rail := body[strings.Index(body, `<aside id="rail"`):strings.Index(body, "</aside>")]; strings.Contains(rail, "aria-current") {
		t.Error("search should select no scope")
	}
}

// A project's delete is disabled while something references it, saying what.
func TestProjectDeleteGuard(t *testing.T) {
	h := projHandler(seedProjects(t))
	if body := get(t, h, "/ui/projects/beta").Body.String(); !strings.Contains(body, `hx-delete="/ui/projects/beta"`) {
		t.Errorf("beta is empty: removable")
	}
	body := get(t, h, "/ui/projects/acme").Body.String()
	if strings.Contains(body, `hx-delete="/ui/projects/acme"`) || !strings.Contains(body, "member(s)") {
		t.Errorf("acme is referenced; its delete should be disabled and say why")
	}
}

func TestCreateAndDeleteProject(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	rec := post(t, h, "/ui/projects", url.Values{"name": {"gamma"}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/projects/gamma" {
		t.Fatalf("create = %d redirect=%q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	if rec := post(t, h, "/ui/projects", url.Values{"name": {"gamma"}}); rec.Code != http.StatusConflict {
		t.Errorf("duplicate = %d, want 409", rec.Code)
	}
	if rec := post(t, h, "/ui/projects", url.Values{"name": {"  "}}); rec.Code != http.StatusBadRequest {
		t.Errorf("blank name = %d, want 400", rec.Code)
	}
	if rec := del(t, h, "/ui/projects/beta"); rec.Code != http.StatusOK {
		t.Errorf("delete empty project = %d, want 200", rec.Code)
	}
	if _, ok := store.GetProject("beta"); ok {
		t.Errorf("beta still exists")
	}
	if rec := del(t, h, "/ui/projects/acme"); rec.Code != http.StatusConflict {
		t.Errorf("delete referenced project = %d, want 409", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/ui/projects", strings.NewReader("name=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-origin create = %d, want 403", rr.Code)
	}
}

// Every project page shows the project's tabs; each section shows its own content and
// nothing from another project.
func TestProjectPage(t *testing.T) {
	h := projHandler(seedProjects(t))
	tabs := []string{
		`<div class="scope-title">acme</div>`,
		`href="/ui/projects/acme/members"`, `href="/ui/projects/acme/agents"`,
		`href="/ui/projects/acme/intercom"`, `href="/ui/projects/acme/escalation"`,
	}
	for path, wants := range map[string][]string{
		"/ui/projects/acme":            {"<h1>acme</h1>", "discord"},
		"/ui/projects/acme/members":    {"<h1>Members</h1>", "alice", "alice-h", "dm-alice", `href="/ui/users/usr_`},
		"/ui/projects/acme/agents":     {"<h1>Agents</h1>", "<b>a1</b>", "<b>a2</b>", "<b>studio-acme</b>", "<h2>Roles</h2>", `<input type="hidden" name="project" value="acme">`},
		"/ui/projects/acme/intercom":   {"<h1>Intercom</h1>", "chan-eng"},
		"/ui/projects/acme/escalation": {"<h1>Escalation</h1>", "human:alice", "30m", "deploy", "channel:eng", "10m"},
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range append(wants, tabs...) {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		if strings.HasSuffix(path, "/agents") {
			if sec := body[strings.Index(body, `<div id="project">`):]; !strings.Contains(sec, `href="/ui/agents/studio-acme?project=acme"`) {
				t.Errorf("%s: studios table missing studio-acme", path)
			}
		}
		// ">a3<", not "a3": the page carries random ids (user links) that may contain it.
		for _, gone := range []string{">a3<", "studio-solo", "/ui/projects/default/roles/solo"} {
			if strings.Contains(body, gone) {
				t.Errorf("%s shows %q from another project", path, gone)
			}
		}
	}
}

func TestProjectPageNotFound(t *testing.T) {
	rec := get(t, projHandler(newStore(t)), "/ui/projects/nope")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `<aside id="rail"`) {
		t.Fatalf("missing project = %d", rec.Code)
	}
}

// Project fields are type-aheads over existing projects (suggested from
// /ui/suggest), prefilled with default — the server still refuses an unknown
// project, so a typo can't land anything.
func TestProjectPickers(t *testing.T) {
	h := projHandler(seedProjects(t))
	for _, path := range []string{"/ui/agents", "/ui/agents/a1"} {
		body := get(t, h, path).Body.String()
		if !strings.Contains(body, `<input name="project" data-ta="projects" value="default"`) || strings.Contains(body, `<select name="project"`) {
			t.Errorf("%s: project should be a type-ahead prefilled with default", path)
		}
	}
	// the agent page's add-grant form posts the typed project
	if rec := post(t, h, "/ui/actors/a1/grants", url.Values{"project": {"acme"}, "role": {"ops"}}); rec.Code != http.StatusOK {
		t.Errorf("add-grant = %d", rec.Code)
	}
	// an unknown project is still refused
	if rec := post(t, h, "/ui/roles", url.Values{"project": {"nope"}, "name": {"r"}}); rec.Code != http.StatusNotFound {
		t.Errorf("role in unknown project = %d, want 404", rec.Code)
	}
}

func TestProjectLinks(t *testing.T) {
	h := projHandler(seedProjects(t))
	if body := get(t, h, "/ui/projects/acme/roles/dev").Body.String(); !strings.Contains(body, `href="/ui/projects/acme"`) {
		t.Errorf("role page should link its project")
	}
	if body := get(t, h, "/ui/projects/acme/agents").Body.String(); !strings.Contains(body, `href="/ui/projects/acme/roles/dev"`) {
		t.Errorf("a project's roles should link their pages")
	}
}

// putRoom adds a project's room on the connection of kind service.
func putRoom(st jam.Store, project, name, service, ref string) error {
	_, _, err := jam.PutRoom(st, project, jam.RoomBody{Name: name, Connection: service, Ref: ref})
	return err
}

// The project page renames a project in place and moves to its new URL.
func TestProjectRename(t *testing.T) {
	store := seedProjects(t)
	h := projHandler(store)
	if body := get(t, h, "/ui/projects/acme").Body.String(); !strings.Contains(body, `hx-post="/ui/projects/acme/rename"`) {
		t.Fatal("project page lacks the rename form")
	}
	rec := post(t, h, "/ui/projects/acme/rename", url.Values{"name": {"apex"}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/projects/apex" {
		t.Fatalf("rename = %d %q %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	if _, ok := store.GetProject("apex"); !ok {
		t.Fatal("not renamed")
	}
	if rec := post(t, h, "/ui/projects/apex/rename", url.Values{"name": {""}}); rec.Code == http.StatusOK {
		t.Fatal("an empty name must be refused")
	}
}

// The overview's agent count is the Agents section's rows — studios in the
// project plus actors holding a grant into it, by id — not just studios.
func TestProjectOverviewAgentCount(t *testing.T) {
	// seedAgents: 4 studios (2 live/raising) + enrolled-only e-only; s-ticket is both.
	body := get(t, projHandler(seedAgents(t)), "/ui/projects/acme").Body.String()
	if !strings.Contains(body, "agents live of 5<") || !strings.Contains(body, "<b>2</b><span>agents live of 5") {
		t.Errorf("overview tile:\n%s", body)
	}
}
