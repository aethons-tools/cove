package jam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
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

// putStandingRole creates project acme and seeds role acme/reviewer with scope, kit-free, and a full
// allocation so tests can check add/rm keep every other field.
func putStandingRole(t *testing.T, store Store) Role {
	t.Helper()
	role := Role{
		Name:  "reviewer",
		Scope: Scope{Destinations: []string{"git"}, Addressing: []string{"human:*"}, TTL: time.Hour},
		Allocation: RoleAllocation{
			MaxEphemeral: 2, MaxPersonal: 3, MaxPersonalPerOwner: 1,
			IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 3 * time.Hour,
		},
	}
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", role); err != nil {
		t.Fatal(err)
	}
	return role
}

// AddStanding/RemoveStanding declare and dismiss standing sessions, keep every
// other field of the role, and GET lists them.
func TestStandingAddListRemove(t *testing.T) {
	h, store := newTestAdmin(t)
	orig := putStandingRole(t, store)

	if err := AddStanding(store, "acme", "reviewer", StandingSession{Name: "alice-bot", Prompt: "review PRs"}); err != nil {
		t.Fatal(err)
	}
	if err := AddStanding(store, "acme", "reviewer", StandingSession{Name: "bob-bot", Prompt: "triage"}); err != nil {
		t.Fatal(err)
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

	if err := RemoveStanding(store, "acme", "reviewer", "alice-bot"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetRole("acme", "reviewer")
	keep.Allocation.Standing = []StandingSession{{Name: "bob-bot", Prompt: "triage"}}
	if !reflect.DeepEqual(got, keep) {
		t.Fatalf("role after rm = %+v, want %+v", got, keep)
	}
	if err := RemoveStanding(store, "acme", "reviewer", "alice-bot"); WriteStatus(err, 500) != http.StatusNotFound {
		t.Fatalf("rm undeclared = %v, want 404", err)
	}
}

func TestStandingValidation(t *testing.T) {
	h, store := newTestAdmin(t)
	putStandingRole(t, store)
	if err := AddStanding(store, "acme", "reviewer", StandingSession{Name: "a b", Prompt: "p"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cases := []struct {
		project, role string
		body          StandingSession
		code          int
	}{
		{"acme", "reviewer", StandingSession{Prompt: "p"}, http.StatusBadRequest},              // no name
		{"acme", "reviewer", StandingSession{Name: "x"}, http.StatusBadRequest},                // no prompt
		{"acme", "reviewer", StandingSession{Name: "a b", Prompt: "p"}, http.StatusBadRequest}, // duplicate
		{"acme", "reviewer", StandingSession{Name: "a/b", Prompt: "p"}, http.StatusBadRequest}, // "/" can't name a route segment
		{"acme", "nobody", StandingSession{Name: "x", Prompt: "p"}, http.StatusNotFound},       // unknown role
		{"nowhere", "reviewer", StandingSession{Name: "x", Prompt: "p"}, http.StatusNotFound},  // unknown project
	}
	for _, c := range cases {
		err := AddStanding(store, c.project, c.role, c.body)
		if err == nil || WriteStatus(err, 500) != c.code {
			t.Fatalf("AddStanding(%s/%s, %+v) = %v, want status %d", c.project, c.role, c.body, err, c.code)
		}
	}
	if rec := doReq(t, h, "GET", "/admin/roles/acme/nobody/standing", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("list unknown role = %d, want 404", rec.Code)
	}
	if err := RemoveStanding(store, "acme", "nobody", "x"); WriteStatus(err, 500) != http.StatusNotFound {
		t.Fatalf("rm on unknown role = %v, want 404", err)
	}
}

// Re-putting a role keeps its standing declarations: they are managed only by
// AddStanding/RemoveStanding.
func TestRolePutKeepsStanding(t *testing.T) {
	_, store := newTestAdmin(t)
	putStandingRole(t, store)
	if err := AddStanding(store, "acme", "reviewer", StandingSession{Name: "alice-bot", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := PutRoleKeeping(store, "acme", Role{Name: "reviewer", Allocation: RoleAllocation{MaxEphemeral: 5}}); err != nil {
		t.Fatalf("re-put role: %v", err)
	}
	got, _ := store.GetRole("acme", "reviewer")
	if got.Allocation.MaxEphemeral != 5 || len(got.Allocation.Standing) != 1 || got.Allocation.Standing[0].Name != "alice-bot" {
		t.Fatalf("role after re-put = %+v, want max-ephemeral 5 and alice-bot kept", got.Allocation)
	}
}

// fakeResetter records resets and fails them with err (pending).
type fakeResetter struct {
	got []string
	err error
}

func (f *fakeResetter) ResetStanding(_ context.Context, project, role, name string) error {
	f.got = append(f.got, project+"/"+role+"/"+name)
	return f.err
}

// Reset goes through the standing reconciler (the supervisor's
// StandingResetter): 200 {pending:false} when done, 202 {pending:true} when
// the reconciler is still finishing it; 404 for an unknown name or role, 409
// for an actor id held by another cove, 503 without a reconciler (COV-249).
func TestAdminStandingReset(t *testing.T) {
	h, store, sup, _ := newTestAdminWithSupervisorAndLauncher(t)
	putStandingRole(t, store)
	if err := AddStanding(store, "acme", "reviewer", StandingSession{Name: "alice-bot", Prompt: "review PRs"}); err != nil {
		t.Fatal(err)
	}
	const path = "/admin/roles/acme/reviewer/standing/alice-bot/reset"
	if rec := doReq(t, h, "POST", path, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no reconciler = %d, want 503", rec.Code)
	}
	rs := &fakeResetter{}
	sup.SetStandingResetter(rs)

	rec := doReq(t, h, "POST", path, nil)
	var res StandingResetResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Pending {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(rs.got, []string{"acme/reviewer/alice-bot"}) {
		t.Fatalf("resetter got %v", rs.got)
	}

	rs.err = errors.New("volume in use")
	rec = doReq(t, h, "POST", path, nil)
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &res) != nil || !res.Pending || !strings.Contains(res.Reason, "volume in use") {
		t.Fatalf("pending reset = %d %s", rec.Code, rec.Body.String())
	}

	for _, p := range []string{"/admin/roles/acme/reviewer/standing/nobody/reset", "/admin/roles/acme/ghost/standing/alice-bot/reset"} {
		if rec := doReq(t, h, "POST", p, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", p, rec.Code)
		}
	}

	// The session id held by a non-standing cove: refused.
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: SeedStandingSession(store, "acme", "reviewer", "alice-bot"), Project: "acme", Role: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "POST", path, nil); rec.Code != http.StatusConflict {
		t.Fatalf("held id = %d, want 409", rec.Code)
	}
}
