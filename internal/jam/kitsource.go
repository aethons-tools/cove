package jam

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
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

// ImportKit fetches src, loads its kit.yml and records it as a new version of
// the kit called name (unchanged content reuses the current version), with the
// source and commit as that version's provenance. It does not track the
// source. An empty name defaults to the kit's own name, else the directory's
// base, else the repo's name. Any failure leaves the store untouched. log may
// be nil.
func ImportKit(ctx context.Context, store Store, f KitFetcher, log *slog.Logger, name string, src gitsource.Source, now time.Time) (KitSourceResult, error) {
	return importKit(ctx, store, f, log, "kit imported", name, src, now)
}

func importKit(ctx context.Context, store Store, f KitFetcher, log *slog.Logger, event, name string, src gitsource.Source, now time.Time) (KitSourceResult, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var res KitSourceResult
	err := f.Fetch(ctx, src, func(root, commit string) error {
		sk, hint, err := gitsource.Load(root, src.Dir)
		if err != nil {
			return err
		}
		if name == "" {
			name = hint
		}
		if name == "" {
			name = repoBase(src.Repo)
		}
		ref, unchanged, err := ensureStudioKitMeta(store, name, sk, KitVersionMeta{
			PushedAt: now,
			Source:   &KitSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref, Commit: commit},
		})
		if err != nil {
			return err
		}
		res = KitSourceResult{Name: name, Version: ref.Version, Unchanged: unchanged, Commit: commit}
		return nil
	})
	if err != nil {
		return KitSourceResult{}, err
	}
	log.Info(event, "kit", res.Name, "repo", src.Repo, "dir", src.Dir, "ref", src.Ref,
		"commit", res.Commit, "version", res.Version, "unchanged", res.Unchanged)
	return res, nil
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
// kit's tracked source, so SyncKit can follow it.
func TrackKit(ctx context.Context, store Store, f KitFetcher, log *slog.Logger, name string, src gitsource.Source, now time.Time) (KitSourceResult, error) {
	res, err := importKit(ctx, store, f, log, "kit tracked", name, src, now)
	if err != nil {
		return KitSourceResult{}, err
	}
	if err := store.SetKitSource(res.Name, &KitSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref, Commit: res.Commit}); err != nil {
		return KitSourceResult{}, fmt.Errorf("track kit %q: %w", res.Name, err)
	}
	return res, nil
}

// UntrackKit stops tracking name's source; its versions are kept. An unknown
// kit wraps ErrNotFound.
func UntrackKit(store Store, log *slog.Logger, name string) error {
	if err := store.SetKitSource(name, nil); err != nil {
		return err
	}
	if log != nil {
		log.Info("kit untracked", "kit", name)
	}
	return nil
}

// SyncKit re-imports a tracked kit from its source. When the source's ref
// still resolves to the commit the kit's current version came from, nothing is
// fetched and the result is unchanged. Otherwise the new commit is imported
// (identical content reuses the current version) and the tracked commit
// advances. Any failure leaves the kit as it was.
func SyncKit(ctx context.Context, store Store, f KitFetcher, log *slog.Logger, name string, now time.Time) (KitSourceResult, error) {
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
	if m := k.Meta[k.Current]; commit == k.Source.Commit && m.Source != nil && m.Source.Commit == commit {
		res := KitSourceResult{Name: name, Version: k.Current, Unchanged: true, Commit: commit}
		if log != nil {
			log.Info("kit synced", "kit", name, "repo", src.Repo, "dir", src.Dir, "ref", src.Ref,
				"commit", commit, "version", res.Version, "unchanged", true)
		}
		return res, nil
	}
	res, err := importKit(ctx, store, f, log, "kit synced", name, src, now)
	if err != nil {
		return KitSourceResult{}, err
	}
	if err := store.SetKitSource(name, &KitSource{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref, Commit: res.Commit}); err != nil {
		return KitSourceResult{}, fmt.Errorf("sync kit %q: %w", name, err)
	}
	return res, nil
}
