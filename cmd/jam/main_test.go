package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// aliveLauncher is a trivial jam.Launcher for CLI-level tests that need a
// non-nil supervisor: it always raises successfully, reports the cove alive,
// and tears down without error.
type aliveLauncher struct{}

func (aliveLauncher) Raise(_ context.Context, spec jam.RaiseSpec, _ jam.LaunchCreds) (string, error) {
	return "fake:" + spec.ActorID, nil
}
func (aliveLauncher) Teardown(_ context.Context, _ jam.Instance) error { return nil }
func (aliveLauncher) Probe(_ context.Context, _ jam.Instance) (jam.Liveness, error) {
	return jam.LivenessAlive, nil
}
func (aliveLauncher) Pause(_ context.Context, _ jam.Instance) error   { return nil }
func (aliveLauncher) Unpause(_ context.Context, _ jam.Instance) error { return nil }
func (aliveLauncher) ApplyEgress(context.Context, jam.Instance, *jam.EgressPolicy) error {
	return nil
}
func (aliveLauncher) PrepareKit(context.Context, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{State: jam.KitReady}, nil
}

// mustCreateProject records each named project directly on store: every
// project-scoped write needs its project to exist first.
func mustCreateProject(t *testing.T, store jam.Store, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := store.CreateProject(n); err != nil {
			t.Fatalf("CreateProject(%q): %v", n, err)
		}
	}
}

func TestEnrollCommandJSON(t *testing.T) {
	store := jam.NewMemStore()
	if err := store.PutRole(jam.DefaultProject, jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic", "git"}}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
	store := jam.NewMemStore()
	mustCreateProject(t, store, "ACME")
	if err := store.PutRole("ACME", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic", "git", "github-api"}}}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []jam.Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: jam.ApplyXAPIKey, Apply: jam.ApplyXAPIKey},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: jam.ApplyBasicPassword, Apply: jam.ApplyBasicPassword},
		{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, Apply: jam.ApplyBearer, Env: map[string]string{"GH_HOST": "{host}"}},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	var out, errb bytes.Buffer
	code := run([]string{
		"enroll", "--admin-url", ts.URL, "--id", "spider-18", "--project", "ACME",
		"--role", "guest",
		"--base-url", "https://jam.local.aethons.tools",
	}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "ANTHROPIC_BASE_URL=\"https://jam.local.aethons.tools/anthropic\"") {
		t.Fatalf("stdout missing snippet:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "JAM_IDENTITY_TOKEN=") {
		t.Fatal("stdout missing minted token line")
	}
	if !strings.Contains(out.String(), `export GH_HOST="jam.local.aethons.tools"`) || !strings.Contains(out.String(), "insteadOf") {
		t.Fatalf("snippet must carry the role's connector (destination env + git):\n%s", out.String())
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
	store := jam.NewMemStore()
	mustCreateProject(t, store, "P")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer
	// seed a role (its CLI write is gone)
	if err := store.PutRole("P", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}

	// role list reflects it
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "P"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "guest") || !strings.Contains(out.String(), "dests=anthropic") {
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
	if err := store.PutRole("P", jam.Role{Name: "admin", Scope: jam.Scope{Destinations: []string{"anthropic", "git"}}}); err != nil {
		t.Fatal(err)
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
	if code := run([]string{"actors", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
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
	if code := run([]string{"actors", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("roster (after ungrant): exit=%d stderr=%s", code, errb.String())
	}
	if strings.Contains(out.String(), "P/admin") {
		t.Fatalf("roster still shows ungranted role:\n%s", out.String())
	}
}

func TestProjectMemberAndRoomCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// seed a member and a connection directly (their CLI verbs are gone)
	alice, err := store.CreateUser(jam.User{Name: "alice", Logins: []string{"auth0|abc"}})
	if err != nil {
		t.Fatal(err)
	}
	acme, _ := store.GetProject("acme")
	if err := store.PutMembership(jam.Membership{ProjectID: acme.ID, UserID: alice.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateConnection(jam.Connection{Kind: "linear", Name: "linear-acme"}); err != nil {
		t.Fatal(err)
	}

	// room list (the room is seeded through the store; its CLI writes are gone)
	if _, _, err := jam.PutRoom(store, "acme", jam.RoomBody{Name: "eng-help", Ref: "ACME-1"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		argv []string
		want string
	}{
		{[]string{"room", "list", "--admin-url", ts.URL, "acme"}, "linear-acme\tref=ACME-1"},
		{[]string{"project", "member", "list", "--admin-url", ts.URL, "acme"}, "member\talice\tusr_"},
	}
	for _, st := range steps {
		out.Reset()
		errb.Reset()
		if code := run(st.argv, getenv, &out, &errb); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", st.argv, code, errb.String())
		}
		if !strings.Contains(out.String(), st.want) {
			t.Fatalf("%v: output %q lacks %q", st.argv, out.String(), st.want)
		}
	}
	if err := jam.RemoveRoom(store, "acme", "eng-help"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"room", "list", "--admin-url", ts.URL, "acme"}, getenv, &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatalf("room list after rm: exit=%d out=%q", code, out.String())
	}

	// role list shows what the store holds: addressing, the allocation caps,
	// the idle ladder (their CLI write, role add, is gone)
	for _, r := range []jam.Role{
		{Name: "impl", Scope: jam.Scope{Addressing: []string{"human:*", "channel:eng-help"}}},
		{Name: "worker", Allocation: jam.RoleAllocation{MaxEphemeral: 4}},
		{Name: "pair", Allocation: jam.RoleAllocation{MaxPersonal: 3, MaxPersonalPerOwner: 1}},
		{Name: "idler", Allocation: jam.RoleAllocation{IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 72 * time.Hour}},
	} {
		if err := store.PutRole("acme", r); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	for _, want := range []string{
		"addressing=human:*,channel:eng-help",
		"max-ephemeral=4",
		"max-personal=3\tmax-personal-per-owner=1",
		"idle-after=1h0m0s\tnag-every=2h0m0s\treclaim-after=72h0m0s",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("role list output missing %q:\n%s", want, out.String())
		}
	}
}

// TestProjectEscalationCommands exercises `project escalation list` end-to-end
// through httptest.Server + MemStore (seeded through the store; the set and
// clear verbs are gone), and the tier-spec parse the removed --tier used.
func TestProjectEscalationCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "p")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer
	list := func() string {
		t.Helper()
		out.Reset()
		errb.Reset()
		if code := run([]string{"project", "escalation", "list", "--admin-url", ts.URL, "p"}, getenv, &out, &errb); code != 0 {
			t.Fatalf("project escalation list: exit=%d stderr=%s", code, errb.String())
		}
		return out.String()
	}

	var tiers []jam.EscalationTier
	for _, spec := range []string{"human:alice,human:bob@15m", "human:carol@1h"} {
		tier, err := jam.ParseEscalationTierSpec(spec)
		if err != nil {
			t.Fatal(err)
		}
		tiers = append(tiers, tier)
	}
	if err := store.SetEscalationPolicy("p", "", tiers); err != nil {
		t.Fatal(err)
	}
	got := list()
	if !strings.Contains(got, "default\ttier 0\thuman:alice,human:bob\ttimeout=15m0s") || !strings.Contains(got, "default\ttier 1\thuman:carol\ttimeout=1h0m0s") {
		t.Fatalf("project escalation list output missing expected fields:\n%s", got)
	}

	// a category chain lists alongside the default; clearing it leaves the default
	infra, err := jam.ParseEscalationTierSpec("human:sre@10m")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEscalationPolicy("p", "infra", []jam.EscalationTier{infra}); err != nil {
		t.Fatal(err)
	}
	if got := list(); !strings.Contains(got, "default\ttier 0\thuman:alice,human:bob\ttimeout=15m0s") || !strings.Contains(got, "infra\ttier 0\thuman:sre\ttimeout=10m0s") {
		t.Fatalf("project escalation list missing default or infra chain:\n%s", got)
	}
	if err := store.SetEscalationPolicy("p", "infra", nil); err != nil {
		t.Fatal(err)
	}
	if got := list(); !strings.Contains(got, "default\ttier 0") || strings.Contains(got, "infra\ttier") {
		t.Fatalf("project escalation list after clearing infra:\n%s", got)
	}
	if err := store.SetEscalationPolicy("p", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := list(); strings.TrimSpace(got) != "" {
		t.Fatalf("project escalation list after clear should be empty:\n%s", got)
	}

	// a tier missing '@timeout' is a parse error
	if _, err := jam.ParseEscalationTierSpec("human:alice"); err == nil || !strings.Contains(err.Error(), "missing '@timeout'") {
		t.Fatalf("malformed tier spec err = %v", err)
	}
}

// TestProjectChatServiceCommands exercises `project chat-service show`
// end-to-end through httptest.Server + MemStore (set and clear are store-level).
func TestProjectChatServiceCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "p")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer
	show := func() string {
		t.Helper()
		out.Reset()
		errb.Reset()
		if code := run([]string{"project", "chat-service", "show", "--admin-url", ts.URL, "--project", "p"}, getenv, &out, &errb); code != 0 {
			t.Fatalf("project chat-service show: exit=%d stderr=%s", code, errb.String())
		}
		return strings.TrimSpace(out.String())
	}
	if got := show(); got != "(none)" {
		t.Fatalf("project chat-service show (before set) = %q, want (none)", got)
	}
	if err := store.SetChatService("p", "discord"); err != nil {
		t.Fatal(err)
	}
	if got := show(); got != "discord" {
		t.Fatalf("project chat-service show (after set) = %q, want discord", got)
	}
	if err := store.SetChatService("p", ""); err != nil {
		t.Fatal(err)
	}
	if got := show(); got != "(none)" {
		t.Fatalf("project chat-service show (after clear) = %q, want (none)", got)
	}
}

// The project, room and context config-writing verbs are gone: each is its
// group's unknown subcommand.
func TestProjectConfigVerbsGone(t *testing.T) {
	for _, gone := range [][]string{
		{"project", "create", "acme"}, {"project", "rm", "acme"}, {"project", "rename", "acme", "x"},
		{"project", "escalation", "set", "acme"}, {"project", "escalation", "clear", "acme"},
		{"project", "chat-service", "set"}, {"project", "chat-service", "clear"},
		{"room", "add", "acme", "eng"}, {"room", "rename", "acme", "eng", "x"}, {"room", "rm", "acme", "eng"},
		{"context", "set"}, {"context", "clear"},
	} {
		var out, errb bytes.Buffer
		if code := run(gone, func(string) string { return "" }, &out, &errb); code != 2 {
			t.Errorf("%v: exit = %d, want 2", gone, code)
		}
		if !strings.Contains(errb.String(), "unknown subcommand") {
			t.Errorf("%v: stderr = %q, want the unknown-subcommand error", gone, errb.String())
		}
	}
}

// The role, standing, egress, destination and model-spec config-writing verbs
// are gone: each is its group's unknown subcommand.
func TestRoleConfigVerbsGone(t *testing.T) {
	for _, gone := range [][]string{
		{"role", "add", "--name", "x"}, {"role", "rm", "x"},
		{"standing", "add", "--role", "x"}, {"standing", "rm", "--role", "x", "y"},
		{"egress", "set", "--role", "x", "a.com"}, {"egress", "clear", "--role", "x"},
		{"destination", "add", "--name", "x"}, {"destination", "rm", "x"}, {"destination", "import", "x.yaml"},
		{"model-spec", "add", "x.yaml"}, {"model-spec", "update", "x.yaml"}, {"model-spec", "delete", "x"},
	} {
		var out, errb bytes.Buffer
		if code := run(gone, func(string) string { return "" }, &out, &errb); code != 2 {
			t.Errorf("%v: exit = %d, want 2", gone, code)
		}
		if !strings.Contains(errb.String(), "unknown subcommand") {
			t.Errorf("%v: stderr = %q, want the unknown-subcommand error", gone, errb.String())
		}
	}
}

// The kit config-writing verbs and the whole kit draft group are gone: each
// is the kit group's unknown subcommand; studio raise has no --kit-draft.
func TestKitConfigVerbsGone(t *testing.T) {
	for _, gone := range [][]string{
		{"kit", "push", "dir"}, {"kit", "pin", "app", "2"}, {"kit", "rm", "app"},
		{"kit", "draft", "open", "app"}, {"kit", "draft", "status", "app"}, {"kit", "draft", "publish", "app"},
		{"kit", "draft", "build", "app"},
	} {
		var out, errb bytes.Buffer
		if code := run(gone, func(string) string { return "" }, &out, &errb); code != 2 {
			t.Errorf("%v: exit = %d, want 2", gone, code)
		}
		if !strings.Contains(errb.String(), "unknown subcommand") {
			t.Errorf("%v: stderr = %q, want the unknown-subcommand error", gone, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"studio", "raise", "--id", "try", "--role", "guest", "--kit-draft", "app"}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Errorf("studio raise --kit-draft: exit = %d, want 2 (%s)", code, errb.String())
	}
}

// A raise's server error is escaped.
func TestStudioRaiseEscapesTheServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "raise: kit.yml: base \"a\x1b[2Jb\" unknown", http.StatusBadRequest)
	}))
	t.Cleanup(ts.Close)
	var out, errb bytes.Buffer
	code := run([]string{"studio", "raise", "--admin-url", ts.URL, "--id", "try", "--role", "guest"}, func(string) string { return "" }, &out, &errb)
	if code != 1 || strings.Contains(errb.String(), "\x1b") || !strings.Contains(errb.String(), `a\x1b[2Jb`) {
		t.Fatalf("raise: %d %q", code, errb.String())
	}
}

func TestUnknownCommandExits2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, func(string) string { return "" }, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestKitCommandsRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	// two versions, seeded in the store (kits change only in git)
	for _, cfg := range []string{
		"kind: studio\nname: web\negress:\n  - github.com\n  - claude.ai\nbuild-args:\n  A: b\n",
		"kind: studio\negress:\n  - github.com\n  - claude.ai\nbuild-args:\n  A: c\n",
	} {
		if _, _, err := jam.PushStudioKit(store, "web", cfg); err != nil {
			t.Fatal(err)
		}
	}

	var out, errb bytes.Buffer

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
	if strings.Contains(out.String(), "name:") || !strings.Contains(out.String(), "kind: studio") ||
		!strings.Contains(out.String(), "egress ceiling: github.com") ||
		!strings.Contains(out.String(), "excluded (COV-208): claude.ai") {
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
}

// TestStudioCommandsRoundTrip exercises the `studio` verb group (raise|list|status|
// teardown) end to end through the CLI entrypoint. Unlike the other CLI round
// trips, studio's mutation routes need a live Supervisor (a nil sup 503s), so
// this test wires one with a fake Launcher. It also pins down the "never
// print the identity token" constraint on `studio raise`.
func TestStudioCommandsRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// studio raise
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"studio", "raise", "--admin-url", ts.URL, "--id", "w1", "--role", "guest", "--unit", "AET-1",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("studio raise: exit=%d stderr=%s", code, errb.String())
	}
	// Only label, session id and phase may be printed — proves the minted
	// identity token never reaches stdout.
	if !regexp.MustCompile(`^raised w1 as session ses_[0-9a-z]{26} \(phase=live\)\n$`).MatchString(out.String()) {
		t.Fatalf("studio raise output = %q, want \"raised w1 as session <ses_id> (phase=live)\" (must not leak the identity token)", out.String())
	}

	// studio list reflects the raised cove
	out.Reset()
	errb.Reset()
	if code := run([]string{"studio", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("studio list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "w1") || !strings.Contains(out.String(), "role=guest") || !strings.Contains(out.String(), "phase=live") {
		t.Fatalf("studio list output missing expected fields:\n%s", out.String())
	}

	// studio status
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"studio", "status", "--admin-url", ts.URL, "--id", "w1", "--activity", "waiting",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("studio status: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "reported w1 activity=waiting") {
		t.Fatalf("studio status output missing expected text:\n%s", out.String())
	}

	// studio list reflects the reported activity
	out.Reset()
	errb.Reset()
	if code := run([]string{"studio", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("studio list (after status): exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "activity=waiting") {
		t.Fatalf("studio list output missing updated activity:\n%s", out.String())
	}

	// studio teardown
	out.Reset()
	errb.Reset()
	if code := run([]string{"studio", "teardown", "--admin-url", ts.URL, "--id", "w1"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("studio teardown: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "tore down w1") {
		t.Fatalf("studio teardown output missing expected text:\n%s", out.String())
	}

	// studio list no longer shows the torn-down cove
	out.Reset()
	errb.Reset()
	if code := run([]string{"studio", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 {
		t.Fatalf("studio list (after teardown): exit=%d stderr=%s", code, errb.String())
	}
	if strings.Contains(out.String(), "w1") {
		t.Fatalf("studio list still shows torn-down cove:\n%s", out.String())
	}

	// required-flag checks
	out.Reset()
	errb.Reset()
	if code := run([]string{"studio", "raise", "--admin-url", ts.URL, "--id", "w1"}, getenv, &out, &errb); code != 2 {
		t.Fatalf("studio raise (no --role): exit=%d, want 2 (stderr=%s)", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"studio", "status", "--admin-url", ts.URL, "--id", "w1"}, getenv, &out, &errb); code != 2 {
		t.Fatalf("studio status (no --activity): exit=%d, want 2 (stderr=%s)", code, errb.String())
	}
}

// grantAllSessions is a jam.SessionAllocator that admits every personal
// request (the real one needs Postgres).
type grantAllSessions struct{}

func (grantAllSessions) GrantPersonal(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}
func (grantAllSessions) GrantEphemeral(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (grantAllSessions) RecordRelease(context.Context, string, string, string) error { return nil }

// TestSessionCommandsRoundTrip exercises `session request|list|release` end to
// end. The loopback operator is "local", so alice is linked to that login.
func TestSessionCommandsRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "pair", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	// Personal sessions are delivered over Discord (request-time check).
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice", Handle: "@alice", Login: "local", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "111"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	h := jam.NewAdminHandler(store, sup, grantAllSessions{}, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("pair with me"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"session", "request", "--admin-url", ts.URL, "--project", "acme", "--role", "pair", "--prompt-file", promptFile}, getenv, &out, &errb); code != 0 {
		t.Fatalf("session request: exit=%d stderr=%s", code, errb.String())
	}
	id := strings.TrimSpace(out.String())
	if !strings.HasPrefix(id, "ses_") || strings.ContainsAny(id, " \t\n") {
		t.Fatalf("session request must print only the session id, got %q", out.String())
	}
	if inst, ok := store.GetInstance(id); !ok || inst.Owner != "alice" || inst.SessionKind != "personal" {
		t.Fatalf("instance = %+v,%v", inst, ok)
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"session", "list", "--admin-url", ts.URL, "--project", "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("session list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), id) || !strings.Contains(out.String(), "role=pair") || !strings.Contains(out.String(), "phase=live") {
		t.Fatalf("session list output:\n%s", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"session", "release", "--admin-url", ts.URL, id}, getenv, &out, &errb); code != 0 {
		t.Fatalf("session release: exit=%d stderr=%s", code, errb.String())
	}
	if _, ok := store.GetInstance(id); ok {
		t.Fatal("instance still present after session release")
	}

	// required-argument checks
	for _, args := range [][]string{
		{"session", "request", "--admin-url", ts.URL, "--project", "acme"},
		{"session", "release", "--admin-url", ts.URL},
		{"session"},
	} {
		out.Reset()
		errb.Reset()
		if code := run(args, getenv, &out, &errb); code != 2 {
			t.Fatalf("%v: exit=%d, want 2 (stderr=%s)", args, code, errb.String())
		}
	}
}

// The allocator adapter maps a personal grant onto allocator.Request and
// translates the no-ledger error to jam.ErrNeedsLedger.
func TestPersonalAllocator_NoLedger(t *testing.T) {
	a := allocator.New(jam.InstanceCounter{Store: nil}, allocator.StaticPolicy{{Project: "acme", Role: "pair"}: {MaxPersonal: 2}}, nil)
	ok, err := personalAllocator{a}.GrantPersonal(context.Background(), "acme", "pair", "personal-alice-1", "alice")
	if ok || !errors.Is(err, jam.ErrNeedsLedger) {
		t.Fatalf("got %v,%v; want jam.ErrNeedsLedger", ok, err)
	}
}

// capturingLedger records the request and caps a grant reaches the ledger with.
type capturingLedger struct {
	req  allocator.Request
	caps allocator.Caps
}

func (c *capturingLedger) Record(context.Context, allocator.Event) error { return nil }
func (c *capturingLedger) Grant(_ context.Context, req allocator.Request, caps allocator.Caps) (bool, error) {
	c.req, c.caps = req, caps
	return true, nil
}
func (c *capturingLedger) OutstandingReservations(context.Context, time.Time) ([]allocator.Reservation, error) {
	return nil, nil
}

// With a ledger, the adapter grants a personal-kind reservation owned by the
// requester against the role's pool and per-owner caps.
func TestPersonalAllocator_MapsRequest(t *testing.T) {
	cl := &capturingLedger{}
	a := allocator.New(jam.InstanceCounter{Store: nil}, allocator.StaticPolicy{{Project: "acme", Role: "pair"}: {MaxPersonal: 2, MaxPersonalPerOwner: 1}}, cl)
	ok, err := personalAllocator{a}.GrantPersonal(context.Background(), "acme", "pair", "personal-alice-1", "alice")
	if !ok || err != nil {
		t.Fatalf("got %v,%v", ok, err)
	}
	want := allocator.Request{Project: "acme", Role: "pair", ReservationID: "personal-alice-1", Kind: allocator.SessionPersonal, Owner: "alice"}
	if cl.req != want || cl.caps != (allocator.Caps{Kind: 2, Owner: 1}) {
		t.Fatalf("ledger saw %+v %+v", cl.req, cl.caps)
	}
}

// teardownResetter is a jam.StandingResetter that tears the session down via
// the supervisor (state is the reconciler's concern), or fails with err.
type teardownResetter struct {
	sup *jam.Supervisor
	err error
}

func (r *teardownResetter) ResetStanding(ctx context.Context, project, role, name string) error {
	if r.err != nil {
		return r.err
	}
	return r.sup.Teardown(ctx, jam.StandingActorID(project, role, name))
}

// `standing reset` resets a declared name through the standing reconciler,
// keeping the declaration; a pending reset says so; an undeclared name exits 1
// (404) (COV-249).
func TestStandingResetCommand(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour},
		Allocation: jam.RoleAllocation{Standing: []jam.StandingSession{{Name: "alice-bot", Prompt: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	rs := &teardownResetter{sup: sup}
	sup.SetStandingResetter(rs)
	id := jam.SeedStandingSession(store, "acme", "reviewer", "alice-bot")
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: id, Project: "acme", Role: "reviewer", Name: "alice-bot", SessionKind: jam.SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	defer ts.Close()
	getenv := func(string) string { return "" }
	args := []string{"standing", "reset", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer", "alice-bot"}

	var out, errb bytes.Buffer
	if code := run(args, getenv, &out, &errb); code != 0 {
		t.Fatalf("standing reset: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "reset standing session alice-bot") {
		t.Fatalf("output = %q", out.String())
	}
	if _, ok := store.GetInstance(id); ok {
		t.Fatal("reset must tear the cove down")
	}
	if r, _ := store.GetRole("acme", "reviewer"); len(r.Allocation.Standing) != 1 {
		t.Fatalf("reset must keep the declaration: %+v", r.Allocation.Standing)
	}

	rs.err = errors.New("volume in use")
	out.Reset()
	if code := run(args, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "pending") || !strings.Contains(out.String(), "volume in use") {
		t.Fatalf("pending reset: exit=%d out=%q", code, out.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"standing", "reset", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer", "nobody"}, getenv, &out, &errb); code != 1 {
		t.Fatalf("reset of an undeclared name: exit=%d, want 1 (stderr=%s)", code, errb.String())
	}
}

// queueUpgrader is a jam.StandingUpgrader queue: it records the last force
// and fails with err.
type queueUpgrader struct {
	force, queued bool
	err           error
}

func (q *queueUpgrader) QueueUpgrade(_, _, _ string, force bool) error {
	if q.err != nil {
		return q.err
	}
	q.force, q.queued = force, true
	return nil
}

func (q *queueUpgrader) UpgradeState(string, string, string) string {
	if q.queued {
		return jam.UpgradeQueued
	}
	return ""
}

// `standing upgrade` queues the upgrade and says how to watch it; --force
// reaches the server; `standing list` shows upgrade=; a pending reset or an
// undeclared name exits 1; an already-current session says so (COV-251).
func TestStandingUpgradeCommand(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour},
		Allocation: jam.RoleAllocation{Standing: []jam.StandingSession{{Name: "alice-bot", Prompt: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	q := &queueUpgrader{}
	sup.SetStandingUpgrader(q)
	jam.SeedStandingSession(store, "acme", "reviewer", "alice-bot") // a session to upgrade (down)
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	defer ts.Close()
	getenv := func(string) string { return "" }
	args := []string{"standing", "upgrade", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer", "alice-bot"}

	var out, errb bytes.Buffer
	if code := run(args, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "queued") || !strings.Contains(out.String(), "standing list") || q.force {
		t.Fatalf("upgrade: exit=%d out=%q stderr=%s force=%v", code, out.String(), errb.String(), q.force)
	}
	out.Reset()
	if code := run(append(args, "--force"), getenv, &out, &errb); code != 0 || !q.force {
		t.Fatalf("forced upgrade: exit=%d out=%q force=%v", code, out.String(), q.force)
	}
	out.Reset()
	if code := run([]string{"standing", "list", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer"}, getenv, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "\tupgrade=queued\n") {
		t.Fatalf("list: exit=%d out=%q", code, out.String())
	}

	q.err = fmt.Errorf("%w: x", jam.ErrStandingResetPending)
	errb.Reset()
	if code := run(args, getenv, &out, &errb); code != 1 || !strings.Contains(errb.String(), "reset") {
		t.Fatalf("during a pending reset: exit=%d stderr=%q, want 1", code, errb.String())
	}
	q.err = nil
	if code := run([]string{"standing", "upgrade", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer", "nobody"}, getenv, &out, &errb); code != 1 {
		t.Fatalf("upgrade of an undeclared name: exit=%d, want 1", code)
	}

	// Already current: nothing restarted, said so.
	cur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jam.StandingUpgradeResult{Image: "jam-kit:abc", Reason: "already current"})
	}))
	defer cur.Close()
	out.Reset()
	if code := run([]string{"standing", "upgrade", "--admin-url", cur.URL, "--role", "reviewer", "alice-bot"}, getenv, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "already runs the current image (jam-kit:abc)") {
		t.Fatalf("already current: exit=%d out=%q", code, out.String())
	}
}

// waitServer is a fake admin API for `standing upgrade --wait`: the upgrade is
// queued; the standing list reports it pending for the first pending polls,
// then gone; the studio's image is image.
func waitServer(t *testing.T, pending int, image string) *httptest.Server {
	t.Helper()
	polls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(jam.StandingUpgradeResult{Pending: true, State: jam.UpgradeQueued})
		case strings.HasSuffix(r.URL.Path, "/standing"):
			polls++
			st := jam.StandingStatus{StandingSession: jam.StandingSession{Name: "bot", Prompt: "p"}, SessionID: jam.StandingActorID(jam.DefaultProject, "dev", "bot")}
			if polls <= pending {
				st.Upgrade = jam.UpgradePreparing
			}
			_ = json.NewEncoder(w).Encode([]jam.StandingStatus{st})
		default: // coves
			_ = json.NewEncoder(w).Encode([]jam.CoveSummary{{ID: jam.StandingActorID(jam.DefaultProject, "dev", "bot"), Phase: "live", Image: image}})
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// `standing upgrade --wait` polls until the upgrade is done and reports the
// studio's image; exit 1 when it ends not ok or times out.
func TestStandingUpgradeWait(t *testing.T) {
	defer func(d time.Duration) { standingWaitPoll = d }(standingWaitPoll)
	standingWaitPoll = time.Millisecond
	getenv := func(string) string { return "" }
	up := func(url string, extra ...string) (int, string) {
		var out, errb bytes.Buffer
		code := run(append([]string{"standing", "upgrade", "--admin-url", url, "--role", "dev", "bot", "--wait"}, extra...), getenv, &out, &errb)
		return code, out.String() + errb.String()
	}
	if code, out := up(waitServer(t, 2, "ok").URL); code != 0 || !strings.Contains(out, "upgraded standing session bot") || !strings.Contains(out, "image=ok") {
		t.Fatalf("wait ok: exit=%d out=%q", code, out)
	}
	if code, out := up(waitServer(t, 0, "-").URL); code != 1 || !strings.Contains(out, "image=-") {
		t.Fatalf("wait, not raised: exit=%d out=%q", code, out)
	}
	if code, out := up(waitServer(t, 1<<30, "stale").URL, "--wait-timeout", "20ms"); code != 1 || !strings.Contains(out, "timed out") || !strings.Contains(out, "preparing") {
		t.Fatalf("wait timeout: exit=%d out=%q", code, out)
	}
}

// `standing list` lists a role's standing sessions (declared through the store;
// declarations change only in git).
func TestStandingListCommand(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour}, Allocation: jam.RoleAllocation{MaxEphemeral: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddStanding(store, "acme", "reviewer", jam.StandingSession{Name: "alice-bot", Prompt: "review every PR"}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer
	if code := run([]string{"standing", "list", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("standing list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "alice-bot") || !strings.Contains(out.String(), "id=-") {
		t.Fatalf("standing list output:\n%s", out.String())
	}

	// required-argument checks
	for _, args := range [][]string{
		{"standing", "reset", "--admin-url", ts.URL, "--role", "reviewer"},
		{"standing", "reset", "--admin-url", ts.URL, "x"},
		{"standing", "upgrade", "--admin-url", ts.URL, "--role", "reviewer"},
		{"standing", "upgrade", "--admin-url", ts.URL, "x"},
		{"standing", "list", "--admin-url", ts.URL},
		{"standing"},
	} {
		out.Reset()
		errb.Reset()
		if code := run(args, getenv, &out, &errb); code != 2 {
			t.Fatalf("%v: exit=%d, want 2 (stderr=%s)", args, code, errb.String())
		}
	}
}

// `egress show` reports a role's egress policy and `role list` carries it (kit
// default, none, or the list); the policy is set through the store (it changes
// only in git).
func TestEgressShowCommand(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour}, Allocation: jam.RoleAllocation{MaxEphemeral: 2}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	runOK := func(args ...string) string {
		t.Helper()
		out.Reset()
		errb.Reset()
		if code := run(args, getenv, &out, &errb); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, errb.String())
		}
		return out.String()
	}
	flags := []string{"--admin-url", ts.URL, "--project", "acme", "--role", "reviewer"}

	if got := runOK(append([]string{"egress", "show"}, flags...)...); !strings.Contains(got, "kit default") {
		t.Fatalf("show (unset) = %q, want kit default", got)
	}
	if got := runOK("role", "list", "--admin-url", ts.URL, "--project", "acme"); !strings.Contains(got, "egress=kit") {
		t.Fatalf("role list (unset) = %q, want egress=kit", got)
	}

	if _, err := jam.SetRoleEgress(store, "acme", "reviewer", []string{"A.com", ".b.org"}); err != nil {
		t.Fatal(err)
	}
	if got := runOK(append([]string{"egress", "show"}, flags...)...); !strings.Contains(got, ".b.org") || !strings.Contains(got, "a.com") {
		t.Fatalf("show = %q", got)
	}
	if got := runOK("role", "list", "--admin-url", ts.URL, "--project", "acme"); !strings.Contains(got, "egress=.b.org,a.com") {
		t.Fatalf("role list = %q, want egress=.b.org,a.com", got)
	}

	if _, err := jam.SetRoleEgress(store, "acme", "reviewer", nil); err != nil {
		t.Fatal(err)
	}
	if got := runOK("role", "list", "--admin-url", ts.URL, "--project", "acme"); !strings.Contains(got, "egress=none") {
		t.Fatalf("role list = %q, want egress=none", got)
	}
	if got := runOK(append([]string{"egress", "show"}, flags...)...); !strings.Contains(got, "none") {
		t.Fatalf("show (empty) = %q, want none", got)
	}

	// usage errors exit 2
	for _, args := range [][]string{
		{"egress"},
		{"egress", "bogus", "--admin-url", ts.URL, "--role", "reviewer"},
		{"egress", "show", "--admin-url", ts.URL},
		{"egress", "show", "--admin-url", ts.URL, "--role", "reviewer", "extra"}, // stray arg
	} {
		out.Reset()
		errb.Reset()
		if code := run(args, getenv, &out, &errb); code != 2 {
			t.Fatalf("%v: exit=%d, want 2 (stderr=%s)", args, code, errb.String())
		}
	}
}

func TestStudioShowSurfacesExcludedRoots(t *testing.T) {
	ceiling, excluded := studioShowEgress([]string{"github.com", "claude.ai"})
	if len(ceiling) != 1 || ceiling[0] != "github.com" {
		t.Fatalf("ceiling=%v", ceiling)
	}
	if len(excluded) != 1 || excluded[0] != "claude.ai" {
		t.Fatalf("excluded=%v", excluded)
	}
}

func TestRoleListDestinationCredentials(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "P")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	ds, creds, err := jam.ParseDestinations("git=git-pat-cove,anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("P", jam.Role{Name: "w", Scope: jam.Scope{Destinations: ds, Credentials: creds}}); err != nil {
		t.Fatal(err)
	}
	r, ok := store.GetRole("P", "w")
	if !ok || !slices.Equal(r.Scope.Destinations, []string{"git", "anthropic"}) || r.Scope.Credentials["git"] != "git-pat-cove" {
		t.Fatalf("stored role = %+v, %v", r, ok)
	}
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "P"}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "dests=git=git-pat-cove,anthropic") {
		t.Fatalf("role list: exit=%d out=%s", code, out.String())
	}
	if _, _, err := jam.ParseDestinations("git="); err == nil {
		t.Fatal("malformed destinations: want an error")
	}
}

func TestDestinationListEnvAndGit(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if err := store.AddDestination(jam.Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, Apply: jam.ApplyBearer,
		Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"}, Git: true}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"destination", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "env=GH_ENTERPRISE_TOKEN,GH_HOST") || !strings.Contains(out.String(), "git") {
		t.Fatalf("destination list: exit=%d out=%s", code, out.String())
	}
}

// A custom apply method without its spec is refused by ValidateDestination
// (the check destinations changed in git still pass).
func TestDestinationCustomSpecValidation(t *testing.T) {
	err := jam.ValidateDestination(jam.Destination{Name: "bad", Route: "/bad/", Upstream: "https://x", Apply: "custom"}, func(string) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "apply_spec") {
		t.Fatalf("custom without spec: %v", err)
	}
}

// An older Jam returns no connector with the enrollment: the CLI falls back to
// the legacy Anthropic + git snippet.
func TestEnrollCommandLegacyServerFallsBack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"x","token":"TOK"}`))
	}))
	defer ts.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"enroll", "--admin-url", ts.URL, "--id", "x", "--role", "guest", "--base-url", "https://jam.example"}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), `ANTHROPIC_BASE_URL="https://jam.example/anthropic"`) || !strings.Contains(out.String(), "insteadOf") {
		t.Fatalf("legacy snippet expected:\n%s", out.String())
	}
}

// kit show prints a broken version's own fixed error, and --own carries a
// directory kit's note file contents.
func TestKitShowNotesAndBroken(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	if _, _, err := jam.PushKitFiles(store, "web", kittreetest.Files("docs/release.md", "RELEASE STEPS",
		"kit.yml", "kind: studio\nnotes:\n  - name: release.md\n    read-when: releasing\n    file: docs/release.md\n")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutKitDocs([]jam.Kit{{Name: "old", Current: 1, Broken: map[int]string{1: "its base.context is not a readable tar.gz"}}}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"kit", "show", "--admin-url", ts.URL, "old"}, getenv, &out, &errb); code != 1 || !strings.Contains(errb.String(), "kit old@v1 could not be migrated to a tree: its base.context is not a readable tar.gz") {
		t.Fatalf("show of a broken version: exit=%d stderr=%s", code, errb.String())
	}
	out.Reset()
	if code := run([]string{"kit", "show", "--admin-url", ts.URL, "--own", "web"}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "RELEASE STEPS") {
		t.Fatalf("show --own must carry the note file's contents: exit=%d\n%s", code, out.String())
	}
}

func TestRoleListModelSpec(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "P")
	if err := store.PutModelSpec(jam.ModelSpec{Name: "opus", Type: jam.HarnessClaude, Version: "2.1.0",
		Principal: jam.ModelPrincipal{Credential: "c"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if err := jam.PutRoleKeeping(store, "P", jam.Role{Name: "w", ModelSpec: "opus"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := store.GetRole("P", "w"); r.ModelSpec != "opus" {
		t.Fatalf("binding = %q", r.ModelSpec)
	}
	if err := jam.PutRoleKeeping(store, "P", jam.Role{Name: "u"}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "P"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "model-spec=opus") || !strings.Contains(out.String(), "model-spec=claude-default") {
		t.Fatalf("role list:\n%s", out.String())
	}
	if err := jam.PutRoleKeeping(store, "P", jam.Role{Name: "w", ModelSpec: "ghost"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown spec: %v", err)
	}
}

// TestUserMemberAccountCommands drives the registry verbs end to end against a
// real admin handler: a user with a login and OIDC binding, a project member
// with a Discord inbox, and the user's Discord account.
func TestUserMemberAccountCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if _, err := store.CreateConnection(jam.Connection{Kind: "discord", Name: "discord"}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	exec := func(want int, argv ...string) string {
		t.Helper()
		out.Reset()
		errb.Reset()
		n := 2 // verb + subcommand; "project member <sub>" is three words
		if argv[0] == "project" {
			n = 3
		}
		argv = append(argv[:n:n], append([]string{"--admin-url", ts.URL}, argv[n:]...)...)
		if code := run(argv, getenv, &out, &errb); code != want {
			t.Fatalf("%v: exit=%d, want %d; stderr=%s", argv, code, want, errb.String())
		}
		return out.String()
	}
	dave, err := store.CreateUser(jam.User{Name: "david", Logins: []string{"auth0|d"}})
	if err != nil {
		t.Fatal(err)
	}
	acme, _ := store.GetProject("acme")
	if err := store.PutMembership(jam.Membership{ProjectID: acme.ID, UserID: dave.ID, Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "chan-9"}}}); err != nil {
		t.Fatal(err)
	}
	exec(0, "user", "oidc", "david", "https://accounts.google.com:dave-sub")
	exec(2, "user", "oidc", "david", "no-colon")
	exec(0, "account", "add", "--connection", "discord", "--uid", "123456789", "--user", "david")

	m, ok := jam.MemberByLogin(store, "acme", "auth0|d")
	if !ok || m.User.Name != "david" || len(m.User.OIDC) != 1 || m.DiscordUID != "123456789" {
		t.Fatalf("david = %+v, %v", m, ok)
	}
	if d, ok := m.Inbox("discord"); !ok || d != "chan-9" {
		t.Fatalf("inbox = %q, %v", d, ok)
	}
	show := exec(0, "user", "show", "david")
	if !strings.Contains(show, "login=auth0|d") || !strings.Contains(show, "projects=acme") || !strings.Contains(show, "uid=123456789") {
		t.Fatalf("user show:\n%s", show)
	}
	if list := exec(0, "project", "member", "list", "acme"); !strings.Contains(list, "delivery=discord:chan-9") {
		t.Fatalf("member list:\n%s", list)
	}
	if u := exec(0, "user", "list"); !strings.Contains(u, "user\tdavid") {
		t.Fatalf("user list:\n%s", u)
	}
	if conns := exec(0, "connection", "list"); !strings.Contains(conns, "connection\tdiscord") {
		t.Fatalf("connection list:\n%s", conns)
	}
	exec(2, "account", "add", "--connection", "discord")

	// The config-writing verbs are gone: each is the group's unknown subcommand.
	for _, gone := range [][]string{
		{"user", "add", "erin"}, {"user", "rename", "david", "x"}, {"user", "rm", "david"}, {"user", "login", "david"},
		{"project", "member", "add", "acme", "david"}, {"project", "member", "rm", "acme", "david"},
		{"connection", "add"}, {"connection", "rename", "discord", "x"}, {"connection", "cred", "discord", "x"}, {"connection", "rm", "discord"},
	} {
		exec(2, gone...)
		if !strings.Contains(errb.String(), "unknown subcommand") {
			t.Errorf("%v: stderr = %q, want the unknown-subcommand error", gone, errb.String())
		}
	}
}

// `studio raise --manager … --placement-timeout …` posts the managers in flag
// order and the timeout verbatim.
func TestStudioRaiseManagerFlagsOrdered(t *testing.T) {
	var got jam.CoveRaiseBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(jam.CoveRaiseResult{ID: "ses_1", Label: got.ID, Phase: "live"})
	}))
	defer ts.Close()
	var out, errb bytes.Buffer
	code := run([]string{"studio", "raise", "--admin-url", ts.URL, "--id", "w1", "--role", "guest",
		"--manager", "b", "--manager", "a", "--placement-timeout", "30s"}, func(string) string { return "" }, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !reflect.DeepEqual(got.Managers, []string{"b", "a"}) || got.PlacementTimeout != "30s" {
		t.Fatalf("posted managers=%v timeout=%q", got.Managers, got.PlacementTimeout)
	}
}

// `jam colima` moved to jam-studio-manager with the in-process launcher: the
// verb is gone, and invoking it says where it went (exit 2, as for any unknown
// command), globals or not.
func TestColimaVerbMoved(t *testing.T) {
	const want = `jam: colima moved to jam-studio-manager: run "jam-studio-manager colima setup-docker|check-docker"`
	for _, argv := range [][]string{
		{"colima"},
		{"colima", "setup-docker"},
		{"--log-level", "debug", "colima", "check-docker"},
		{"--dry-run", "colima"},
	} {
		var out, errb bytes.Buffer
		if code := run(argv, func(string) string { return "" }, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2", argv, code)
		}
		if !strings.Contains(errb.String(), "colima moved to jam-studio-manager") || strings.TrimSpace(errb.String()) != want {
			t.Errorf("%v: stderr = %q, want %q", argv, errb.String(), want)
		}
	}
	var out, errb bytes.Buffer
	run([]string{"help"}, func(string) string { return "" }, &out, &errb)
	if strings.Contains(out.String(), "colima") {
		t.Errorf("help still lists colima:\n%s", out.String())
	}
}
