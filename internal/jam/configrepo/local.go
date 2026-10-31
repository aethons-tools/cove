package configrepo

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
)

// IsLocal reports whether url names a repo on this machine: an absolute
// path or a file:// URL.
func IsLocal(url string) bool {
	return strings.HasPrefix(url, "file://") || filepath.IsAbs(url)
}

// LocalPath is a local url's cleaned absolute path: no file://, no trailing
// "/" and no trailing "/.git".
func LocalPath(u string) string {
	if rest, ok := strings.CutPrefix(u, "file://"); ok {
		u = rest
		if p, err := url.PathUnescape(u); err == nil {
			u = p
		}
	}
	p := filepath.Clean(u)
	if filepath.Base(p) == ".git" {
		p = filepath.Dir(p)
	}
	return p
}

var installOnce sync.Once

// installLocal swaps go-git's "file" transport, which runs the git binary,
// for its in-process server over the repos on disk.
func installLocal() {
	installOnce.Do(func() {
		client.InstallProtocol("file", server.NewClient(localLoader{}))
	})
}

// localLoader opens the repo at an endpoint's path, bare or not, as the
// storage the server reads and writes (go-git's own loader would treat a
// checkout's directory as the storage).
type localLoader struct{}

func (localLoader) Load(ep *transport.Endpoint) (storer.Storer, error) {
	repo, err := git.PlainOpenWithOptions(ep.Path, &git.PlainOpenOptions{DetectDotGit: false, EnableDotGitCommonDir: true})
	if err != nil {
		return nil, transport.ErrRepositoryNotFound
	}
	return repo.Storer, nil
}

// checkedOut is the branches checked out in the repo at path, by name, each
// with the worktree directory that has it: the main worktree's HEAD and
// every linked worktree's. A detached HEAD holds no branch. go-git's
// receive-pack would move such a branch under the checkout, so push refuses.
func checkedOut(path string) (map[string]string, error) {
	out := map[string]string{}
	add := func(headFile, dir string) error {
		b, err := os.ReadFile(headFile)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/"); ok {
			out[ref] = dir
		}
		return nil
	}
	gitDir := filepath.Join(path, ".git")
	if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() {
		if err := add(filepath.Join(gitDir, "HEAD"), path); err != nil {
			return nil, err
		}
	} else {
		gitDir = path // bare: no main worktree
	}
	linked, _ := filepath.Glob(filepath.Join(gitDir, "worktrees", "*"))
	for _, w := range linked {
		dir := w
		if b, err := os.ReadFile(filepath.Join(w, "gitdir")); err == nil {
			dir = filepath.Dir(strings.TrimSpace(string(b))) // <worktree>/.git
		}
		if err := add(filepath.Join(w, "HEAD"), dir); err != nil {
			return nil, err
		}
	}
	return out, nil
}
