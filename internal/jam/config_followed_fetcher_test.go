package jam

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
