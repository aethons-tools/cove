// Package storetest is a backend-agnostic conformance suite for harbor.Store.
// Both FileStore (hermetic) and PostgresStore (integration) run it, guaranteeing
// the two backends behave identically. The assertions encode the contract as
// FileStore implements it (see internal/harbor/filestore.go).
package storetest

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// RunConformance exercises the full harbor.Store contract. newStore must return
// a fresh, empty store on each call.
func RunConformance(t *testing.T, newStore func(t *testing.T) harbor.Store) {
	t.Run("actors_add_lookup_remove", func(t *testing.T) {
		s := newStore(t)
		a := harbor.Actor{ID: "cove-1", TokenHash: "hash-1"}
		if err := s.AddActor(a); err != nil {
			t.Fatalf("AddActor: %v", err)
		}
		if err := s.AddActor(harbor.Actor{ID: "cove-1", TokenHash: "hash-2"}); err == nil {
			t.Fatal("AddActor of a duplicate id must error")
		}
		got, ok := s.Lookup("hash-1")
		if !ok || got.ID != "cove-1" {
			t.Fatalf("Lookup = %+v, %v", got, ok)
		}
		if all := s.ListActors(); len(all) != 1 {
			t.Fatalf("ListActors = %d, want 1", len(all))
		}
		if err := s.RemoveActor("cove-1"); err != nil {
			t.Fatalf("RemoveActor: %v", err)
		}
		if _, ok := s.Lookup("hash-1"); ok {
			t.Fatal("actor still present after RemoveActor")
		}
		if err := s.RemoveActor("cove-1"); err == nil {
			t.Fatal("RemoveActor of an absent actor must error")
		}
	})

	t.Run("grants_upsert_remove", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddActor(harbor.Actor{ID: "cove-1", TokenHash: "h"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddGrant("absent", harbor.Grant{Project: "acme", Role: "worker"}); err == nil {
			t.Fatal("AddGrant to an absent actor must error")
		}
		if err := s.AddGrant("cove-1", harbor.Grant{Project: "acme", Role: "worker"}); err != nil {
			t.Fatalf("AddGrant: %v", err)
		}
		// upsert by (project, role): a second AddGrant for the same pair replaces.
		if err := s.AddGrant("cove-1", harbor.Grant{Project: "acme", Role: "worker"}); err != nil {
			t.Fatalf("AddGrant (upsert): %v", err)
		}
		if a, _ := s.Lookup("h"); len(a.Grants) != 1 {
			t.Fatalf("grants after upsert = %d, want 1", len(a.Grants))
		}
		if err := s.RemoveGrant("cove-1", "acme", "worker"); err != nil {
			t.Fatalf("RemoveGrant: %v", err)
		}
		if err := s.RemoveGrant("cove-1", "acme", "worker"); err == nil {
			t.Fatal("RemoveGrant of an absent grant must error")
		}
		if a, _ := s.Lookup("h"); len(a.Grants) != 0 {
			t.Fatalf("grants after remove = %d, want 0", len(a.Grants))
		}
	})

	t.Run("roles_crud_projects", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRole("acme", harbor.Role{Name: "worker", Scope: harbor.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
			t.Fatalf("PutRole: %v", err)
		}
		// upsert: putting the same name again replaces without error.
		if err := s.PutRole("acme", harbor.Role{Name: "worker", Scope: harbor.Scope{Repos: []string{"acme/*"}}}); err != nil {
			t.Fatalf("PutRole (upsert): %v", err)
		}
		r, ok := s.GetRole("acme", "worker")
		if !ok || len(r.Scope.Repos) != 1 {
			t.Fatalf("GetRole = %+v, %v", r, ok)
		}
		if got := s.ListRoles("acme"); len(got) != 1 {
			t.Fatalf("ListRoles = %v", got)
		}
		if got := s.ListProjects(); len(got) != 1 || got[0] != "acme" {
			t.Fatalf("ListProjects = %v", got)
		}
		if err := s.RemoveRole("acme", "worker"); err != nil {
			t.Fatalf("RemoveRole: %v", err)
		}
		if err := s.RemoveRole("acme", "worker"); err == nil {
			t.Fatal("RemoveRole of an absent role must error")
		}
	})

	t.Run("role_kit_reference_validation", func(t *testing.T) {
		s := newStore(t)
		// PutRole referencing a kit that does not exist must error.
		if err := s.PutRole("acme", harbor.Role{Name: "r", Kit: "ghost"}); err == nil {
			t.Fatal("PutRole with an unknown kit must error")
		}
		if _, err := s.PushKit("base", "listen: :443"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRole("acme", harbor.Role{Name: "r", Kit: "base"}); err != nil {
			t.Fatalf("PutRole with an existing kit: %v", err)
		}
		// RoleReferencingKit reports the referencing (project, role).
		p, role, ok := s.RoleReferencingKit("base")
		if !ok || p != "acme" || role != "r" {
			t.Fatalf("RoleReferencingKit = %q,%q,%v", p, role, ok)
		}
		if _, _, ok := s.RoleReferencingKit("base-unref"); ok {
			t.Fatal("RoleReferencingKit for an unreferenced name must be false")
		}
	})

	t.Run("kits_versions_pin_remove", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.PushKit("", "x"); err == nil {
			t.Fatal("PushKit with empty name must error")
		}
		v1, err := s.PushKit("base", "v1")
		if err != nil || v1 != 1 {
			t.Fatalf("PushKit #1 = %d, %v (want 1)", v1, err)
		}
		v2, err := s.PushKit("base", "v2")
		if err != nil || v2 != 2 {
			t.Fatalf("PushKit #2 = %d, %v (want 2)", v2, err)
		}
		k, ok := s.GetKit("base")
		if !ok || k.Current != 2 || len(k.Versions) != 2 {
			t.Fatalf("GetKit = %+v, %v", k, ok)
		}
		if cfg, ok := s.KitConfig("base", 0); !ok || cfg != "v2" { // 0 => current
			t.Fatalf("KitConfig(current) = %q, %v", cfg, ok)
		}
		if cfg, ok := s.KitConfig("base", 1); !ok || cfg != "v1" {
			t.Fatalf("KitConfig(1) = %q, %v", cfg, ok)
		}
		if err := s.PinKit("base", 1); err != nil {
			t.Fatalf("PinKit: %v", err)
		}
		if k, _ := s.GetKit("base"); k.Current != 1 {
			t.Fatalf("Current after pin = %d, want 1", k.Current)
		}
		if err := s.PinKit("base", 99); err == nil {
			t.Fatal("PinKit to an unknown version must error")
		}
		if got := s.ListKits(); len(got) != 1 {
			t.Fatalf("ListKits = %d, want 1", len(got))
		}
		// RemoveKit deletes even when referenced (blocking is a higher layer's job).
		if err := s.RemoveKit("base"); err != nil {
			t.Fatalf("RemoveKit: %v", err)
		}
		if err := s.RemoveKit("base"); err == nil {
			t.Fatal("RemoveKit of an absent kit must error")
		}
	})

	t.Run("instances_crud", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutInstance(harbor.Instance{}); err == nil {
			t.Fatal("PutInstance without an actor id must error")
		}
		inst := harbor.Instance{ActorID: "cove-1", Project: "acme", Role: "worker", Phase: harbor.PhaseLive}
		if err := s.PutInstance(inst); err != nil {
			t.Fatalf("PutInstance: %v", err)
		}
		inst.Phase = harbor.PhaseTerminating
		if err := s.PutInstance(inst); err != nil { // upsert
			t.Fatalf("PutInstance (upsert): %v", err)
		}
		got, ok := s.GetInstance("cove-1")
		if !ok || got.Phase != harbor.PhaseTerminating {
			t.Fatalf("GetInstance = %+v, %v", got, ok)
		}
		if all := s.ListInstances(); len(all) != 1 {
			t.Fatalf("ListInstances = %d, want 1", len(all))
		}
		if err := s.RemoveInstance("cove-1"); err != nil {
			t.Fatalf("RemoveInstance: %v", err)
		}
		if err := s.RemoveInstance("cove-1"); err == nil {
			t.Fatal("RemoveInstance of an absent instance must error")
		}
	})

	t.Run("destinations_and_match", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddDestination(harbor.Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com"}); err != nil {
			t.Fatalf("AddDestination: %v", err)
		}
		if err := s.AddDestination(harbor.Destination{Name: "anthropic-msgs", Route: "/anthropic/v1/messages", Upstream: "https://api.anthropic.com"}); err != nil {
			t.Fatal(err)
		}
		if all := s.ListDestinations(); len(all) != 2 {
			t.Fatalf("ListDestinations = %d, want 2", len(all))
		}
		// longest-prefix wins.
		d, ok := s.Match("/anthropic/v1/messages")
		if !ok || d.Name != "anthropic-msgs" {
			t.Fatalf("Match = %+v, %v (want anthropic-msgs)", d, ok)
		}
		if d, ok := s.Match("/anthropic/other"); !ok || d.Name != "anthropic" {
			t.Fatalf("Match = %+v, %v (want anthropic)", d, ok)
		}
		if _, ok := s.Match("/nope"); ok {
			t.Fatal("Match of an unrouted path must be false")
		}
		if err := s.RemoveDestination("anthropic"); err != nil {
			t.Fatalf("RemoveDestination: %v", err)
		}
		if err := s.RemoveDestination("anthropic"); err == nil {
			t.Fatal("RemoveDestination of an absent destination must error")
		}
	})

	t.Run("roster_and_escalation", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddHuman("acme", harbor.Human{Name: "alice", Handle: "@alice"}); err != nil {
			t.Fatalf("AddHuman: %v", err)
		}
		if err := s.AddHuman("acme", harbor.Human{Name: "alice", Handle: "@alice2"}); err != nil { // upsert by name
			t.Fatalf("AddHuman (upsert): %v", err)
		}
		if err := s.AddChannel("acme", harbor.Channel{Name: "eng", Ref: "ACME-1"}); err != nil {
			t.Fatalf("AddChannel: %v", err)
		}
		ros, ok := s.GetRoster("acme")
		if !ok || len(ros.Humans) != 1 || ros.Humans[0].Handle != "@alice2" || len(ros.Channels) != 1 {
			t.Fatalf("GetRoster = %+v, %v", ros, ok)
		}
		// AddChannel defaults Service to "linear".
		if ros.Channels[0].Service != "linear" {
			// Service is defaulted on write; re-read via GetProject to confirm.
			if p, _ := s.GetProject("acme"); len(p.Roster.Channels) == 1 && p.Roster.Channels[0].Service != "linear" {
				t.Fatalf("AddChannel should default Service to linear, got %q", p.Roster.Channels[0].Service)
			}
		}
		tiers := []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: time.Minute}}
		if err := s.SetEscalationPolicy("acme", "", tiers); err != nil {
			t.Fatalf("SetEscalationPolicy (default): %v", err)
		}
		if err := s.SetEscalationPolicy("acme", "urgent", tiers); err != nil {
			t.Fatalf("SetEscalationPolicy (category): %v", err)
		}
		p, ok := s.GetProject("acme")
		if !ok || len(p.Escalation) != 1 || len(p.EscalationByCategory["urgent"]) != 1 {
			t.Fatalf("GetProject escalation = %+v, %v", p, ok)
		}
		if err := s.RemoveHuman("acme", "alice"); err != nil {
			t.Fatalf("RemoveHuman: %v", err)
		}
		if err := s.RemoveChannel("acme", "eng"); err != nil {
			t.Fatalf("RemoveChannel: %v", err)
		}
		if ros, _ := s.GetRoster("acme"); len(ros.Humans) != 0 || len(ros.Channels) != 0 {
			t.Fatalf("roster after removals = %+v", ros)
		}
		if _, ok := s.GetProject("absent"); ok {
			t.Fatal("GetProject for an absent project must be false")
		}
		if err := s.RemoveHuman("absent", "x"); err == nil {
			t.Fatal("RemoveHuman on an absent project must error")
		}
	})

	t.Run("failed_write_leaves_cache_unchanged", func(t *testing.T) {
		// The commit-then-cache invariant: a write that errors must not alter the
		// readable state.
		s := newStore(t)
		if err := s.AddActor(harbor.Actor{ID: "cove-1", TokenHash: "h1"}); err != nil {
			t.Fatal(err)
		}
		before := s.ListActors()
		// A duplicate-id AddActor fails; the store must be unchanged afterward.
		if err := s.AddActor(harbor.Actor{ID: "cove-1", TokenHash: "h2"}); err == nil {
			t.Fatal("expected duplicate AddActor to fail")
		}
		if after := s.ListActors(); len(after) != len(before) {
			t.Fatalf("failed write changed the cache: before=%d after=%d", len(before), len(after))
		}
		if _, ok := s.Lookup("h2"); ok {
			t.Fatal("failed write left a phantom actor (token hash h2) in the cache")
		}
	})
}
