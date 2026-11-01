package jam

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/aethons-tools/cove/internal/jam/configrepo"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// FollowedFetcher is the serve's KitFetcher with the followed config repo
// read through its clone: Followed (the same repo, dir and ref) is read from
// Repo, whatever its kind (github, gitlab, a local path or ssh); every other
// source goes to Rest, which keeps refusing what it cannot fetch (a kit
// .source.yml naming a local path, say).
type FollowedFetcher struct {
	Repo     *configrepo.Repo
	Followed gitsource.Source
	Rest     KitFetcher // nil = no other git sources
}

// followed reports whether src is the followed source.
func (f *FollowedFetcher) followed(src gitsource.Source) bool {
	return f.Repo != nil && f.Followed.Repo != "" && src == f.Followed
}

// Resolve is the head of the followed branch, or Rest's resolve.
func (f *FollowedFetcher) Resolve(ctx context.Context, src gitsource.Source) (string, error) {
	if !f.followed(src) {
		if f.Rest == nil {
			return "", errNoGitSources
		}
		return f.Rest.Resolve(ctx, src)
	}
	heads, err := f.Repo.Branches(ctx)
	if err != nil {
		return "", err
	}
	for _, h := range heads {
		if h.Name == src.Ref {
			return h.Head, nil
		}
	}
	return "", configrepo.ErrNoBranch
}

// Fetch checks the followed branch's head out of the clone into a temp dir
// and calls fn with it (removed after), or is Rest's fetch.
func (f *FollowedFetcher) Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error {
	if !f.followed(src) {
		if f.Rest == nil {
			return errNoGitSources
		}
		return f.Rest.Fetch(ctx, src, fn)
	}
	head, files, err := f.Repo.Files(ctx, src.Ref)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "jam-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := materializeFiles(dir, files); err != nil {
		return err
	}
	return fn(dir, head)
}

// errNoGitSources is a fetch on a serve with no git access.
var errNoGitSources = writeErr(http.StatusServiceUnavailable, "config: git sources are not available on this serve")

// materializeFiles writes files (slash paths) into dir at their modes:
// executable 0755, else 0644. A path leading out of dir is refused.
func materializeFiles(dir string, files map[string]configrepo.File) error {
	for p, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if !strings.HasPrefix(full, dir+string(filepath.Separator)) {
			return fmt.Errorf("config repo: unsafe path %q", p)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.Mode == filemode.Executable {
			mode = 0o755
		}
		if err := os.WriteFile(full, f.Data, mode); err != nil {
			return err
		}
	}
	return nil
}
