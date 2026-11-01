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

// Fetch checks the followed branch's head's config dir (Followed.Dir) out
// of the clone into a temp dir and calls fn with its root (removed after),
// or is Rest's fetch.
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
	if err := materializeFiles(dir, filesUnder(files, src.Dir)); err != nil {
		return err
	}
	return fn(dir, head)
}

// filesUnder is files' files in dir ("" = all), at their repo paths: only
// the config dir is checked out, so nothing outside it (a symlink, say)
// can fail the load.
func filesUnder(files map[string]configrepo.File, dir string) map[string]configrepo.File {
	if dir == "" {
		return files
	}
	out := map[string]configrepo.File{}
	for p, f := range files {
		if strings.HasPrefix(p, dir+"/") {
			out[p] = f
		}
	}
	return out
}

// errNoGitSources is a fetch on a serve with no git access.
var errNoGitSources = writeErr(http.StatusServiceUnavailable, "config: git sources are not available on this serve")

// materializeFiles writes files (slash paths) into dir at their modes:
// executable 0755, else 0644. The config tree holds only regular files: a
// symlink (or any other mode) is refused (400) before anything is written,
// as the tree loader refuses a symlink on disk. A path leading out of dir
// is refused.
func materializeFiles(dir string, files map[string]configrepo.File) error {
	for p, f := range files {
		switch f.Mode {
		case filemode.Regular, filemode.Deprecated, filemode.Executable:
		default:
			return writeErr(http.StatusBadRequest, "config repo: %s is a symlink (or submodule); the config tree holds only regular files", p)
		}
	}
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
