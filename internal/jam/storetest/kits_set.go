package storetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// runKitSetConformance exercises the atomic multi-kit doc write.
func runKitSetConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	doc := func(name string, cur int, cfgs ...string) jam.Kit {
		k := jam.Kit{Name: name, Current: cur, Versions: map[int]string{}, Meta: map[int]jam.KitVersionMeta{}}
		for i, c := range cfgs {
			k.Versions[i+1] = c
			k.Meta[i+1] = jam.KitVersionMeta{PushedAt: time.Unix(int64(100+i), 0).UTC()}
		}
		return k
	}
	t.Run("put kit docs writes every kit with deep copies", func(t *testing.T) {
		st := mk(t)
		src := &jam.KitSource{Repo: "https://github.com/acme/kits", Dir: "a", Ref: "main"}
		a := doc("a", 2, `{"kind":"studio"}`, `{"kind":"studio","prompt":"x"}`)
		a.Source = src
		b := doc("b", 1, `{"kind":"studio"}`)
		if err := st.PutKitDocs([]jam.Kit{a, b}); err != nil {
			t.Fatal(err)
		}
		ga, ok := st.GetKit("a")
		if !ok || !reflect.DeepEqual(ga, a) {
			t.Fatalf("a = %+v ok=%v", ga, ok)
		}
		if gb, ok := st.GetKit("b"); !ok || !reflect.DeepEqual(gb, b) {
			t.Fatalf("b = %+v ok=%v", gb, ok)
		}
		// Mutating the inputs afterwards must not reach the store.
		a.Versions[1] = "mutated"
		a.Source.Ref = "mutated"
		ga, _ = st.GetKit("a")
		if ga.Versions[1] != `{"kind":"studio"}` || ga.Source.Ref != "main" {
			t.Fatalf("store aliased the input: %+v", ga)
		}
		// Replaces the whole doc, not a merge.
		if err := st.PutKitDocs([]jam.Kit{doc("a", 1, `{"kind":"studio","prompt":"y"}`)}); err != nil {
			t.Fatal(err)
		}
		if ga, _ = st.GetKit("a"); len(ga.Versions) != 1 || ga.Source != nil || ga.Current != 1 {
			t.Fatalf("whole-doc replace: %+v", ga)
		}
	})
	t.Run("put kit docs with an empty slice is a no-op", func(t *testing.T) {
		st := mk(t)
		if _, err := st.PushKit("a", `{"kind":"studio"}`); err != nil {
			t.Fatal(err)
		}
		before := st.ListKits()
		if err := st.PutKitDocs(nil); err != nil {
			t.Fatal(err)
		}
		if err := st.PutKitDocs([]jam.Kit{}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(st.ListKits(), before) {
			t.Fatal("empty PutKitDocs changed the store")
		}
	})
	t.Run("put kit docs refuses an empty name and writes nothing", func(t *testing.T) {
		st := mk(t)
		if err := st.PutKitDocs([]jam.Kit{doc("a", 1, `{"kind":"studio"}`), doc("", 1, `{"kind":"studio"}`)}); err == nil {
			t.Fatal("empty name must be refused")
		}
		if _, ok := st.GetKit("a"); ok {
			t.Fatal("a was written although the call failed")
		}
		if len(st.ListKits()) != 0 {
			t.Fatalf("kits = %+v", st.ListKits())
		}
	})
}
