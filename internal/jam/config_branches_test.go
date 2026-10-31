package jam

import (
	"context"
	"errors"
	"net/http"
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
