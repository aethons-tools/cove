package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// twoManagers is a placing launcher over two connected managers that name
// images per manager.
type twoManagers struct{ placeholderLauncher }

func (twoManagers) RaiseOn(context.Context, jam.RaiseSpec, jam.LaunchCreds) (jam.Placed, error) {
	return jam.Placed{}, nil
}
func (twoManagers) PrepareKitOn(context.Context, string, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{State: jam.KitReady}, nil
}
func (twoManagers) ImageTagOn(m string, ref jam.KitRef) string { return "img-" + m }
func (twoManagers) HasManager(string) bool                     { return true }
func (twoManagers) AvailableNames() []string                   { return []string{"m1", "local"} }

func TestRoleImagesCLI(t *testing.T) {
	st := jam.NewMemStore()
	if _, err := jam.EnsureStudioKit(st, "web", studio.StudioKit{Kind: studio.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole(jam.DefaultProject, jam.Role{Name: "dev", Kit: "web", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(st, twoManagers{}, "h", time.Minute, 30*time.Second, nil, log)
	ts := httptest.NewServer(jam.NewAdminHandler(st, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	defer ts.Close()
	var out, errb bytes.Buffer
	code := run([]string{"role", "images", "default/dev", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("role images = %d: %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "m1\tweb\timg-m1") || !strings.HasPrefix(lines[1], "local\tweb\timg-local") {
		t.Fatalf("output:\n%s", out.String())
	}
}

// Without a router the one row's manager is "": it prints as local, never as
// a leading tab.
func TestRoleImagesCLINoRouterPrintsLocal(t *testing.T) {
	st := jam.NewMemStore()
	if _, err := jam.EnsureStudioKit(st, "web", studio.StudioKit{Kind: studio.Kind}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRole(jam.DefaultProject, jam.Role{Name: "dev", Kit: "web", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(st, placeholderLauncher{}, "h", time.Minute, 30*time.Second, nil, log)
	ts := httptest.NewServer(jam.NewAdminHandler(st, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	defer ts.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"role", "images", "default/dev", "--admin-url", ts.URL}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("role images = %d: %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "local\tweb\t") {
		t.Fatalf("output:\n%q", out.String())
	}
}
