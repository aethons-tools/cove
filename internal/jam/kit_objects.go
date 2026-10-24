package jam

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	"github.com/aethons-tools/cove/internal/kittree"
)

// KitObjects are the content-addressed objects a kit write carries: trees
// and the blobs their entries name, by sha256. A store writes them in the
// same transaction as the docs that reference them.
type KitObjects struct {
	Trees []kittree.Tree
	Blobs map[string][]byte
}

func (o KitObjects) empty() bool { return len(o.Trees) == 0 && len(o.Blobs) == 0 }

// KitGC reports a GCKitObjects run: the trees and blobs it deleted.
type KitGC struct{ Trees, Blobs int }

// HasVersion reports whether the kit has version v: a tree, a pre-tree
// config or a broken one.
func (k Kit) HasVersion(v int) bool {
	_, text := k.Versions[v]
	_, tree := k.Trees[v]
	_, broken := k.Broken[v]
	return text || tree || broken
}

// VersionNumbers is every version of the kit, ascending.
func (k Kit) VersionNumbers() []int {
	set := map[int]bool{}
	for _, m := range []map[int]string{k.Versions, k.Trees, k.Broken} {
		for v := range m {
			set[v] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// oneFileKit is kitYML as a one-file tree (kit.yml, 0644) and its objects.
func oneFileKit(kitYML string) (kittree.Tree, KitObjects, error) {
	t, blobs, err := kittree.Build([]kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: []byte(kitYML)}})
	if err != nil {
		return kittree.Tree{}, KitObjects{}, err
	}
	return t, KitObjects{Trees: []kittree.Tree{t}, Blobs: blobs}, nil
}

// checkKitObjects refuses a kit write that does not hold together: a tree
// that does not validate, a blob whose bytes are not its sha256, a tree entry
// whose blob is neither written nor stored (or differs in size), or a doc
// naming a tree that is neither. Caller holds mu.
func checkKitObjects(m *memState, kits []Kit, objs KitObjects) error {
	for sha, b := range objs.Blobs {
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != sha {
			return fmt.Errorf("kit blob %.12s: content does not match its sha256", sha)
		}
	}
	written := map[string]bool{}
	for _, t := range objs.Trees {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("kit tree: %w", err)
		}
		for _, e := range t.Entries {
			b, ok := objs.Blobs[e.SHA256]
			if !ok {
				b, ok = m.kitBlobs[e.SHA256]
			}
			if !ok || int64(len(b)) != e.Size {
				return fmt.Errorf("kit tree %.12s: %s has no stored content", t.Hash(), e.Path)
			}
		}
		written[t.Hash()] = true
	}
	for _, k := range kits {
		for _, v := range slices.Sorted(maps.Keys(k.Trees)) {
			if sha := k.Trees[v]; !written[sha] {
				if _, ok := m.kitTrees[sha]; !ok {
					return fmt.Errorf("kit %s v%d: tree %.12s is not stored", k.Name, v, sha)
				}
			}
		}
	}
	return nil
}

// applyKitObjects caches copies of objs. Caller holds mu and has checked them.
func (m *memState) applyKitObjects(objs KitObjects) {
	for _, t := range objs.Trees {
		m.kitTrees[t.Hash()] = kittree.Tree{Entries: slices.Clone(t.Entries)}
	}
	for sha, b := range objs.Blobs {
		m.kitBlobs[sha] = bytes.Clone(b)
	}
}

// deadKitObjects lists, sorted, the trees no kit version references and the
// blobs no remaining tree names. Caller holds mu.
func (m *memState) deadKitObjects() (trees, blobs []string) {
	live := map[string]bool{}
	for _, k := range m.kits {
		for _, sha := range k.Trees {
			live[sha] = true
		}
	}
	liveBlobs := map[string]bool{}
	for sha, t := range m.kitTrees {
		if !live[sha] {
			trees = append(trees, sha)
			continue
		}
		for _, e := range t.Entries {
			liveBlobs[e.SHA256] = true
		}
	}
	for sha := range m.kitBlobs {
		if !liveBlobs[sha] {
			blobs = append(blobs, sha)
		}
	}
	slices.Sort(trees)
	slices.Sort(blobs)
	return trees, blobs
}

// applyDropKitObjects deletes trees and blobs from the cache. Caller holds mu.
func (m *memState) applyDropKitObjects(trees, blobs []string) {
	for _, sha := range trees {
		delete(m.kitTrees, sha)
	}
	for _, sha := range blobs {
		delete(m.kitBlobs, sha)
	}
}

// setKitTree records tree as version ver of k with meta, current, and k's
// tracked source when track is non-nil.
func setKitTree(k *Kit, ver int, tree string, meta KitVersionMeta, track *KitSource) {
	if k.Trees == nil {
		k.Trees = map[int]string{}
	}
	if k.Meta == nil {
		k.Meta = map[int]KitVersionMeta{}
	}
	k.Trees[ver] = tree
	k.Meta[ver] = copyKitMeta(meta)
	k.Current = ver
	if track != nil {
		k.Source = copyKitSource(track)
	}
}

// KitTree returns a copy of stored tree sha.
func (m *memState) KitTree(sha string) (kittree.Tree, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.kitTrees[sha]
	return kittree.Tree{Entries: slices.Clone(t.Entries)}, ok
}

// KitBlob returns a copy of stored blob sha.
func (m *memState) KitBlob(sha string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.kitBlobs[sha]
	return bytes.Clone(b), ok
}

// kitConfigLocked is version's kit.yml text (a pre-tree version's stored
// config); version 0 is current. Caller holds mu, or owns m.
func (m *memState) kitConfigLocked(name string, version int) (string, bool) {
	k, ok := m.kits[name]
	if !ok {
		return "", false
	}
	if version == 0 {
		version = k.Current
	}
	if sha, ok := k.Trees[version]; ok {
		if e, ok := m.kitTrees[sha].Lookup(kittree.KitFile); ok {
			if b, ok := m.kitBlobs[e.SHA256]; ok {
				return string(b), true
			}
		}
		return "", false
	}
	cfg, ok := k.Versions[version]
	return cfg, ok
}
