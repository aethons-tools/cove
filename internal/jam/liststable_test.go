package jam

import (
	"fmt"
	"slices"
	"testing"
)

// Every list read returns a deterministic order (sorted by its key), so the
// UI and API render stably across polls instead of in Go map order.
func TestListsAreSortedByKey(t *testing.T) {
	st := NewMemStore()
	mustCreateProject(t, st, "acme")
	names := []string{"m", "c", "x", "a", "q", "f", "z", "b"}
	for _, n := range names {
		must(t, st.AddActor(Actor{ID: n, TokenHash: "h-" + n}))
		must(t, st.PutRole("acme", Role{Name: n}))
		must(t, st.PutInstance(Instance{ActorID: n, Project: "acme", Role: n}))
		must(t, st.AddDestination(Destination{Name: n, Route: "/" + n + "/", Upstream: "https://" + n}))
		if _, err := st.PushKit(n, "x: 1\n"); err != nil {
			t.Fatal(err)
		}
	}
	want := slices.Sorted(slices.Values(names))
	for range 10 { // map order varies per range; one pass could pass by luck
		check := func(what string, got []string) {
			if !slices.Equal(got, want) {
				t.Fatalf("%s order = %v, want %v", what, got, want)
			}
		}
		check("actors", keys(st.ListActors(), func(a Actor) string { return a.ID }))
		check("roles", keys(st.ListRoles("acme"), func(r Role) string { return r.Name }))
		check("instances", keys(st.ListInstances(), func(i Instance) string { return i.ActorID }))
		check("kits", keys(st.ListKits(), func(k Kit) string { return k.Name }))
		check("destinations", keys(st.ListDestinations(), func(d Destination) string { return d.Name }))
	}
}

func keys[T any](xs []T, k func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = k(x)
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(fmt.Errorf("setup: %w", err))
	}
}
