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

// ErrConfigNotTracked is a sync, or a diff/apply naming no source, on a Jam
// with no tracked config source.
var ErrConfigNotTracked = errors.New("no config source is tracked (use config track --git <source>, or --dir)")

// ApplyConfigSource fetches the config repo src (zero = the tracked source,
// else ErrConfigNotTracked), loads its tree at src.Dir, and plans or applies
// it as ApplyConfigTree does, with the fetched commit on the marks and the
// kit versions. An apply from the tracked source records its commit and
// time in the same write. configMu is held across every fetch.
func ApplyConfigSource(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ConfigFetchTimeout)
	defer cancel()
	configMu.Lock()
	defer configMu.Unlock()
	return applyConfigSourceLocked(ctx, store, f, src, env, opt, log, actor)
}

// TrackConfigSource fetches and plans src (a dry run), then records it as
// the Jam's one config source and returns the plan. Re-tracking the same
// source keeps its last commit and time; another source replaces it. A
// fetch or load failure records nothing; a plan with refusals is still
// recorded (the operator fixes the repo, then syncs).
func TrackConfigSource(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, now time.Time, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	if src.Repo == "" {
		return ConfigApplyResult{}, writeErr(http.StatusBadRequest, "config track: a source is required (<repo>[//<dir>]@<ref>)")
	}
	ctx, cancel := context.WithTimeout(ctx, ConfigFetchTimeout)
	defer cancel()
	configMu.Lock()
	defer configMu.Unlock()
	res, err := applyConfigSourceLocked(ctx, store, f, src, env, ConfigApplyOptions{DryRun: true, Now: now}, log, actor)
	if err != nil {
		return res, err
	}
	rec := ConfigSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref}
	if cur, ok := store.GetConfigSource(); ok && cur.Source() == src {
		rec = cur
	}
	if err := store.SetConfigSource(&rec); err != nil {
		return res, fmt.Errorf("config track: %w", err)
	}
	if log != nil {
		log.Info("config tracked", "operator", actor, "source", configSourceLabel(src), "commit", res.Commit)
	}
	return res, nil
}

// UntrackConfigSource forgets the tracked source; objects and their marks
// stay. Untracking with none tracked is a no-op.
func UntrackConfigSource(store Store, log *slog.Logger, actor string) error {
	configMu.Lock()
	defer configMu.Unlock()
	cur, ok := store.GetConfigSource()
	if !ok {
		return nil
	}
	if err := store.SetConfigSource(nil); err != nil {
		return fmt.Errorf("config untrack: %w", err)
	}
	if log != nil {
		log.Info("config untracked", "operator", actor, "source", configSourceLabel(cur.Source()))
	}
	return nil
}

// applyConfigSourceLocked is ApplyConfigSource for a caller holding configMu.
func applyConfigSourceLocked(ctx context.Context, store Store, f KitFetcher, src gitsource.Source, env ConfigEnv, opt ConfigApplyOptions, log *slog.Logger, actor string) (ConfigApplyResult, error) {
	if src.Repo == "" {
		cur, ok := store.GetConfigSource()
		if !ok {
			return ConfigApplyResult{}, ErrConfigNotTracked
		}
		src = cur.Source()
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
// gitsource.Parse (clean, relative), so the refusal may name it.
func configCheckoutDir(root, dir string) (string, error) {
	cur := root
	if dir == "" {
		return cur, nil
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

// trackedRecord is the tracked-source record an apply of opt writes, nil =
// none: an apply from the tracked source (same repo, dir and ref) records
// its commit and time when the commit moved or the apply changed something.
func trackedRecord(store Store, opt ConfigApplyOptions, changes bool) *ConfigSource {
	cur, ok := store.GetConfigSource()
	if !ok || cur.Source() != (gitsource.Source{Repo: opt.Source.Repo, Dir: opt.Source.Dir, Ref: opt.Source.Ref}) {
		return nil
	}
	if !changes && cur.Commit == opt.Commit {
		return nil
	}
	cur.Commit, cur.AppliedAt = opt.Commit, opt.Now
	return &cur
}
