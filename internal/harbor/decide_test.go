package harbor

import (
	"testing"
	"time"
)

func TestDecideAllowsListedDestination(t *testing.T) {
	id := Identity{ID: "spider-18", Destinations: []string{"anthropic"}}
	dest := testConfig().Destinations[0] // anthropic
	dec, err := Decide(id, dest, "", time.Now())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.NeedCred || dec.CredName != "anthropic-bearer" || dec.Apply != ApplyBearer {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestDecideDeniesUnlistedDestination(t *testing.T) {
	id := Identity{ID: "spider-18", Destinations: []string{"anthropic"}}
	git := testConfig().Destinations[1]
	if _, err := Decide(id, git, "acme/api", time.Now()); err == nil {
		t.Fatal("expected denial for unlisted destination")
	}
}

func TestDecideEnforcesRepoScope(t *testing.T) {
	id := Identity{ID: "spider-18", Destinations: []string{"git"}, Repos: []string{"aethons-tools/*"}}
	git := testConfig().Destinations[1]
	if _, err := Decide(id, git, "aethons-tools/cove", time.Now()); err != nil {
		t.Fatalf("allowed repo denied: %v", err)
	}
	if _, err := Decide(id, git, "someone-else/secret", time.Now()); err == nil {
		t.Fatal("expected denial for out-of-scope repo")
	}
}

func TestDecideRejectsExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	id := Identity{ID: "x", Destinations: []string{"anthropic"}, Expiry: past}
	if _, err := Decide(id, testConfig().Destinations[0], "", time.Now()); err == nil {
		t.Fatal("expected expired identity to be rejected")
	}
}
