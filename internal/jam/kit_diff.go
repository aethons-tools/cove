package jam

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/kittree"
)

// KitDiffResult is GET /admin/kits/{name}/diff: version From → To of kit
// Name, as kittree.DiffText renders it.
type KitDiffResult struct {
	Name string `json:"name"`
	From int    `json:"from"`
	To   int    `json:"to"`
	Text string `json:"text"`
}

// KitVersionDiff diffs kit name's version from → to (to 0 = current; from 0
// = the version before to). An unknown kit or version is 404; a kit with
// only one version, from == to, no earlier version, a broken version (its own fixed error) or a version not
// stored as a tree is 400. Only stored trees (validated on write) are
// diffed.
func KitVersionDiff(store Store, name string, from, to int) (KitDiffResult, error) {
	k, ok := store.GetKit(name)
	if !ok {
		return KitDiffResult{}, writeErr(http.StatusNotFound, "no such kit")
	}
	if to == 0 {
		to = k.Current
	}
	if from == 0 {
		for _, v := range k.VersionNumbers() {
			if v < to {
				from = v
			}
		}
	}
	if from == 0 && len(k.VersionNumbers()) == 1 {
		return KitDiffResult{}, writeErr(http.StatusBadRequest, "kit %s has only one version", name)
	}
	if from == 0 || from == to {
		return KitDiffResult{}, writeErr(http.StatusBadRequest, "kit %s: pick two different versions (--from, --to)", name)
	}
	a, okA := LoadKitVersion(store, name, from)
	b, okB := LoadKitVersion(store, name, to)
	if !okA || !okB {
		missing := from
		if okA {
			missing = to
		}
		return KitDiffResult{}, writeErr(http.StatusNotFound, "kit %s has no version v%d", name, missing)
	}
	for _, v := range []KitVersion{a, b} {
		_, broken := k.Broken[v.Version]
		switch {
		case broken:
			return KitDiffResult{}, writeErr(http.StatusBadRequest, "%s", v.Err) // the migration's fixed reason
		case v.TreeHash == "":
			return KitDiffResult{}, writeErr(http.StatusBadRequest, "kit %s v%d is not stored as a tree", name, v.Version)
		case v.Tree.Entries == nil:
			return KitDiffResult{}, writeErr(http.StatusBadRequest, "%s", v.Err) // LoadKitVersion's fixed "tree … is not stored"
		}
	}
	return KitDiffResult{Name: name, From: from, To: to, Text: kittree.DiffText(a.Tree, b.Tree, store.KitBlob)}, nil
}
