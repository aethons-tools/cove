package jam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/kittree"
)

// KitMigrateKind is the condition kind of a kit whose current version the
// migration kept broken (kit.migrate:<name>).
const KitMigrateKind = "kit.migrate"

// KitTreeMigration reports a MigrateKitTrees run.
type KitTreeMigration struct {
	Migrated []string // kits whose pre-tree versions became trees
	Broken   []string // name@vN of each version kept broken
}

// MigrateKitTrees is the one-time kit migration
// (docs/usage/jam/kits-directory.md#stored-as-trees). It runs at every serve
// start and after a config import. Every kit still holding pre-tree config
// text (Kit.Versions) gets each version as a tree (legacyKitTree), or, for
// one whose context cannot be read, a Broken reason; the text moves to
// Kit.Legacy.
//
// A managed kit's config mark that recorded its current version's old
// canonical form (legacyKitCanonDigest) is re-keyed to the new one, so the
// upgrade shows no drift: the tree's listing (kitCanon), or for a version
// kept broken its "# broken:" canon. A mark that recorded anything else (the
// kit was edited in Jam before the upgrade) is left, still drifted.
// Everything goes in one store.ApplyConfig, logged "kits migrated to trees";
// a store with nothing to migrate is neither written nor logged.
//
// Then the kit.migrate conditions are re-evaluated (t may be nil): open for
// each kit whose current version is broken, cleared for every other.
func MigrateKitTrees(store Store, t *condition.Tracker, log *slog.Logger) (KitTreeMigration, error) {
	configMu.Lock()
	defer configMu.Unlock()
	return migrateKitTreesLocked(store, t, log)
}

// migrateKitTreesLocked is MigrateKitTrees for a caller holding configMu.
func migrateKitTreesLocked(store Store, t *condition.Tracker, log *slog.Logger) (KitTreeMigration, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	var rep KitTreeMigration
	w := ConfigWrite{KitObjects: KitObjects{Blobs: map[string][]byte{}}}
	trees := map[string]kittree.Tree{}
	marks := map[ConfigKey]ConfigMark{}
	for _, mk := range store.ListConfigMarks() {
		marks[mk.Key()] = mk
	}
	for _, k := range store.ListKits() {
		if len(k.Versions) == 0 {
			continue
		}
		doc := copyKit(k)
		if doc.Trees == nil {
			doc.Trees = map[int]string{}
		}
		doc.Legacy = maps.Clone(k.Versions)
		for _, v := range slices.Sorted(maps.Keys(k.Versions)) {
			tree, blobs, err := legacyKitTree(k.Versions[v])
			if err != nil {
				if doc.Broken == nil {
					doc.Broken = map[int]string{}
				}
				doc.Broken[v] = err.Error()
				rep.Broken = append(rep.Broken, fmt.Sprintf("%s@v%d", k.Name, v))
				continue
			}
			doc.Trees[v] = tree.Hash()
			trees[tree.Hash()] = tree
			maps.Copy(w.KitObjects.Blobs, blobs)
		}
		doc.Versions = nil
		w.Kits = append(w.Kits, doc)
		rep.Migrated = append(rep.Migrated, k.Name)
		if mk, ok := rekeyedKitMark(marks, k, doc, trees, w.KitObjects.Blobs); ok {
			w.Marks = append(w.Marks, mk)
		}
	}
	if len(w.Kits) == 0 {
		kitMigrateConditions(store, t)
		return rep, nil
	}
	for _, sha := range slices.Sorted(maps.Keys(trees)) {
		w.KitObjects.Trees = append(w.KitObjects.Trees, trees[sha])
	}
	if err := store.ApplyConfig(w); err != nil {
		return rep, fmt.Errorf("migrate kits to trees: %w", err)
	}
	if log != nil {
		log.Info("kits migrated to trees", "kits", len(rep.Migrated), "broken", rep.Broken)
	}
	kitMigrateConditions(store, t)
	return rep, nil
}

// rekeyedKitMark is pre-tree kit k's managed mark re-keyed to its migrated
// doc's current version; ok=false when k is not managed, its current version
// was not pre-tree text, or the mark recorded something else.
func rekeyedKitMark(marks map[ConfigKey]ConfigMark, k, doc Kit, trees map[string]kittree.Tree, blobs map[string][]byte) (ConfigMark, bool) {
	mk, managed := marks[ConfigKey{ConfigKindKit, k.Name}]
	cur, hasCur := k.Versions[k.Current]
	if !managed || !hasCur || (mk.Digest != legacyKitCanonDigest(cur, k.Source)) {
		return ConfigMark{}, false
	}
	canon := brokenKitCanon(doc.Broken[k.Current])
	if tree, ok := trees[doc.Trees[k.Current]]; ok {
		canon = kitCanon(tree, kittree.MapBlobs(blobs), k.Source)
	}
	mk.Digest = configObject{Canon: canon}.Digest()
	return mk, true
}

// legacyKitCanonDigest is the digest a released (pre-tree) Jam's config
// mark recorded for a kit: sha256 of kitCanon as it was there (frozen copy
// of ddd02da's config_export.go): the stored config text, then for a
// tracked kit "\n# source: <repo>[//dir]@<ref>\n".
func legacyKitCanonDigest(config string, tracked *KitSource) string {
	return sha256Hex(legacyKitCanonSuffix(config, tracked))
}

// legacyKitCanonSuffix is config with the old kitCanon's source line.
func legacyKitCanonSuffix(config string, tracked *KitSource) string {
	if tracked == nil {
		return config
	}
	src := gitsource.Source{Repo: tracked.Repo, Dir: tracked.Dir, Ref: tracked.Ref}
	return config + "\n# source: " + src.String() + "\n"
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// kitMigrateConditions opens kit.migrate:<name> for each kit whose current
// version is broken and clears every other open one. Its texts are fixed:
// the kit's name and version, and the reason the migration recorded. The
// migration, the import and every RunConfigDriftCheck round call it.
func kitMigrateConditions(store Store, t *condition.Tracker) {
	open := map[string]bool{}
	for _, k := range store.ListKits() {
		reason, broken := k.Broken[k.Current]
		if !broken {
			continue
		}
		key := condition.Key(KitMigrateKind, k.Name)
		open[key] = true
		t.Raise(condition.Condition{Key: key, Severity: condition.Warning,
			Summary: fmt.Sprintf("kit %s v%d could not be migrated to a tree", k.Name, k.Current),
			Detail:  reason,
			Fix:     "push the kit as a directory (a Dockerfile next to kit.yml), or pin a version that migrated (at-jam kit pin)"})
	}
	for _, key := range t.OpenKeys(KitMigrateKind) {
		if !open[key] {
			t.Clear(key)
		}
	}
}
