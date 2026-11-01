package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func getProject(t *testing.T, store jam.Store, name string) adminui.ProjectDetail {
	t.Helper()
	var d adminui.ProjectDetail
	if rec := apiDo(t, uiHandler(t, store), "GET", "/app/admin/api/projects/"+name, nil, &d); rec.Code != 200 {
		t.Fatalf("GET project %s = %d %s", name, rec.Code, rec.Body)
	}
	return d
}

// A project's view counts its roles and agents (studios live or raising of
// all its agents), lists its members, rooms and every chain with tiers, and
// shows nothing of another project.
func TestProjectAPIView(t *testing.T) {
	store := seedProjects(t)
	d := getProject(t, store, "acme")
	if d.Roles != 2 || d.Agents != 3 || d.LiveAgents != 1 || d.ChatService != "discord" {
		t.Errorf("counts = %+v", d)
	}
	if len(d.Members) != 1 || d.Members[0].Name != "alice" || d.Members[0].Handle != "alice-h" || !strings.Contains(d.Members[0].DeliverySpec, "dm-alice") {
		t.Errorf("members = %+v", d.Members)
	}
	if len(d.Rooms) != 1 || d.Rooms[0].Name != "eng" || d.Rooms[0].Ref != "chan-eng" {
		t.Errorf("rooms = %+v", d.Rooms)
	}
	var chains []string
	for _, c := range d.Escalation {
		chains = append(chains, c.Category+"="+c.Spec)
	}
	if strings.Join(chains, ";") != "=human:alice@30m;deploy=human:alice,channel:eng@10m" {
		t.Errorf("chains = %q", chains)
	}
	if !strings.Contains(d.InUseBy, "member(s)") {
		t.Errorf("acme in use by = %q", d.InUseBy)
	}
	if b := getProject(t, store, "beta"); b.InUseBy != "" || b.Agents != 0 || b.Roles != 0 || len(b.Escalation) != 0 {
		t.Errorf("beta = %+v", b)
	}
	// seedAgents: four studios, two live or raising, and an enrolled-only agent.
	if a := getProject(t, seedAgents(t), "acme"); a.Agents != 5 || a.LiveAgents != 2 {
		t.Errorf("agents %d live %d, want 5 and 2", a.Agents, a.LiveAgents)
	}
}

// What keeps a project from being removed is named: a running agent, else an
// actor's grant into it; neither once the studio is gone and the grant
// revoked.
func TestProjectAPIInUseBy(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "p")
	if err := store.PutInstance(jam.Instance{ActorID: "s1", Project: "p", Role: "r", Phase: jam.PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(jam.Actor{ID: "bot", TokenHash: "h", Grants: []jam.Grant{{Project: "p", Role: "r"}}}); err != nil {
		t.Fatal(err)
	}
	if d := getProject(t, store, "p"); d.InUseBy != "running agent s1" {
		t.Errorf("in use by = %q", d.InUseBy)
	}
	if err := store.PutInstance(jam.Instance{ActorID: "s1", Project: "p", Role: "r", Phase: jam.PhaseGone}); err != nil {
		t.Fatal(err)
	}
	if d := getProject(t, store, "p"); d.InUseBy != "actor bot's grant of p/r" {
		t.Errorf("in use by = %q", d.InUseBy)
	}
	if err := store.RemoveActor("bot"); err != nil {
		t.Fatal(err)
	}
	if d := getProject(t, store, "p"); d.InUseBy != "" {
		t.Errorf("in use by = %q", d.InUseBy)
	}
}

// A project's section writes refuse what doesn't parse or is incomplete: a
// room without its fields, an empty chain, a bad delivery line, an unknown
// user.
func TestProjectAPIEditRefusals(t *testing.T) {
	h := editOn(t, editSetup{seed: seedProjects(t)}).H
	for _, tc := range []struct {
		path string
		body map[string]any
		code int
		want string
	}{
		{"rooms", map[string]any{"name": "ops", "connection": "", "ref": "x"}, 400, "room name, connection and ref are required"},
		{"escalation", map[string]any{"category": "deploy", "tiers": "\n\n"}, 400, "a chain needs at least one tier"},
		{"members", map[string]any{"user": "alice", "delivery": "nonsense"}, 400, `delivery \"nonsense\"`},
		{"members", map[string]any{"user": "nobody", "delivery": ""}, 404, "nobody"},
	} {
		rec := apiDo(t, h, "POST", "/app/admin/api/projects/acme/"+tc.path, tc.body, nil)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s %v = %d %s, want %d %q", tc.path, tc.body, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
	// A category chain is listed after the default; clearing it drops it.
	var d adminui.ProjectDetail
	apiDo(t, h, "POST", "/app/admin/api/projects/acme/escalation", map[string]any{"category": " ops ", "tiers": "human:alice@5m"}, &d)
	var cats []string
	for _, c := range d.Escalation {
		cats = append(cats, c.Category)
	}
	if strings.Join(cats, ",") != ",deploy,ops" {
		t.Errorf("categories = %v", cats)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/projects/acme/escalation?category=ops", nil, &d)
	if len(d.Escalation) != 2 {
		t.Errorf("after clearing ops = %+v", d.Escalation)
	}
}

// A user's OIDC lines are refused when one doesn't parse, naming it.
func TestUserAPIOIDCRefusal(t *testing.T) {
	h, store := adminHandler(t)
	u, err := store.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/users/"+string(u.ID)+"/oidc", map[string]string{"oidc": "https://idp.example sub-1\nnot-an-identity"}, nil)
	wantErr(t, rec, 400, "bad_request")
	if !strings.Contains(rec.Body.String(), `oidc \"not-an-identity\"`) {
		t.Errorf("oidc refusal = %s", rec.Body)
	}
	if got, _ := store.GetUser(u.ID); len(got.OIDC) != 0 {
		t.Errorf("a refused edit stored %+v", got.OIDC)
	}
}
