package storetest

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// runKitSourceConformance exercises kit version provenance and the tracked
// source.
func runKitSourceConformance(t *testing.T, mk func(t *testing.T) jam.Store) {
	t.Run("push records meta", func(t *testing.T) {
		st := mk(t)
		at := time.Unix(1_000_000, 0).UTC()
		src := &jam.KitSource{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main", Commit: strings.Repeat("a", 40)}
		v, err := st.PushKitMeta("web", `{"kind":"studio"}`, jam.KitVersionMeta{PushedAt: at, Source: src})
		if err != nil || v != 1 {
			t.Fatalf("push: %d %v", v, err)
		}
		k, _ := st.GetKit("web")
		if got := k.Meta[1]; !got.PushedAt.Equal(at) || !reflect.DeepEqual(got.Source, src) {
			t.Fatalf("meta %+v", got)
		}
		if _, err := st.PushKit("web", `{"kind":"studio","prompt":"x"}`); err != nil {
			t.Fatal(err)
		}
		k, _ = st.GetKit("web")
		if k.Meta[2].Source != nil || k.Meta[2].PushedAt.IsZero() {
			t.Fatalf("local push meta %+v", k.Meta[2])
		}
	})
	t.Run("tracked source set/clear and copies are deep", func(t *testing.T) {
		st := mk(t)
		_, _ = st.PushKit("web", `{"kind":"studio"}`)
		src := &jam.KitSource{Repo: "https://gitlab.example.com/g/k", Ref: "v1"}
		if err := st.SetKitSource("web", src); err != nil {
			t.Fatal(err)
		}
		k, _ := st.GetKit("web")
		k.Source.Ref = "mutated"
		k2, _ := st.GetKit("web")
		if k2.Source.Ref != "v1" {
			t.Fatal("GetKit returned an aliased Source")
		}
		if err := st.SetKitSource("web", nil); err != nil {
			t.Fatal(err)
		}
		if k3, _ := st.GetKit("web"); k3.Source != nil {
			t.Fatal("source not cleared")
		}
		if !errors.Is(st.SetKitSource("nope", src), jam.ErrNotFound) {
			t.Fatal("unknown kit")
		}
	})
}
