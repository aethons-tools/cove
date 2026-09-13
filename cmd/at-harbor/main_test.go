package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/harbor"
)

// aliveLauncher is a trivial harbor.Launcher for CLI-level tests that need a
// non-nil supervisor: it always raises successfully, reports the cove alive,
// and tears down without error.
type aliveLauncher struct{}

func (aliveLauncher) Raise(_ context.Context, spec harbor.RaiseSpec) (string, error) {
	return "fake:" + spec.ActorID, nil
}
func (aliveLauncher) Teardown(_ context.Context, _ harbor.Instance) error { return nil }
func (aliveLauncher) Probe(_ context.Context, _ harbor.Instance) (harbor.Liveness, error) {
	return harbor.LivenessAlive, nil
}

func TestEnrollCommandJSON(t *testing.T) {
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.PutRole(harbor.DefaultProject, harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic", "git"}}}); err != nil {
		t.Fatal(err)
	}
	h := harbor.NewAdminHandler(store, nil, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	// --json needs no --base-url (no snippet rendered)
	code := run([]string{
		"enroll", "--json", "--admin-url", ts.URL, "--id", "spider-18",
		"--role", "guest",
	}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	var got struct{ ID, Token string }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if got.ID != "spider-18" || got.Token == "" {
		t.Fatalf("enroll --json = %+v", got)
	}
	if strings.Contains(out.String(), "ANTHROPIC_BASE_URL") || strings.Contains(out.String(), "export ") {
		t.Fatalf("--json must not print the snippet:\n%s", out.String())
	}
}

func TestEnrollCommandPrintsSnippet(t *testing.T) {
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.PutRole("ACME", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"}}}); err != nil {
		t.Fatal(err)
	}
	h := harbor.NewAdminHandler(store, nil, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	code := run([]string{
		"enroll", "--admin-url", ts.URL, "--id", "spider-18", "--project", "ACME",
		"--role", "guest",
		"--base-url", "https://harbor.local.aethons.tools",
	}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic") {
		t.Fatalf("stdout missing snippet:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "AT_HARBOR_IDENTITY_TOKEN=") {
		t.Fatal("stdout missing minted token line")
	}
	if len(store.ListActors()) != 1 {
		t.Fatal("identity was not created via the admin API")
	}
}

func TestEnrollRejectsScopeFlags(t *testing.T) {
	var out, errb bytes.Buffer
	code := cmdEnroll([]string{"--id", "x", "--role", "guest", "--destinations", "anthropic"}, cli.Globals{}, &out, &errb)
	if code == 0 {
		t.Fatalf("expected non-zero exit when --destinations is passed; got 0 (err=%q)", errb.String())
	}
	if !strings.Contains(errb.String(), "flag provided but not defined: -destinations") {
		t.Fatalf("expected the removed --destinations flag to be rejected as undefined; stderr=%q", errb.String())
	}
}

func TestRoleGrantUngrantRosterCommands(t *testing.T) {
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := harbor.NewAdminHandler(store, nil, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer
	// role add
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--project", "P",
		"--name", "guest", "--destinations", "anthropic", "--repos", "acme/*",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add: exit=%d stderr=%s", code, errb.String())
	}

	// role list reflects it
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "P"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "guest") || !strings.Contains(out.String(), "dests=anthropic") || !strings.Contains(out.String(), "repos=acme/*") {
		t.Fatalf("role list output missing expected fields:\n%s", out.String())
	}

	// enroll an actor under that role so it exists to grant onto
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"enroll", "--json", "--admin-url", ts.URL, "--id", "spider-1", "--project", "P", "--role", "guest",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("enroll: exit=%d stderr=%s", code, errb.String())
	}

	// a second role to grant
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--project", "P", "--name", "admin", "--destinations", "anthropic,git",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add admin: exit=%d stderr=%s", code, errb.String())
	}

	// grant
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"grant", "--admin-url", ts.URL, "--id", "spider-1", "--project", "P", "--role", "admin",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("grant: exit=%d stderr=%s", code, errb.String())
	}

	// roster shows both grants
	out.Reset()
	errb.Reset()
	if code := run([]string{"roster", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("roster: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "spider-1\tP/guest") || !strings.Contains(out.String(), "spider-1\tP/admin") {
		t.Fatalf("roster output missing expected grants:\n%s", out.String())
	}

	// ungrant
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"ungrant", "--admin-url", ts.URL, "--id", "spider-1", "--project", "P", "--role", "admin",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("ungrant: exit=%d stderr=%s", code, errb.String())
	}

	// roster no longer shows the ungranted role
	out.Reset()
	errb.Reset()
	if code := run([]string{"roster", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("roster (after ungrant): exit=%d stderr=%s", code, errb.String())
	}
	if strings.Contains(out.String(), "P/admin") {
		t.Fatalf("roster still shows ungranted role:\n%s", out.String())
	}

	// role rm
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "rm", "--admin-url", ts.URL, "--project", "P", "guest"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role rm: exit=%d stderr=%s", code, errb.String())
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestKitPushRejectsMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yml")
	if err := os.WriteFile(bad, []byte("name: x\nnope_unknown_key: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := cmdKit([]string{"push", "--name", "x", "--config", bad}, cli.Globals{}, &out, &errb)
	if code == 0 {
		t.Fatalf("expected non-zero exit for a malformed kit config; stderr=%q", errb.String())
	}
	// Pin down that the rejection came from config parsing (not some unrelated
	// failure), so this test can't silently pass for the wrong reason.
	if !strings.Contains(errb.String(), "invalid kit config") {
		t.Fatalf("stderr = %q, want it to contain %q", errb.String(), "invalid kit config")
	}
}

func TestKitCommandsRoundTrip(t *testing.T) {
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := harbor.NewAdminHandler(store, nil, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yml")
	if err := os.WriteFile(valid, []byte("name: web\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer

	// kit push
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"kit", "push", "--admin-url", ts.URL, "--name", "web", "--config", valid,
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit push: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "pushed web v1") {
		t.Fatalf("kit push output missing expected text:\n%s", out.String())
	}

	// kit push again to create a second version, for pin to target
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"kit", "push", "--admin-url", ts.URL, "--name", "web", "--config", valid,
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit push (v2): exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "pushed web v2") {
		t.Fatalf("kit push (v2) output missing expected text:\n%s", out.String())
	}

	// kit list
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "web") || !strings.Contains(out.String(), "current=v2") || !strings.Contains(out.String(), "versions=2") {
		t.Fatalf("kit list output missing expected fields:\n%s", out.String())
	}

	// kit show
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "show", "--admin-url", ts.URL, "web"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit show: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "name: web") {
		t.Fatalf("kit show output missing expected config:\n%s", out.String())
	}

	// kit versions
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "versions", "--admin-url", ts.URL, "web"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit versions: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "v1") || !strings.Contains(out.String(), "v2") {
		t.Fatalf("kit versions output missing expected versions:\n%s", out.String())
	}

	// kit pin
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "pin", "--admin-url", ts.URL, "web", "1"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit pin: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "pinned web to v1") {
		t.Fatalf("kit pin output missing expected text:\n%s", out.String())
	}

	// role add --kit
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--name", "impl", "--kit", "web", "--destinations", "anthropic",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add --kit: exit=%d stderr=%s", code, errb.String())
	}
	if r, ok := store.GetRole(harbor.DefaultProject, "impl"); !ok || r.Kit != "web" {
		t.Fatalf("role add --kit did not bind the kit: role=%+v ok=%v", r, ok)
	}

	// kit rm — must fail while a role still references it (409-style store error)
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "rm", "--admin-url", ts.URL, "web"}, getenv, &out, &errb); code == 0 {
		t.Fatalf("kit rm: expected non-zero exit while role %q still references kit web; stdout=%s", "impl", out.String())
	}

	// remove the referencing role, then kit rm should succeed
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "rm", "--admin-url", ts.URL, "impl"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role rm: exit=%d stderr=%s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "rm", "--admin-url", ts.URL, "web"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit rm: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "removed kit web") {
		t.Fatalf("kit rm output missing expected text:\n%s", out.String())
	}
}

// TestCoveCommandsRoundTrip exercises the `cove` verb group (raise|list|status|
// teardown) end to end through the CLI entrypoint. Unlike the other CLI round
// trips, cove's mutation routes need a live Supervisor (a nil sup 503s), so
// this test wires one with a fake Launcher. It also pins down the "never
// print the identity token" constraint on `cove raise`.
func TestCoveCommandsRoundTrip(t *testing.T) {
	store, _ := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.PutRole("default", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := harbor.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := harbor.NewAdminHandler(store, sup, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// cove raise
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"cove", "raise", "--admin-url", ts.URL, "--id", "w1", "--role", "guest", "--unit", "AET-1",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("cove raise: exit=%d stderr=%s", code, errb.String())
	}
	// Only id+phase may be printed — proves the minted identity token never
	// reaches stdout.
	if out.String() != "raised w1 (phase=live)\n" {
		t.Fatalf("cove raise output = %q, want exactly %q (must not leak the identity token)", out.String(), "raised w1 (phase=live)\n")
	}

	// cove list reflects the raised cove
	out.Reset()
	errb.Reset()
	if code := run([]string{"cove", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("cove list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "w1") || !strings.Contains(out.String(), "role=guest") || !strings.Contains(out.String(), "phase=live") {
		t.Fatalf("cove list output missing expected fields:\n%s", out.String())
	}

	// cove status
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"cove", "status", "--admin-url", ts.URL, "--id", "w1", "--activity", "waiting",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("cove status: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "reported w1 activity=waiting") {
		t.Fatalf("cove status output missing expected text:\n%s", out.String())
	}

	// cove list reflects the reported activity
	out.Reset()
	errb.Reset()
	if code := run([]string{"cove", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("cove list (after status): exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "activity=waiting") {
		t.Fatalf("cove list output missing updated activity:\n%s", out.String())
	}

	// cove teardown
	out.Reset()
	errb.Reset()
	if code := run([]string{"cove", "teardown", "--admin-url", ts.URL, "--id", "w1"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("cove teardown: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "tore down w1") {
		t.Fatalf("cove teardown output missing expected text:\n%s", out.String())
	}

	// cove list no longer shows the torn-down cove
	out.Reset()
	errb.Reset()
	if code := run([]string{"cove", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("cove list (after teardown): exit=%d stderr=%s", code, errb.String())
	}
	if strings.Contains(out.String(), "w1") {
		t.Fatalf("cove list still shows torn-down cove:\n%s", out.String())
	}

	// required-flag checks
	out.Reset()
	errb.Reset()
	if code := run([]string{"cove", "raise", "--admin-url", ts.URL, "--id", "w1"}, getenv, &out, &errb); code != 2 {
		t.Fatalf("cove raise (no --role): exit=%d, want 2 (stderr=%s)", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"cove", "status", "--admin-url", ts.URL, "--id", "w1"}, getenv, &out, &errb); code != 2 {
		t.Fatalf("cove status (no --activity): exit=%d, want 2 (stderr=%s)", code, errb.String())
	}
}
