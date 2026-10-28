package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// actorByID is the stored actor id, or false.
func actorByID(store jam.Store, id string) (jam.Actor, bool) {
	for _, a := range store.ListActors() {
		if a.ID == id {
			return a, true
		}
	}
	return jam.Actor{}, false
}

// overridesFixture: roles acme/w (destination git) and acme/v.
func overridesFixture(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	for _, r := range []jam.Role{{Name: "w", Scope: jam.Scope{Destinations: []string{"git"}}}, {Name: "v"}} {
		if err := store.PutRole("acme", r); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// An enrollment stores its destination overrides — a mapped credential
// checked against the role's effective scope — and refuses a missing id or
// role, an unknown credential, or a bad destinations field.
func TestEnrollmentsAPIOverrides(t *testing.T) {
	store := overridesFixture(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credOK, nil)
	const path = "/app/admin/api/enrollments"
	for _, tc := range []struct {
		body map[string]string
		want string
	}{
		{map[string]string{"id": " ", "role": "w", "project": "acme"}, "id is required"},
		{map[string]string{"id": "m", "role": "", "project": "acme"}, "role is required"},
		{map[string]string{"id": "m", "role": "w", "project": "acme", "destinations": "git=unknown-cred"}, ""},
		{map[string]string{"id": "m", "role": "w", "project": "acme", "destinations": "git="}, ""},
		{map[string]string{"id": "m", "role": "later", "project": "acme", "destinations": "git=known-cred"}, "not found in project"},
	} {
		rec := apiDo(t, h, "POST", path, tc.body, nil)
		wantErr(t, rec, 400, "bad_request")
		if !strings.Contains(rec.Body.String(), tc.want) || strings.Contains(rec.Body.String(), "unknown-cred") {
			t.Errorf("%v = %s, want %q", tc.body, rec.Body, tc.want)
		}
	}
	if len(store.ListActors()) != 0 {
		t.Fatalf("a refused enrollment stored an actor: %+v", store.ListActors())
	}

	var e adminui.Enrolled
	if rec := apiDo(t, h, "POST", path, map[string]string{"id": "m", "role": "w", "project": "acme", "destinations": "git=known-cred"}, &e); rec.Code != 200 || e.Token == "" {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
	}
	a, _ := actorByID(store, "m")
	if len(a.Grants) != 1 || a.Grants[0].Overrides == nil || a.Grants[0].Overrides.Credentials["git"] != "known-cred" || strings.Join(a.Grants[0].Overrides.Destinations, ",") != "git" {
		t.Fatalf("grant = %+v", a.Grants)
	}
	// Destinations without credentials need no credential check; none at all
	// inherit the role's scope.
	apiDo(t, h, "POST", path, map[string]string{"id": "n", "role": "v", "project": "acme", "destinations": "git, anthropic"}, &e)
	if a, _ := actorByID(store, "n"); len(a.Grants) != 1 || a.Grants[0].Overrides == nil || a.Grants[0].Overrides.Credentials != nil || len(a.Grants[0].Overrides.Destinations) != 2 {
		t.Errorf("unmapped override = %+v", a.Grants)
	}
	apiDo(t, h, "POST", path, map[string]string{"id": "o", "role": "w", "project": "acme"}, &e)
	if a, _ := actorByID(store, "o"); len(a.Grants) != 1 || a.Grants[0].Overrides != nil {
		t.Errorf("no override = %+v", a.Grants)
	}
	// An id already enrolled is refused.
	wantErr(t, apiDo(t, h, "POST", path, map[string]string{"id": "o", "role": "w", "project": "acme"}, nil), 400, "bad_request")
}

// A grant stores its overrides; a missing role, a mapped credential on a
// role that doesn't exist or an unknown credential is a 400, an unknown
// agent a 404.
func TestAgentGrantsAPIOverrides(t *testing.T) {
	store := overridesFixture(t)
	if err := store.AddActor(jam.Actor{ID: "m", TokenHash: "h", Grants: []jam.Grant{{Project: "acme", Role: "v"}}}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, nil, credOK, nil)
	const path = "/app/admin/api/agents/m/grants"
	for _, b := range []map[string]string{
		{"project": "acme", "role": ""},
		{"project": "acme", "role": "later", "destinations": "git=known-cred"},
		{"project": "acme", "role": "w", "destinations": "git=unknown-cred"},
		{"project": "acme", "role": "w", "destinations": "=x"},
	} {
		wantErr(t, apiDo(t, h, "POST", path, b, nil), 400, "bad_request")
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/agents/ghost/grants", map[string]string{"project": "acme", "role": "w"}, nil), 404, "not_found")
	var d adminui.AgentDetail
	if rec := apiDo(t, h, "POST", path, map[string]string{"project": "acme", "role": "w", "destinations": "git=known-cred"}, &d); rec.Code != 200 || len(d.Grants) != 2 {
		t.Fatalf("grant = %d %s", rec.Code, rec.Body)
	}
	a, _ := actorByID(store, "m")
	if len(a.Grants) != 2 || a.Grants[1].Role != "w" || a.Grants[1].Overrides == nil || a.Grants[1].Overrides.Credentials["git"] != "known-cred" {
		t.Errorf("grants = %+v", a.Grants)
	}
}

// A manual raise needs an id and a role.
func TestAgentsAPIRaiseRefusals(t *testing.T) {
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), newSup(t, store), nil, anyCred, nil)
	for b, want := range map[[2]string]string{{"", "worker"}: "id is required", {"w1", ""}: "role is required"} {
		rec := apiDo(t, h, "POST", "/app/admin/api/agents", map[string]string{"id": b[0], "role": b[1], "project": "acme"}, nil)
		wantErr(t, rec, 400, "bad_request")
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%v = %s, want %q", b, rec.Body, want)
		}
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/agents", map[string]string{"id": "w1", "role": "nope", "project": "acme"}, nil)
	if rec.Code < 400 {
		t.Errorf("raise of a missing role = %d %s", rec.Code, rec.Body)
	}
	if len(store.ListInstances()) != 0 {
		t.Errorf("a refused raise raised: %+v", store.ListInstances())
	}
}
