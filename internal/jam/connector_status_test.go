package jam

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

func TestCoveSummariesConnectorStatus(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	gh := Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", Env: map[string]string{"GH_HOST": "{host}"}}
	if err := store.AddDestination(gh); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "w", Destinations: []string{"gh"}})
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "w1", Project: "acme", Role: "w"})
	actor := actorByID(t, store, "w1")
	want, err := ConnectorFor(store, actor)
	if err != nil {
		t.Fatal(err)
	}
	put := func(fp string) {
		if err := store.PutInstance(Instance{ActorID: "w1", Project: "acme", Role: "w", Phase: PhaseLive, Connector: fp}); err != nil {
			t.Fatal(err)
		}
	}
	status := func() string {
		for _, c := range CoveSummaries(store) {
			if c.ID == "w1" {
				return c.Connector
			}
		}
		t.Fatal("w1 missing")
		return ""
	}

	put("")
	if s := status(); s != "unknown" {
		t.Fatalf("never reported = %q, want unknown", s)
	}
	put(snippet.Fingerprint(want))
	if s := status(); s != "ok" {
		t.Fatalf("current = %q, want ok", s)
	}

	// Edit the destination's env: the reported fingerprint is now stale.
	if err := store.RemoveDestination("gh"); err != nil {
		t.Fatal(err)
	}
	gh.Env = map[string]string{"GH_HOST": "{host}", "X": "y"}
	if err := store.AddDestination(gh); err != nil {
		t.Fatal(err)
	}
	if s := status(); s != "stale" {
		t.Fatalf("after edit = %q, want stale", s)
	}

	// A second destination setting GH_HOST differently → conflict → error.
	if err := store.AddDestination(Destination{Name: "gh2", Route: "/x/", Upstream: "https://x.example", Env: map[string]string{"GH_HOST": "other"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", Role{Name: "w", Scope: Scope{Destinations: []string{"gh", "gh2"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if s := status(); s != "error" {
		t.Fatalf("conflict = %q, want error", s)
	}
}

func actorByID(t *testing.T, store Store, id string) Actor {
	t.Helper()
	for _, a := range store.ListActors() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no actor %s", id)
	return Actor{}
}
