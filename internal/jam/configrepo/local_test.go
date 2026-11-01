package configrepo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// localRepo is a non-bare repo on main with files committed and checked out.
func localRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	for p, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func localFollower(t *testing.T, url string) *Repo {
	return &Repo{URL: url, Branch: "main", Path: filepath.Join(t.TempDir(), "config-repo")}
}

func localRef(t *testing.T, dir, ref string) plumbing.Hash {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.Reference(plumbing.ReferenceName(ref), true)
	if err != nil {
		t.Fatalf("%s: %v", ref, err)
	}
	return r.Hash()
}

func TestLocalBranchesFilesAndCreate(t *testing.T) {
	dir := localRepo(t, map[string]string{"a.yml": "a\n"})
	headBefore, _ := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	r := localFollower(t, dir)
	bs, err := r.Branches(ctx)
	if err != nil || len(bs) != 1 || bs[0].Name != "main" {
		t.Fatalf("branches = %+v %v", bs, err)
	}
	if _, files, err := r.Files(ctx, "main"); err != nil || string(files["a.yml"].Data) != "a\n" {
		t.Fatalf("files = %v %v", files, err)
	}
	h, err := r.CreateBranch(ctx, "jam/me")
	if err != nil {
		t.Fatal(err)
	}
	if got := localRef(t, dir, "refs/heads/jam/me"); got.String() != h || got != localRef(t, dir, "refs/heads/main") {
		t.Fatalf("jam/me = %s, want main's head %s", got, h)
	}
	headAfter, _ := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if string(headBefore) != string(headAfter) {
		t.Fatalf("HEAD changed: %q -> %q", headBefore, headAfter)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.yml")); string(b) != "a\n" {
		t.Fatalf("worktree file = %q", b)
	}
	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	if st, err := wt.Status(); err != nil || !st.IsClean() {
		t.Fatalf("status = %v %v", st, err)
	}
}

func TestLocalURLForms(t *testing.T) {
	dir := localRepo(t, map[string]string{"a.yml": "a\n"})
	want, err := localFollower(t, dir).Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{dir + "/", dir + "/.git", "file://" + dir} {
		got, err := localFollower(t, url).Branches(ctx)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v %v", url, got, err)
		}
	}
}

func TestLocalCommitToCheckedOutRefused(t *testing.T) {
	dir := localRepo(t, map[string]string{"a.yml": "a\n"})
	r := localFollower(t, dir)
	h, err := r.CreateBranch(ctx, "jam/me")
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	if err := wt.Checkout(&git.CheckoutOptions{Branch: "refs/heads/jam/me"}); err != nil {
		t.Fatal(err)
	}
	_, err = r.Commit(ctx, "jam/me", h, put("a", "x"), alice, "m")
	if !errors.Is(err, ErrCheckedOut) || !strings.Contains(err.Error(), "jam/me is checked out in") {
		t.Fatalf("err = %v", err)
	}
	if got := localRef(t, dir, "refs/heads/jam/me"); got.String() != h {
		t.Fatalf("jam/me moved to %s", got)
	}
}

func TestLocalMergeIntoCheckedOutRefused(t *testing.T) {
	dir := localRepo(t, map[string]string{"a.yml": "a\n"})
	r := localFollower(t, dir)
	h0, err := r.CreateBranch(ctx, "jam/me")
	if err != nil {
		t.Fatal(err)
	}
	h1, err := r.Commit(ctx, "jam/me", h0, put("b.yml", "b\n"), alice, "b")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Merge(ctx, "jam/me", h1, alice, "merge", noCheck)
	if !errors.Is(err, ErrCheckedOut) {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalLinkedWorktreeRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git CLI")
	}
	dir := localRepo(t, map[string]string{"a.yml": "a\n"})
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", wt2, "-b", "jam/other").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	r := localFollower(t, dir)
	bs, err := r.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var h string
	for _, b := range bs {
		if b.Name == "jam/other" {
			h = b.Head
		}
	}
	if _, err := r.Commit(ctx, "jam/other", h, put("a", "x"), alice, "m"); !errors.Is(err, ErrCheckedOut) {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalCommitAndMerge(t *testing.T) {
	dir := localRepo(t, map[string]string{"a.yml": "a\n"})
	r := localFollower(t, dir)
	h0, err := r.CreateBranch(ctx, "jam/me")
	if err != nil {
		t.Fatal(err)
	}
	h1, err := r.Commit(ctx, "jam/me", h0, put("b.yml", "b\n"), alice, "b")
	if err != nil || h1 == h0 {
		t.Fatalf("commit = %s %v", h1, err)
	}
	if got := localRef(t, dir, "refs/heads/jam/me"); got.String() != h1 {
		t.Fatalf("jam/me = %s, want %s", got, h1)
	}
}

func TestLocalGlobCharsInPathStillRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git CLI")
	}
	dir := filepath.Join(t.TempDir(), "re[x]po*?")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := localRepo(t, map[string]string{"a.yml": "a\n"})
	if out, err := exec.Command("git", "clone", "-q", src, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", wt2, "-b", "jam/other").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	r := localFollower(t, dir)
	bs, err := r.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var h string
	for _, b := range bs {
		if b.Name == "jam/other" {
			h = b.Head
		}
	}
	if _, err := r.Commit(ctx, "jam/other", h, put("a", "x"), alice, "m"); !errors.Is(err, ErrCheckedOut) {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalURLIsLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git CLI")
	}
	dir := localRepo(t, map[string]string{"a.yml": "a\n"})
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", wt2, "-b", "jam/other").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	r := &Repo{URL: wt2, Branch: "jam/other", Path: filepath.Join(t.TempDir(), "config-repo")}
	bs, err := r.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var h string
	for _, b := range bs {
		if b.Name == "main" {
			h = b.Head
		}
	}
	// main is checked out in the main worktree, not in wt2's own HEAD.
	if _, err := r.Commit(ctx, "main", h, put("a", "x"), alice, "m"); !errors.Is(err, ErrCheckedOut) {
		t.Fatalf("err = %v", err)
	}
}
