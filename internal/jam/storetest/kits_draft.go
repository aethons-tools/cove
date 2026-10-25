package storetest

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// draftObjs builds a draft tree (kit.yml not required) from path → content
// pairs and the objects that carry it.
func draftObjs(t *testing.T, kv ...string) (kittree.Tree, jam.KitObjects) {
	t.Helper()
	tr, blobs, err := kittree.BuildFiles(kittreetest.Files(kv...))
	if err != nil {
		t.Fatal(err)
	}
	return tr, jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}
}

// sameDraft compares drafts with time.Equal: Postgres reads times back in UTC.
func sameDraft(a, b jam.KitDraft) bool {
	return a.Kit == b.Kit && a.Base == b.Base && a.Tree == b.Tree && a.Actor == b.Actor &&
		a.OpenedAt.Equal(b.OpenedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

// runKitDraftConformance exercises kit drafts: the one row per kit, its
// objects, GC roots, removal with the kit, and staying out of exports.
func runKitDraftConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	meta := jam.KitVersionMeta{PushedAt: time.Unix(10, 0).UTC()}
	opened, saved := time.Unix(20, 0).UTC(), time.Unix(30, 0).UTC()
	// withKit is a fresh store holding kit app v1 (kit.yml only).
	withKit := func(t *testing.T) jam.Store {
		t.Helper()
		st := mk(t)
		tr, objs := treeObjs(t, "kit.yml", "kind: studio\n")
		if _, err := st.PushKitVersion("app", tr.Hash(), objs, meta, nil); err != nil {
			t.Fatal(err)
		}
		return st
	}
	draftOn := func(tree string) jam.KitDraft {
		return jam.KitDraft{Kit: "app", Base: 1, Tree: tree, OpenedAt: opened, UpdatedAt: saved, Actor: "local"}
	}
	t.Run("put a draft without kit.yml and read it back", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "run.sh*", "echo\n")
		d := draftOn(tr.Hash())
		if err := st.PutKitDraft(d, objs); err != nil {
			t.Fatal(err)
		}
		got, ok := st.GetKitDraft("app")
		if !ok || !sameDraft(got, d) {
			t.Fatalf("draft = %+v %v, want %+v", got, ok, d)
		}
		if back, ok := st.KitTree(tr.Hash()); !ok || back.Hash() != tr.Hash() {
			t.Fatalf("tree = %+v %v", back, ok)
		}
		if _, ok := st.GetKitDraft("other"); ok {
			t.Fatal("a draft for a kit that has none")
		}
	})
	t.Run("refuses a draft that does not hold together and writes nothing", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "a.txt", "x")
		bad := kittree.Tree{Entries: slices.Clone(tr.Entries)}
		bad.Entries[0].Mode = fs.FileMode(0o600)
		for name, tc := range map[string]struct {
			d    jam.KitDraft
			objs jam.KitObjects
		}{
			"unknown kit":   {jam.KitDraft{Kit: "nope", Base: 1, Tree: tr.Hash()}, objs},
			"unknown base":  {jam.KitDraft{Kit: "app", Base: 9, Tree: tr.Hash()}, objs},
			"unstored tree": {draftOn(tr.Hash()), jam.KitObjects{}},
			"bad mode":      {draftOn(bad.Hash()), jam.KitObjects{Trees: []kittree.Tree{bad}, Blobs: objs.Blobs}},
		} {
			err := st.PutKitDraft(tc.d, tc.objs)
			if err == nil {
				t.Fatalf("%s: accepted", name)
			}
			if name == "unknown kit" && !errors.Is(err, jam.ErrNotFound) {
				t.Fatalf("unknown kit: %v, want ErrNotFound", err)
			}
		}
		if _, ok := st.GetKitDraft("app"); ok {
			t.Fatal("a refused draft was written")
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("a refused draft's tree was stored")
		}
	})
	t.Run("an empty draft tree round-trips", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t)
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		if back, ok := st.KitTree(tr.Hash()); !ok || len(back.Entries) != 0 {
			t.Fatalf("empty tree = %+v %v", back, ok)
		}
	})
	t.Run("GC keeps a draft's objects and drops a replaced draft's", func(t *testing.T) {
		st := withKit(t)
		t1, o1 := draftObjs(t, "kit.yml", "kind: studio\n", "a.txt", "one")
		t2, o2 := draftObjs(t, "kit.yml", "kind: studio\n", "a.txt", "two")
		if err := st.PutKitDraft(draftOn(t1.Hash()), o1); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{}) {
			t.Fatalf("gc = %+v %v (the draft references its tree)", gc, err)
		}
		if err := st.PutKitDraft(draftOn(t2.Hash()), o2); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 1}) {
			t.Fatalf("gc = %+v %v (want the replaced draft tree and its a.txt)", gc, err)
		}
		if _, ok := st.KitTree(t1.Hash()); ok {
			t.Fatal("the replaced draft tree survived GC")
		}
		if err := st.RemoveKitDraft("app"); err != nil {
			t.Fatal(err)
		}
		if gc, err := st.GCKitObjects(); err != nil || gc != (jam.KitGC{Trees: 1, Blobs: 1}) {
			t.Fatalf("gc after discard = %+v %v", gc, err)
		}
		if k, _ := st.GetKit("app"); k.Current != 1 {
			t.Fatal("discarding the draft touched the kit")
		}
		if err := st.RemoveKitDraft("app"); !errors.Is(err, jam.ErrNotFound) {
			t.Fatalf("second remove = %v, want ErrNotFound", err)
		}
	})
	t.Run("removing the kit removes its draft", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "draft-only.txt", "x")
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		if err := st.RemoveKit("app"); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.GetKitDraft("app"); ok {
			t.Fatal("the draft outlived its kit")
		}
		if _, err := st.GCKitObjects(); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatal("the removed draft's tree survived GC")
		}
	})
	t.Run("a config apply removing the kit removes its draft", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "draft-only.txt", "x")
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		if err := st.ApplyConfig(jam.ConfigWrite{RemoveKits: []string{"app"}}); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.GetKitDraft("app"); ok {
			t.Fatal("the draft outlived its kit")
		}
	})
	t.Run("drafts are not exported", func(t *testing.T) {
		st := withKit(t)
		tr, objs := draftObjs(t, "draft-only.txt", "x")
		if err := st.PutKitDraft(draftOn(tr.Hash()), objs); err != nil {
			t.Fatal(err)
		}
		snap := st.ExportConfig()
		for _, x := range snap.KitTrees {
			if x.Hash() == tr.Hash() {
				t.Fatal("a draft tree was exported")
			}
		}
		e, _ := tr.Lookup("draft-only.txt")
		if _, ok := snap.KitBlobs[e.SHA256]; ok {
			t.Fatal("a draft blob was exported")
		}
		dst := mk(t)
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		if _, ok := dst.GetKitDraft("app"); ok {
			t.Fatal("an import made a draft")
		}
	})
}
