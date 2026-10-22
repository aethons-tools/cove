package jam

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/studio"
)

// A kit set is a git-imported kit together with its path-parent ancestors
// (base.kit values starting with "."), all from the same checkout at the same
// commit. It imports as one unit: root first, each path reference rewritten to
// the exact version its parent ends at (name@vN), every kit validated against
// the set's pending versions, and everything written in one PutKitDocs — or
// nothing at all.

// kitSetEntry is one kit of a set, as planned.
type kitSetEntry struct {
	Name      string
	Dir       string           // source dir in the checkout ("" = the root)
	Kit       studio.StudioKit // with path refs rewritten to name@vN
	Version   int              // the version this import leaves it at
	Unchanged bool

	text   string        // Kit's canonical JSON, the stored config
	parent *KitParentRef // the parent Version is resolved against; nil = none
}

// planKitSet loads src.Dir's kit and its path ancestors from root (root first),
// assigns names, applies the refusals, rewrites path refs, and validates every
// entry against an overlay of store holding the set's pending versions.
func planKitSet(store Store, root string, src gitsource.Source, name string) ([]kitSetEntry, error) {
	set, err := loadKitSet(root, src, name)
	if err != nil {
		return nil, err
	}
	return resolveKitSet(store, set, src.Repo)
}

// loadKitSet is the checkout half of planKitSet: it loads the top kit (named
// name, else its hint, else the repo's base name) and walks its path parents,
// returning the set root first with path refs as written. Each ancestor is
// named by its kit.yml name:, else its directory, else the repo. A path that
// leaves the checkout or has no kit.yml, a cycle, a set longer than
// MaxKitChain and two kits sharing a name are refused (400).
func loadKitSet(root string, src gitsource.Source, name string) ([]kitSetEntry, error) {
	sk, hint, err := gitsource.Load(root, src.Dir)
	if err != nil {
		// The kit in the repo is unusable: the operator's input, not a
		// fetch failure.
		return nil, writeErr(http.StatusBadRequest, "%s", err)
	}
	if name == "" {
		name = hint
	}
	if name == "" {
		name = repoBase(src.Repo)
	}
	if err := sk.CheckName(name); err != nil {
		return nil, writeErr(http.StatusBadRequest, "%s", err)
	}
	set := []kitSetEntry{{Name: name, Dir: src.Dir, Kit: sk}} // child → parent
	for {
		child := set[len(set)-1]
		p, isPath := child.Kit.Base.ParentPath()
		if !isPath {
			break
		}
		dir := path.Join(checkoutDir(child.Dir), p)
		names := make([]string, len(set))
		for i, e := range set {
			names[i] = e.Name
		}
		for _, e := range set {
			if checkoutDir(e.Dir) == dir {
				return nil, writeErr(http.StatusBadRequest, "cycle: %s", strings.Join(append(names, e.Name), " → "))
			}
		}
		psk, phint, err := gitsource.LoadDir(root, dir)
		switch {
		case errors.Is(err, gitsource.ErrOutsideCheckout):
			return nil, writeErr(http.StatusBadRequest, "parent path %s (of %s) leaves the checkout", p, child.Name)
		case errors.Is(err, gitsource.ErrNoKitFile):
			return nil, writeErr(http.StatusBadRequest, "parent path %s (of %s) has no kit.yml", p, child.Name)
		case err != nil:
			return nil, writeErr(http.StatusBadRequest, "%s", err)
		}
		pname := phint
		if pname == "" {
			pname = repoBase(src.Repo)
		}
		if err := psk.CheckName(pname); err != nil {
			return nil, writeErr(http.StatusBadRequest, "%s", err)
		}
		if len(set) >= MaxKitChain {
			return nil, writeErr(http.StatusBadRequest, "kit chain too deep (max %d): %s", MaxKitChain, strings.Join(append(names, pname), " → "))
		}
		for _, e := range set {
			if e.Name == pname {
				return nil, writeErr(http.StatusBadRequest, "kit name %s is used by both %s and %s in this set", pname, checkoutDir(e.Dir), dir)
			}
		}
		if dir == "." {
			dir = "" // a KitSource spells the root ""
		}
		set = append(set, kitSetEntry{Name: pname, Dir: dir, Kit: psk})
	}
	slices.Reverse(set)
	return set, nil
}

// checkoutDir is a source dir as a clean slash path, the root spelled ".".
func checkoutDir(dir string) string { return path.Clean("./" + dir) }

// sameSource reports whether s is repo at dir (any ref).
func sameSource(s *KitSource, repo, dir string) bool {
	return s != nil && s.Repo == repo && checkoutDir(s.Dir) == checkoutDir(dir)
}

// resolveKitSet is the store half of planKitSet, run under kitMu when
// applying: root first, it rewrites each path ref to name@v<N> of the entry
// before it, refuses an ancestor that is not this source's to write
// (checkSetAncestor), validates the entry through an overlay of store holding
// the set's pending versions, and assigns its version: the current one when
// the stored config is identical, else the next. set is not modified.
func resolveKitSet(store Store, set []kitSetEntry, repo string) ([]kitSetEntry, error) {
	return resolveKitEntries(store, set, func(i int, e kitSetEntry) error {
		if i < len(set)-1 {
			return checkSetAncestor(store, e.Name, repo, e.Dir)
		}
		return nil
	})
}

// resolveKitEntries is resolveKitSet for any parents-first list: each path
// ref is rewritten to name@v<N> of the earlier entry at that dir; check (nil =
// none) vets entry i before it is validated.
func resolveKitEntries(store Store, set []kitSetEntry, check func(i int, e kitSetEntry) error) ([]kitSetEntry, error) {
	out := slices.Clone(set)
	ov := &overlayStore{Store: store, pending: map[string]kitSetEntry{}}
	for i := range out {
		e := &out[i]
		if p, isPath := e.Kit.Base.ParentPath(); isPath {
			want := path.Join(checkoutDir(e.Dir), p)
			j := slices.IndexFunc(out[:i], func(o kitSetEntry) bool { return checkoutDir(o.Dir) == want })
			if j < 0 {
				return nil, writeErr(http.StatusBadRequest, "parent path %s (of %s) is not in this set", p, e.Name)
			}
			e.Kit.Base.Kit = fmt.Sprintf("%s@v%d", out[j].Name, out[j].Version)
		}
		if check != nil {
			if err := check(i, *e); err != nil {
				return nil, err
			}
		}
		text, chain, err := validateKit(ov, e.Name, e.Kit)
		if err != nil {
			return nil, err
		}
		e.text, e.parent = text, chainParent(chain)
		k, _ := store.GetKit(e.Name)
		if cur, ok := store.KitConfig(e.Name, k.Current); ok && k.Current != 0 && cur == text {
			e.Version, e.Unchanged = k.Current, true
		} else {
			e.Version, e.Unchanged = nextKitVersion(k), false
		}
		ov.pending[e.Name] = *e
	}
	return out, nil
}

// checkSetAncestor refuses (409) to write a path parent's name over a kit that
// is not from repo at dir: one tracking another source, one never imported
// from this one, or one with a local push newer than its last import from it.
// An unknown name is free.
func checkSetAncestor(store Store, name, repo, dir string) error {
	k, ok := store.GetKit(name)
	if !ok {
		return nil
	}
	from := repo
	if d := checkoutDir(dir); d != "." {
		from += "//" + d
	}
	last := 0
	for v, m := range k.Meta {
		if sameSource(m.Source, repo, dir) && v > last {
			last = v
		}
	}
	if (k.Source != nil && !sameSource(k.Source, repo, dir)) || last == 0 {
		return writeErr(http.StatusConflict, "kit %s exists and is not from %s; rename the kit or untrack it first", name, from)
	}
	for v := range k.Versions {
		if m, ok := k.Meta[v]; v > last && (!ok || m.Source == nil) {
			return writeErr(http.StatusConflict, "kit %s has a local push newer than its last import from %s; re-push or untrack it first", name, from)
		}
	}
	return nil
}

// overlayStore is store with a kit set's pending versions laid over it: a
// pending kit's GetKit has the pending version as current, and KitConfig
// serves its config. Everything else reads through.
type overlayStore struct {
	Store
	pending map[string]kitSetEntry
}

func (o *overlayStore) GetKit(name string) (Kit, bool) {
	k, ok := o.Store.GetKit(name)
	e, has := o.pending[name]
	if !has {
		return k, ok
	}
	k = copyKit(k)
	k.Name, k.Current = name, e.Version
	k.Versions[e.Version] = e.text
	return k, true
}

func (o *overlayStore) KitConfig(name string, version int) (string, bool) {
	if e, has := o.pending[name]; has && version == e.Version {
		return e.text, true
	}
	return o.Store.KitConfig(name, version)
}

// buildKitDocs is the new doc of each kit the planned set changes, root
// first: a new version with its own provenance {repo, dir, ref, commit} and
// parent, and the tracked source moved to this commit when track is set or
// the kit already tracks this repo, dir and ref (an ancestor tracking this repo
// and dir at another ref keeps that tracking). An unchanged kit whose
// tracked source does not move is left out.
func buildKitDocs(store Store, entries []kitSetEntry, src gitsource.Source, commit string, now time.Time, track bool) []Kit {
	var docs []Kit
	for i, e := range entries {
		at := KitSource{Repo: src.Repo, Dir: e.Dir, Ref: src.Ref, Commit: commit}
		k, ok := store.GetKit(e.Name)
		var tracked *KitSource
		sameRef := ok && sameSource(k.Source, src.Repo, e.Dir) && k.Source.Ref == src.Ref
		// An ancestor tracking this repo and dir at another ref keeps that
		// tracking; only the top kit is retargeted by a track through this ref.
		otherRef := ok && sameSource(k.Source, src.Repo, e.Dir) && k.Source.Ref != src.Ref
		if sameRef || (track && (i == len(entries)-1 || !otherRef)) {
			tracked = &at
		}
		if e.Unchanged && (tracked == nil || (k.Source != nil && *k.Source == *tracked)) {
			continue
		}
		doc := copyKit(k)
		doc.Name = e.Name
		if !e.Unchanged {
			if doc.Meta == nil {
				doc.Meta = map[int]KitVersionMeta{}
			}
			doc.Versions[e.Version] = e.text
			doc.Meta[e.Version] = KitVersionMeta{PushedAt: now, Source: copyKitSource(&at), Parent: e.parent}
			doc.Current = e.Version
		}
		if tracked != nil {
			doc.Source = copyKitSource(tracked)
		}
		docs = append(docs, doc)
	}
	return docs
}

// kitSetResult reports a planned set: the top kit, with its ancestors in Also,
// root first.
func kitSetResult(entries []kitSetEntry, commit string) KitSourceResult {
	one := func(e kitSetEntry) KitSourceResult {
		return KitSourceResult{Name: e.Name, Version: e.Version, Unchanged: e.Unchanged, Commit: commit}
	}
	res := one(entries[len(entries)-1])
	for _, e := range entries[:len(entries)-1] {
		res.Also = append(res.Also, one(e))
	}
	return res
}
