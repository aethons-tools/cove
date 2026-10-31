// Package configrepo writes to a Jam's followed config repo
// (docs/usage/jam/config-git-sources.md) with go-git alone: its branches,
// a branch's files, new branches, commits to them, merges into the followed
// branch (refused on a per-file conflict), and GitHub pull requests. It
// keeps one bare clone at Path, fetched in full before every operation.
package configrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

var (
	// ErrStale is a branch that moved since the caller read it: someone
	// else committed, or pushed by hand. Nothing was written.
	ErrStale = errors.New("the branch moved since it was read; reload it")
	// ErrNoBranch is a branch the repo doesn't have.
	ErrNoBranch = errors.New("no such branch")
	// ErrBranchExists is a branch name already taken.
	ErrBranchExists = errors.New("the branch already exists")
	// ErrFollowedBranch is a write to the followed branch itself: it changes
	// only by Merge (or in git).
	ErrFollowedBranch = errors.New("the followed branch is read-only; edit on a branch")
)

// Repo is the followed config repo: its URL, the followed branch, the
// credentials (gitsource.Creds.For(URL)) and the bare clone's directory.
// Its methods are safe for concurrent use; they run one at a time.
type Repo struct {
	URL    string
	Branch string
	Creds  gitsource.Creds
	Path   string

	mu sync.Mutex
}

// BranchInfo is one branch and its head commit.
type BranchInfo struct {
	Name string `json:"name"`
	Head string `json:"head"`
}

// File is one file's content and mode (filemode.Regular or Executable).
type File struct {
	Data []byte
	Mode filemode.FileMode
}

const remoteName = "origin"

func remoteRef(branch string) plumbing.ReferenceName {
	return plumbing.NewRemoteReferenceName(remoteName, branch)
}

// open opens (or creates) the bare clone and fetches every branch, pruning
// those gone. The caller holds mu.
func (r *Repo) open(ctx context.Context) (*git.Repository, transport.AuthMethod, error) {
	repo, err := git.PlainOpen(r.Path)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.PlainInit(r.Path, true)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("config repo: opening the clone: %w", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return nil, nil, fmt.Errorf("config repo: reading the clone's config: %w", err)
	}
	if rc, ok := cfg.Remotes[remoteName]; !ok || len(rc.URLs) != 1 || rc.URLs[0] != r.URL {
		cfg.Remotes[remoteName] = &config.RemoteConfig{Name: remoteName, URLs: []string{r.URL},
			Fetch: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"}}
		if err := repo.SetConfig(cfg); err != nil {
			return nil, nil, fmt.Errorf("config repo: writing the clone's config: %w", err)
		}
	}
	auth, err := r.Creds.AuthFor(r.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("config repo: %w", err)
	}
	err = repo.FetchContext(ctx, &git.FetchOptions{RemoteName: remoteName, Auth: auth, Force: true, Prune: true,
		RefSpecs: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"}})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, nil, r.explain("fetch", err)
	}
	return repo, auth, nil
}

// explain is a git failure as an operator can act on it, redacted.
func (r *Repo) explain(op string, err error) error {
	repo := gitsource.Redact(r.URL)
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return fmt.Errorf("config repo: %s %s: auth failed (check config.github.cred)", op, repo)
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return fmt.Errorf("config repo: %s %s: repository not found (or the credential can't see it)", op, repo)
	}
	return fmt.Errorf("config repo: %s %s: %s", op, repo, gitsource.Redact(err.Error()))
}

// Branches lists the repo's branches, by name.
func (r *Repo) Branches(ctx context.Context) ([]BranchInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, _, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	return branches(repo)
}

func branches(repo *git.Repository) ([]BranchInfo, error) {
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	defer refs.Close()
	prefix := "refs/remotes/" + remoteName + "/"
	var out []BranchInfo
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		if ref.Type() != plumbing.HashReference || !strings.HasPrefix(name, prefix) || name == prefix+"HEAD" {
			return nil
		}
		out = append(out, BranchInfo{Name: strings.TrimPrefix(name, prefix), Head: ref.Hash().String()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// head is branch's fetched commit; ErrNoBranch when there is none.
func head(repo *git.Repository, branch string) (*object.Commit, error) {
	ref, err := repo.Reference(remoteRef(branch), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, ErrNoBranch
	}
	if err != nil {
		return nil, err
	}
	return repo.CommitObject(ref.Hash())
}

// Files is branch's head and every file in it, by slash path.
func (r *Repo) Files(ctx context.Context, branch string) (string, map[string]File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, _, err := r.open(ctx)
	if err != nil {
		return "", nil, err
	}
	c, err := head(repo, branch)
	if err != nil {
		return "", nil, err
	}
	files, err := filesOf(c)
	return c.Hash.String(), files, err
}

// filesOf reads every file in c's tree.
func filesOf(c *object.Commit) (map[string]File, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	out := map[string]File{}
	err = tree.Files().ForEach(func(f *object.File) error {
		rd, err := f.Reader()
		if err != nil {
			return err
		}
		defer rd.Close()
		data, err := io.ReadAll(rd)
		if err != nil {
			return err
		}
		out[f.Name] = File{Data: data, Mode: f.Mode}
		return nil
	})
	return out, err
}
