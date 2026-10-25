package storetest

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// treeObjs builds a tree from path → content pairs (a "*" suffix on the path
// makes it 0755) and the objects that carry it.
func treeObjs(t *testing.T, kv ...string) (kittree.Tree, jam.KitObjects) {
	t.Helper()
	tr, blobs := kittreetest.Tree(t, kv...)
	return tr, jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}
}

// kitYML is version's kit.yml text as LoadKitVersion reads it (0 = current).
func kitYML(st jam.Store, name string, version int) (string, bool) {
	v, ok := jam.LoadKitVersion(st, name, version)
	return string(v.KitYML), ok && v.KitYML != nil
}

// runKitTreeConformance exercises kit trees, blobs, their atomic writes and GC.
func runKitTreeConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	meta := jam.KitVersionMeta{PushedAt: time.Unix(10, 0).UTC()}
	t.Run("push a tree version and read it back", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
		v, err := st.PushKitVersion("app", tr.Hash(), objs, meta, nil)
		if err != nil || v != 1 {
			t.Fatalf("push = %d, %v", v, err)
		}
		k, _ := st.GetKit("app")
		if k.Current != 1 || k.Trees[1] != tr.Hash() || !k.HasVersion(1) || len(k.VersionNumbers()) != 1 {
			t.Fatalf("doc = %+v", k)
		}
		got, ok := st.KitTree(tr.Hash())
		if !ok || got.Hash() != tr.Hash() {
			t.Fatalf("tree = %+v %v", got, ok)
		}
		e, _ := got.Lookup("run.sh")
		b, ok := st.KitBlob(e.SHA256)
		if !ok || string(b) != "echo\n" || e.Mode != 0o755 {
			t.Fatalf("blob = %q %v", b, ok)
		}
		b[0] = 'X'
		if again, _ := st.KitBlob(e.SHA256); !bytes.Equal(again, []byte("echo\n")) {
			t.Fatal("KitBlob returned an aliased slice")
		}
		if cfg, ok := kitYML(st, "app", 1); !ok || cfg != "kind: studio\n" {
			t.Fatalf("kit.yml = %q %v", cfg, ok)
		}
	})
	t.Run("push kit stores a one-file tree", func(t *testing.T) {
		st := mk(t)
		if _, err := st.PushKit("web", `{"kind":"studio"}`); err != nil {
			t.Fatal(err)
		}
		k, _ := st.GetKit("web")
		tr, ok := st.KitTree(k.Trees[1])
		if !ok || len(tr.Entries) != 1 || tr.Entries[0].Path != kittree.KitFile || len(k.Versions) != 0 {
			t.Fatalf("doc %+v tree %+v", k, tr)
		}
	})
	t.Run("refuses a doc naming an unstored tree and writes nothing", func(t *testing.T) {
		st := mk(t)
		tr, _ := treeObjs(t, "kit.yml", "x")
		if _, err := st.PushKitVersion("app", tr.Hash(), jam.KitObjects{}, meta, nil); err == nil {
			t.Fatal("an unstored tree must be refused")
		}
		if _, ok := st.GetKit("app"); ok {
			t.Fatal("the doc was written")
		}
	})
	t.Run("refuses a blob that is not its hash", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "x")
		for sha := range objs.Blobs {
			objs.Blobs[sha] = []byte("tampered")
		}
		if _, err := st.PushKitVersion("app", tr.Hash(), objs, meta, nil); err == nil {
			t.Fatal("a tampered blob must be refused")
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("the tree was stored")
		}
	})
	t.Run("identical trees share objects and GC keeps what is referenced", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n", "Dockerfile", "FROM x\n")
		for _, n := range []string{"a", "b"} {
			if _, err := st.PushKitVersion(n, tr.Hash(), objs, meta, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.RemoveKit("a"); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{}) {
			t.Fatalf("gc = %+v %v (b still references the tree)", gc, err)
		}
		if err := st.RemoveKit("b"); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 2}) {
			t.Fatalf("gc = %+v %v", gc, err)
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("an unreferenced tree survived GC")
		}
	})
	t.Run("put kit docs writes objects with the docs, all or nothing", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		good := jam.Kit{Name: "a", Current: 1, Trees: map[int]string{1: tr.Hash()}, Meta: map[int]jam.KitVersionMeta{1: meta}}
		bad := jam.Kit{Name: "b", Current: 1, Trees: map[int]string{1: strings.Repeat("0", 64)}}
		if err := st.PutKitDocs([]jam.Kit{good, bad}, objs); err == nil {
			t.Fatal("a doc naming an unknown tree must refuse the call")
		}
		if _, ok := st.GetKit("a"); ok {
			t.Fatal("a was written although the call failed")
		}
		if err := st.PutKitDocs([]jam.Kit{good}, objs); err != nil {
			t.Fatal(err)
		}
		if cfg, ok := kitYML(st, "a", 0); !ok || cfg != "kind: studio\n" {
			t.Fatalf("kit.yml = %q %v", cfg, ok)
		}
	})
	t.Run("apply config writes kit objects", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		w := jam.ConfigWrite{Kits: []jam.Kit{{Name: "a", Current: 1, Trees: map[int]string{1: tr.Hash()}}}, KitObjects: objs}
		if err := st.ApplyConfig(w); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.KitTree(tr.Hash()); !ok {
			t.Fatal("tree not stored")
		}
	})
	t.Run("pin and versions span text, tree and broken versions", func(t *testing.T) {
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		doc := jam.Kit{Name: "m", Current: 3, Versions: map[int]string{1: `{"kind":"studio"}`}, Broken: map[int]string{2: "unreadable"}, Trees: map[int]string{3: tr.Hash()}}
		if err := st.PutKitDocs([]jam.Kit{doc}, objs); err != nil {
			t.Fatal(err)
		}
		k, _ := st.GetKit("m")
		if got := k.VersionNumbers(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
			t.Fatalf("versions = %v", got)
		}
		if err := st.PinKit("m", 1); err != nil {
			t.Fatal(err)
		}
		if v, err := st.PushKit("m", `{"kind":"studio","prompt":"x"}`); err != nil || v != 4 {
			t.Fatalf("next version = %d %v", v, err)
		}
	})
	t.Run("GC drops a tree version a whole-doc write removed", func(t *testing.T) {
		st := mk(t)
		t1, o1 := treeObjs(t, "kit.yml", "kind: studio\n", "old.sh", "old\n")
		t2, o2 := treeObjs(t, "kit.yml", "kind: studio\n", "new.sh", "new\n")
		for _, o := range []jam.KitObjects{o1, o2} {
			if _, err := st.PushKitVersion("app", o.Trees[0].Hash(), o, meta, nil); err != nil {
				t.Fatal(err)
			}
		}
		k, _ := st.GetKit("app")
		delete(k.Trees, 1)
		delete(k.Meta, 1)
		if err := st.PutKitDocs([]jam.Kit{k}, jam.KitObjects{}); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 1}) {
			t.Fatalf("gc = %+v %v (want the old tree and old.sh)", gc, err)
		}
		if _, ok := st.KitTree(t1.Hash()); ok {
			t.Fatal("the dropped version's tree survived")
		}
		if _, ok := st.KitTree(t2.Hash()); !ok {
			t.Fatal("the current tree was collected")
		}
	})
	t.Run("import drops objects no kit references", func(t *testing.T) {
		src := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		if _, err := src.PushKitVersion("app", tr.Hash(), objs, meta, nil); err != nil {
			t.Fatal(err)
		}
		snap := src.ExportConfig()
		stray, strayObjs := treeObjs(t, "kit.yml", "stray\n")
		snap.KitTrees = append(snap.KitTrees, stray)
		for sha, b := range strayObjs.Blobs {
			snap.KitBlobs[sha] = b
		}
		dst := mk(t)
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		if _, ok := dst.KitTree(stray.Hash()); ok {
			t.Fatal("an unreferenced tree was imported")
		}
		for sha := range strayObjs.Blobs {
			if _, ok := dst.KitBlob(sha); ok {
				t.Fatal("an unreferenced blob was imported")
			}
		}
		if cfg, ok := kitYML(dst, "app", 0); !ok || cfg != "kind: studio\n" {
			t.Fatalf("kit.yml = %q %v", cfg, ok)
		}
		if gc, err := dst.GCKitObjects(); err != nil || gc != (jam.KitGC{}) {
			t.Fatalf("gc after import = %+v %v", gc, err)
		}
	})
	t.Run("export and import carry trees and blobs", func(t *testing.T) {
		src := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
		if _, err := src.PushKitVersion("app", tr.Hash(), objs, meta, nil); err != nil {
			t.Fatal(err)
		}
		snap := src.ExportConfig()
		if snap.Version != jam.ConfigSnapshotVersion || len(snap.KitTrees) != 1 || len(snap.KitBlobs) != 2 {
			t.Fatalf("snapshot v%d trees %d blobs %d", snap.Version, len(snap.KitTrees), len(snap.KitBlobs))
		}
		dst := mk(t)
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		e, _ := tr.Lookup("run.sh")
		if b, ok := dst.KitBlob(e.SHA256); !ok || string(b) != "echo\n" {
			t.Fatalf("blob after import = %q %v", b, ok)
		}
	})
}
