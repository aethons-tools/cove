package jam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// ConfigFetchTimeout bounds one config run's git fetches together: the
// config repo's and every kits/<name>.source.yml checkout.
const ConfigFetchTimeout = 4 * time.Minute

// configFetchTimeout is ConfigFetchTimeout, shortened by tests.
var configFetchTimeout = ConfigFetchTimeout

// ErrConfigNotTracked is a sync, or a diff/apply naming no source, on a Jam
// with no followed config repo.
var ErrConfigNotTracked = errors.New("no config repo is configured (config.github in the serve config)")

// ApplyConfigSource fetches the config repo src (zero = env.Followed, else
// ErrConfigNotTracked), loads its tree at src.Dir, and plans or applies it as
// ApplyConfigTree does, with the fetched commit on the marks and the kit
// versions. An apply from the followed repo records its commit and time in
// the same write. configMu is held across every fetch.
func ApplyConfigSource(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	configMu.Lock() // the fetch deadline starts here: queue wait does not count
	defer configMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, configFetchTimeout)
	defer cancel()
	return applyConfigSourceLocked(ctx, store, f, src, env, opt, log, actor)
}

// ConfigFollowConfirmed reports whether the followed config repo's first
// apply happened: the stored record is of env.Followed. Until then the
// follower plans but never applies (an operator confirms on the Tracking
// page, or with config sync).
func ConfigFollowConfirmed(store Store, env ConfigEnv) bool {
	cur, ok := store.GetConfigSource()
	// A record with no commit is config track's, from before following: it
	// was never confirmed as the followed repo's first apply.
	return ok && env.Followed.Repo != "" && cur.Source() == env.Followed && cur.Commit != ""
}

// applyConfigSourceLocked is ApplyConfigSource for a caller holding configMu.
func applyConfigSourceLocked(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	if src.Repo == "" {
		if env.Followed.Repo == "" {
			return ConfigApplyResult{}, ErrConfigNotTracked
		}
		src = env.Followed
	}
	if f == nil {
		return ConfigApplyResult{}, writeErr(http.StatusServiceUnavailable, "config: git sources are not available on this serve")
	}
	tree, commit, err := fetchConfigTree(ctx, f, src)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ConfigApplyResult{}, errConfigFetchTimedOut
		}
		if cerr := configFetchErr(ctx); cerr != nil {
			return ConfigApplyResult{}, cerr
		}
		return ConfigApplyResult{}, err
	}
	opt.Source, opt.Commit = src, commit
	return applyConfigTreeLocked(ctx, store, f, tree, env, opt, log, actor)
}

// errConfigFetchTimedOut is a config run whose fetches outran their shared
// deadline (ConfigFetchTimeout, or the caller's).
var errConfigFetchTimedOut = writeErr(http.StatusBadGateway, "config fetch timed out")

// configFetchErr is ctx's end as a config run's error, nil while it runs: a
// passed deadline is errConfigFetchTimedOut (502), a cancel (the client went
// away) the context error itself.
func configFetchErr(ctx context.Context) error {
	switch err := ctx.Err(); {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return errConfigFetchTimedOut
	default:
		return fmt.Errorf("config: %w", err)
	}
}

// fetchConfigTree checks src out and loads the tree at src.Dir inside the
// checkout; the tree is in memory before the checkout is removed. Kit
// sources are fetched later, outside this fetch (the fetcher serializes per
// repo, and a .source.yml may name this repo).
func fetchConfigTree(ctx context.Context, f KitFetcher, src gitsource.Source) (*ConfigTree, string, error) {
	var tree *ConfigTree
	var commit string
	err := f.Fetch(ctx, src, func(root, c string) error {
		dir, err := configCheckoutDir(root, src.Dir)
		if err != nil {
			return err
		}
		t, err := LoadConfigTree(dir)
		if err != nil {
			return writeErr(http.StatusBadRequest, "config source: the checkout cannot be read")
		}
		tree, commit = t, c
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return tree, commit, nil
}

// configCheckoutDir is root/dir when every component of dir is a real
// directory in the checkout: a symlink could lead out of it. dir passed
// gitsource.Parse (clean, relative), so the refusal may name it; a dir that
// is not clean and relative anyway (a hand-edited stored record) is refused
// with a fixed text that does not.
func configCheckoutDir(root, dir string) (string, error) {
	cur := root
	if dir == "" {
		return cur, nil
	}
	for part := range strings.SplitSeq(dir, "/") {
		if part == "" || part == "." || part == ".." {
			return "", writeErr(http.StatusBadRequest, "config source: the dir is not a clean relative path")
		}
	}
	for part := range strings.SplitSeq(dir, "/") {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err != nil || !fi.IsDir() {
			return "", writeErr(http.StatusBadRequest, "config source: %s is not a directory in the repo", dir)
		}
	}
	return cur, nil
}

// configSourceLabel is how logs and results name a run's source: "dir" for
// an upload, else the source as gitsource.Parse renders it. A source that
// does not re-parse (never one the API built — say, a hand-made URL with
// userinfo) is "invalid", so no log line can carry a credential.
func configSourceLabel(src gitsource.Source) string {
	switch src.Repo {
	case ConfigDirRepo:
		return "dir"
	case "":
		return ""
	}
	if _, err := gitsource.Parse(src.String()); err != nil {
		return "invalid"
	}
	return src.String()
}

// trackedRecord is the followed-repo record an apply of opt writes, nil =
// none: an apply from env.Followed (same repo, dir and ref) records its
// commit and time when there was no record of it (the first, confirmed
// apply; a record of another repo is replaced), the commit moved, or the
// apply changed something.
func trackedRecord(store Store, env ConfigEnv, opt ConfigApplyOptions, changes bool) *ConfigSource {
	src := gitsource.Source{Repo: opt.Source.Repo, Dir: opt.Source.Dir, Ref: opt.Source.Ref}
	if env.Followed.Repo == "" || src != env.Followed {
		return nil
	}
	cur, ok := store.GetConfigSource()
	if !ok || cur.Source() != src {
		return &ConfigSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref, Commit: opt.Commit, AppliedAt: opt.Now}
	}
	if !changes && cur.Commit == opt.Commit {
		return nil
	}
	cur.Commit, cur.AppliedAt = opt.Commit, opt.Now
	return &cur
}
