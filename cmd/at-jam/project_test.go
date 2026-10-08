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

// TestProjectLifecycleCommands exercises `project create|list|rm`
// end-to-end through httptest.Server + MemStore.
func TestProjectLifecycleCommands(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	cmd := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return run(append(append([]string{"project"}, args...), "--admin-url", ts.URL), getenv, &out, &errb)
	}

	if code := cmd("create", "acme"); code != 0 {
		t.Fatalf("project create: exit=%d stderr=%s", code, errb.String())
	}
	if code := cmd("create", "acme"); code != 1 || !strings.Contains(errb.String(), "already exists") {
		t.Fatalf("duplicate project create: exit=%d stderr=%s", code, errb.String())
	}
	if code := cmd("create"); code != 2 {
		t.Fatalf("project create without a name: exit=%d, want 2", code)
	}
	if code := cmd("list"); code != 0 || strings.TrimSpace(out.String()) != "acme" {
		t.Fatalf("project list: exit=%d out=%q stderr=%s", code, out.String(), errb.String())
	}

	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	if code := cmd("rm", "acme"); code != 1 || !strings.Contains(errb.String(), "role acme/worker") {
		t.Fatalf("project rm while referenced: exit=%d stderr=%s", code, errb.String())
	}
	if err := store.RemoveRole("acme", "worker"); err != nil {
		t.Fatal(err)
	}
	if code := cmd("rm", "acme"); code != 0 {
		t.Fatalf("project rm: exit=%d stderr=%s", code, errb.String())
	}
	if code := cmd("list"); code != 0 || strings.TrimSpace(out.String()) != "" {
		t.Fatalf("project list after rm: exit=%d out=%q", code, out.String())
	}
}

// `project rename` renames a project in place: its roles follow.
func TestProjectRenameCommand(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	var out, errb bytes.Buffer
	cmd := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return run(append(append([]string{"project"}, args...), "--admin-url", ts.URL), func(string) string { return "" }, &out, &errb)
	}
	if code := cmd("create", "acme"); code != 0 {
		t.Fatal(errb.String())
	}
	if err := store.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	if code := cmd("rename", "acme", "apex"); code != 0 || !strings.Contains(out.String(), "renamed project acme to apex") {
		t.Fatalf("rename: exit=%d out=%q stderr=%s", code, out.String(), errb.String())
	}
	if _, ok := store.GetRole("apex", "worker"); !ok {
		t.Fatal("the role follows the rename")
	}
	if code := cmd("create", "beta"); code != 0 {
		t.Fatal(errb.String())
	}
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"rename", "apex", "beta"}, 1, "already exists"},
		{[]string{"rename", "ghost", "x"}, 1, "not found"},
		{[]string{"rename", "apex"}, 2, "expected <project> <new-name>"},
	} {
		if code := cmd(c.args...); code != c.code || !strings.Contains(errb.String(), c.msg) {
			t.Errorf("%v: exit=%d stderr=%q", c.args, code, errb.String())
		}
	}
}
