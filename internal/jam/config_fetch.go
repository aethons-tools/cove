package jam

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// ConfigKitSourceLimit caps the distinct kit sources (repo and ref) one
// config tree's kits/<name>.source.yml files may name: each is a fetch.
const ConfigKitSourceLimit = 16

// ConfigKitGroup is one checkout FetchConfigKits loaded .source.yml kits from.
type ConfigKitGroup struct {
	Source gitsource.Source // repo and ref (no dir)
	Commit string
}

// kitGroupKey names the checkout a kit source loads from: its repo and ref.
func kitGroupKey(src gitsource.Source) string {
	return gitsource.Source{Repo: src.Repo, Ref: src.Ref}.String()
}

// FetchConfigKits fetches tree's kits/<name>.source.yml kits before any plan:
// one checkout per distinct repo and ref, one at a time, each kit's set
// (the kit and its path parents) loaded from it with loadKitSet, then merged
// with the inline kits into tree.Kits, parents first. The caller holds
// configMu and no other lock (a fetch can take a minute). Every failure is a
// refusal at the .source.yml (a fetch error is the fetcher's redacted text)
// and marks its repo and ref in tree.KitGroupsFailed; a set with a kit name
// already loaded from elsewhere is skipped whole. A second call is a no-op.
func FetchConfigKits(ctx context.Context, f KitFetcher, tree *ConfigTree) {
	if tree.KitsFetched {
		return
	}
	tree.KitsFetched = true
	if len(tree.KitSources) == 0 {
		return
	}
	tree.KitGroups = map[string]ConfigKitGroup{}
	tree.KitGroupsFailed = map[string]bool{}
	refuse := func(format string, a ...any) { tree.Refusals = append(tree.Refusals, fmt.Sprintf(format, a...)) }
	defer func() {
		slices.Sort(tree.Refusals)
		tree.Refusals = slices.Compact(tree.Refusals)
	}()
	groups := map[string][]string{} // group key → kit names, sorted
	for _, n := range slices.Sorted(maps.Keys(tree.KitSources)) {
		k := kitGroupKey(tree.KitSources[n].Source)
		groups[k] = append(groups[k], n)
	}
	failAll := func() {
		for g := range groups {
			tree.KitGroupsFailed[g] = true
		}
	}
	if len(groups) > ConfigKitSourceLimit {
		refuse("kits: %d kit sources (distinct repo and ref); at most %d per config repo", len(groups), ConfigKitSourceLimit)
		failAll()
		return
	}
	if f == nil {
		for _, ref := range tree.KitSources {
			refuse("%s: kit sources cannot be fetched here", ref.Path)
		}
		failAll()
		return
	}
	type at struct{ group, dir string }
	byDir := map[at]kitSetEntry{}
	byName := map[string]at{}
	from := map[string]string{} // kit name → the file that brought it in
	// addSet adds set (from file, in group) unless one of its kits' names is
	// already loaded from another checkout or dir, or its dir under another
	// name: then it refuses each collision and adds none of the set, so no
	// child loses its parent and no kit is silently dropped.
	addSet := func(set []kitSetEntry, group, file string) bool {
		ok := true
		for _, e := range set {
			if prev, seen := byName[e.Name]; seen && prev != (at{group, checkoutDir(e.Dir)}) {
				refuse("%s: kit %s is also %s", file, e.Name, from[e.Name])
				ok = false
			}
			if prev, seen := byDir[at{group, checkoutDir(e.Dir)}]; seen && prev.Name != e.Name {
				refuse("%s: kit %s is the same kit as %s (%s)", file, e.Name, prev.Name, from[prev.Name])
				ok = false
			}
		}
		if !ok {
			return false
		}
		for _, e := range set {
			k := at{group, checkoutDir(e.Dir)}
			if _, seen := byName[e.Name]; seen {
				continue // the same kit from the same checkout: loaded once
			}
			e.Group = group
			byName[e.Name], from[e.Name], byDir[k] = k, file, e
		}
		return true
	}
	for _, e := range tree.Kits {
		addSet([]kitSetEntry{e}, "", "kits/"+e.Name+"/kit.yml")
	}
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		names := groups[g]
		first := tree.KitSources[names[0]].Source
		gsrc := gitsource.Source{Repo: first.Repo, Ref: first.Ref}
		sets := map[string][]kitSetEntry{}
		var commit string
		err := f.Fetch(ctx, gsrc, func(root, c string) error {
			commit = c
			for _, n := range names {
				ref := tree.KitSources[n]
				set, err := loadKitSet(root, ref.Source, n)
				if err != nil {
					refuse("%s: %s", ref.Path, err)
					tree.KitGroupsFailed[g] = true
					continue
				}
				if msg := checkSourceKitSet(ref.Path, set); msg != "" {
					refuse("%s", msg)
					tree.KitGroupsFailed[g] = true
					continue
				}
				sets[n] = set
			}
			return nil
		})
		if err != nil {
			for _, n := range names {
				refuse("%s: %s", tree.KitSources[n].Path, err)
			}
			tree.KitGroupsFailed[g] = true
			continue
		}
		tree.KitGroups[g] = ConfigKitGroup{Source: gsrc, Commit: commit}
		for _, n := range names {
			ref := tree.KitSources[n]
			ref.Commit = commit
			tree.KitSources[n] = ref
			if set, ok := sets[n]; ok && !addSet(set, g, ref.Path) {
				tree.KitGroupsFailed[g] = true
			}
		}
	}
	ordered, cycle := orderKitEntries(slices.Collect(maps.Values(byDir)))
	if len(cycle) > 0 {
		refuse("kits: cycle among %s", strings.Join(cycle, ", "))
		for _, n := range cycle {
			if g := byName[n].group; g != "" {
				tree.KitGroupsFailed[g] = true
			}
		}
	}
	tree.Kits = ordered
}

// checkSourceKitSet is a fetched set's refusal, "" = none: a root pinning a
// registry parent's version (versions are per-Jam), as for inline kits.
func checkSourceKitSet(file string, set []kitSetEntry) string {
	root := set[0]
	if _, isPath := root.Kit.Base.ParentPath(); !isPath && root.Kit.Base.HasParent() {
		if _, v, err := root.Kit.Base.ParentRef(); err == nil && v > 0 {
			return fmt.Sprintf("%s: kit %s's base.kit %s pins a version; versions are per-Jam — name the kit or use a path parent", file, root.Name, root.Kit.Base.Kit)
		}
	}
	return ""
}
