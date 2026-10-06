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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/cli"
	"github.com/aethons-tools/cove/internal/jam"
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
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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
	if !strings.Contains(out.String(), "AT_JAM_IDENTITY_TOKEN=") {
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
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer
	// role add
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--project", "P",
		"--name", "guest", "--destinations", "anthropic",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add: exit=%d stderr=%s", code, errb.String())
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

	// role rm
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "rm", "--admin-url", ts.URL, "--project", "P", "guest"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role rm: exit=%d stderr=%s", code, errb.String())
	}
}

func TestProjectRosterCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// user add + project member add
	for _, argv := range [][]string{
		{"user", "add", "--admin-url", ts.URL, "--login", "auth0|abc", "alice"},
		{"project", "member", "add", "--admin-url", ts.URL, "acme", "alice"},
	} {
		out.Reset()
		errb.Reset()
		if code := run(argv, getenv, &out, &errb); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", argv, code, errb.String())
		}
	}
	if hu, ok := jam.HumanByLogin(store, "acme", "auth0|abc"); !ok || hu.Name != "alice" {
		t.Fatalf("user add --login + member add did not link alice: %+v,%v", hu, ok)
	}

	// project roster add-channel
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "roster", "add-channel", "--admin-url", ts.URL, "acme",
		"--name", "eng-help", "--ref", "ACME-1", "--service", "linear",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project roster add-channel: exit=%d stderr=%s", code, errb.String())
	}

	// project roster list reflects both
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "roster", "list", "--admin-url", ts.URL, "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project roster list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "member\talice\tusr_") || !strings.Contains(out.String(), "channel\teng-help\tservice=linear\tref=ACME-1") {
		t.Fatalf("project roster list output missing expected fields:\n%s", out.String())
	}

	// project member rm
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "member", "rm", "--admin-url", ts.URL, "acme", "alice"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project member rm: exit=%d stderr=%s", code, errb.String())
	}

	// project roster rm-channel
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "roster", "rm-channel", "--admin-url", ts.URL, "acme", "eng-help"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project roster rm-channel: exit=%d stderr=%s", code, errb.String())
	}

	// project roster list is now empty
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "roster", "list", "--admin-url", ts.URL, "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project roster list (after removal): exit=%d stderr=%s", code, errb.String())
	}
	if strings.Contains(out.String(), "alice") || strings.Contains(out.String(), "eng-help") {
		t.Fatalf("project roster list still shows removed entries:\n%s", out.String())
	}

	// role add --addressing plumbs through to the role's Scope.Addressing
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--project", "acme",
		"--name", "impl", "--addressing", "human:*,channel:eng-help",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add --addressing: exit=%d stderr=%s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "addressing=human:*,channel:eng-help") {
		t.Fatalf("role list output missing addressing:\n%s", out.String())
	}
	// role add --max-ephemeral sets the role's allocation policy; role list shows it
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--project", "acme",
		"--name", "worker", "--max-ephemeral", "4",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add --max-ephemeral: exit=%d stderr=%s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "max-ephemeral=4") {
		t.Fatalf("role list output missing max-ephemeral:\n%s", out.String())
	}
	// role add --max-personal / --max-personal-per-owner set the personal caps
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--project", "acme",
		"--name", "pair", "--max-personal", "3", "--max-personal-per-owner", "1",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add --max-personal: exit=%d stderr=%s", code, errb.String())
	}
	if r, ok := store.GetRole("acme", "pair"); !ok || r.Allocation.MaxPersonal != 3 || r.Allocation.MaxPersonalPerOwner != 1 {
		t.Fatalf("stored role = %+v,%v", r, ok)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "max-personal=3\tmax-personal-per-owner=1") {
		t.Fatalf("role list output missing personal caps:\n%s", out.String())
	}
	// role add --idle-after / --nag-every / --reclaim-after set the idle ladder
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"role", "add", "--admin-url", ts.URL, "--project", "acme",
		"--name", "idler", "--idle-after", "1h", "--nag-every", "2h", "--reclaim-after", "72h",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add --idle-after: exit=%d stderr=%s", code, errb.String())
	}
	if r, ok := store.GetRole("acme", "idler"); !ok || r.Allocation.IdleAfter != time.Hour || r.Allocation.NagEvery != 2*time.Hour || r.Allocation.ReclaimAfter != 72*time.Hour {
		t.Fatalf("stored role = %+v,%v", r, ok)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "acme"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "idle-after=1h0m0s\tnag-every=2h0m0s\treclaim-after=72h0m0s") {
		t.Fatalf("role list output missing idle settings:\n%s", out.String())
	}
	for _, flagName := range []string{"--idle-after", "--nag-every", "--reclaim-after"} {
		out.Reset()
		errb.Reset()
		if code := run([]string{"role", "add", "--admin-url", ts.URL, "--name", "bad", flagName, "-1h"}, getenv, &out, &errb); code != 2 {
			t.Fatalf("role add %s -1h: exit=%d, want 2", flagName, code)
		}
	}
	for _, flagName := range []string{"--max-personal", "--max-personal-per-owner"} {
		out.Reset()
		errb.Reset()
		if code := run([]string{"role", "add", "--admin-url", ts.URL, "--name", "bad", flagName, "-1"}, getenv, &out, &errb); code != 2 {
			t.Fatalf("role add %s -1: exit=%d, want 2", flagName, code)
		}
	}
}

// TestProjectEscalationCommands exercises `project escalation set|list|clear`
// end-to-end through httptest.Server + MemStore, including the --tier
// 'targets@timeout' parse and its missing-'@' error.
func TestProjectEscalationCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "p")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// project escalation set with two --tier flags
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "escalation", "set", "--admin-url", ts.URL, "p",
		"--tier", "human:alice,human:bob@15m",
		"--tier", "human:carol@1h",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation set: exit=%d stderr=%s", code, errb.String())
	}

	// project escalation list shows both tiers in order
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "escalation", "list", "--admin-url", ts.URL, "p"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "default\ttier 0\thuman:alice,human:bob\ttimeout=15m0s") || !strings.Contains(out.String(), "default\ttier 1\thuman:carol\ttimeout=1h0m0s") {
		t.Fatalf("project escalation list output missing expected fields:\n%s", out.String())
	}

	// project escalation clear empties the policy
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "escalation", "clear", "--admin-url", ts.URL, "p"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation clear: exit=%d stderr=%s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "escalation", "list", "--admin-url", ts.URL, "p"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation list (after clear): exit=%d stderr=%s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("project escalation list after clear should be empty:\n%s", out.String())
	}

	// --tier missing '@timeout' is a parse error
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "escalation", "set", "--admin-url", ts.URL, "p",
		"--tier", "human:alice",
	}, getenv, &out, &errb); code == 0 {
		t.Fatalf("project escalation set with malformed --tier should fail, got exit=0 out=%s", out.String())
	}
}

// TestProjectEscalationCategoryCommands exercises `--category` on
// set/list/clear: setting a category chain alongside the default, listing both,
// then clearing just the category and confirming the default survives.
func TestProjectEscalationCategoryCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "p")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// default chain
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "escalation", "set", "--admin-url", ts.URL, "p",
		"--tier", "human:oncall@30m",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation set (default): exit=%d stderr=%s", code, errb.String())
	}

	// infra category chain
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "escalation", "set", "--admin-url", ts.URL, "--category", "infra", "p",
		"--tier", "human:sre@10m",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation set --category infra: exit=%d stderr=%s", code, errb.String())
	}

	// list shows both default and infra
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "escalation", "list", "--admin-url", ts.URL, "p"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "default\ttier 0\thuman:oncall\ttimeout=30m0s") {
		t.Fatalf("project escalation list missing default chain:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "infra\ttier 0\thuman:sre\ttimeout=10m0s") {
		t.Fatalf("project escalation list missing infra chain:\n%s", out.String())
	}

	// clear --category infra removes just infra
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "escalation", "clear", "--admin-url", ts.URL, "--category", "infra", "p"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation clear --category infra: exit=%d stderr=%s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "escalation", "list", "--admin-url", ts.URL, "p"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project escalation list (after clear infra): exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "default\ttier 0\thuman:oncall\ttimeout=30m0s") {
		t.Fatalf("project escalation list should still show default chain after clearing infra:\n%s", out.String())
	}
	if strings.Contains(out.String(), "infra\ttier") {
		t.Fatalf("project escalation list should not show infra chain after clear:\n%s", out.String())
	}
}

// TestProjectChatServiceCommands exercises `project chat-service
// set|show|clear` end-to-end through httptest.Server + MemStore.
func TestProjectChatServiceCommands(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "p")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// show before set prints "(none)"
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "chat-service", "show", "--admin-url", ts.URL, "--project", "p",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project chat-service show: exit=%d stderr=%s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "(none)" {
		t.Fatalf("project chat-service show (before set) = %q, want (none)", out.String())
	}

	// set
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "chat-service", "set", "--admin-url", ts.URL, "--project", "p", "--service", "discord",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project chat-service set: exit=%d stderr=%s", code, errb.String())
	}

	// show reflects it
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "chat-service", "show", "--admin-url", ts.URL, "--project", "p",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project chat-service show: exit=%d stderr=%s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "discord" {
		t.Fatalf("project chat-service show (after set) = %q, want discord", out.String())
	}

	// clear
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "chat-service", "clear", "--admin-url", ts.URL, "--project", "p",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project chat-service clear: exit=%d stderr=%s", code, errb.String())
	}

	// show is back to "(none)"
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "chat-service", "show", "--admin-url", ts.URL, "--project", "p",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project chat-service show: exit=%d stderr=%s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "(none)" {
		t.Fatalf("project chat-service show (after clear) = %q, want (none)", out.String())
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
	if !strings.Contains(errb.String(), "invalid studio kit config") {
		t.Fatalf("stderr = %q, want it to contain %q", errb.String(), "invalid studio kit config")
	}
}

func TestKitCommandsRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yml")
	if err := os.WriteFile(valid, []byte("kind: studio\nname: web\negress:\n  - github.com\n  - claude.ai\nbuild-args:\n  A: b\n"), 0o600); err != nil {
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

	// an identical re-push makes no new version
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"kit", "push", "--admin-url", ts.URL, "--name", "web", "--config", valid,
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit push (same): exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "web unchanged (current v1)") {
		t.Fatalf("identical kit push output:\n%s", out.String())
	}

	// a changed kit makes a second version, for pin to target
	changed := filepath.Join(dir, "changed.yml")
	if err := os.WriteFile(changed, []byte("kind: studio\negress:\n  - github.com\n  - claude.ai\nbuild-args:\n  A: c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"kit", "push", "--admin-url", ts.URL, "--name", "web", "--config", changed,
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
	if r, ok := store.GetRole(jam.DefaultProject, "impl"); !ok || r.Kit != "web" {
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
	h := jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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
	// Only id+phase may be printed — proves the minted identity token never
	// reaches stdout.
	if out.String() != "raised w1 (phase=live)\n" {
		t.Fatalf("studio raise output = %q, want exactly %q (must not leak the identity token)", out.String(), "raised w1 (phase=live)\n")
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
	if err := store.AddHuman("acme", jam.Human{Name: "alice", Handle: "@alice", Login: "local", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "111"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	h := jam.NewAdminHandler(store, sup, grantAllSessions{}, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil)
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
	if !strings.HasPrefix(id, "personal-alice-") || strings.ContainsAny(id, " \t\n") {
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
	id := jam.StandingActorID("acme", "reviewer", "alice-bot")
	if _, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: id, Project: "acme", Role: "reviewer", Name: "alice-bot", SessionKind: jam.SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil))
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
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil))
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
		_ = json.NewEncoder(w).Encode(jam.StandingUpgradeResult{Image: "cove-kit:abc", Reason: "already current"})
	}))
	defer cur.Close()
	out.Reset()
	if code := run([]string{"standing", "upgrade", "--admin-url", cur.URL, "--role", "reviewer", "alice-bot"}, getenv, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "already runs the current image (cove-kit:abc)") {
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
			st := jam.StandingStatus{StandingSession: jam.StandingSession{Name: "bot", Prompt: "p"}}
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

// `standing add|list|rm` declare, list and dismiss a role's standing sessions;
// the prompt is read from a file host-side, and the role's other fields are kept.
func TestStandingCommandsRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour}, Allocation: jam.RoleAllocation{MaxEphemeral: 2}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("review every PR"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"standing", "add", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer", "--name", "alice-bot", "--prompt-file", promptFile}, getenv, &out, &errb); code != 0 {
		t.Fatalf("standing add: exit=%d stderr=%s", code, errb.String())
	}
	r, _ := store.GetRole("acme", "reviewer")
	if len(r.Allocation.Standing) != 1 || r.Allocation.Standing[0] != (jam.StandingSession{Name: "alice-bot", Prompt: "review every PR"}) {
		t.Fatalf("standing = %+v", r.Allocation.Standing)
	}
	if r.Allocation.MaxEphemeral != 2 || len(r.Scope.Destinations) != 1 {
		t.Fatalf("role fields not kept: %+v", r)
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"standing", "list", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("standing list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "alice-bot") || !strings.Contains(out.String(), jam.StandingActorID("acme", "reviewer", "alice-bot")) {
		t.Fatalf("standing list output:\n%s", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"standing", "rm", "--admin-url", ts.URL, "--project", "acme", "--role", "reviewer", "alice-bot"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("standing rm: exit=%d stderr=%s", code, errb.String())
	}
	if r, _ := store.GetRole("acme", "reviewer"); len(r.Allocation.Standing) != 0 {
		t.Fatalf("standing after rm = %+v", r.Allocation.Standing)
	}

	// a server-side refusal (unknown role) exits 1
	out.Reset()
	errb.Reset()
	if code := run([]string{"standing", "add", "--admin-url", ts.URL, "--project", "acme", "--role", "nobody", "--name", "x", "--prompt-file", promptFile}, getenv, &out, &errb); code != 1 {
		t.Fatalf("add on unknown role: exit=%d, want 1 (stderr=%s)", code, errb.String())
	}

	// required-argument checks
	for _, args := range [][]string{
		{"standing", "add", "--admin-url", ts.URL, "--role", "reviewer", "--prompt-file", promptFile},
		{"standing", "add", "--admin-url", ts.URL, "--role", "reviewer", "--name", "x"},
		{"standing", "add", "--admin-url", ts.URL, "--name", "x", "--prompt-file", promptFile},
		{"standing", "rm", "--admin-url", ts.URL, "--role", "reviewer"},
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

// `egress set|show|clear` manage a role's egress policy; `role list` shows it
// (kit default, none, or the list), and the role's other fields are kept.
func TestEgressCommandsRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour}, Allocation: jam.RoleAllocation{MaxEphemeral: 2}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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

	runOK(append(append([]string{"egress", "set"}, flags...), "A.com, .b.org")...)
	r, _ := store.GetRole("acme", "reviewer")
	if r.Scope.Egress == nil || strings.Join(r.Scope.Egress.Domains, ",") != ".b.org,a.com" {
		t.Fatalf("egress = %+v", r.Scope.Egress)
	}
	if r.Allocation.MaxEphemeral != 2 || len(r.Scope.Destinations) != 1 {
		t.Fatalf("role fields not kept: %+v", r)
	}
	if got := runOK(append([]string{"egress", "show"}, flags...)...); !strings.Contains(got, ".b.org") || !strings.Contains(got, "a.com") {
		t.Fatalf("show = %q", got)
	}
	if got := runOK("role", "list", "--admin-url", ts.URL, "--project", "acme"); !strings.Contains(got, "egress=.b.org,a.com") {
		t.Fatalf("role list = %q, want egress=.b.org,a.com", got)
	}

	runOK(append([]string{"egress", "set", "--none"}, flags...)...)
	if r, _ := store.GetRole("acme", "reviewer"); r.Scope.Egress == nil || len(r.Scope.Egress.Domains) != 0 {
		t.Fatalf("--none = %+v, want set and empty", r.Scope.Egress)
	}
	if got := runOK("role", "list", "--admin-url", ts.URL, "--project", "acme"); !strings.Contains(got, "egress=none") {
		t.Fatalf("role list = %q, want egress=none", got)
	}
	if got := runOK(append([]string{"egress", "show"}, flags...)...); !strings.Contains(got, "none") {
		t.Fatalf("show (empty) = %q, want none", got)
	}

	runOK(append([]string{"egress", "clear"}, flags...)...)
	if r, _ := store.GetRole("acme", "reviewer"); r.Scope.Egress != nil {
		t.Fatalf("after clear = %+v, want nil", r.Scope.Egress)
	}

	// a server-side refusal (bad domain) exits 1 and names the domain
	out.Reset()
	errb.Reset()
	if code := run(append(append([]string{"egress", "set"}, flags...), "https://evil.com"), getenv, &out, &errb); code != 1 || !strings.Contains(errb.String(), "https://evil.com") {
		t.Fatalf("bad domain: exit=%d stderr=%s", code, errb.String())
	}

	// usage errors exit 2
	for _, args := range [][]string{
		{"egress"},
		{"egress", "bogus", "--admin-url", ts.URL, "--role", "reviewer"},
		{"egress", "show", "--admin-url", ts.URL},
		{"egress", "set", "--admin-url", ts.URL, "--role", "reviewer"},                    // neither list nor --none
		{"egress", "set", "--admin-url", ts.URL, "--role", "reviewer", "--none", "a.com"}, // both
		{"egress", "clear", "--admin-url", ts.URL, "--role", "reviewer", "extra"},         // stray arg
	} {
		out.Reset()
		errb.Reset()
		if code := run(args, getenv, &out, &errb); code != 2 {
			t.Fatalf("%v: exit=%d, want 2 (stderr=%s)", args, code, errb.String())
		}
	}
}

func TestKitPushPacksContextDir(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	dir := t.TempDir()
	ctx := filepath.Join(dir, "ctx")
	if err := os.MkdirAll(ctx, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx, "Dockerfile"), []byte("FROM ${COVE_BASE_IMAGE}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// base.context-dir is relative → resolved against the kit file's directory.
	kitFile := filepath.Join(dir, "kit.yml")
	if err := os.WriteFile(kitFile, []byte("kind: studio\nname: ctxkit\nbase:\n  context-dir: ctx\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, "--name", "ctxkit", "--config", kitFile}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit push: exit=%d stderr=%s", code, errb.String())
	}
	// The stored kit must be a packed context (zip), with context-dir stripped.
	out.Reset()
	errb.Reset()
	if code := run([]string{"kit", "show", "--admin-url", ts.URL, "ctxkit"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("kit show: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "context:") || strings.Contains(out.String(), "context-files") {
		t.Fatalf("stored kit should carry a packed `context:` zip (not context-files); show:\n%s", out.String())
	}
	if strings.Contains(out.String(), "context-dir") {
		t.Fatalf("context-dir is client-only and must not be stored; show:\n%s", out.String())
	}
}

func TestKitPushValidatesStudioKit(t *testing.T) {
	good := "kind: studio\nname: web\negress:\n  - github.com\n"
	bad := "kind: studio\nname: web\nworkers: {}\n"
	if err := validatePushedKit([]byte(good)); err != nil {
		t.Fatalf("good studio kit rejected: %v", err)
	}
	if err := validatePushedKit([]byte(bad)); err == nil {
		t.Fatal("bad studio kit must be rejected at push")
	}
	if err := validatePushedKit([]byte("name: web\n")); err == nil {
		t.Fatal("non-studio kit must be rejected at push")
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

func TestRoleAddDestinationCredentials(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "P")
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if code := run([]string{"role", "add", "--admin-url", ts.URL, "--project", "P", "--name", "w", "--destinations", "git=git-pat-cove,anthropic"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add: exit=%d stderr=%s", code, errb.String())
	}
	r, ok := store.GetRole("P", "w")
	if !ok || !slices.Equal(r.Scope.Destinations, []string{"git", "anthropic"}) || r.Scope.Credentials["git"] != "git-pat-cove" {
		t.Fatalf("stored role = %+v, %v", r, ok)
	}
	out.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "P"}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "dests=git=git-pat-cove,anthropic") {
		t.Fatalf("role list: exit=%d out=%s", code, out.String())
	}
	errb.Reset()
	if code := run([]string{"role", "add", "--admin-url", ts.URL, "--name", "x", "--destinations", "git="}, getenv, &out, &errb); code != 2 {
		t.Fatalf("malformed --destinations: exit=%d, want 2 (stderr=%s)", code, errb.String())
	}
}

func TestDestinationAddEnvAndGit(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if code := run([]string{"destination", "add", "--admin-url", ts.URL, "--name", "gh", "--route", "/api/v3/", "--upstream", "https://api.github.com",
		"--identity-in", "bearer", "--apply", "bearer", "--env", "GH_HOST={host}", "--env", "GH_ENTERPRISE_TOKEN={token}", "--git"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("destination add: exit=%d stderr=%s", code, errb.String())
	}
	d := store.ListDestinations()
	if len(d) != 1 || d[0].Env["GH_HOST"] != "{host}" || d[0].Env["GH_ENTERPRISE_TOKEN"] != "{token}" || !d[0].Git {
		t.Fatalf("stored = %+v", d)
	}
	out.Reset()
	if code := run([]string{"destination", "list", "--admin-url", ts.URL}, getenv, &out, &errb); code != 0 || !strings.Contains(out.String(), "env=GH_ENTERPRISE_TOKEN,GH_HOST") || !strings.Contains(out.String(), "git") {
		t.Fatalf("destination list: exit=%d out=%s", code, out.String())
	}
	if code := run([]string{"destination", "add", "--admin-url", ts.URL, "--name", "x", "--route", "/x/", "--upstream", "https://x", "--env", "NOEQUALS"}, getenv, &out, &errb); code != 2 {
		t.Fatalf("malformed --env: exit=%d, want 2", code)
	}
}

// The removed --oauth-beta flag is a hard error pointing at its replacement
// (the broker's pool-credential beta), and nothing is written.
func TestDestinationAddOAuthBetaRemoved(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	var out, errb bytes.Buffer
	code := run([]string{"destination", "add", "--admin-url", ts.URL, "--name", "anthropic", "--route", "/anthropic/", "--upstream", "https://api.anthropic.com",
		"--identity-in", "bearer", "--cred-name", "anthropic-sub", "--apply", "bearer", "--oauth-beta"}, func(string) string { return "" }, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--oauth-beta was removed") || !strings.Contains(errb.String(), "subscription-pool credential") ||
		!strings.Contains(errb.String(), "docs/usage/jam/pool.md") {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if len(store.ListDestinations()) != 0 {
		t.Fatal("nothing may be written when --oauth-beta is passed")
	}
}

// --identity-in/--apply take preset names (incl. raw); a custom header spec
// comes in through `destination import` YAML.
func TestDestinationRawPresetAndCustomSpecImport(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if code := run([]string{"destination", "add", "--admin-url", ts.URL, "--name", "linear", "--route", "/linear/", "--upstream", "https://api.linear.app",
		"--identity-in", "raw", "--apply", "raw"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("destination add raw: exit=%d stderr=%s", code, errb.String())
	}
	if code := run([]string{"destination", "add", "--admin-url", ts.URL, "--name", "bad", "--route", "/bad/", "--upstream", "https://x",
		"--apply", "custom"}, getenv, &out, &errb); code != 1 || !strings.Contains(errb.String(), "apply_spec") {
		t.Fatalf("custom without spec: exit=%d stderr=%s", code, errb.String())
	}
	f := filepath.Join(t.TempDir(), "d.yaml")
	if err := os.WriteFile(f, []byte(`destinations:
  - name: gl
    route: /gl/
    upstream: https://gitlab.example
    identity_in: custom
    identity_in_spec: {header: Private-Token}
    apply: custom
    apply_spec: {header: Private-Token, template: "{cred}"}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"destination", "import", "--admin-url", ts.URL, f}, getenv, &out, &errb); code != 0 {
		t.Fatalf("destination import: exit=%d stderr=%s", code, errb.String())
	}
	got := map[string]jam.Destination{}
	for _, d := range store.ListDestinations() {
		got[d.Name] = d
	}
	if got["linear"].Apply != jam.ApplyRaw || got["linear"].IdentityIn != jam.ApplyRaw {
		t.Fatalf("linear = %+v", got["linear"])
	}
	if gl := got["gl"]; gl.ApplySpec == nil || gl.ApplySpec.Header != "Private-Token" || gl.IdentityInSpec == nil {
		t.Fatalf("gl = %+v", gl)
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

// kit push reads note files relative to the kit file and refuses bad notes.
func TestKitPushNotes(t *testing.T) {
	store := jam.NewMemStore()
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "release.md"), []byte("RELEASE STEPS"), 0o600)
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	var out, errb bytes.Buffer
	good := write("good.yml", "kind: studio\nnotes:\n  - name: release.md\n    read-when: releasing\n    file: docs/release.md\n")
	if code := run([]string{"kit", "push", "--admin-url", ts.URL, "--name", "web", "--config", good}, getenv, &out, &errb); code != 0 {
		t.Fatalf("push: exit=%d stderr=%s", code, errb.String())
	}
	if cfg, _ := store.KitConfig("web", 0); !strings.Contains(cfg, "RELEASE STEPS") || strings.Contains(cfg, "docs/release.md") {
		t.Fatalf("stored kit must carry the note body, not the path: %s", cfg)
	}
	for name, body := range map[string]string{
		"reserved.yml": "kind: studio\nnotes:\n  - {name: tools.md, read-when: w, body: B}\n",
		"escape.yml":   "kind: studio\nnotes:\n  - {name: x.md, read-when: w, file: ../../etc/passwd}\n",
	} {
		errb.Reset()
		if code := run([]string{"kit", "push", "--admin-url", ts.URL, "--name", "web", "--config", write(name, body)}, getenv, &out, &errb); code == 0 {
			t.Errorf("%s: push must fail", name)
		}
	}
}

func TestRoleAddModelSpec(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "P")
	if err := store.PutModelSpec(jam.ModelSpec{Name: "opus", Type: jam.HarnessClaude, Version: "2.1.0",
		Principal: jam.ModelPrincipal{Credential: "c"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }
	var out, errb bytes.Buffer
	if code := run([]string{"role", "add", "--admin-url", ts.URL, "--project", "P", "--name", "w", "--model-spec", "opus"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add: exit=%d stderr=%s", code, errb.String())
	}
	if r, _ := store.GetRole("P", "w"); r.ModelSpec != "opus" {
		t.Fatalf("binding = %q", r.ModelSpec)
	}
	if code := run([]string{"role", "add", "--admin-url", ts.URL, "--project", "P", "--name", "u"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role add: exit=%d stderr=%s", code, errb.String())
	}
	out.Reset()
	if code := run([]string{"role", "list", "--admin-url", ts.URL, "--project", "P"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("role list: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "model-spec=opus") || !strings.Contains(out.String(), "model-spec=claude-default") {
		t.Fatalf("role list:\n%s", out.String())
	}
	errb.Reset()
	if code := run([]string{"role", "add", "--admin-url", ts.URL, "--project", "P", "--name", "w", "--model-spec", "ghost"}, getenv, &out, &errb); code == 0 ||
		!strings.Contains(errb.String(), "does not exist") {
		t.Fatalf("unknown spec: exit=%d stderr=%s", code, errb.String())
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
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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
	exec(0, "user", "add", "--login", "auth0|d", "--oidc", "https://accounts.google.com:dave-sub", "dave")
	exec(0, "project", "member", "add", "--delivery", "discord:chan-9", "acme", "dave")
	exec(0, "account", "add", "--connection", "discord", "--uid", "123456789", "--user", "dave")

	r, _ := store.GetRoster("acme")
	if len(r.Humans) != 1 || r.Humans[0].Login != "auth0|d" || len(r.Humans[0].Identity) != 1 {
		t.Fatalf("roster = %+v", r.Humans)
	}
	if d, ok := r.Humans[0].DeliveryFor("discord"); !ok || d.Address != "chan-9" || d.UserID != "123456789" {
		t.Fatalf("delivery = %+v, %v", d, ok)
	}
	show := exec(0, "user", "show", "dave")
	if !strings.Contains(show, "login=auth0|d") || !strings.Contains(show, "projects=acme") || !strings.Contains(show, "uid=123456789") {
		t.Fatalf("user show:\n%s", show)
	}
	if list := exec(0, "project", "member", "list", "acme"); !strings.Contains(list, "delivery=discord:chan-9") {
		t.Fatalf("member list:\n%s", list)
	}
	exec(0, "user", "rename", "dave", "david")
	exec(0, "user", "login", "david")
	if u := exec(0, "user", "list"); !strings.Contains(u, "user\tdavid") || strings.Contains(u, "login=") {
		t.Fatalf("user list after rename + clearing logins:\n%s", u)
	}
	if conns := exec(0, "connection", "list"); !strings.Contains(conns, "connection\tdiscord") {
		t.Fatalf("connection list:\n%s", conns)
	}
	exec(0, "connection", "add", "--kind", "linear", "--name", "linear-acme", "--cred", "lin-tok")
	exec(0, "connection", "rename", "linear-acme", "linear-main")
	exec(0, "connection", "cred", "linear-main", "lin-tok-2")
	if conns := exec(0, "connection", "list"); !strings.Contains(conns, "connection\tlinear-main") || !strings.Contains(conns, "cred=lin-tok-2") {
		t.Fatalf("connection list after add/rename/cred:\n%s", conns)
	}
	exec(0, "connection", "rm", "linear-main")
	exec(2, "connection", "add", "--kind", "linear")

	exec(2, "user", "add", "--oidc", "no-colon", "erin")
	exec(2, "user", "login", "--login", "x", "david")    // --login is add's; the set is positional
	exec(0, "project", "member", "add", "acme", "david") // no --delivery: keeps the inbox
	if list := exec(0, "project", "member", "list", "acme"); !strings.Contains(list, "delivery=discord:chan-9") {
		t.Fatalf("member add without --delivery dropped the inbox:\n%s", list)
	}
	exec(2, "project", "member", "add", "--delivery", "discord:chan:123", "acme", "david")
	exec(2, "account", "add", "--connection", "discord")
	exec(1, "user", "add", "david")
}
