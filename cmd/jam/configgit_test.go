package main

import (
	"context"
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
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/kittree"
)

func configServer(t *testing.T) (string, *jam.MemStore) {
	t.Helper()
	st := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := func(string) bool { return true }
	ts := httptest.NewServer(jam.NewAdminHandler(st, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil,
		jam.WithConfig(st, nil, jam.ConfigEnv{CredExists: creds}, log)))
	t.Cleanup(ts.Close)
	return ts.URL, st
}

// applyDir applies the config checkout dir to st as a --dir apply did,
// seeding state for tests of the kept verbs.
func applyDir(t *testing.T, st *jam.MemStore, dir string) jam.ConfigApplyResult {
	t.Helper()
	tr, err := jam.LoadConfigTree(dir)
	cfgMust(t, err)
	res, err := jam.ApplyConfigTree(context.Background(), st, nil, tr, jam.ConfigEnv{CredExists: func(string) bool { return true }},
		jam.ConfigApplyOptions{Source: gitsource.Source{Repo: jam.ConfigDirRepo}, Commit: jam.ConfigDirCommit, Now: time.Unix(1, 0)}, nil, "test")
	cfgMust(t, err)
	return res
}

// planIsEmpty reports whether a printed plan has no create, update, delete
// or forget item.
func planIsEmpty(out string) bool {
	for _, verb := range []string{"create  ", "update  ", "delete  ", "forget  "} {
		if strings.Contains("\n"+out, "\n"+verb) {
			return false
		}
	}
	return true
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

func TestConfigWriteThenDiffReplicates(t *testing.T) {
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
	if !applyDir(t, dst, repo).Applied {
		t.Fatal("seed apply wrote nothing")
	}
	if code, out, errs := runJam("config", "diff", "--dir", repo, "--admin-url", dstURL); code != 0 || !planIsEmpty(out) {
		t.Fatalf("diff after the apply: %d\n%s\n%s", code, out, errs)
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
	applyDir(t, st, repo)
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

func TestConfigSyncPromptsAndRefuses(t *testing.T) {
	cfgURL, _, update := gittest.Serve(t, map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\n"})
	followed, err := gitsource.Parse(cfgURL + "//jam@main")
	cfgMust(t, err)
	url, st := configGitServer(t, followed)
	configStdin = strings.NewReader("n\n")
	t.Cleanup(func() { configStdin = os.Stdin })
	if code, out, errs := runJam("config", "sync", "--admin-url", url); code != 1 || !strings.Contains(out, "Apply 1 change? [y/N]") || !strings.Contains(errs, "aborted") || len(st.ListUsers()) != 0 {
		t.Fatalf("declined sync: %d %s", code, out)
	}
	update(map[string]string{"jam/projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: nope\n"})
	code, out, _ := runJam("config", "sync", "--yes", "--admin-url", url)
	if code != 1 || !strings.Contains(out, "refused:\n  projects/app/roles/ic.yml: ") {
		t.Fatalf("refusals: %d\n%s", code, out)
	}
}

func TestConfigRejectsForce(t *testing.T) {
	for _, sub := range []string{"diff", "sync"} {
		if code, _, _ := runJam("config", sub, "--force"); code != 2 {
			t.Errorf("%s --force: %d, want 2", sub, code)
		}
	}
}

// configGitServer is configServer with a real, anonymous kit fetcher,
// following followed (zero = no config repo).
func configGitServer(t *testing.T, followed gitsource.Source) (string, *jam.MemStore) {
	t.Helper()
	st := jam.NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	creds := func(string) bool { return true }
	f := &gitsource.Fetcher{}
	env := jam.ConfigEnv{CredExists: creds, Followed: followed}
	env.Follower = &jam.ConfigFollower{Store: st, Fetcher: f, Env: env}
	ts := httptest.NewServer(jam.NewAdminHandler(st, nil, nil, jam.LoopbackAuthenticator{}, creds, nil, log, nil,
		jam.WithConfig(st, f, env, log)))
	t.Cleanup(ts.Close)
	return ts.URL, st
}

func TestConfigFollowSyncStatus(t *testing.T) {
	kitsURL, _, _ := gittest.Serve(t, map[string]string{"api/kit.yml": "kind: studio\nprompt: api\n"})
	cfgURL, first, update := gittest.Serve(t, map[string]string{
		"jam/users/bob.yml":       "kind: user\nversion: 1\n",
		"jam/kits/web/kit.yml":    "kind: studio\n",
		"jam/kits/api.source.yml": "kind: kit-source\nversion: 1\ngit: " + kitsURL + "//api@main\n",
	})
	src := cfgURL + "//jam@main"
	followed, err := gitsource.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	url, st := configGitServer(t, followed)
	if code, out, errs := runJam("config", "status", "--admin-url", url); code != 0 || !strings.Contains(out, "state: off") {
		t.Fatalf("status before a tick: %d\n%s\n%s", code, out, errs)
	}
	code, out, errs := runJam("config", "diff", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "create  user bob") ||
		!strings.Contains(out, "kit-source  kits/api.source.yml  "+kitsURL+"//api@main @ ") {
		t.Fatalf("diff: %d\n%s\n%s", code, out, errs)
	}
	if len(st.ListUsers()) != 0 {
		t.Fatal("diff wrote")
	}
	code, out, errs = runJam("config", "sync", "--yes", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "config "+src+" @ "+first[:7]) || !strings.Contains(out, "applied: 3 created") {
		t.Fatalf("sync: %d\n%s\n%s", code, out, errs)
	}
	if got, _ := st.GetConfigSource(); got.Commit != first {
		t.Fatalf("record %+v", got)
	}
	code, out, errs = runJam("config", "status", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "state: in-sync") || !strings.Contains(out, "applied: "+first[:7]) || !strings.Contains(out, "source: "+src) {
		t.Fatalf("status: %d\n%s\n%s", code, out, errs)
	}
	if k, _ := st.GetKit("api"); k.Source == nil || k.Source.Repo != kitsURL || k.Source.Dir != "api" {
		t.Fatalf("api tracked %+v", k.Source)
	}
	// A README-only commit: no changes, yet sync still records the commit.
	readme := update(map[string]string{"README.md": "hello\n"})
	code, out, errs = runJam("config", "sync", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "nothing to apply") || strings.Contains(out, "Apply ") {
		t.Fatalf("readme sync: %d\n%s\n%s", code, out, errs)
	}
	if got, _ := st.GetConfigSource(); got.Commit != readme {
		t.Fatalf("record after readme %+v", got)
	}
	for _, verb := range []string{"track", "untrack"} {
		if code, _, _ := runJam("config", verb, "--admin-url", url); code != 2 {
			t.Errorf("config %s: exit %d, want 2 (removed)", verb, code)
		}
	}
	none, _ := configGitServer(t, gitsource.Source{})
	if code, _, errs := runJam("config", "sync", "--yes", "--admin-url", none); code != 1 || !strings.Contains(errs, "no config repo is configured") {
		t.Fatalf("sync without a followed repo: %d %s", code, errs)
	}
}

func TestConfigDiffWithoutTrackedSource(t *testing.T) {
	url, _ := configServer(t)
	if code, _, errs := runJam("config", "diff", "--admin-url", url); code != 1 || !strings.Contains(errs, "no config repo is configured") {
		t.Fatalf("%d %s", code, errs)
	}
}

// config apply is gone: the config changes only through git, by sync.
func TestConfigApplyGone(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	code, _, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url)
	if code != 2 || !strings.Contains(errs, "jam config: expected write <dir>") || strings.Contains(errs, "apply") || len(st.ListUsers()) != 0 {
		t.Fatalf("config apply: %d %s", code, errs)
	}
	if code, out, errs := runJam("config", "diff", "--dir", repo, "--admin-url", url); code != 0 || !strings.Contains(out, "create  user bob") || len(st.ListUsers()) != 0 {
		t.Fatalf("config diff --dir: %d %s %s", code, out, errs)
	}
}

func TestConfigVerbFlags(t *testing.T) {
	for _, args := range [][]string{
		{"config", "diff", "--dir", "x", "--git", "https://github.com/a/b@main"},
		{"config", "track"},
		{"config", "sync", "--git", "https://github.com/a/b@main"},
		{"config", "status", "extra"},
	} {
		if code, _, _ := runJam(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// The repo wins over an edit made in Jam since the last sync.
func TestConfigSyncWritesOverJamEdit(t *testing.T) {
	cfgURL, _, update := gittest.Serve(t, map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\n"})
	followed, err := gitsource.Parse(cfgURL + "//jam@main")
	cfgMust(t, err)
	url, st := configGitServer(t, followed)
	if code, out, errs := runJam("config", "sync", "--yes", "--admin-url", url); code != 0 {
		t.Fatalf("sync: %d %s %s", code, out, errs)
	}
	cfgMust(t, st.SetUserLogins(findUser(t, st, "bob").ID, []string{"bob@jam"}))
	update(map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@repo]\n"})
	code, out, errs := runJam("config", "sync", "--yes", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "update  user bob\n") || !strings.Contains(out, "1 updated") {
		t.Fatalf("sync: %d\n%s\n%s", code, out, errs)
	}
	if got := findUser(t, st, "bob").Logins; !reflect.DeepEqual(got, []string{"bob@repo"}) {
		t.Fatalf("logins %v", got)
	}
}

// A mark whose object was removed in Jam is a visible "forget" item in the
// plan of the repo without it; applying forgets it and leaves a later object
// of that name alone.
func TestConfigStaleMarkForgottenOnApply(t *testing.T) {
	url, st := configServer(t)
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "users/bob.yml"), "kind: user\nversion: 1\n", 0o644)
	applyDir(t, st, repo)
	cfgMust(t, st.RemoveUser(findUser(t, st, "bob").ID))
	cfgMust(t, os.Remove(filepath.Join(repo, "users/bob.yml")))
	code, out, errs := runJam("config", "diff", "--dir", repo, "--admin-url", url)
	if code != 0 || !strings.Contains(out, "forget  user bob  (mark only)") {
		t.Fatalf("diff: %d\n%s\n%s", code, out, errs)
	}
	// Applying it removes the mark only; a bob created later is unmanaged
	// and the next apply leaves it alone.
	if res := applyDir(t, st, repo); res.Counts[jam.ConfigForget] != 1 {
		t.Fatalf("apply counts %v, want 1 forgotten", res.Counts)
	}
	if len(st.ListConfigMarks()) != 0 {
		t.Fatalf("marks kept: %v", st.ListConfigMarks())
	}
	_, err := st.CreateUser(jam.User{ID: ident.New(ident.User), Name: "bob", Status: jam.StatusLive})
	cfgMust(t, err)
	code, out, errs = runJam("config", "diff", "--dir", repo, "--admin-url", url)
	if code != 0 || strings.Contains(out, "delete") || !strings.Contains(out, "unmanaged: user bob") {
		t.Fatalf("re-plan: %d\n%s\n%s", code, out, errs)
	}
	applyDir(t, st, repo)
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

// config write captures a Jam-side edit; the following sync has nothing to
// change but still reaches the server, whose metadata-only write refreshes
// the stale mark — so the next real repo change is not refused as drift
// (issue 010), and there is no prompt.
func TestConfigWriteThenSyncRefreshesMark(t *testing.T) {
	cfgURL, _, update := gittest.Serve(t, map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\n"})
	followed, err := gitsource.Parse(cfgURL + "//jam@main")
	cfgMust(t, err)
	url, st := configGitServer(t, followed)
	if code, out, errs := runJam("config", "sync", "--yes", "--admin-url", url); code != 0 {
		t.Fatalf("sync: %d %s %s", code, out, errs)
	}
	cfgMust(t, st.SetUserLogins(findUser(t, st, "bob").ID, []string{"bob@jam"}))
	repo := gitDir(t)
	if code, out, errs := runJam("config", "write", repo, "--admin-url", url); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	// The repo carries Jam's edit now: nothing to change, so no prompt.
	update(map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@jam]\n"})
	code, out, errs := runJam("config", "sync", "--admin-url", url)
	if code != 0 || !strings.Contains(out, "nothing to apply") || strings.Contains(out, "[y/N]") {
		t.Fatalf("zero-change sync: %d\n%s\n%s", code, out, errs)
	}
	update(map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@repo]\n"})
	code, out, errs = runJam("config", "sync", "--yes", "--admin-url", url)
	if code != 0 || strings.Contains(out, "drift") || !strings.Contains(out, "1 updated") {
		t.Fatalf("next change: %d\n%s\n%s", code, out, errs)
	}
}

// An update prints Jam's and the repo's canonical forms as a compact line
// diff (what the apply overwrites); a delete prints Jam's side.
func TestPrintConfigPlanUpdateAndDeleteDiff(t *testing.T) {
	before := "# users/bob.yml\nkind: user\nversion: 1\nlogins:\n- a@x\n"
	after := "# users/bob.yml\nkind: user\nversion: 1\nlogins:\n- b@x\n"
	var b strings.Builder
	printConfigPlan(&b, jam.ConfigPlan{Ops: []jam.ConfigOp{{Kind: jam.ConfigKindUser, Name: "bob", Action: jam.ConfigUpdate, Before: before, After: after}}})
	want := "update  user bob\n  --- jam\n  +++ repo\n  … 1 unchanged line(s)\n   kind: user\n   version: 1\n   logins:\n  -- a@x\n  +- b@x\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Errorf("update: got %q\nwant prefix %q", b.String(), want)
	}
	b.Reset()
	printConfigPlan(&b, jam.ConfigPlan{Ops: []jam.ConfigOp{{Kind: jam.ConfigKindUser, Name: "bob", Action: jam.ConfigDelete, Before: "# users/bob.yml\nkind: user\n"}}})
	want = "delete  user bob\n  jam:\n    # users/bob.yml\n    kind: user\n  repo:\n    (not in the repo)\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Errorf("delete: got %q\nwant prefix %q", b.String(), want)
	}
	b.Reset()
	printConfigPlan(&b, jam.ConfigPlan{Ops: []jam.ConfigOp{{Kind: jam.ConfigKindUser, Name: "bob", Action: jam.ConfigCreate, After: after}}})
	if !strings.HasPrefix(b.String(), "create  user bob\n0 unchanged\n") {
		t.Errorf("create: got %q", b.String())
	}
	// A kit's canonical form is its kit.yml and file listing: the diff points
	// at the changed line.
	b.Reset()
	printConfigPlan(&b, jam.ConfigPlan{Ops: []jam.ConfigOp{{Kind: jam.ConfigKindKit, Name: "ic", Action: jam.ConfigUpdate,
		Before: "kind: studio\nprompt: a\n# files:\n#   0644 25 aa kit.yml\n# source: https://x/k//ic@main\n",
		After:  "kind: studio\nprompt: b\n# files:\n#   0644 25 bb kit.yml\n# source: https://x/k//ic@main\n"}}})
	if !strings.Contains(b.String(), "  -prompt: a\n  +prompt: b\n") || !strings.Contains(b.String(), "  -#   0644 25 aa kit.yml\n  +#   0644 25 bb kit.yml\n") {
		t.Errorf("kit diff %q", b.String())
	}
}

// A kit update prints its version diff under its line, escaped for the
// terminal, in place of the listing diff.
func TestPrintConfigPlanKitVersionDiff(t *testing.T) {
	var b strings.Builder
	printConfigPlan(&b, jam.ConfigPlan{Ops: []jam.ConfigOp{{Kind: jam.ConfigKindKit, Name: "app", Action: jam.ConfigUpdate,
		Before: "kind: studio\nprompt: a\n", After: "kind: studio\nprompt: b\n",
		Diff: "kit.yml:\n  -prompt: a\n  +prompt: b\nfiles: 1 changed, 0 unchanged\n  mode      run.sh  0644 → 0755\x1b[2J\n"}}})
	want := "update  kit app\n    kit.yml:\n      -prompt: a\n      +prompt: b\n    files: 1 changed, 0 unchanged\n      mode      run.sh  0644 → 0755\\x1b[2J\n0 unchanged\n"
	if !strings.HasPrefix(b.String(), want) {
		t.Errorf("got %q\nwant prefix %q", b.String(), want)
	}
}

// planKitDir plans a config dir holding kits/app/kit.yml = kitYML against
// store as an apply of acme/jam@main.
func planKitDir(t *testing.T, store jam.Store, kitYML string) jam.ConfigPlan {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "kits", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kits", "app", "kit.yml"), []byte(kitYML), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := jam.LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	return jam.PlanConfig(jam.ConfigPlanInput{Tree: tr, Snapshot: store.ExportConfig(), Marks: store.ListConfigMarks(),
		Env: jam.ConfigEnv{CredExists: func(string) bool { return true }}, Source: gitsource.Source{Repo: "https://github.com/acme/jam", Ref: "main"},
		Commit: "c2", Now: time.Unix(2, 0)})
}

// A kit whose tree is unchanged but whose source differs (tracked in Jam,
// inline in the repo) shows the `# source:` line diff, not "no changes".
func TestPrintConfigPlanKitSourceOnlyChange(t *testing.T) {
	store := jam.NewMemStore()
	if _, err := store.PushKit("app", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	k, _ := store.GetKit("app")
	k.Source = &jam.KitSource{Repo: "https://github.com/acme/kits", Dir: "app", Ref: "main", Commit: "k1"}
	if err := store.PutKitDocs([]jam.Kit{k}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	printConfigPlan(&b, planKitDir(t, store, "kind: studio\n"))
	if !strings.Contains(b.String(), "update  kit app\n") || !strings.Contains(b.String(), "  -# source: ") || strings.Contains(b.String(), "no changes") {
		t.Errorf("source-only change: %q", b.String())
	}
}

// A kit edited in Jam since its apply prints its version diff (Jam's current
// tree → the repo's) under its update line.
func TestPrintConfigPlanEditedKitVersionDiff(t *testing.T) {
	store := jam.NewMemStore()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "kits", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kits", "app", "kit.yml"), []byte("kind: studio\nprompt: a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := jam.LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := jam.ApplyConfigTree(context.Background(), store, nil, tr, jam.ConfigEnv{CredExists: func(string) bool { return true }},
		jam.ConfigApplyOptions{Source: gitsource.Source{Repo: "https://github.com/acme/jam", Ref: "main"}, Commit: "c1", Now: time.Unix(1, 0)}, nil, "test"); err != nil || !res.Applied {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if _, _, err := jam.PushKitFiles(store, "app", []kittree.File{{Path: "kit.yml", Data: []byte("kind: studio\nprompt: jam\n"), Mode: 0o644}}); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	printConfigPlan(&b, planKitDir(t, store, "kind: studio\nprompt: repo\n"))
	if !strings.Contains(b.String(), "update  kit app\n    kit.yml:\n       kind: studio\n      -prompt: jam\n      +prompt: repo\n") {
		t.Errorf("edited kit: %q", b.String())
	}
}
