package adminui_test

import (
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

// A project's Agents view is one row per agent running in the project or
// holding a grant into it, in id order, with its kind, status and roles
// there; an agent only in another project is not listed.
func TestProjectAgentsAPI(t *testing.T) {
	store := seedAgents(t)
	mustCreateProject(t, store, "beta")
	if err := store.PutRole("beta", jam.Role{Name: "w"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []jam.Actor{
		{ID: "b-only", TokenHash: "h3", Grants: []jam.Grant{{Project: "beta", Role: "w"}}},
		{ID: "both", TokenHash: "h4", Grants: []jam.Grant{{Project: "beta", Role: "w"}, {Project: "acme", Role: "ops"}, {Project: "acme", Role: "dev"}}},
	} {
		if err := store.AddActor(a); err != nil {
			t.Fatal(err)
		}
	}
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	var pa adminui.ProjectAgents
	apiDo(t, h, "GET", "/app/admin/api/projects/acme/agents", nil, &pa)
	var got []string
	for _, a := range pa.Agents {
		got = append(got, a.ID+":"+a.Kind+":"+a.Status+":"+strings.Join(a.Roles, "+"))
	}
	want := []string{
		"both:enrolled:pending:ops+dev",
		"e-only:enrolled:pending:ops",
		"s-manual:manual:lost:ops",
		"s-personal:personal:idled:dev",
		"s-standing:standing:running:dev",
		"s-ticket:ticket:setting up:dev", // a studio and an enrolled grant: one row, one role
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("acme agents =\n%v\nwant\n%v", got, want)
	}
	for _, a := range pa.Agents {
		if a.ID == "s-ticket" && a.Unit != "COV-7" {
			t.Errorf("ticket row = %+v", a)
		}
	}
	var beta adminui.ProjectAgents
	apiDo(t, h, "GET", "/app/admin/api/projects/beta/agents", nil, &beta)
	if len(beta.Agents) != 2 || beta.Agents[0].ID != "b-only" || beta.Agents[1].ID != "both" || strings.Join(beta.Agents[1].Roles, ",") != "w" {
		t.Errorf("beta agents = %+v", beta.Agents)
	}
}
