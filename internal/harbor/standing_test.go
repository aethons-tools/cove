package harbor

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

// StandingActorID is "standing-<project>-<role>-<name>", each part sanitized to
// [A-Za-z0-9._-] like a personal session id.
func TestStandingActorID(t *testing.T) {
	cases := []struct{ project, role, name, want string }{
		{"acme", "reviewer", "alice-bot", "standing-acme-reviewer-alice-bot"},
		{"acme", "rev iewer", "bot/1", "standing-acme-rev-iewer-bot-1"},
		{"a.b", "r_1", "Ünï", "standing-a.b-r_1--n-"},
	}
	for _, c := range cases {
		if got := StandingActorID(c.project, c.role, c.name); got != c.want {
			t.Fatalf("StandingActorID(%q, %q, %q) = %q, want %q", c.project, c.role, c.name, got, c.want)
		}
	}
}

// putStandingRole seeds role acme/reviewer with scope, kit-free, and a full
// allocation so tests can check add/rm keep every other field.
func putStandingRole(t *testing.T, store Store) Role {
	t.Helper()
	role := Role{
		Name:  "reviewer",
		Scope: Scope{Destinations: []string{"git"}, Repos: []string{"acme/*"}, Addressing: []string{"human:*"}, TTL: time.Hour},
		Allocation: RoleAllocation{
			MaxEphemeral: 2, MaxPersonal: 3, MaxPersonalPerOwner: 1,
			IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 3 * time.Hour,
		},
	}
	if err := store.PutRole("acme", role); err != nil {
		t.Fatal(err)
	}
	return role
}

// Standing declarations round-trip through POST/GET/DELETE and keep every other
// field of the role.
func TestAdminStandingAddListRemove(t *testing.T) {
	h, store := newTestAdmin(t)
	orig := putStandingRole(t, store)

	if rec := doJSON(t, h, "POST", "/admin/roles/acme/reviewer/standing", StandingSession{Name: "alice-bot", Prompt: "review PRs"}); rec.Code != http.StatusCreated {
		t.Fatalf("add = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, "POST", "/admin/roles/acme/reviewer/standing", StandingSession{Name: "bob-bot", Prompt: "triage"}); rec.Code != http.StatusCreated {
		t.Fatalf("add 2 = %d, body=%s", rec.Code, rec.Body.String())
	}
	var list []StandingSession
	getJSON(t, h, "/admin/roles/acme/reviewer/standing", &list)
	want := []StandingSession{{Name: "alice-bot", Prompt: "review PRs"}, {Name: "bob-bot", Prompt: "triage"}}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("list = %+v, want %+v", list, want)
	}
	got, _ := store.GetRole("acme", "reviewer")
	keep := orig
	keep.Allocation.Standing = want
	if !reflect.DeepEqual(got, keep) {
		t.Fatalf("role after add = %+v, want %+v (other fields kept)", got, keep)
	}

	if rec := doReq(t, h, "DELETE", "/admin/roles/acme/reviewer/standing/alice-bot", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("rm = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ = store.GetRole("acme", "reviewer")
	keep.Allocation.Standing = []StandingSession{{Name: "bob-bot", Prompt: "triage"}}
	if !reflect.DeepEqual(got, keep) {
		t.Fatalf("role after rm = %+v, want %+v", got, keep)
	}
	if rec := doReq(t, h, "DELETE", "/admin/roles/acme/reviewer/standing/alice-bot", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("rm undeclared = %d, want 404", rec.Code)
	}
}

func TestAdminStandingValidation(t *testing.T) {
	h, store := newTestAdmin(t)
	putStandingRole(t, store)
	if rec := doJSON(t, h, "POST", "/admin/roles/acme/reviewer/standing", StandingSession{Name: "a b", Prompt: "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("seed = %d", rec.Code)
	}
	cases := []struct {
		path string
		body StandingSession
		code int
	}{
		{"/admin/roles/acme/reviewer/standing", StandingSession{Prompt: "p"}, http.StatusBadRequest},              // no name
		{"/admin/roles/acme/reviewer/standing", StandingSession{Name: "x"}, http.StatusBadRequest},                // no prompt
		{"/admin/roles/acme/reviewer/standing", StandingSession{Name: "a b", Prompt: "p"}, http.StatusBadRequest}, // duplicate
		{"/admin/roles/acme/reviewer/standing", StandingSession{Name: "a/b", Prompt: "p"}, http.StatusBadRequest}, // same actor id as "a b"
		{"/admin/roles/acme/nobody/standing", StandingSession{Name: "x", Prompt: "p"}, http.StatusNotFound},       // unknown role
		{"/admin/roles/nowhere/reviewer/standing", StandingSession{Name: "x", Prompt: "p"}, http.StatusNotFound},  // unknown project
	}
	for _, c := range cases {
		if rec := doJSON(t, h, "POST", c.path, c.body); rec.Code != c.code {
			t.Fatalf("POST %s %+v = %d, want %d (body=%s)", c.path, c.body, rec.Code, c.code, rec.Body.String())
		}
	}
	if rec := doReq(t, h, "GET", "/admin/roles/acme/nobody/standing", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("list unknown role = %d, want 404", rec.Code)
	}
	if rec := doReq(t, h, "DELETE", "/admin/roles/acme/nobody/standing/x", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("rm on unknown role = %d, want 404", rec.Code)
	}
}

// Re-putting a role (`role add` again) keeps its standing declarations: they are
// managed only by the standing endpoints.
func TestAdminRolePutKeepsStanding(t *testing.T) {
	h, store := newTestAdmin(t)
	putStandingRole(t, store)
	if rec := doJSON(t, h, "POST", "/admin/roles/acme/reviewer/standing", StandingSession{Name: "alice-bot", Prompt: "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("add = %d", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "reviewer", MaxEphemeral: 5}); rec.Code != http.StatusCreated {
		t.Fatalf("re-put role = %d", rec.Code)
	}
	got, _ := store.GetRole("acme", "reviewer")
	if got.Allocation.MaxEphemeral != 5 || len(got.Allocation.Standing) != 1 || got.Allocation.Standing[0].Name != "alice-bot" {
		t.Fatalf("role after re-put = %+v, want max-ephemeral 5 and alice-bot kept", got.Allocation)
	}
}

// Actor ids are unique across every role and project, not just within one: a
// name whose StandingActorID another role's declaration already holds is 400.
func TestAdminStandingRejectsCrossRoleIDCollision(t *testing.T) {
	h, store := newTestAdmin(t)
	for _, r := range []string{"a-b", "a"} {
		if err := store.PutRole("acme", Role{Name: r}); err != nil {
			t.Fatal(err)
		}
	}
	if rec := doJSON(t, h, "POST", "/admin/roles/acme/a-b/standing", StandingSession{Name: "c", Prompt: "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("seed = %d", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/admin/roles/acme/a/standing", StandingSession{Name: "b-c", Prompt: "p"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("colliding id standing-acme-a-b-c = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}
