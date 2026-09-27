package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestEnrollCommandJSON(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
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
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.PutRole("ACME", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"}}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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

func TestProjectRosterCommands(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// project roster add-human
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "roster", "add-human", "--admin-url", ts.URL, "acme",
		"--name", "alice", "--handle", "alice.h", "--login", "auth0|abc",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project roster add-human: exit=%d stderr=%s", code, errb.String())
	}
	if hu, ok := jam.HumanByLogin(store, "acme", "auth0|abc"); !ok || hu.Name != "alice" {
		t.Fatalf("add-human --login did not link alice: %+v,%v", hu, ok)
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
	if !strings.Contains(out.String(), "human\talice\thandle=alice.h\tlogin=auth0|abc") || !strings.Contains(out.String(), "channel\teng-help\tservice=linear\tref=ACME-1") {
		t.Fatalf("project roster list output missing expected fields:\n%s", out.String())
	}

	// project roster rm-human
	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "roster", "rm-human", "--admin-url", ts.URL, "acme", "alice"}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project roster rm-human: exit=%d stderr=%s", code, errb.String())
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
// end-to-end through httptest.Server + FileStore, including the --tier
// 'targets@timeout' parse and its missing-'@' error.
func TestProjectEscalationCommands(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
// set|show|clear` end-to-end through httptest.Server + FileStore.
func TestProjectChatServiceCommands(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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

// TestProjectRosterAddHumanDelivery exercises `project roster add-human
// --delivery service:address` (repeatable) end-to-end, including its
// malformed-input errors.
func TestProjectRosterAddHumanDelivery(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()
	getenv := func(string) string { return "" }

	var out, errb bytes.Buffer

	// valid --delivery reaches the roster
	out.Reset()
	errb.Reset()
	if code := run([]string{
		"project", "roster", "add-human", "--admin-url", ts.URL, "acme",
		"--name", "dave", "--handle", "dave.h",
		"--delivery", "discord:chan-9",
	}, getenv, &out, &errb); code != 0 {
		t.Fatalf("project roster add-human --delivery: exit=%d stderr=%s", code, errb.String())
	}
	rr, ok := store.GetRoster("acme")
	if !ok {
		t.Fatal("GetRoster acme")
	}
	var dave jam.Human
	for _, hu := range rr.Humans {
		if hu.Name == "dave" {
			dave = hu
		}
	}
	if d, ok := dave.DeliveryFor("discord"); !ok || d.Address != "chan-9" {
		t.Fatalf("dave delivery = %+v, ok=%v", d, ok)
	}

	// malformed --delivery values are rejected with exit 2, roster unchanged
	for _, bad := range []string{"discord:", ":x", "x"} {
		out.Reset()
		errb.Reset()
		code := run([]string{
			"project", "roster", "add-human", "--admin-url", ts.URL, "acme",
			"--name", "eve", "--handle", "eve.h",
			"--delivery", bad,
		}, getenv, &out, &errb)
		if code != 2 {
			t.Fatalf("project roster add-human --delivery %q: exit=%d, want 2 (stderr=%s)", bad, code, errb.String())
		}
	}
	rr, ok = store.GetRoster("acme")
	if !ok {
		t.Fatal("GetRoster acme")
	}
	for _, hu := range rr.Humans {
		if hu.Name == "eve" {
			t.Fatalf("eve should not have been added with a malformed --delivery: %+v", hu)
		}
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
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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

// TestCoveCommandsRoundTrip exercises the `cove` verb group (raise|list|status|
// teardown) end to end through the CLI entrypoint. Unlike the other CLI round
// trips, cove's mutation routes need a live Supervisor (a nil sup 503s), so
// this test wires one with a fake Launcher. It also pins down the "never
// print the identity token" constraint on `cove raise`.
func TestCoveCommandsRoundTrip(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
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

// `standing add|list|rm` declare, list and dismiss a role's standing sessions;
// the prompt is read from a file host-side, and the role's other fields are kept.
func TestStandingCommandsRoundTrip(t *testing.T) {
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err := store.PutRole("acme", jam.Role{Name: "reviewer", Scope: jam.Scope{Destinations: []string{"git"}, TTL: time.Hour}, Allocation: jam.RoleAllocation{MaxEphemeral: 2}}); err != nil {
		t.Fatal(err)
	}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
	store, _ := jam.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
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
