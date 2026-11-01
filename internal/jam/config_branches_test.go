package jam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/configrepo"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

var branchFiles = map[string]string{
	"jam/users/bob.yml":              "kind: user\nversion: 1\n",
	"jam/kits/web/kit.yml":           "kind: studio\n",
	"jam/kits/web/.dockerignore":     "node_modules\n",
	"jam/kits/web/node_modules/x.js": "ignored\n",
	"README.md":                      "not config\n",
}

var opAlice = configrepo.Signature{Name: "alice", Email: "alice@jam"}

// branchesFor serves branchFiles and a ConfigBranches over it, with branch
// jam/alice made at main.
func branchesFor(t *testing.T) (*ConfigBranches, string, func(map[string]string) string) {
	t.Helper()
	url, _, update := gittest.Serve(t, branchFiles, gittest.WithPush())
	followed := gitsource.Source{Repo: url, Dir: "jam", Ref: "main"}
	repo := &configrepo.Repo{URL: url, Branch: "main", Path: filepath.Join(t.TempDir(), "config-repo")}
	env := allCreds
	env.Followed = followed
	env.Serve = map[string]any{"display-name": "Live"} // never written to a branch
	b := &ConfigBranches{Repo: repo, Dir: "jam", Env: env, Live: NewMemStore()}
	if _, err := repo.CreateBranch(context.Background(), "jam/alice"); err != nil {
		t.Fatal(err)
	}
	return b, url, update
}

// hasUser reports whether st has a user named name.
func hasUser(st Store, name string) bool {
	_, ok := st.LookupName(ident.User, name)
	return ok
}

// handPush commits files to branch as a person with git would.
func handPush(t *testing.T, b *ConfigBranches, branch string, files map[string]string) {
	t.Helper()
	head, _, err := b.Repo.Files(context.Background(), branch)
	if err != nil {
		t.Fatal(err)
	}
	put := map[string]configrepo.File{}
	for p, c := range files {
		put[p] = configrepo.File{Data: []byte(c), Mode: 0o100644}
	}
	if _, err := b.Repo.Commit(context.Background(), branch, head, configrepo.Changes{Put: put}, configrepo.Signature{Name: "bob", Email: "bob@x"}, "by hand"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLoadsAndCaches(t *testing.T) {
	b, _, _ := branchesFor(t)
	ctx := context.Background()
	ws, err := b.Workspace(ctx, "jam/alice")
	if err != nil {
		t.Fatal(err)
	}
	if ok := hasUser(ws.Store, "bob"); !ok {
		t.Fatalf("bob not loaded: %v", ws.Store.ListUsers())
	}
	if again, _ := b.Workspace(ctx, "jam/alice"); again != ws {
		t.Fatal("not cached")
	}
	handPush(t, b, "jam/alice", map[string]string{"jam/users/dan.yml": "kind: user\nversion: 1\n"})
	fresh, err := b.Workspace(ctx, "jam/alice")
	if err != nil || fresh == ws {
		t.Fatalf("not rebuilt after a push: %v", err)
	}
	if ok := hasUser(fresh.Store, "dan"); !ok {
		t.Fatal("dan not loaded")
	}
	if _, err := b.Workspace(ctx, "main"); !errors.Is(err, configrepo.ErrFollowedBranch) {
		t.Fatalf("main = %v", err)
	}
}

func TestSaveCommitsTheDifference(t *testing.T) {
	b, url, _ := branchesFor(t)
	ctx := context.Background()
	ws, err := b.Workspace(ctx, "jam/alice")
	if err != nil {
		t.Fatal(err)
	}
	before := ws.Head
	// no-op save: nothing changed, no commit.
	if err := b.Save(ctx, ws, opAlice, "jam: nothing"); err != nil || ws.Head != before || gittest.Head(t, url, "jam/alice") != before {
		t.Fatalf("no-op save: %v head %s", err, ws.Head)
	}
	if _, err := ws.Store.CreateUser(User{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, ws, opAlice, "jam: POST users"); err != nil {
		t.Fatal(err)
	}
	if ws.Head == before || gittest.Head(t, url, "jam/alice") != ws.Head {
		t.Fatalf("head %s", ws.Head)
	}
	if got, ok := gittest.Show(t, url, "jam/alice", "jam/users/carol.yml"); !ok || !strings.Contains(got, "kind: user") {
		t.Fatalf("carol = %q %v", got, ok)
	}
	// dockerignored survives; files outside the config dir are untouched.
	if _, ok := gittest.Show(t, url, "jam/alice", "jam/kits/web/node_modules/x.js"); !ok {
		t.Fatal("a dockerignored file was removed")
	}
	if _, ok := gittest.Show(t, url, "jam/alice", "jam/jam.yml"); ok {
		t.Fatal("a save wrote jam.yml")
	}
	if got, _ := gittest.Show(t, url, "jam/alice", "README.md"); got != "not config\n" {
		t.Fatal("README touched")
	}
	if a := strings.TrimSpace(gittest.Git(t, url, "log", "-1", "--format=%an|%s", "jam/alice")); a != "alice|jam: POST users" {
		t.Fatalf("log %q", a)
	}
	if gittest.Head(t, url, "main") == ws.Head {
		t.Fatal("main moved")
	}
}

func TestSaveStale(t *testing.T) {
	b, _, _ := branchesFor(t)
	ctx := context.Background()
	ws, _ := b.Workspace(ctx, "jam/alice")
	handPush(t, b, "jam/alice", map[string]string{"jam/users/dan.yml": "kind: user\nversion: 1\n"})
	if _, err := ws.Store.CreateUser(User{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	err := b.Save(ctx, ws, opAlice, "jam: POST users")
	if status, msg := ConfigStatus(err); !errors.Is(err, configrepo.ErrStale) || status != http.StatusConflict || !strings.Contains(msg, "reload") {
		t.Fatalf("stale: %v %d %q", err, status, msg)
	}
	fresh, _ := b.Workspace(ctx, "jam/alice")
	if fresh == ws {
		t.Fatal("the stale workspace was kept")
	}
	if ok := hasUser(fresh.Store, "carol"); ok {
		t.Fatal("the refused edit survived")
	}
}

func TestBrokenBranchDoesNotLoad(t *testing.T) {
	b, _, _ := branchesFor(t)
	handPush(t, b, "jam/alice", map[string]string{"jam/users/eve.yml": "kind: user\nversion: 9\n"})
	_, err := b.Workspace(context.Background(), "jam/alice")
	if status, msg := ConfigStatus(err); status != http.StatusConflict || !strings.Contains(msg, "jam/alice") || !strings.Contains(msg, "eve") {
		t.Fatalf("broken: %d %q", status, msg)
	}
}

func TestListAndCreate(t *testing.T) {
	b, url, _ := branchesFor(t)
	ctx := context.Background()
	got, err := b.Create(ctx, DefaultBranchName("Bob@Example"))
	if err != nil || got.Name != "jam/bob-example" || got.Head != gittest.Head(t, url, "main") {
		t.Fatalf("create = %+v %v", got, err)
	}
	list, err := b.List(ctx)
	if err != nil || len(list) != 3 || list[0].Name != "main" || !list[0].Followed || list[1].Name != "jam/alice" || list[2].Name != "jam/bob-example" {
		t.Fatalf("list = %+v %v", list, err)
	}
	if _, err := b.Create(ctx, "jam/alice"); !errors.Is(err, configrepo.ErrBranchExists) {
		t.Fatalf("again = %v", err)
	}
}

func TestDefaultBranchName(t *testing.T) {
	for in, want := range map[string]string{"alice": "jam/alice", "Alice@Example": "jam/alice-example", "op 1/x": "jam/op-1-x", "": "jam/operator"} {
		if got := DefaultBranchName(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

func TestMergeAfterSave(t *testing.T) {
	b, url, _ := branchesFor(t)
	ctx := context.Background()
	ws, _ := b.Workspace(ctx, "jam/alice")
	if _, err := ws.Store.CreateUser(User{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, ws, opAlice, "jam: POST users"); err != nil {
		t.Fatal(err)
	}
	res, err := b.Merge(ctx, "jam/alice", ws.Head, opAlice)
	if err != nil || !res.FastForward || gittest.Head(t, url, "main") != ws.Head {
		t.Fatalf("merge = %+v %v", res, err)
	}
}

func TestMergeRefusedByThePlan(t *testing.T) {
	b, url, _ := branchesFor(t)
	ctx := context.Background()
	handPush(t, b, "jam/alice", map[string]string{"jam/projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: missing\n"})
	head := gittest.Head(t, url, "jam/alice")
	main := gittest.Head(t, url, "main")
	_, err := b.Merge(ctx, "jam/alice", head, opAlice)
	if !errors.Is(err, ErrConfigRefused) || gittest.Head(t, url, "main") != main {
		t.Fatalf("merge = %v", err)
	}
}

func TestMergeConflict(t *testing.T) {
	b, url, update := branchesFor(t)
	ctx := context.Background()
	handPush(t, b, "jam/alice", map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@a]\n"})
	update(map[string]string{"jam/users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@b]\n"})
	_, err := b.Merge(ctx, "jam/alice", gittest.Head(t, url, "jam/alice"), opAlice)
	if status, msg := ConfigStatus(err); status != http.StatusConflict || !strings.Contains(msg, "jam/users/bob.yml") || !strings.Contains(msg, "pull request") {
		t.Fatalf("conflict = %d %q", status, msg)
	}
}

func TestPRs(t *testing.T) {
	b, _, _ := branchesFor(t)
	ctx := context.Background()
	if _, err := b.PR(ctx, "jam/alice"); err == nil {
		t.Fatal("a PR without GitHub")
	} else if status, _ := ConfigStatus(err); status != http.StatusNotImplemented {
		t.Fatalf("no GitHub = %d", status)
	}
	var title string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			title = body["title"]
			w.WriteHeader(201)
		}
		_, _ = w.Write([]byte(`{"number": 1, "html_url": "https://github.com/acme/jam/pull/1"}`))
	}))
	defer ts.Close()
	b.PRs = configrepo.GitHub{URL: "https://github.com/acme/jam", APIBase: ts.URL, Token: func() (string, error) { return "t", nil }}
	pr, err := b.PR(ctx, "jam/alice")
	if err != nil || pr.URL != "https://github.com/acme/jam/pull/1" || title != "Jam config: jam/alice" {
		t.Fatalf("pr = %+v %v title %q", pr, err, title)
	}
}

// An object created and then deleted in one cached workspace leaves the
// branch: deletes are found against the last save, not only the load.
func TestSaveDeletesWhatTheWorkspaceCreated(t *testing.T) {
	b, url, _ := branchesFor(t)
	ctx := context.Background()
	ws, _ := b.Workspace(ctx, "jam/alice")
	u, err := ws.Store.CreateUser(User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, ws, opAlice, "add"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Store.RemoveUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, ws, opAlice, "remove"); err != nil {
		t.Fatal(err)
	}
	if _, ok := gittest.Show(t, url, "jam/alice", "jam/users/carol.yml"); ok {
		t.Fatal("carol is still on the branch")
	}
}

// A save commits only the files whose rendering changed: a hand-edited
// file the save didn't touch keeps its comments.
func TestSaveKeepsUntouchedHandEdits(t *testing.T) {
	b, url, _ := branchesFor(t)
	ctx := context.Background()
	handPush(t, b, "jam/alice", map[string]string{"jam/users/bob.yml": "# bob, by hand\nkind: user\nversion: 1\n"})
	ws, _ := b.Workspace(ctx, "jam/alice")
	if _, err := ws.Store.CreateUser(User{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, ws, opAlice, "add"); err != nil {
		t.Fatal(err)
	}
	if got, _ := gittest.Show(t, url, "jam/alice", "jam/users/bob.yml"); !strings.HasPrefix(got, "# bob, by hand") {
		t.Fatalf("bob rewritten: %q", got)
	}
}

// A symlink committed to a branch's config dir does not load: the
// workspace refuses it (400), as the tree loader refuses one on disk.
// (Repo.Commit writes only regular files, so the branch is a local repo's
// main, committed by hand; the Jam follows another branch.)
func TestBranchSymlinkRefused(t *testing.T) {
	dir, _ := localGitRepo(t, map[string]string{"config/users/bob.yml": localBob})
	commitSymlink(t, dir, "config/users/eve.yml", "/etc/passwd")
	env := allCreds
	env.Followed = gitsource.Source{Repo: dir, Dir: "config", Ref: "release"}
	repo := &configrepo.Repo{URL: dir, Branch: "release", Path: filepath.Join(t.TempDir(), "config-repo")}
	b := &ConfigBranches{Repo: repo, Dir: "config", Env: env, Live: NewMemStore()}
	_, err := b.Workspace(context.Background(), "main")
	if WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), "users/eve.yml is a symlink") {
		t.Fatalf("workspace: %v", err)
	}
}
