package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func TestNavMarksCurrentPage(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	for path, href := range map[string]string{
		"/ui/":             `href="/ui/"`,
		"/ui/actors":       `href="/ui/coves"`, // Agents
		"/ui/kits":         `href="/ui/kits"`,  // Specs
		"/ui/destinations": `href="/ui/kits"`,
		"/ui/users":        `href="/ui/users"`,
		"/ui/projects":     `href="/ui/projects"`,
	} {
		body := get(t, h, path).Body.String()
		nav := body[strings.Index(body, "<nav>"):strings.Index(body, "</nav>")] // the top nav (sub-tabs mark their own)
		if !strings.Contains(nav, href+` aria-current="page"`) {
			t.Errorf("%s: nav should mark %s as current", path, href)
		}
		if n := strings.Count(nav, ` aria-current="page">`); n != 1 {
			t.Errorf("%s: %d nav items marked current, want 1", path, n)
		}
	}
}

// Every page carries the flash region and the htmx error hook, so a 4xx/5xx
// write response is shown to the operator instead of silently dropped.
func TestLayoutShipsFlashAndErrorHook(t *testing.T) {
	body := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/projects").Body.String()
	for _, want := range []string{`id="flash"`, "htmx:responseError", `href="/ui/static/jam.css`} {
		if !strings.Contains(body, want) {
			t.Errorf("layout missing %q", want)
		}
	}
}

func TestDashboardShowsStatTiles(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/").Body.String()
	for _, want := range []string{`data-stat="live"`, `data-stat="attention"`, `data-stat="actors"`, `href="/ui/kits"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestPhasePill(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/coves").Body.String()
	if !strings.Contains(body, `class="pill phase-live"`) {
		t.Errorf("studios table should render the phase as a pill; got:\n%s", body)
	}
}

// The roster renders one collapsed add-grant form per actor and grants as
// removable chips, not a full form row under every grant table.
func TestRosterPerActorGrantForm(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-1", "a-2"} {
		if err := store.AddActor(jam.Actor{ID: id, TokenHash: "h-" + id, Grants: []jam.Grant{{Project: "acme", Role: "worker"}}}); err != nil {
			t.Fatal(err)
		}
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/actors").Body.String()
	if n := strings.Count(body, `hx-post="/ui/actors/`); n != 2 {
		t.Errorf("want one add-grant form per actor (2), got %d", n)
	}
	if n := strings.Count(body, `<details class="grant-add"`); n != 2 {
		t.Errorf("add-grant forms should be collapsed <details>, got %d", n)
	}
	if !strings.Contains(body, `hx-delete="/ui/actors/a-1/grants/acme/worker"`) {
		t.Errorf("grant chip should carry its remove action")
	}
}

func TestEnrollResultHasCopyButton(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	rec := post(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/enrollments",
		url.Values{"id": {"s-1"}, "project": {"acme"}, "role": {"worker"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-copy`) {
		t.Errorf("token panel should offer a copy button; got:\n%s", rec.Body.String())
	}
}

// The kit page's version rail offers Pin on every non-current version.
func TestKitPageOffersPinPerVersion(t *testing.T) {
	store := newStore(t)
	for _, c := range []string{"kind: studio\nprompt: one\n", "kind: studio\nprompt: two\n"} {
		if _, _, err := jam.PushStudioKit(store, "base", c); err != nil {
			t.Fatal(err)
		}
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/kits/base").Body.String()
	if n := strings.Count(body, `hx-post="/ui/kits/base/pin"`); n != 1 {
		t.Errorf("want one Pin (v1; v2 is current), got %d", n)
	}
	if !strings.Contains(body, `name="version" value="1"`) {
		t.Errorf("pin form should target v1")
	}
}

// Role Request reports into the page's shared flash region, not a bespoke one.
func TestRoleRequestTargetsFlash(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "pair"}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil), "/ui/projects/acme/roles").Body.String()
	if !strings.Contains(body, `hx-target="#flash"`) || strings.Contains(body, "role-request-msg") {
		t.Errorf("Request should target #flash; got:\n%s", body)
	}
}
