package jam

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestAdminProjectCreateListRemove(t *testing.T) {
	h, store := newTestAdmin(t)

	if err := store.CreateProject("acme"); err != nil {
		t.Fatalf("create = %v", err)
	}
	if err := store.CreateProject("acme"); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("duplicate create = %v, want ErrProjectExists", err)
	}
	if err := store.CreateProject(""); err == nil {
		t.Fatal("create without a name succeeded")
	}
	var names []string
	getJSON(t, h, "/admin/projects", &names)
	if len(names) != 1 || names[0] != "acme" {
		t.Fatalf("list = %v, want [acme]", names)
	}

	if err := store.PutRole("acme", Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveProject("acme"); !errors.Is(err, ErrProjectInUse) {
		t.Fatalf("remove while a role references it = %v, want ErrProjectInUse", err)
	}
	if err := store.RemoveRole("acme", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveProject("acme"); err != nil {
		t.Fatalf("remove = %v", err)
	}
	if err := store.RemoveProject("acme"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("remove of an absent project = %v, want ErrProjectNotFound", err)
	}
}

// The project, room, escalation, chat-service and context config routes are
// gone: those settings change only in git. GET /admin/projects still lists.
func TestProjectConfigRoutesGone(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	for _, gone := range []struct{ method, path string }{
		{"POST", "/admin/projects"},
		{"PUT", "/admin/projects/acme/name"},
		{"DELETE", "/admin/projects/acme"},
		{"PUT", "/admin/projects/acme/escalation"},
		{"PUT", "/admin/projects/acme/chat-service"},
		{"POST", "/admin/projects/acme/rooms"},
		{"PUT", "/admin/projects/acme/rooms/eng/name"},
		{"DELETE", "/admin/projects/acme/rooms/eng"},
		{"PUT", "/admin/roles/acme/worker/context"},
		{"DELETE", "/admin/roles/acme/worker/context"},
		{"PUT", "/admin/projects/acme/context"},
		{"DELETE", "/admin/projects/acme/context"},
		{"PUT", "/admin/jam/context"},
		{"DELETE", "/admin/jam/context"},
	} {
		if rec := doJSON(t, h, gone.method, gone.path, map[string]string{"name": "x"}); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", gone.method, gone.path, rec.Code)
		}
	}
	var names []string
	getJSON(t, h, "/admin/projects", &names) // fails the test unless 200
	if len(names) != 1 || names[0] != "acme" {
		t.Fatalf("a gone route changed the projects: %v", names)
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
		{"POST", "/admin/projects/ghost/channels", RosterChannel{Name: "eng", Ref: "ENG-1"}},
	}
	for _, w := range writes {
		if rec := doJSON(t, h, w.method, w.path, w.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", w.method, w.path, rec.Code, rec.Body)
		}
	}
	// The store-level project writes refuse an unknown project the same way.
	if err := store.SetEscalationPolicy("ghost", "", nil); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("SetEscalationPolicy(ghost) = %v, want ErrProjectNotFound", err)
	}
	if err := store.SetChatService("ghost", "discord"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("SetChatService(ghost) = %v, want ErrProjectNotFound", err)
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
