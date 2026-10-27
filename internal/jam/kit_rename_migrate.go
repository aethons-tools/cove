package jam

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/studio"
)

// KitOriginMigration is KitVersionMeta.Origin of a version the jam-rename
// kit migration wrote, so a kit's history shows it.
const KitOriginMigration = "migration"

// renamedFromAtDoc is the runbook every jam-rename refusal and condition
// points at.
const renamedFromAtDoc = "docs/usage/jam/renamed-from-at.md"

// KitRenameMigration reports a MigrateKitRename run.
type KitRenameMigration struct {
	Rewritten []string // name@vN of each version written
	Kept      []string // kits still on a pre-rename name (own or pinned): kit.migrate open
}

// MigrateKitRename is the jam rename's kit migration (renamedFromAtDoc),
// run at serve start after MigrateKitTrees. Each kit whose current tree's
// kit.yml or Dockerfiles use a pre-rename build-arg/ENV name or base
// repository gets a new version with the new names (Meta.Origin
// "migration"; a base image digest is kept); a kit pinned to a parent version
// the migration rewrote is re-pinned to the rewrite. Passes repeat (one
// PutKitDocs each) until nothing changes, so chains of any depth settle. A
// kit that cannot be rewritten keeps its version, and one pinned to a parent
// version still on an old name that the migration did not rewrite keeps its
// pin: both open kit.migrate:<name> (kitMigrateConditions). Only a store
// write error is returned. Logs carry kit names and versions only.
func MigrateKitRename(store Store, t *condition.Tracker, log *slog.Logger) (KitRenameMigration, error) {
	configMu.Lock()
	defer configMu.Unlock()
	kitMu.Lock()
	defer kitMu.Unlock()
	var rep KitRenameMigration
	failed := map[string]string{} // kit → why its rewrite failed (last pass)
	for pass := 0; pass <= len(store.ListKits()); pass++ {
		var pend []pendingKitVersion
		for _, k := range store.ListKits() {
			p, ok, err := renamedKitVersionLocked(store, k)
			if err != nil {
				failed[k.Name] = err.Error()
				continue
			}
			delete(failed, k.Name)
			if ok {
				pend = append(pend, p)
			}
		}
		if len(pend) == 0 {
			break
		}
		if err := writeKitVersionsLocked(store, pend); err != nil {
			return rep, fmt.Errorf("migrate kits to the jam names: %w", err)
		}
		for _, p := range pend {
			rep.Rewritten = append(rep.Rewritten, fmt.Sprintf("%s@v%d", p.ref.ID, p.ref.Version))
		}
	}
	for _, k := range store.ListKits() {
		if _, ok := legacyKitCondition(store, k); ok {
			rep.Kept = append(rep.Kept, k.Name)
		}
	}
	slices.Sort(rep.Kept)
	if log != nil {
		for _, name := range slices.Sorted(maps.Keys(failed)) {
			log.Warn("kit kept on its pre-rename names", "kit", name, "error", failed[name])
		}
		if len(rep.Rewritten) > 0 {
			log.Info("kits migrated to the jam names", "versions", rep.Rewritten, "kept", rep.Kept, "see", renamedFromAtDoc)
		}
	}
	kitMigrateConditions(store, t)
	return rep, nil
}

// renamedKitVersionLocked plans kit k's migration version: ok=false when its
// current version needs none (or has no tree). The caller holds kitMu.
func renamedKitVersionLocked(store Store, k Kit) (pendingKitVersion, bool, error) {
	if k.Current == 0 || k.Broken[k.Current] != "" {
		return pendingKitVersion{}, false, nil
	}
	cur, ok := LoadKitVersion(store, k.Name, 0)
	if !ok || cur.TreeHash == "" {
		return pendingKitVersion{}, false, nil
	}
	files, err := currentFilesWithKitYML(store, k.Name, cur.KitYML)
	if err != nil {
		return pendingKitVersion{}, false, err
	}
	changed := false
	for i, f := range files {
		if !legacynames.KitFileScanned(f.Path) {
			continue
		}
		if out, c := legacynames.RewriteKitText(string(f.Data)); c {
			files[i].Data, changed = []byte(out), true
		}
	}
	if repin, ok := repinTarget(store, cur); ok {
		for i, f := range files {
			if f.Path == kittree.KitFile {
				yml, err := studio.SetBaseKit(f.Data, repin)
				if err != nil {
					return pendingKitVersion{}, false, err
				}
				files[i].Data, changed = yml, true
			}
		}
	}
	if !changed {
		return pendingKitVersion{}, false, nil
	}
	tree, objs, sk, err := kitTreeOf(k.Name, files)
	if err != nil {
		return pendingKitVersion{}, false, err
	}
	ref, meta, unchanged, err := planKitTreeLocked(store, k.Name, tree, sk, KitOriginMigration)
	if err != nil || unchanged {
		return pendingKitVersion{}, false, err
	}
	return pendingKitVersion{ref: ref, meta: meta, tree: tree, objs: objs}, true, nil
}

// repinTarget is "parent@vN" when cur pins a parent version the migration
// rewrote: the parent's current version N is a migration version, and the
// pin is the version that run of migration versions was written over (or
// one of them).
func repinTarget(store Store, cur KitVersion) (string, bool) {
	if cur.Err != nil || !cur.Kit.Base.HasParent() {
		return "", false
	}
	parent, pin, err := cur.Kit.Base.ParentRef()
	if err != nil || pin == 0 {
		return "", false
	}
	pk, ok := store.GetKit(parent)
	if !ok || pin >= pk.Current || pk.Meta[pk.Current].Origin != KitOriginMigration {
		return "", false
	}
	source := pk.Current
	for source > 0 && pk.Meta[source].Origin == KitOriginMigration {
		source--
	}
	if pin < source {
		return "", false
	}
	return fmt.Sprintf("%s@v%d", parent, pk.Current), true
}

// legacyKitName is the first pre-rename name in tree's kit.yml or
// Dockerfiles, and the file it is in.
func legacyKitName(blob func(string) ([]byte, bool), tree kittree.Tree) (old, path string, ok bool) {
	for _, e := range tree.Entries {
		if !legacynames.KitFileScanned(e.Path) {
			continue
		}
		b, found := blob(e.SHA256)
		if !found {
			continue
		}
		if old, ok := legacynames.FindKitName(string(b)); ok {
			return old, e.Path, true
		}
	}
	return "", "", false
}

// legacyKitFilesErr refuses kit name's files when a kit.yml or Dockerfile
// still names a pre-rename name, naming its new one: the push-time check.
func legacyKitFilesErr(name string, files []kittree.File) error {
	for _, f := range files {
		if !legacynames.KitFileScanned(f.Path) {
			continue
		}
		if old, ok := legacynames.FindKitName(string(f.Data)); ok {
			return legacyKitNameErr(name, f.Path, old)
		}
	}
	return nil
}

// legacyKitNameErr is the refusal of kit name's file path naming old.
func legacyKitNameErr(name, path, old string) error {
	return fmt.Errorf("kit %s: %s uses %s, renamed %s (%s)", name, path, old, legacynames.NewKitName(old), renamedFromAtDoc)
}

// legacyKitCondition is kit k's kit.migrate condition when its current
// version still uses a pre-rename name, or pins an ancestor version that
// does (the migration re-pins only to versions it rewrote). Fixed texts:
// names, versions and the old and new names only.
func legacyKitCondition(store Store, k Kit) (condition.Condition, bool) {
	cur, ok := LoadKitVersion(store, k.Name, 0)
	if !ok || cur.TreeHash == "" {
		return condition.Condition{}, false
	}
	key := condition.Key(KitMigrateKind, k.Name)
	if old, path, found := legacyKitName(store.KitBlob, cur.Tree); found {
		return condition.Condition{Key: key, Severity: condition.Warning,
			Summary: fmt.Sprintf("kit %s v%d still uses the pre-rename name %s", k.Name, k.Current, old),
			Detail:  fmt.Sprintf("%s names %s; Jam now passes %s", path, old, legacynames.NewKitName(old)),
			Fix:     "rename it in the kit and push a new version (" + renamedFromAtDoc + ")"}, true
	}
	v := cur
	for range len(store.ListKits()) {
		if v.Err != nil || !v.Kit.Base.HasParent() {
			break
		}
		parent, pin, err := v.Kit.Base.ParentRef()
		if err != nil {
			break
		}
		pv, ok := LoadKitVersion(store, parent, pin)
		if !ok || pv.TreeHash == "" {
			break
		}
		if pin != 0 {
			if old, path, found := legacyKitName(store.KitBlob, pv.Tree); found {
				ref := fmt.Sprintf("%s@v%d", parent, pin)
				return condition.Condition{Key: key, Severity: condition.Warning,
					Summary: fmt.Sprintf("kit %s v%d pins %s, which still uses the pre-rename name %s", k.Name, k.Current, ref, old),
					Detail:  fmt.Sprintf("%s of %s names %s; Jam now passes %s", path, ref, old, legacynames.NewKitName(old)),
					Fix:     "pin it to a version of " + parent + " without pre-rename names (jam kit pin) (" + renamedFromAtDoc + ")"}, true
			}
		}
		v = pv
	}
	return condition.Condition{}, false
}
