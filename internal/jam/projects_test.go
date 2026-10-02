package jam

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestAdminProjectCreateListRemove(t *testing.T) {
	h, store := newTestAdmin(t)

	if rec := doJSON(t, h, "POST", "/admin/projects", ProjectBody{Name: "acme"}); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, "POST", "/admin/projects", ProjectBody{Name: "acme"}); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/admin/projects", ProjectBody{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without a name = %d, want 400", rec.Code)
	}
	var names []string
	getJSON(t, h, "/admin/projects", &names)
	if len(names) != 1 || names[0] != "acme" {
		t.Fatalf("list = %v, want [acme]", names)
	}

	if err := store.PutRole("acme", Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "DELETE", "/admin/projects/acme", nil); rec.Code != http.StatusConflict {
		t.Fatalf("remove while a role references it = %d, want 409", rec.Code)
	}
	if err := store.RemoveRole("acme", "worker"); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "DELETE", "/admin/projects/acme", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "DELETE", "/admin/projects/acme", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("remove of an absent project = %d, want 404", rec.Code)
	}
}

// A project-scoped write into an unknown project is a 404 that tells the
// operator to create the project — never an implicit creation.
func TestAdminWritesIntoUnknownProject404(t *testing.T) {
	h, store := newTestAdmin(t)
	writes := []struct {
		method, path string
		body         any
	}{
		{"POST", "/admin/roles", RoleBody{Project: "ghost", Name: "worker"}},
		{"POST", "/admin/projects/ghost/humans", Human{Name: "alice", Handle: "@alice"}},
		{"POST", "/admin/projects/ghost/channels", Channel{Name: "eng", Ref: "ENG-1"}},
		{"PUT", "/admin/projects/ghost/escalation", EscalationBody{}},
		{"PUT", "/admin/projects/ghost/chat-service", ChatServiceBody{Service: "discord"}},
	}
	for _, w := range writes {
		if rec := doJSON(t, h, w.method, w.path, w.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", w.method, w.path, rec.Code, rec.Body)
		}
	}
	if got := store.ListProjects(); len(got) != 0 {
		t.Fatalf("a refused write created a project: %v", got)
	}
}

// An import of a pre-first-class snapshot gets the same backfill.
func TestImportConfigBackfillsReferencedProjects(t *testing.T) {
	s := NewMemStore()
	snap := ConfigSnapshot{
		Version: ConfigSnapshotVersion,
		Roles:   map[string]map[string]Role{"acme": {"worker": {Name: "worker"}}},
		Actors:  []Actor{{ID: "a", TokenHash: "h", Grants: []Grant{{Project: "beta", Role: "r"}}}},
	}
	if err := s.ImportConfig(snap); err != nil {
		t.Fatal(err)
	}
	if got, want := s.ListProjects(), []string{"acme", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListProjects = %v, want %v", got, want)
	}
}

// Shared write paths (the admin UI's) map the project errors the same way the
// admin API does.
func TestWriteStatusMapsProjectErrors(t *testing.T) {
	for err, want := range map[error]int{
		ErrProjectNotFound: http.StatusNotFound,
		ErrProjectExists:   http.StatusConflict,
		ErrProjectInUse:    http.StatusConflict,
	} {
		if got := WriteStatus(fmt.Errorf("wrapped: %w", err), http.StatusBadRequest); got != want {
			t.Errorf("WriteStatus(%v) = %d, want %d", err, got, want)
		}
	}
}
