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
		"/ui/agents":       `href="/ui/agents"`,
		"/ui/kits":         `href="/ui/specs"`, // Specs
		"/ui/destinations": `href="/ui/specs"`,
		"/ui/users":        `href="/ui/users"`,
		"/ui/intercom":     `href="/ui/intercom"`,
	} {
		body := get(t, h, path).Body.String()
		tabs := body[strings.Index(body, `<nav class="tabs"`):]
		tabs = tabs[:strings.Index(tabs, "</nav>")] // Jam's tabs (sub-tabs mark their own)
		if !strings.Contains(tabs, href+` aria-current="page"`) {
			t.Errorf("%s: tabs should mark %s as current", path, href)
		}
		if n := strings.Count(tabs, ` aria-current="page">`); n != 1 {
			t.Errorf("%s: %d tabs marked current, want 1", path, n)
		}
	}
}

// Every page carries the flash region and the htmx error hook, so a 4xx/5xx
// write response is shown to the operator instead of silently dropped.
func TestLayoutShipsFlashAndErrorHook(t *testing.T) {
	body := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/ui/").Body.String()
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
	for _, want := range []string{`data-stat="running"`, `data-stat="waiting"`, `data-stat="setting-up"`, `data-stat="attention"`, `data-stat="agents"`, `href="/ui/specs"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestPhasePill(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/agents").Body.String()
	if !strings.Contains(body, `<span class="pill st-running">running</span>`) {
		t.Errorf("the agents table should render the status as a pill; got:\n%s", body)
	}
}

// An agent's page renders its grants as removable chips and one collapsed
// add-grant form.
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
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/agents/a-1").Body.String()
	if n := strings.Count(body, `hx-post="/ui/actors/a-1/grants"`); n != 1 {
		t.Errorf("want one add-grant form on the agent's page, got %d", n)
	}
	if n := strings.Count(body, `<details class="grant-add"`); n != 1 {
		t.Errorf("the add-grant form should be a collapsed <details>, got %d", n)
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
	body := get(t, adminui.Handler(store, testLogger(), &jam.Supervisor{}, nil, anyCred, nil), "/ui/projects/acme/agents").Body.String()
	if !strings.Contains(body, `hx-target="#flash"`) || strings.Contains(body, "role-request-msg") {
		t.Errorf("Request should target #flash; got:\n%s", body)
	}
}
