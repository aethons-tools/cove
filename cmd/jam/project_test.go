package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// TestProjectLifecycleCommands exercises `project list` end-to-end through
// httptest.Server + MemStore, and the store-level create/remove guards the
// removed `project create|rm` verbs used to surface.
func TestProjectLifecycleCommands(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	cmd := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return run(append(append([]string{"project"}, args...), "--admin-url", ts.URL), getenv, &out, &errb)
	}

	if err := store.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProject("acme"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate project create: %v", err)
	}
	if code := cmd("list"); code != 0 || strings.TrimSpace(out.String()) != "acme" {
		t.Fatalf("project list: exit=%d out=%q stderr=%s", code, out.String(), errb.String())
	}
	if code := cmd("list", "extra"); code != 2 {
		t.Fatalf("project list with an argument: exit=%d, want 2", code)
	}

	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveProject("acme"); err == nil || !strings.Contains(err.Error(), "role acme/worker") {
		t.Fatalf("project rm while referenced: %v", err)
	}
	if err := store.RemoveRole("acme", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveProject("acme"); err != nil {
		t.Fatal(err)
	}
	if code := cmd("list"); code != 0 || strings.TrimSpace(out.String()) != "" {
		t.Fatalf("project list after rm: exit=%d out=%q", code, out.String())
	}
}

// Renaming a project (store-level; the `project rename` verb is gone) moves
// it in place: its roles follow.
func TestProjectRename(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameProject("acme", "apex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetRole("apex", "worker"); !ok {
		t.Fatal("the role follows the rename")
	}
	mustCreateProject(t, store, "beta")
	for _, c := range []struct{ ref, name, msg string }{
		{"apex", "beta", "already exists"},
		{"ghost", "x", "not found"},
	} {
		if err := store.RenameProject(c.ref, c.name); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("RenameProject(%q, %q) = %v, want %q", c.ref, c.name, err, c.msg)
		}
	}
}
