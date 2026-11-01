package jam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/aethons-tools/cove/internal/jam/configrepo"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// localGitRepo is a local (non-bare) repo with files committed on main, and
// a commit func that writes more files to main and returns the new head.
func localGitRepo(t *testing.T, files map[string]string) (string, func(map[string]string) string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(files map[string]string) string {
		t.Helper()
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
		h, err := wt.Commit("commit", &git.CommitOptions{Author: sig, Committer: sig})
		if err != nil {
			t.Fatal(err)
		}
		return h.String()
	}
	commit(files)
	return dir, commit
}

// localFollowed is a FollowedFetcher over a fresh local repo holding files,
// following its config dir on main, with rest for every other source.
func localFollowed(t *testing.T, files map[string]string, rest KitFetcher) (*FollowedFetcher, func(map[string]string) string) {
	t.Helper()
	dir, commit := localGitRepo(t, files)
	followed := gitsource.Source{Repo: dir, Dir: "config", Ref: "main"}
	repo := &configrepo.Repo{URL: dir, Branch: "main", Path: filepath.Join(t.TempDir(), "config-repo")}
	return &FollowedFetcher{Repo: repo, Followed: followed, Rest: rest}, commit
}

// recordingFetcher records the sources it was asked for and serves nothing.
type recordingFetcher struct {
	mu   sync.Mutex
	srcs []gitsource.Source
}

var errRecorded = errors.New("recorded")

func (r *recordingFetcher) record(src gitsource.Source) {
	r.mu.Lock()
	r.srcs = append(r.srcs, src)
	r.mu.Unlock()
}

func (r *recordingFetcher) Resolve(_ context.Context, src gitsource.Source) (string, error) {
	r.record(src)
	return "", errRecorded
}

func (r *recordingFetcher) Fetch(_ context.Context, src gitsource.Source, _ func(root, commit string) error) error {
	r.record(src)
	return errRecorded
}

func (r *recordingFetcher) calls() []gitsource.Source {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gitsource.Source(nil), r.srcs...)
}

const localBob = "kind: user\nversion: 1\n"

func TestFollowedFetcherRoutes(t *testing.T) {
	rest := &recordingFetcher{}
	f, commit := localFollowed(t, map[string]string{"config/users/bob.yml": localBob}, rest)
	ctx := context.Background()
	head := commit(map[string]string{"README.md": "hi\n"})
	got, err := f.Resolve(ctx, f.Followed)
	if err != nil || got != head {
		t.Fatalf("resolve = %q, %v; want %q", got, err, head)
	}
	var seen string
	err = f.Fetch(ctx, f.Followed, func(root, c string) error {
		data, err := os.ReadFile(filepath.Join(root, "config", "users", "bob.yml"))
		if err != nil {
			return err
		}
		if string(data) != localBob || c != head {
			t.Errorf("fetch saw %q at %q", data, c)
		}
		seen = root
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Errorf("checkout %s not removed: %v", seen, err)
	}
	if len(rest.calls()) != 0 {
		t.Fatalf("the followed source reached rest: %v", rest.calls())
	}
	other := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main"}
	branch := f.Followed
	branch.Ref = "other" // another ref of the same repo is not the followed source
	if _, err := f.Resolve(ctx, other); !errors.Is(err, errRecorded) {
		t.Fatalf("resolve other: %v", err)
	}
	if err := f.Fetch(ctx, branch, func(string, string) error { return nil }); !errors.Is(err, errRecorded) {
		t.Fatalf("fetch other: %v", err)
	}
	if c := rest.calls(); len(c) != 2 || c[0] != other || c[1] != branch {
		t.Fatalf("rest calls %v", c)
	}
	f.Rest = nil
	if err := f.Fetch(ctx, other, func(string, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "git sources are not available") {
		t.Fatalf("nil rest: %v", err)
	}
}

func TestKitSourceStillRefusesLocal(t *testing.T) {
	rest := &recordingFetcher{}
	f, _ := localFollowed(t, map[string]string{
		"config/users/bob.yml":     localBob,
		"config/kits/k.source.yml": "kind: kit-source\nversion: 1\ngit: /etc//x@main\n",
	}, rest)
	env := allCreds
	env.Followed = f.Followed
	_, err := ApplyConfigSource(context.Background(), NewMemStore(), f, gitsource.Source{}, env, syncOpts(), nil, "alice")
	if !errors.Is(err, ErrConfigRefused) || !strings.Contains(err.Error(), "kits/k.source.yml: git: ") {
		t.Fatalf("apply: %v", err)
	}
	if c := rest.calls(); len(c) != 0 {
		t.Fatalf("a refused kit source was fetched: %v", c)
	}
}

// commitSymlink commits a symlink at p (to target) on the local repo dir's
// main and returns the new head.
func commitSymlink(t *testing.T, dir, p, target string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(p); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@t", When: time.Now()}
	h, err := wt.Commit("link", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

// A symlink in the followed config dir is refused (400) as the tree loader
// refuses one on disk; one outside the config dir is never checked out.
func TestFollowedFetcherRefusesSymlink(t *testing.T) {
	f, _ := localFollowed(t, map[string]string{"config/users/bob.yml": localBob}, nil)
	ctx := context.Background()
	commitSymlink(t, f.Repo.URL, "docs-link", "config")
	if err := f.Fetch(ctx, f.Followed, func(string, string) error { return nil }); err != nil {
		t.Fatalf("a symlink outside the config dir: %v", err)
	}
	commitSymlink(t, f.Repo.URL, "config/users/eve.yml", "/etc/passwd")
	called := false
	err := f.Fetch(ctx, f.Followed, func(string, string) error { called = true; return nil })
	if called || WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), "config/users/eve.yml is a symlink") {
		t.Fatalf("fetch: called=%v %v", called, err)
	}
	st := NewMemStore()
	env := allCreds
	env.Followed = f.Followed
	_, err = ApplyConfigSource(ctx, st, f, gitsource.Source{}, env, syncOpts(), nil, "alice")
	if status, msg := ConfigStatus(err); status != http.StatusBadRequest || !strings.Contains(msg, "is a symlink") {
		t.Fatalf("apply: %d %s", status, msg)
	}
	if len(st.ListUsers()) != 0 {
		t.Fatal("a refused tree was applied")
	}
}

// materializeFiles refuses a non-regular file before writing any.
func TestMaterializeFilesRefusesSymlinkFirst(t *testing.T) {
	dir := t.TempDir()
	files := map[string]configrepo.File{}
	for i := range 20 {
		files[fmt.Sprintf("users/u%d.yml", i)] = configrepo.File{Data: []byte(localBob), Mode: filemode.Regular}
	}
	files["users/link.yml"] = configrepo.File{Data: []byte("/etc/passwd"), Mode: filemode.Symlink}
	if err := materializeFiles(dir, files); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("materialize: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("wrote %v", ents)
	}
}

func TestFollowedFetcherMissingBranch(t *testing.T) {
	f, _ := localFollowed(t, map[string]string{"config/users/bob.yml": localBob}, nil)
	f.Followed.Ref = "nope"
	if _, err := f.Resolve(context.Background(), f.Followed); !errors.Is(err, configrepo.ErrNoBranch) {
		t.Fatalf("resolve: %v", err)
	}
}
