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

func TestProjectsTabFollowsDashboard(t *testing.T) {
	body := get(t, projHandler(seedProjects(t)), "/ui/projects").Body.String()
	dash, proj, studios := strings.Index(body, `href="/ui/"`), strings.Index(body, `href="/ui/projects"`), strings.Index(body, `href="/ui/coves"`)
	if !(dash >= 0 && dash < proj && proj < studios) {
		t.Errorf("nav order: dashboard@%d projects@%d studios@%d", dash, proj, studios)
	}
	if !strings.Contains(body, `href="/ui/projects" aria-current="page">Projects`) {
		t.Errorf("Projects tab should be current on /ui/projects")
	}
}

func TestProjectsList(t *testing.T) {
	body := get(t, projHandler(seedProjects(t)), "/ui/projects").Body.String()
	for _, want := range []string{
		`href="/ui/projects/acme"`, `href="/ui/projects/beta"`, `href="/ui/projects/default"`,
		`data-id="acme" data-roles="2" data-actors="2" data-studios="1"`,
		"discord",
		`hx-delete="/ui/projects/beta"`, // empty: removable
	} {
		if !strings.Contains(body, want) {
			t.Errorf("projects list missing %q", want)
		}
	}
	if strings.Contains(body, `hx-delete="/ui/projects/acme"`) {
		t.Errorf("acme is referenced; its delete should be disabled")
	}
	if !strings.Contains(body, "member(s)") {
		t.Errorf("a disabled delete should say what still references the project")
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

// Every project page shows the tree; each section shows its own content and
// nothing from another project.
func TestProjectPage(t *testing.T) {
	h := projHandler(seedProjects(t))
	tree := []string{
		`aria-current="page">Projects`, `class="ptree"`,
		`href="/ui/projects/acme/members"`, `href="/ui/projects/acme/agents"`, `href="/ui/projects/acme/roles"`,
		`href="/ui/projects/acme/intercom"`, `href="/ui/projects/acme/escalation"`,
		`href="/ui/projects/acme/roles/dev"`, `href="/ui/projects/acme/roles/ops"`, // roles under Roles
		"studio-acme", // its live agent under Agents
		">eng<",       // its room under Intercom
	}
	for path, wants := range map[string][]string{
		"/ui/projects/acme":            {"<h1>acme</h1>", "discord"},
		"/ui/projects/acme/members":    {"<h1>Members</h1>", "alice", "alice-h", "dm-alice", `href="/ui/users/usr_`},
		"/ui/projects/acme/agents":     {"<h1>Agents</h1>", ">a1<", ">a2<", "studio-acme"},
		"/ui/projects/acme/roles":      {"<h1>Roles</h1>", `<input type="hidden" name="project" value="acme">`},
		"/ui/projects/acme/intercom":   {"<h1>Intercom</h1>", "chan-eng"},
		"/ui/projects/acme/escalation": {"<h1>Escalation</h1>", "human:alice", "30m", "deploy", "channel:eng", "10m"},
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range append(wants, tree...) {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
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
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<nav") {
		t.Fatalf("missing project = %d", rec.Code)
	}
}

// Project fields are type-aheads over existing projects (suggested from
// /ui/suggest), prefilled with default — the server still refuses an unknown
// project, so a typo can't land anything.
func TestProjectPickers(t *testing.T) {
	h := projHandler(seedProjects(t))
	for _, path := range []string{"/ui/actors", "/ui/coves"} {
		body := get(t, h, path).Body.String()
		if !strings.Contains(body, `<input name="project" data-ta="projects" value="default"`) || strings.Contains(body, `<select name="project"`) {
			t.Errorf("%s: project should be a type-ahead prefilled with default", path)
		}
	}
	// the roster's per-actor add-grant form, re-rendered after a write, keeps it
	rec := post(t, h, "/ui/actors/a1/grants", url.Values{"project": {"acme"}, "role": {"ops"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="project" data-ta="projects"`) {
		t.Errorf("roster fragment after add-grant lost the project type-ahead: %d", rec.Code)
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
	if body := get(t, h, "/ui/projects/acme/roles").Body.String(); !strings.Contains(body, `href="/ui/projects/acme/roles/dev"`) {
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
