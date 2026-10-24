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
// new version) and the commit the content came from. Also lists the path-parent
// ancestors imported or checked with it (its kit set), root first.
type KitSourceResult struct {
	Name      string            `json:"name"`
	Version   int               `json:"version"`
	Unchanged bool              `json:"unchanged,omitempty"`
	Commit    string            `json:"commit"`
	Also      []KitSourceResult `json:"also,omitempty"`
}

// ErrNotTracked is returned by SyncKit for a kit with no tracked source.
var ErrNotTracked = errors.New("kit has no tracked source (use kit track)")

// kitLocks serializes the git-sourced operations on one kit name, so
// concurrent imports, tracks and syncs of a kit cannot interleave (or regress
// its content), and a sync waiting behind another sees its commit and skips
// the fetch. An operation holds at most one kit lock — its top kit's, taken
// before the fetch when the name is known, else just after it — and always
// before kitMu; nothing waits on a kit lock while holding kitMu. A set's path
// ancestors take no kit lock: kitMu covers their resolve and write. Entries
// are refcounted and dropped when unused.
var kitLocks struct {
	mu sync.Mutex
	m  map[string]*kitLock
}

type kitLock struct {
	mu   sync.Mutex
	refs int
}

// lockKit takes name's lock and returns its release.
func lockKit(name string) func() {
	kitLocks.mu.Lock()
	if kitLocks.m == nil {
		kitLocks.m = map[string]*kitLock{}
	}
	l := kitLocks.m[name]
	if l == nil {
		l = &kitLock{}
		kitLocks.m[name] = l
	}
	l.refs++
	kitLocks.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		kitLocks.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(kitLocks.m, name)
		}
		kitLocks.mu.Unlock()
	}
}

// ImportKit fetches src, loads its kit.yml and records it as a new version of
// the kit called name (unchanged content reuses the current version), with the
// source and commit as that version's provenance. It does not track the
// source. An empty name defaults to the kit's own name, else the directory's
// base, else the repo's name. Path parents (base.kit "../web") import with it
// as one kit set: see planKitSet. Any failure leaves the store untouched. log
// may be nil.
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

// importKit fetches src and records its kit set (the kit and its path
// parents): it plans the set from the checkout, then under kitMu resolves it
// against the store as it now is and writes every changed doc in one
// PutKitDocs. The caller holds the top kit's lock when name is set; a
// defaulted name is locked here, once the checkout names it.
//
// Accepted residual: only the top kit is locked across the fetch, so a sync
// of app (whose set includes web-dev) and a sync of web-dev fetching
// different commits can leave web-dev at the older commit's content until
// its next sync.
//
// With track, src (each kit at its own dir) at the fetched commit also becomes
// every kit's tracked source; the same happens for a kit that already tracks
// exactly that source.
func importKit(ctx context.Context, store Store, f KitFetcher, name string, src gitsource.Source, now time.Time, track bool) (KitSourceResult, error) {
	var res KitSourceResult
	err := f.Fetch(ctx, src, func(root, commit string) error {
		set, err := loadKitSet(root, src, name)
		if err != nil {
			return err
		}
		if name == "" {
			defer lockKit(set[len(set)-1].Name)() // a defaulted name is only known now
		}
		kitMu.Lock()
		defer kitMu.Unlock()
		entries, err := resolveKitSet(store, set, src.Repo)
		if err != nil {
			return err
		}
		if docs := buildKitDocs(store, entries, src, commit, now, track); len(docs) > 0 {
			if err := store.PutKitDocs(docs, KitObjects{}); err != nil {
				return fmt.Errorf("studio kit %q: write: %w", entries[len(entries)-1].Name, err)
			}
		}
		res = kitSetResult(entries, commit)
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
	args := []any{"kit", res.Name, "repo", src.Repo, "dir", src.Dir, "ref", src.Ref,
		"commit", res.Commit, "version", res.Version, "unchanged", res.Unchanged}
	if len(res.Also) > 0 {
		also := make([]string, len(res.Also))
		for i, a := range res.Also {
			also[i] = fmt.Sprintf("%s@v%d", a.Name, a.Version)
		}
		args = append(args, "also", also)
	}
	log.Info(event, args...)
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
// tracked source of the kit and of each path parent (at its own dir), so
// SyncKit can follow them. The new versions and the tracked sources are
// written together.
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
	kitMu.Lock()
	defer kitMu.Unlock()
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
// Otherwise the whole kit set is re-imported at the new commit, tracked
// (identical content reuses each kit's current version) and every tracked
// commit advances, written together with any new versions. Any failure leaves
// every kit as it was. name's lock is held throughout, so a sync waiting
// behind another sees its commit and does not fetch again.
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
