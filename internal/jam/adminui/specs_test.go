package adminui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// seedSpecUse: model-spec "opus" bound by acme/dev (and the default spec); acme/ops binds none (so it
// runs the default); a kit "web" bound by acme/dev.
func seedSpecUse(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	// "opus", and the default spec a serving Jam seeds (a bare store has none).
	for _, name := range []string{"opus", jam.DefaultModelSpec} {
		if err := store.PutModelSpec(jam.ModelSpec{Name: name, Type: jam.HarnessClaude, Version: "2.1.0",
			Principal: jam.ModelPrincipal{Credential: "anth"}, Model: jam.ModelChoice{ID: "claude-" + name + "-5-5"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PushKit("web", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []jam.Role{{Name: "dev", ModelSpec: "opus", Kit: "web"}, {Name: "ops"}} {
		if err := store.PutRole("acme", r); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// Specs opens on its first sub-tab.
func TestSpecsLanding(t *testing.T) {
	rec := get(t, adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil), "/app/admin/specs")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/app/admin/kits" {
		t.Fatalf("GET /app/admin/specs = %d → %q, want 302 → /app/admin/kits", rec.Code, rec.Header().Get("Location"))
	}
}

// A role's kit links to the kit's page, not the kit list.
func TestRoleKitLinksKitPage(t *testing.T) {
	body := get(t, adminui.Handler(seedSpecUse(t), testLogger(), nil, nil, anyCred, nil), "/app/admin/projects/acme/roles/dev").Body.String()
	if !strings.Contains(body, `<a class="chip accent" href="/app/admin/kits/web">web</a>`) {
		t.Errorf("role page should link its kit's page")
	}
}

// A model-spec's page lists the roles that run it — bound ones, and for the
// default spec the unbound ones — and the list counts them.
func TestModelSpecUsedBy(t *testing.T) {
	store := seedSpecUse(t)
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	body := get(t, h, "/app/admin/model-specs/opus").Body.String()
	if !strings.Contains(body, "<h2>Used by</h2>") || !strings.Contains(body, `href="/app/admin/projects/acme/roles/dev">acme/dev</a>`) || strings.Contains(body, "acme/ops") {
		t.Errorf("opus used-by should list acme/dev only:\n%s", body)
	}
	body = get(t, h, "/app/admin/model-specs/"+jam.DefaultModelSpec).Body.String()
	if !strings.Contains(body, `acme/ops <span class="unset">no model-spec set</span>`) || strings.Contains(body, "acme/dev") {
		t.Errorf("the default spec's used-by should list the unbound acme/ops only:\n%s", body)
	}
	list := get(t, h, "/app/admin/model-specs").Body.String()
	row := list[strings.Index(list, `<tr data-id="opus">`):]
	row = row[:strings.Index(row, "</tr>")]
	if !strings.Contains(row, "<td>1</td>") {
		t.Errorf("model-specs list should count opus's one role:\n%s", row)
	}
}

// Search covers model-specs and gives one Agents hit per id, whether it is a
// studio, an enrolled actor or both.
func TestSearchModelSpecsAndAgents(t *testing.T) {
	store := seedSpecUse(t)
	if err := store.PutInstance(jam.Instance{ActorID: "zz-agent", Project: "acme", Role: "dev", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(jam.Actor{ID: "zz-agent", TokenHash: "h", Grants: []jam.Grant{{Project: "acme", Role: "dev"}}}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	body := get(t, h, "/app/admin/search?q=zz-agent").Body.String()
	if n := strings.Count(body, `href="/app/admin/agents/zz-agent"`); n != 1 {
		t.Errorf("a studio that is also enrolled should be one search hit, got %d", n)
	}
	body = get(t, h, "/app/admin/search?q=claude-opus").Body.String()
	if !strings.Contains(body, `href="/app/admin/model-specs/opus"`) {
		t.Errorf("search should find a model-spec by its model:\n%s", body)
	}
	if rec := get(t, h, "/app/admin/search?go=1&q=opus"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app/admin/model-specs/opus" {
		t.Errorf("an exact model-spec name should jump to it: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
}

// The dashboard's studio tiles open the Agents list filtered to their phase;
// its count tiles follow the tabs — Projects, listed in the rail, is a plain
// count.
func TestDashboardTilesFollowSections(t *testing.T) {
	body := get(t, adminui.Handler(seedSpecUse(t), testLogger(), nil, nil, anyCred, nil), "/app/admin/").Body.String()
	for _, want := range []string{
		`href="/app/admin/agents?status=running" data-stat="running"`, `href="/app/admin/agents?status=waiting" data-stat="waiting"`,
		`href="/app/admin/agents?status=setting-up" data-stat="setting-up"`, `href="/app/admin/agents?status=attention" data-stat="attention"`,
		`href="/app/admin/agents?status=idled" data-stat="idled"`,
		`<div class="tile" data-stat="projects"`, `href="/app/admin/agents" data-stat="agents"`,
		`href="/app/admin/users" data-stat="users"`, `href="/app/admin/specs" data-stat="specs"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %s", want)
		}
	}
	for _, gone := range []string{`data-stat="roles"`, `data-stat="actors"`, `href="/app/admin/roles"`, `href="/app/admin/coves"`, `href="/app/admin/projects"`} {
		if strings.Contains(body, gone) {
			t.Errorf("dashboard still has %s", gone)
		}
	}
	// The agents page's project fields point at the rail, not a dead list.
	if body := get(t, adminui.Handler(seedSpecUse(t), testLogger(), &jam.Supervisor{}, nil, anyCred, nil), "/app/admin/agents").Body.String(); strings.Contains(body, `href="/app/admin/projects"`) {
		t.Error("the agents page still links /app/admin/projects")
	}
}
