package jam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// KitFetcher is the git access ImportKit, TrackKit and SyncKit need;
// *gitsource.Fetcher implements it.
type KitFetcher interface {
	Fetch(ctx context.Context, src gitsource.Source, fn func(root, commit string) error) error
	Resolve(ctx context.Context, src gitsource.Source) (string, error)
}

// KitSourceResult reports what an import, track or sync did: the kit's name,
// its current version after the call, whether the definition was unchanged (no
// new version) and the commit the content came from.
type KitSourceResult struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Unchanged bool   `json:"unchanged,omitempty"`
	Commit    string `json:"commit"`
}

// ErrNotTracked is returned by SyncKit for a kit with no tracked source.
var ErrNotTracked = errors.New("kit has no tracked source (use kit track)")

// kitLocks serializes the git-sourced operations on one kit name, so
// concurrent imports, tracks and syncs cannot interleave (or regress content).
var kitLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

// lockKit takes name's lock and returns its release.
func lockKit(name string) func() {
	kitLocks.mu.Lock()
	if kitLocks.m == nil {
		kitLocks.m = map[string]*sync.Mutex{}
	}
	l := kitLocks.m[name]
	if l == nil {
		l = &sync.Mutex{}
		kitLocks.m[name] = l
	}
	kitLocks.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// ImportKit fetches src, loads its kit.yml and records it as a new version of
// the kit called name (unchanged content reuses the current version), with the
// source and commit as that version's provenance. It does not track the
// source. An empty name defaults to the kit's own name, else the directory's
// base, else the repo's name. Any failure leaves the store untouched. log may
// be nil.
func ImportKit(ctx context.Context, store Store, f KitFetcher, log *slog.Logger, name string, src gitsource.Source, now time.Time) (KitSourceResult, error) {
	if name != "" {
		defer lockKit(name)()
	}
	res, err := importKit(ctx, store, f, name, src, now, false)
	if err != nil {
		return KitSourceResult{}, err
	}
	logKitSource(log, "kit imported", src, res)
	return res, nil
}

// importKit fetches and records src; with track, src at the fetched commit also
// becomes the kit's tracked source, atomically with the new version.
func importKit(ctx context.Context, store Store, f KitFetcher, name string, src gitsource.Source, now time.Time, track bool) (KitSourceResult, error) {
	var res KitSourceResult
	err := f.Fetch(ctx, src, func(root, commit string) error {
		sk, hint, err := gitsource.Load(root, src.Dir)
		if err != nil {
			return err
		}
		locked := name != ""
		if name == "" {
			name = hint
		}
		if name == "" {
			name = repoBase(src.Repo)
		}
		if !locked {
			defer lockKit(name)() // a defaulted name is only known now
		}
		at := &KitSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref, Commit: commit}
		var tracked *KitSource
		if track {
			tracked = at
		}
		ref, unchanged, err := ensureStudioKitMeta(store, name, sk, KitVersionMeta{PushedAt: now, Source: at}, tracked)
		if err != nil {
			return err
		}
		res = KitSourceResult{Name: name, Version: ref.Version, Unchanged: unchanged, Commit: commit}
		return nil
	})
	if err != nil {
		return KitSourceResult{}, err
	}
	return res, nil
}

// logKitSource is the audit record of a git-sourced kit change; log may be nil.
func logKitSource(log *slog.Logger, event string, src gitsource.Source, res KitSourceResult) {
	if log == nil {
		return
	}
	log.Info(event, "kit", res.Name, "repo", src.Repo, "dir", src.Dir, "ref", src.Ref,
		"commit", res.Commit, "version", res.Version, "unchanged", res.Unchanged)
}

// repoBase is the last path segment of a repo URL without ".git" — the name
// fallback for a kit at the repo root; "" when there is none.
func repoBase(repo string) string {
	repo = strings.TrimRight(repo, "/")
	repo = strings.TrimSuffix(repo, ".git")
	if i := strings.LastIndexAny(repo, "/:"); i >= 0 {
		repo = repo[i+1:]
	}
	return repo
}

// TrackKit is ImportKit that also records src (at the imported commit) as the
// kit's tracked source, so SyncKit can follow it. The new version and the
// tracked source are written together.
func TrackKit(ctx context.Context, store Store, f KitFetcher, log *slog.Logger, name string, src gitsource.Source, now time.Time) (KitSourceResult, error) {
	if name != "" {
		defer lockKit(name)()
	}
	res, err := importKit(ctx, store, f, name, src, now, true)
	if err != nil {
		return KitSourceResult{}, err
	}
	logKitSource(log, "kit tracked", src, res)
	return res, nil
}

// UntrackKit stops tracking name's source; its versions are kept. An unknown
// kit wraps ErrNotFound.
func UntrackKit(store Store, log *slog.Logger, name string) error {
	defer lockKit(name)()
	if err := store.SetKitSource(name, nil); err != nil {
		return err
	}
	if log != nil {
		log.Info("kit untracked", "kit", name)
	}
	return nil
}

// SyncKit re-imports a tracked kit from its source. When the source's ref
// still resolves to the tracked commit and the kit's current version came from
// that same repo, dir and ref, nothing is fetched and the result is unchanged.
// Otherwise the new commit is imported (identical content reuses the current
// version) and the tracked commit advances, written together with any new
// version. Any failure leaves the kit as it was.
func SyncKit(ctx context.Context, store Store, f KitFetcher, log *slog.Logger, name string, now time.Time) (KitSourceResult, error) {
	defer lockKit(name)()
	k, ok := store.GetKit(name)
	if !ok {
		return KitSourceResult{}, fmt.Errorf("kit %q: %w", name, ErrNotFound)
	}
	if k.Source == nil {
		return KitSourceResult{}, ErrNotTracked
	}
	src := gitsource.Source{Repo: k.Source.Repo, Dir: k.Source.Dir, Ref: k.Source.Ref}
	commit, err := f.Resolve(ctx, src)
	if err != nil {
		return KitSourceResult{}, err
	}
	if cur := k.Meta[k.Current].Source; commit == k.Source.Commit && cur != nil &&
		cur.Repo == src.Repo && cur.Dir == src.Dir && cur.Ref == src.Ref {
		res := KitSourceResult{Name: name, Version: k.Current, Unchanged: true, Commit: commit}
		logKitSource(log, "kit synced", src, res)
		return res, nil
	}
	res, err := importKit(ctx, store, f, name, src, now, true)
	if err != nil {
		return KitSourceResult{}, err
	}
	logKitSource(log, "kit synced", src, res)
	return res, nil
}
