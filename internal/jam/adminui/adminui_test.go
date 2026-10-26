package adminui_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newStore(t *testing.T) jam.Store {
	t.Helper()
	st := jam.NewMemStore()
	return st
}

// mustCreateProject records project name on st: every project-scoped write
// needs its project to exist first.
func mustCreateProject(t *testing.T, st jam.Store, name string) {
	t.Helper()
	if err := st.CreateProject(name); err != nil {
		t.Fatalf("CreateProject(%q): %v", name, err)
	}
}

// get serves GET path to h as a Local admin — as the /app gate does — so the
// page's title bar carries the admin tabs.
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(httptest.NewRequest(http.MethodGet, path, nil), appui.Principal{Admin: true, Local: true}))
	return rec
}

func TestDashboard(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	if err := store.AddActor(jam.Actor{ID: "mgr-1"}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app/admin/ = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="coves"`, "spider-9", `hx-trigger="every 3s"`, `data-stat="agents"><b>2</b>`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func seedCove(t *testing.T, store jam.Store) {
	t.Helper()
	if err := store.PutInstance(jam.Instance{
		ActorID: "spider-9", Project: "acme", Role: "worker", Unit: "COV-1",
		Phase: jam.PhaseLive, Activity: jam.ActivityRunning,
		Lease:    jam.Lease{Holder: "jam-a"},
		RaisedAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCovesFullPage(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	rec := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/agents")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app/admin/agents = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<nav", `id="agents"`, "spider-9", "live", "running", `hx-trigger="every 3s"`} {
		if !strings.Contains(body, want) {
			t.Errorf("full page missing %q", want)
		}
	}
}

func TestCovesFragment(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app/admin/agents", nil)
	req.Header.Set("HX-Request", "true")
	adminui.Handler(store, testLogger(), nil, nil, anyCred, nil).ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `id="agents"`) || !strings.Contains(body, "spider-9") {
		t.Errorf("fragment missing table/row; got:\n%s", body)
	}
	if strings.Contains(body, "<nav") || strings.Contains(body, "<html") {
		t.Errorf("fragment must not include page chrome; got:\n%s", body)
	}
}

func TestCovesNoSecretLeak(t *testing.T) {
	store := newStore(t)
	if err := store.PutInstance(jam.Instance{
		ActorID: "spider-9", Project: "acme", Role: "worker",
		Phase: jam.PhaseLive, LaunchSecretHash: "SECRET-HASH-XYZ",
		Lease: jam.Lease{Holder: "h"}, RaisedAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/agents")
	if strings.Contains(rec.Body.String(), "SECRET-HASH-XYZ") {
		t.Error("cove view leaked the launch-secret hash")
	}
}

func TestRosterView(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "worker", Scope: jam.Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(jam.Actor{ID: "spider-2", TokenHash: "HASH-NOPE", Grants: []jam.Grant{{Project: "acme", Role: "worker"}}}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	body := get(t, h, "/app/admin/agents").Body.String()
	for _, want := range []string{`href="/app/admin/agents/spider-2"`, "enrolled", `href="/app/admin/projects/acme/roles/worker"`} {
		if !strings.Contains(body, want) {
			t.Errorf("agents list missing %q", want)
		}
	}
	// the agent page shows its grants with their effective destinations
	body = get(t, h, "/app/admin/agents/spider-2").Body.String()
	for _, want := range []string{"acme/worker", "destinations: anthropic"} {
		if !strings.Contains(body, want) {
			t.Errorf("agent page missing %q", want)
		}
	}
}

func TestRosterViewNoSecretLeak(t *testing.T) {
	store := newStore(t)
	if err := store.AddActor(jam.Actor{ID: "spider-2", TokenHash: "HASH-NOPE"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	if strings.Contains(get(t, h, "/app/admin/agents").Body.String()+get(t, h, "/app/admin/agents/spider-2").Body.String(), "HASH-NOPE") {
		t.Error("roster view leaked a token hash")
	}
}

func TestRolesView(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "review", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour}, Kit: ""}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/projects/acme/agents").Body.String()
	for _, want := range []string{`href="/app/admin/projects/acme/roles/review"`, "git", `<input type="hidden" name="project" value="acme">`} {
		if !strings.Contains(body, want) {
			t.Errorf("project roles view missing %q", want)
		}
	}
}

func TestKitsView(t *testing.T) {
	store := newStore(t)
	if _, err := store.PushKit("base", "listen: :443"); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/kits").Body.String()
	if !strings.Contains(body, "base") {
		t.Errorf("kits view missing kit name; got:\n%s", body)
	}
}

func TestDestinationsView(t *testing.T) {
	store := newStore(t)
	if err := store.AddDestination(jam.Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", CredName: "anthropic-key"}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/destinations").Body.String()
	for _, want := range []string{"anthropic", "/anthropic/", "https://api.anthropic.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("destinations view missing %q", want)
		}
	}
}

func TestDashboardManagersPanel(t *testing.T) {
	store := newStore(t)
	list := func() []jam.ManagerStatus {
		return []jam.ManagerStatus{
			{Name: "local", Local: true, Connected: true, Studios: 1},
			{Name: "mac-1", Connected: true, Version: "v1.2.3", Studios: 2, Capacity: 4,
				Strays: []jam.ManagedStudio{{ActorID: "stray-7"}}},
		}
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithManagers(list)), "/app/admin/").Body.String()
	for _, want := range []string{"Managers", "mac-1", "connected", "2/4", "v1.2.3", `title="stray-7"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	plain := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/").Body.String()
	if strings.Contains(plain, "Managers") {
		t.Error("dashboard without WithManagers must not show a Managers panel")
	}
}

func TestCovesTableManagerColumn(t *testing.T) {
	store := newStore(t)
	if err := store.PutInstance(jam.Instance{
		ActorID: "spider-9", Project: "acme", Role: "worker", Manager: "mac-1",
		Phase: jam.PhaseLive, Activity: jam.ActivityRunning,
		RaisedAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/app/admin/").Body.String()
	if !strings.Contains(body, "<th>Manager</th>") || !strings.Contains(body, "mac-1") {
		t.Errorf("studios table missing Manager column/value; got:\n%s", body)
	}
}
