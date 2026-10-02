package adminclient

import (
	"errors"
	"slices"
	"testing"
)

func TestClientProjectLifecycle(t *testing.T) {
	srv, _ := newServer(t)
	c := New(srv.URL, "")
	if err := c.CreateProject("acme"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := c.CreateProject("acme"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate CreateProject = %v, want ErrConflict", err)
	}
	names, err := c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names, "acme") {
		t.Fatalf("ListProjects = %v, want acme listed", names)
	}
	if err := c.RemoveProject("acme"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if err := c.RemoveProject("acme"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RemoveProject of an absent project = %v, want ErrNotFound", err)
	}
}
