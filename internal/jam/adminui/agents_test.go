package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// seedAgents: one agent of each kind — standing, personal, ticket, manual
// (studios), enrolled (an actor with no studio) — and one studio whose actor
// is also enrolled (merged into one row).
func seedAgents(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	for _, r := range []string{"dev", "ops"} {
		if err := store.PutRole("acme", jam.Role{Name: r}); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []jam.Instance{
		{ActorID: "s-standing", Project: "acme", Role: "dev", Name: "nightly", SessionKind: "standing", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "s-personal", Project: "acme", Role: "dev", SessionKind: "personal", Owner: "alice", Phase: jam.PhaseIdled},
		{ActorID: "s-ticket", Project: "acme", Role: "dev", Unit: "COV-7", Phase: jam.PhaseRaising},
		{ActorID: "s-manual", Project: "acme", Role: "ops", Name: "scratch", Phase: jam.PhaseLost},
	} {
		if err := store.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []jam.Actor{
		{ID: "e-only", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "ops"}}},
		{ID: "s-ticket", TokenHash: "h2", Grants: []jam.Grant{{Project: "acme", Role: "dev"}}},
	} {
		if err := store.AddActor(a); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func agentRow(t *testing.T, body, id string) string {
	t.Helper()
	i := strings.Index(body, `<tr data-id="`+id+`">`)
	if i < 0 {
		t.Fatalf("no row for %s in:\n%s", id, body)
	}
	return body[i : i+strings.Index(body[i:], "</tr>")]
}

// The list merges actors and studios by id, in id order, and names each
// agent's kind.
func TestAgentsListMergesAndKinds(t *testing.T) {
	body := get(t, adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil), "/ui/agents").Body.String()
	for id, kind := range map[string]string{
		"s-standing": "standing", "s-personal": "personal", "s-ticket": "ticket", "s-manual": "manual", "e-only": "enrolled",
	} {
		if row := agentRow(t, body, id); !strings.Contains(row, `<span class="chip">`+kind+`</span>`) {
			t.Errorf("%s: want kind %s in\n%s", id, kind, row)
		}
	}
	if n := strings.Count(body, `<tr data-id="s-ticket">`); n != 1 {
		t.Errorf("an enrolled studio is one row, got %d", n)
	}
	if row := agentRow(t, body, "e-only"); !strings.Contains(row, "pending") || !strings.Contains(row, `href="/ui/projects/acme/roles/ops"`) {
		t.Errorf("enrolled-only row: %s", row)
	}
	last := -1
	for _, id := range []string{"e-only", "s-manual", "s-personal", "s-standing", "s-ticket"} {
		i := strings.Index(body, `<tr data-id="`+id+`">`)
		if i < last {
			t.Errorf("rows not in id order at %s", id)
		}
		last = i
	}
}

// ?status= filters the list by status group, as the dashboard tiles count,
// and the polled table keeps the filter; an unknown filter shows everything,
// and the old ?phase= values still work.
func TestAgentsStatusFilter(t *testing.T) {
	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
	for query, want := range map[string][]string{
		"status=running":    {"s-standing"},
		"status=waiting":    {},
		"status=setting-up": {"s-ticket"},
		"status=idled":      {"s-personal"},
		"status=attention":  {"s-manual"},
		"status=bogus":      {"s-standing", "s-ticket", "s-personal", "s-manual", "e-only"},
		"phase=live":        {"s-standing"}, // old links
		"phase=raising":     {"s-ticket"},
	} {
		body := get(t, h, "/ui/agents?"+query).Body.String()
		if n := strings.Count(body, `<tr data-id="`); n != len(want) {
			t.Errorf("%s: %d rows, want %d", query, n, len(want))
		}
		for _, id := range want {
			if !strings.Contains(body, `<tr data-id="`+id+`">`) {
				t.Errorf("%s: missing %s", query, id)
			}
		}
	}
	body := get(t, h, "/ui/agents?status=running").Body.String()
	if !strings.Contains(body, `hx-get="/ui/agents?status=running" hx-trigger="every 3s"`) || !strings.Contains(body, `class="tab sel" href="/ui/agents?status=running"`) {
		t.Errorf("the poll and the filter strip should keep status=running")
	}
}

// Each row shows one status: the studio's phase and the agent's activity on
// one axis; an agent with no studio is pending.
func TestAgentsListStatus(t *testing.T) {
	body := get(t, adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil), "/ui/agents").Body.String()
	for id, want := range map[string]string{
		"s-standing": `<span class="pill st-running">running</span>`,
		"s-personal": `<span class="pill st-idled">idled</span>`,
		"s-ticket":   `<span class="pill st-setting-up">setting up</span>`,
		"s-manual":   `<span class="pill st-lost">lost</span>`,
		"e-only":     `<span class="pill st-pending">pending</span>`,
	} {
		if row := agentRow(t, body, id); !strings.Contains(row, want) {
			t.Errorf("%s: want %s in\n%s", id, want, row)
		}
	}
	if strings.Contains(body, "<th>Phase</th>") || strings.Contains(body, "<th>Activity</th>") {
		t.Error("phase and activity are one Status column")
	}
}

// The retired studio and actor pages answer 301 to their agent pages.
func TestAgentRedirects(t *testing.T) {
	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
	for from, to := range map[string]string{
		"/ui/coves":                    "/ui/agents",
		"/ui/actors":                   "/ui/agents",
		"/ui/coves/s-ticket":           "/ui/agents/s-ticket",
		"/ui/coves/s-ticket/session?x": "/ui/agents/s-ticket/session?x",
	} {
		rec := get(t, h, from)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
			t.Errorf("GET %s = %d → %q, want 301 → %q", from, rec.Code, rec.Header().Get("Location"), to)
		}
	}
}

// An agent's page shows its identity — grants with their controls and
// Revoke — beside its studio; an enrolled-only agent has a page too.
func TestAgentPageIdentity(t *testing.T) {
	h := adminui.Handler(seedAgents(t), testLogger(), &jam.Supervisor{}, nil, anyCred, nil)
	body := get(t, h, "/ui/agents/s-ticket").Body.String()
	for _, want := range []string{
		`<h1 class="mono">s-ticket</h1>`, `<span class="chip">ticket</span>`,
		"<h2>Identity</h2>", `href="/ui/projects/acme/roles/dev"`,
		`hx-delete="/ui/actors/s-ticket/grants/acme/dev"`, `hx-post="/ui/actors/s-ticket/grants"`,
		`hx-delete="/ui/enrollments/s-ticket"`, `hx-delete="/ui/coves/s-ticket"`,
		`<a href="/ui/agents">Agents</a> / <a href="/ui/agents/s-ticket">s-ticket</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("agent page missing %q", want)
		}
	}
	rec := get(t, h, "/ui/agents/e-only")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "This agent isn't running.") || !strings.Contains(rec.Body.String(), `<span class="chip">enrolled</span>`) {
		t.Errorf("enrolled-only agent = %d:\n%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `hx-delete="/ui/coves/e-only"`) {
		t.Error("no studio, no Teardown")
	}
	// a studio without an enrolled actor offers no Revoke
	if body := get(t, h, "/ui/agents/s-manual").Body.String(); strings.Contains(body, `hx-delete="/ui/enrollments/`) || !strings.Contains(body, "Not enrolled") {
		t.Error("an unenrolled studio should say so and offer no Revoke")
	}
}

// Holders on role and project pages link to their agent pages.
func TestHoldersLinkAgents(t *testing.T) {
	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
	if body := get(t, h, "/ui/projects/acme/roles/ops").Body.String(); !strings.Contains(body, `<a href="/ui/agents/e-only?project=acme"`) {
		t.Error("role holders should link their agent pages")
	}
	if body := get(t, h, "/ui/projects/acme/agents").Body.String(); !strings.Contains(body, `<a class="mono" href="/ui/agents/e-only?project=acme"><b>e-only</b></a>`) {
		t.Error("a project's agents should link their agent pages")
	}
}

// The dashboard's studio table polls the dashboard, which answers an htmx
// request with just the table.
func TestDashboardPollsItself(t *testing.T) {
	h := adminui.Handler(seedAgents(t), testLogger(), nil, nil, anyCred, nil)
	if body := get(t, h, "/ui/").Body.String(); !strings.Contains(body, `id="coves" hx-get="/ui/" hx-trigger="every 3s"`) {
		t.Errorf("dashboard studio table should poll /ui/")
	}
	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if body := rec.Body.String(); !strings.HasPrefix(strings.TrimSpace(body), `<div class="card" id="coves"`) || strings.Contains(body, "<nav") || !strings.Contains(body, "s-standing") {
		t.Errorf("poll fragment:\n%s", body)
	}
}

// The personal owner on the agent page links to the user page.
func TestAgentPageLinksOwner(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutInstance(jam.Instance{ActorID: "s-own", Project: "acme", Role: "dev", SessionKind: "personal", Owner: "alice", OwnerID: "usr_alice", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/agents/s-own").Body.String()
	if !strings.Contains(body, `<a href="/ui/users/usr_alice">alice</a>`) {
		t.Errorf("owner should link the user page:\n%s", body)
	}
}

// A project's Agents tab is one table: every agent running in the project or
// holding a grant into it, one row per id, with its roles there.
func TestProjectAgentsOneTable(t *testing.T) {
	store := seedAgents(t)
	mustCreateProject(t, store, "beta")
	if err := store.PutRole("beta", jam.Role{Name: "w"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(jam.Actor{ID: "b-only", TokenHash: "h3", Grants: []jam.Grant{{Project: "beta", Role: "w"}}}); err != nil {
		t.Fatal(err)
	}
	body := get(t, adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), "/ui/projects/acme/agents").Body.String()
	sec := body[strings.Index(body, `<div id="project">`):]
	// the role list, then the one agents table
	if n := strings.Count(sec, "<table"); n != 2 || strings.Index(sec, "<h2>Roles</h2>") > strings.Index(sec, "<h2>Agents</h2>") {
		t.Fatalf("want the Roles table above one Agents table, got %d tables", n)
	}
	if strings.Contains(sec, "Identities") {
		t.Error("the separate Identities table is gone")
	}
	for id, want := range map[string][]string{
		"s-ticket":   {`<span class="chip">ticket</span>`, `href="/ui/projects/acme/roles/dev"`, `class="pill st-setting-up"`}, // studio + enrolled: one row
		"e-only":     {`<span class="chip">enrolled</span>`, `href="/ui/projects/acme/roles/ops"`, `class="pill st-pending"`},
		"s-standing": {`<span class="chip">standing</span>`, `class="pill st-running"`},
	} {
		row := agentRow(t, sec, id)
		for _, w := range want {
			if !strings.Contains(row, w) {
				t.Errorf("%s row missing %s:\n%s", id, w, row)
			}
		}
		if !strings.Contains(row, `href="/ui/agents/`+id+`?project=acme"`) {
			t.Errorf("%s should link its agent page in acme's scope", id)
		}
	}
	if n := strings.Count(sec, `<tr data-id="s-ticket">`); n != 1 {
		t.Errorf("s-ticket should be one row, got %d", n)
	}
	if strings.Contains(sec, "b-only") {
		t.Error("an agent only in beta is not acme's")
	}
}
