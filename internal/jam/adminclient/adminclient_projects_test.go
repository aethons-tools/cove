package adminclient

import (
	"slices"
	"testing"
)

func TestClientListProjects(t *testing.T) {
	srv, store := newServer(t)
	c := New(srv.URL, "")
	if err := store.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	names, err := c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names, "acme") {
		t.Fatalf("ListProjects = %v, want acme listed", names)
	}
}
