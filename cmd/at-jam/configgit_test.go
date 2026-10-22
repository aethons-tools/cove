package main

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func configServer(t *testing.T) (string, *jam.MemStore) {
	t.Helper()
	st := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(st, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil, nil,
		jam.WithConfig(st, nil, jam.ConfigEnv{CredExists: creds}, log)))
	t.Cleanup(ts.Close)
	return ts.URL, st
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func gitDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	mustMkdir(t, filepath.Join(d, ".git"))
	return d
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func findUser(t *testing.T, st *jam.MemStore, name string) jam.User {
	t.Helper()
	for _, u := range st.ListUsers() {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no user %s", name)
	return jam.User{}
}

func cfgMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seedJam populates st through the exported store API.
func seedJam(t *testing.T, st *jam.MemStore) {
	t.Helper()
	creds := func(string) bool { return true }
	cfgMust(t, jam.CreateDestination(st, jam.Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", CredName: "gh-pat",
		Env: map[string]string{"GH_HOST": "{host}"}, AllowPaths: []string{"/repos/*"}}, creds))
	bob, err := st.CreateUser(jam.User{ID: ident.New(ident.User), Name: "bob", Status: jam.StatusLive, Logins: []string{"bob@acme"}})
	cfgMust(t, err)
	_, err = st.CreateUser(jam.User{ID: ident.New(ident.User), Name: "alice", Status: jam.StatusLive})
	cfgMust(t, err)
	conn, err := st.CreateConnection(jam.Connection{ID: ident.New(ident.Connection), Kind: "discord", Name: "acme", CredName: "acme-bot", Status: jam.StatusLive})
	cfgMust(t, err)
	if _, _, err := jam.PushStudioKit(st, "web-dev", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PushStudioKit(st, "app", "kind: studio\nbase:\n  kit: web-dev@v1\n"); err != nil {
		t.Fatal(err)
	}
	cfgMust(t, st.CreateProject("app"))
	p, _ := st.GetProject("app")
	cfgMust(t, st.SetEscalationPolicy("app", "", []jam.EscalationTier{{Targets: []string{"user:" + string(bob.ID)}, Timeout: time.Hour}}))
	cfgMust(t, st.PutMembership(jam.Membership{ProjectID: p.ID, UserID: bob.ID, Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "bob#1"}}}))
	_, err = st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: p.ID, Kind: jam.SourceRoom, Key: "ops", Label: "Ops", Status: jam.StatusLive,
		Bindings: []jam.Binding{{ConnectionID: conn.ID, Ref: "ACME-1", Mode: jam.BindBoth}}})
	cfgMust(t, err)
	cfgMust(t, st.SetProjectContext("app", sessionctx.Layer{Core: "goals"}, nil))
	cfgMust(t, st.PutRole("app", jam.Role{Name: "ic", Kit: "app", Scope: jam.Scope{Destinations: []string{"gh"}, TTL: time.Hour}}))
	cfgMust(t, st.SetJamContext(sessionctx.Layer{Core: "jam rules"}))
}

func TestConfigWriteThenApplyReplicates(t *testing.T) {
	srcURL, src := configServer(t)
	seedJam(t, src)
	repo := gitDir(t)
	if code, out, errs := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	first := readTree(t, repo)
	if code, _, _ := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 || !reflect.DeepEqual(readTree(t, repo), first) {
		t.Fatal("second write changed the tree")
	}
	dstURL, dst := configServer(t)
	code, out, errs := runJam("config", "diff", "--dir", repo, "--admin-url", dstURL)
	if code != 0 || !strings.Contains(out, "create  user bob") || !strings.Contains(out, "create  kit app") {
		t.Fatalf("diff: %d\n%s\n%s", code, out, errs)
	}
	if len(dst.ListUsers()) != 0 {
		t.Fatal("diff wrote")
	}
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", dstURL); code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("apply: %d\n%s\n%s", code, out, errs)
	}
	if code, out, _ := runJam("config", "apply", "--dir", repo, "--admin-url", dstURL); code != 0 || !strings.Contains(out, "nothing to apply") || strings.Contains(out, "[y/N]") {
		t.Fatalf("second apply: %d\n%s", code, out)
	}
	back := gitDir(t)
	runJam("config", "write", back, "--all", "--admin-url", dstURL)
	if !reflect.DeepEqual(readTree(t, back), first) {
		t.Fatal("the replica's tree differs from the source's")
	}
}

func TestConfigWriteRefusesOutsideGit(t *testing.T) {
	url, _ := configServer(t)
	dir := t.TempDir()
	code, _, errs := runJam("config", "write", dir, "--admin-url", url)
	if code != 1 || !strings.Contains(errs, "is not inside a git working tree (use --force to write anyway)") {
		t.Fatalf("%d %s", code, errs)
	}
	if code, _, _ := runJam("config", "write", dir, "--force", "--admin-url", url); code != 0 {
		t.Fatal("--force")
	}
}

func TestConfigWriteRemovesOnlyManagedGone(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	mustWrite(t, filepath.Join(repo, "README.md"), "mine\n", 0o644)
	runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	bob := findUser(t, st, "bob")
	if err := st.RemoveUser(bob.ID); err != nil {
		t.Fatal(err)
	}
	runJam("config", "write", repo, "--admin-url", url)
	if _, err := os.Stat(filepath.Join(repo, "users/bob.yml")); !os.IsNotExist(err) {
		t.Fatal("managed file of a removed user kept")
	}
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Fatal("unrelated file removed")
	}
}

func TestConfigApplyPromptsAndRefuses(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	configStdin = strings.NewReader("n\n")
	t.Cleanup(func() { configStdin = os.Stdin })
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--admin-url", url); code != 1 || !strings.Contains(out, "Apply 1 change? [y/N]") || !strings.Contains(errs, "aborted") || len(st.ListUsers()) != 0 {
		t.Fatalf("declined apply: %d %s", code, out)
	}
	mustWrite(t, filepath.Join(repo, "projects/app/roles/ic.yml"), "kind: role\nversion: 1\nkit: nope\n", 0o644)
	code, out, _ := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	if code != 1 || !strings.Contains(out, "refused:\n  projects/app/roles/ic.yml: ") {
		t.Fatalf("refusals: %d\n%s", code, out)
	}
}

func TestConfigDiffRejectsForce(t *testing.T) {
	if code, _, _ := runJam("config", "diff", "--dir", t.TempDir(), "--force"); code != 2 {
		t.Fatalf("diff --force: %d, want 2", code)
	}
}

func TestConfigNeedsDir(t *testing.T) {
	if code, _, errs := runJam("config", "diff"); code != 2 || !strings.Contains(errs, "--dir is required (git sources are not supported yet)") {
		t.Fatalf("%d %s", code, errs)
	}
}

func TestConfigApplyForceWritesOverDrift(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url); code != 0 {
		t.Fatalf("apply: %d %s %s", code, out, errs)
	}
	cfgMust(t, st.SetUserLogins(findUser(t, st, "bob").ID, []string{"bob@jam"}))
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\nlogins: [bob@repo]\n", 0o644)
	code, out, _ := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	if code != 1 || !strings.Contains(out, "user bob drifted") {
		t.Fatalf("apply without --force: %d\n%s", code, out)
	}
	code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--force", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "update  user bob  (drift)") || !strings.Contains(out, "1 updated") {
		t.Fatalf("apply --force: %d\n%s\n%s", code, out, errs)
	}
	if got := findUser(t, st, "bob").Logins; !reflect.DeepEqual(got, []string{"bob@repo"}) {
		t.Fatalf("logins %v", got)
	}
}

// A mark whose object was removed in Jam is a visible "forget" item that
// apply removes, so a later unmanaged object of that name stays untouched.
func TestConfigApplyForgetsStaleMark(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	cfgMust(t, st.RemoveUser(findUser(t, st, "bob").ID))
	cfgMust(t, os.Remove(filepath.Join(repo, "users/bob.yml")))
	code, out, errs := runJam("config", "diff", "--dir", repo, "--admin-url", url)
	if code != 0 || !strings.Contains(out, "forget  user bob  (mark only)") {
		t.Fatalf("diff: %d\n%s\n%s", code, out, errs)
	}
	code, out, errs = runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "1 forgotten") {
		t.Fatalf("apply: %d\n%s\n%s", code, out, errs)
	}
	if len(st.ListConfigMarks()) != 0 {
		t.Fatalf("marks kept: %v", st.ListConfigMarks())
	}
	_, err := st.CreateUser(jam.User{ID: ident.New(ident.User), Name: "bob", Status: jam.StatusLive})
	cfgMust(t, err)
	code, out, _ = runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	if code != 0 || strings.Contains(out, "delete") || !strings.Contains(out, "unmanaged: user bob") {
		t.Fatalf("re-apply: %d\n%s", code, out)
	}
	findUser(t, st, "bob")
}

func TestConfigWriteRefusesBehindPin(t *testing.T) {
	url, st := configServer(t)
	seedJam(t, st)
	if _, _, err := jam.PushStudioKit(st, "web-dev", "kind: studio\nprompt: v2\n"); err != nil {
		t.Fatal(err)
	}
	repo := gitDir(t)
	code, _, errs := runJam("config", "write", repo, "--all", "--admin-url", url)
	if code != 1 || !strings.Contains(errs, "kit app pins web-dev@v1 but web-dev is at v2; versions are per-Jam — re-push app against v2 (or pin web-dev to v1) before config write") {
		t.Fatalf("%d %s", code, errs)
	}
	if len(readTree(t, repo)) != 0 {
		t.Fatal("a refused write wrote files")
	}
}
