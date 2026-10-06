// Package storetest is a backend-agnostic conformance suite for jam.Store.
// Both MemStore (hermetic) and PostgresStore (integration) run it, guaranteeing
// the two backends behave identically. The assertions encode the contract as
// MemStore implements it (see internal/jam/memstore.go).
package storetest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// RunConformance exercises the full jam.Store contract. newStore must return
// a fresh, empty store on each call.
func RunConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	runRegistryConformance(t, newStore)
	runChannelConformance(t, newStore)

	// newStoreWithAcme returns a fresh store holding the (empty) project "acme",
	// which most subtests write into.
	newStoreWithAcme := func(t *testing.T) jam.Store {
		t.Helper()
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatalf("CreateProject acme: %v", err)
		}
		return s
	}

	t.Run("destination_specs_round_trip_and_copy", func(t *testing.T) {
		s := newStore(t)
		in := &jam.InboundSpec{Header: "X-Jam", Prefixes: []string{"Jam "}}
		out := &jam.OutboundSpec{Header: "Private-Token", Template: "{cred}"}
		d := jam.Destination{Name: "gl", Route: "/gl/", Upstream: "https://gl", IdentityIn: jam.ApplyCustom, IdentityInSpec: in,
			Apply: jam.ApplyCustom, ApplySpec: out, Env: map[string]string{"A": "{url}"}}
		if err := s.AddDestination(d); err != nil {
			t.Fatalf("AddDestination: %v", err)
		}
		// Mutating the caller's value after the write does not reach the store.
		in.Prefixes[0], out.Header, d.Env["A"] = "mutated", "Mutated", "mutated"
		got := s.ListDestinations()
		if len(got) != 1 || got[0].IdentityInSpec.Prefixes[0] != "Jam " || got[0].ApplySpec.Header != "Private-Token" || got[0].Env["A"] != "{url}" {
			t.Fatalf("stored = %+v", got)
		}
		// Mutating a returned value does not reach the store.
		got[0].IdentityInSpec.Prefixes[0], got[0].ApplySpec.Template, got[0].Env["A"] = "mutated", "mutated", "mutated"
		m, ok := s.Match("/gl/x")
		if !ok || m.IdentityInSpec.Prefixes[0] != "Jam " || m.ApplySpec.Template != "{cred}" || m.Env["A"] != "{url}" {
			t.Fatalf("ListDestinations must return copies, got %+v", m)
		}
	})

	t.Run("project_context_set_and_copy", func(t *testing.T) {
		s := newStoreWithAcme(t)
		l := sessionctx.Layer{Core: "Goals.", Leaves: []sessionctx.Leaf{{Name: "a.md", ReadWhen: "w", Body: "b"}}}
		rs := []sessionctx.Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}
		if err := s.SetProjectContext("acme", l, rs); err != nil {
			t.Fatalf("SetProjectContext: %v", err)
		}
		p, _ := s.GetProject("acme")
		if p.Context.Core != "Goals." || len(p.Context.Leaves) != 1 || len(p.Resources) != 1 {
			t.Fatalf("got %+v", p)
		}
		p.Resources[0].Name = "mutated"
		p.Context.Leaves[0].Name = "mutated.md"
		if again, _ := s.GetProject("acme"); again.Resources[0].Name != "cove" || again.Context.Leaves[0].Name != "a.md" {
			t.Fatal("GetProject must return a copy")
		}
		if err := s.SetProjectContext("ghost", l, nil); !errors.Is(err, jam.ErrProjectNotFound) {
			t.Fatalf("unknown project = %v, want ErrProjectNotFound", err)
		}
	})

	t.Run("jam_context_set_get_clear", func(t *testing.T) {
		s := newStore(t)
		if got := s.GetJamContext(); !got.Empty() {
			t.Fatalf("fresh store: %+v", got)
		}
		l := sessionctx.Layer{Core: "Never push to main.", Leaves: []sessionctx.Leaf{{Name: "r.md", ReadWhen: "w", Body: "b"}}}
		if err := s.SetJamContext(l); err != nil {
			t.Fatal(err)
		}
		got := s.GetJamContext()
		if got.Core != l.Core || len(got.Leaves) != 1 {
			t.Fatalf("got %+v", got)
		}
		got.Leaves[0].Name = "mutated.md"
		if s.GetJamContext().Leaves[0].Name != "r.md" {
			t.Fatal("GetJamContext must return a copy")
		}
		if err := s.SetJamContext(sessionctx.Layer{}); err != nil || !s.GetJamContext().Empty() {
			t.Fatalf("clear: err=%v got=%+v", err, s.GetJamContext())
		}
	})

	t.Run("actors_add_lookup_remove", func(t *testing.T) {
		s := newStore(t)
		a := jam.Actor{ID: "cove-1", TokenHash: "hash-1"}
		if err := s.AddActor(a); err != nil {
			t.Fatalf("AddActor: %v", err)
		}
		if err := s.AddActor(jam.Actor{ID: "cove-1", TokenHash: "hash-2"}); err == nil {
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
		s := newStoreWithAcme(t)
		if err := s.AddActor(jam.Actor{ID: "cove-1", TokenHash: "h"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddGrant("absent", jam.Grant{Project: "acme", Role: "worker"}); err == nil {
			t.Fatal("AddGrant to an absent actor must error")
		}
		if err := s.AddGrant("cove-1", jam.Grant{Project: "acme", Role: "worker"}); err != nil {
			t.Fatalf("AddGrant: %v", err)
		}
		// upsert by (project, role): a second AddGrant for the same pair replaces.
		if err := s.AddGrant("cove-1", jam.Grant{Project: "acme", Role: "worker"}); err != nil {
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
		s := newStoreWithAcme(t)
		if err := s.PutRole("acme", jam.Role{Name: "worker", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
			t.Fatalf("PutRole: %v", err)
		}
		// upsert: putting the same name again replaces without error.
		if err := s.PutRole("acme", jam.Role{Name: "worker", Scope: jam.Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat-acme"}}}); err != nil {
			t.Fatalf("PutRole (upsert): %v", err)
		}
		r, ok := s.GetRole("acme", "worker")
		if !ok || r.Scope.Credentials["git"] != "git-pat-acme" {
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

	// A role's egress policy distinguishes nil (the kit default), set-but-empty
	// (nothing beyond base + infra) and a set list; all three must round-trip.
	t.Run("role_egress_policy_round_trip", func(t *testing.T) {
		s := newStoreWithAcme(t)
		cases := map[string]*jam.EgressPolicy{
			"kit-default": nil,
			"empty":       {Domains: []string{}},
			"set":         {Domains: []string{".b.org", "a.com"}},
		}
		for name, eg := range cases {
			if err := s.PutRole("acme", jam.Role{Name: name, Scope: jam.Scope{Egress: eg}}); err != nil {
				t.Fatalf("PutRole %s: %v", name, err)
			}
		}
		for name, want := range cases {
			r, ok := s.GetRole("acme", name)
			if !ok {
				t.Fatalf("GetRole %s missing", name)
			}
			switch {
			case want == nil && r.Scope.Egress != nil:
				t.Fatalf("%s: egress = %+v, want nil", name, r.Scope.Egress)
			case want != nil && (r.Scope.Egress == nil || len(r.Scope.Egress.Domains) != len(want.Domains)):
				t.Fatalf("%s: egress = %+v, want %+v", name, r.Scope.Egress, want)
			case want != nil && len(want.Domains) > 0 && strings.Join(r.Scope.Egress.Domains, ",") != strings.Join(want.Domains, ","):
				t.Fatalf("%s: egress = %+v, want %+v", name, r.Scope.Egress, want)
			}
		}
	})

	t.Run("role_kit_reference_validation", func(t *testing.T) {
		s := newStoreWithAcme(t)
		// PutRole referencing a kit that does not exist must error.
		if err := s.PutRole("acme", jam.Role{Name: "r", Kit: "ghost"}); err == nil {
			t.Fatal("PutRole with an unknown kit must error")
		}
		if _, err := s.PushKit("base", "listen: :443"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRole("acme", jam.Role{Name: "r", Kit: "base"}); err != nil {
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
		if err := s.PutInstance(jam.Instance{}); err == nil {
			t.Fatal("PutInstance without an actor id must error")
		}
		inst := jam.Instance{ActorID: "cove-1", Project: "acme", Role: "worker", Phase: jam.PhaseLive}
		if err := s.PutInstance(inst); err != nil {
			t.Fatalf("PutInstance: %v", err)
		}
		inst.Phase = jam.PhaseTerminating
		if err := s.PutInstance(inst); err != nil { // upsert
			t.Fatalf("PutInstance (upsert): %v", err)
		}
		got, ok := s.GetInstance("cove-1")
		if !ok || got.Phase != jam.PhaseTerminating {
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

	t.Run("instance_egress_fields", func(t *testing.T) {
		s := newStore(t)
		inst := jam.Instance{ActorID: "cove-1", Phase: jam.PhaseLive, Egress: "d:.b.org,a.com", EgressFailures: 2}
		if err := s.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		got, ok := s.GetInstance("cove-1")
		if !ok || got.Egress != "d:.b.org,a.com" || got.EgressFailures != 2 {
			t.Fatalf("GetInstance = %+v, %v; want the egress fingerprint and failure count kept", got, ok)
		}
	})

	t.Run("advance_commit_cursor", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutInstance(jam.Instance{ActorID: "cove-1", Phase: jam.PhaseLive}); err != nil {
			t.Fatal(err)
		}
		inst, err := s.AdvanceCommitCursor("cove-1", "id-5", 5)
		if err != nil || inst.CommitCursor != "id-5" || inst.CommitSeq != 5 {
			t.Fatalf("advance to id-5/5 = %+v, %v", inst, err)
		}
		// monotonic: a backward/equal upToSeq is a no-op success (both fields
		// unchanged) — even though "id-3" would sort AFTER "id-5" lexically,
		// proving the gate is Seq, not the id string.
		inst, err = s.AdvanceCommitCursor("cove-1", "id-3", 3)
		if err != nil || inst.CommitCursor != "id-5" || inst.CommitSeq != 5 {
			t.Fatalf("backward advance must no-op: %+v, %v", inst, err)
		}
		inst, err = s.AdvanceCommitCursor("cove-1", "id-5-again", 5) // equal Seq is also a no-op
		if err != nil || inst.CommitCursor != "id-5" || inst.CommitSeq != 5 {
			t.Fatalf("equal-seq advance must no-op: %+v, %v", inst, err)
		}
		inst, err = s.AdvanceCommitCursor("cove-1", "id-9", 9)
		if err != nil || inst.CommitCursor != "id-9" || inst.CommitSeq != 9 {
			t.Fatalf("forward advance = %+v, %v", inst, err)
		}
		if _, err := s.AdvanceCommitCursor("absent", "id-1", 1); err == nil {
			t.Fatal("advance on an absent actor must error")
		}
	})

	t.Run("unread_cursor_per_participant_channel", func(t *testing.T) {
		s := newStore(t)
		if _, ok := s.UnreadCursor("human:alice", "studio:ACME-1"); ok {
			t.Fatal("an uncommitted (participant, channel) cursor must be absent")
		}
		if err := s.CommitUnread("human:alice", "studio:ACME-1", 5); err != nil {
			t.Fatalf("CommitUnread: %v", err)
		}
		if seq, ok := s.UnreadCursor("human:alice", "studio:ACME-1"); !ok || seq != 5 {
			t.Fatalf("cursor = %d, %v; want 5, true", seq, ok)
		}
		// Forward-only: a backward/equal seq is a no-op success.
		if err := s.CommitUnread("human:alice", "studio:ACME-1", 3); err != nil {
			t.Fatalf("CommitUnread (backward): %v", err)
		}
		if err := s.CommitUnread("human:alice", "studio:ACME-1", 5); err != nil {
			t.Fatalf("CommitUnread (equal): %v", err)
		}
		if seq, _ := s.UnreadCursor("human:alice", "studio:ACME-1"); seq != 5 {
			t.Fatalf("cursor after non-forward commits = %d, want 5", seq)
		}
		if err := s.CommitUnread("human:alice", "studio:ACME-1", 9); err != nil {
			t.Fatalf("CommitUnread (forward): %v", err)
		}
		// Keyed by (participant, channel): a second channel and a second
		// participant are independent.
		if err := s.CommitUnread("human:alice", "dm:x", 2); err != nil {
			t.Fatal(err)
		}
		if err := s.CommitUnread("human:bob", "studio:ACME-1", 7); err != nil {
			t.Fatal(err)
		}
		got := s.UnreadCursors("human:alice")
		if len(got) != 2 || got["studio:ACME-1"] != 9 || got["dm:x"] != 2 {
			t.Fatalf("UnreadCursors(alice) = %v; want {studio:ACME-1:9, dm:x:2}", got)
		}
		if got := s.UnreadCursors("human:bob"); len(got) != 1 || got["studio:ACME-1"] != 7 {
			t.Fatalf("UnreadCursors(bob) = %v; want {studio:ACME-1:7}", got)
		}
		// Empty participant/channel is rejected.
		if err := s.CommitUnread("", "c", 1); err == nil {
			t.Fatal("CommitUnread with an empty participant must error")
		}
		if err := s.CommitUnread("p", "", 1); err == nil {
			t.Fatal("CommitUnread with an empty channel must error")
		}
	})

	t.Run("destinations_and_match", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddDestination(jam.Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com"}); err != nil {
			t.Fatalf("AddDestination: %v", err)
		}
		if err := s.AddDestination(jam.Destination{Name: "anthropic-msgs", Route: "/anthropic/v1/messages", Upstream: "https://api.anthropic.com"}); err != nil {
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

	t.Run("model_specs_put_get_list_remove", func(t *testing.T) {
		s := newStore(t)
		if _, ok := s.GetModelSpec("claude-default"); ok {
			t.Fatal("fresh store has a model-spec")
		}
		m := jam.ModelSpec{
			Name: "claude-default", Type: jam.HarnessClaude, Version: "2.x",
			Principal: jam.ModelPrincipal{Credential: "anthropic"},
			Model:     jam.ModelChoice{ID: "claude-opus-5-5"},
			Policy:    jam.ModelPolicy{Mode: "bypassPermissions", Allow: []string{"Bash"}},
			Claude: &jam.ClaudeSpec{
				Provider:    "vertex",
				ProviderEnv: map[string]string{"CLOUD_ML_REGION": "us-east5"},
				Settings:    map[string]any{"theme": "dark", "nested": map[string]any{"n": 1.0}},
				Plugins:     []string{"p@m"},
			},
		}
		if err := s.PutModelSpec(m); err != nil {
			t.Fatalf("PutModelSpec: %v", err)
		}
		if err := s.PutModelSpec(jam.ModelSpec{Name: "a-first", Type: jam.HarnessClaude, Version: "2.x", Claude: &jam.ClaudeSpec{Provider: "anthropic"}}); err != nil {
			t.Fatal(err)
		}
		got, ok := s.GetModelSpec("claude-default")
		if !ok || got.Version != "2.x" || got.Principal.Credential != "anthropic" || got.Claude == nil ||
			got.Claude.ProviderEnv["CLOUD_ML_REGION"] != "us-east5" || got.Claude.Settings["theme"] != "dark" ||
			len(got.Claude.Plugins) != 1 || got.Policy.Allow[0] != "Bash" {
			t.Fatalf("GetModelSpec = %+v, %v", got, ok)
		}
		// Copies: mutating a returned spec does not reach the store.
		got.Claude.ProviderEnv["CLOUD_ML_REGION"] = "mutated"
		got.Claude.Settings["nested"].(map[string]any)["n"] = 2.0
		got.Policy.Allow[0] = "mutated"
		again, _ := s.GetModelSpec("claude-default")
		if again.Claude.ProviderEnv["CLOUD_ML_REGION"] != "us-east5" || again.Claude.Settings["nested"].(map[string]any)["n"] != 1.0 || again.Policy.Allow[0] != "Bash" {
			t.Fatalf("GetModelSpec must return a copy, got %+v", again)
		}
		// Upsert replaces.
		m.Version = "2.1.x"
		if err := s.PutModelSpec(m); err != nil {
			t.Fatal(err)
		}
		all := s.ListModelSpecs()
		if len(all) != 2 || all[0].Name != "a-first" || all[1].Name != "claude-default" || all[1].Version != "2.1.x" {
			t.Fatalf("ListModelSpecs = %+v (want 2, sorted, upserted)", all)
		}
		if err := s.PutModelSpec(jam.ModelSpec{}); err == nil {
			t.Fatal("PutModelSpec without a name must error")
		}
		if err := s.RemoveModelSpec("claude-default"); err != nil {
			t.Fatalf("RemoveModelSpec: %v", err)
		}
		if err := s.RemoveModelSpec("claude-default"); err == nil {
			t.Fatal("RemoveModelSpec of an absent spec must error")
		}
		if _, ok := s.GetModelSpec("claude-default"); ok {
			t.Fatal("spec still present after remove")
		}
	})

	t.Run("model_specs_remove_refused_while_bound", func(t *testing.T) {
		s := newStore(t)
		spec := func(name string) jam.ModelSpec {
			return jam.ModelSpec{Name: name, Type: jam.HarnessClaude, Version: "2.x", Principal: jam.ModelPrincipal{Credential: "c"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}
		}
		for _, n := range []string{"opus", jam.DefaultModelSpec} {
			if err := s.PutModelSpec(spec(n)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutRole("", jam.Role{Name: "bound", ModelSpec: "opus"}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRole("", jam.Role{Name: "unbound"}); err != nil {
			t.Fatal(err)
		}
		// A role's model_spec persists (the role doc round-trips it).
		if r, ok := s.GetRole("", "bound"); !ok || r.ModelSpec != "opus" {
			t.Fatalf("GetRole = %+v, %v", r, ok)
		}
		// Bound explicitly, or by default (an unbound role resolves to the default).
		for _, n := range []string{"opus", jam.DefaultModelSpec} {
			if err := s.RemoveModelSpec(n); !errors.Is(err, jam.ErrModelSpecInUse) {
				t.Fatalf("RemoveModelSpec(%s) = %v, want ErrModelSpecInUse", n, err)
			}
			if _, ok := s.GetModelSpec(n); !ok {
				t.Fatalf("%s removed despite the refusal", n)
			}
		}
		if err := s.RemoveRole("", "bound"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveModelSpec("opus"); err != nil {
			t.Fatalf("RemoveModelSpec once unbound: %v", err)
		}
	})

	t.Run("model_specs_export_import", func(t *testing.T) {
		src := newStore(t)
		if err := src.PutModelSpec(jam.ModelSpec{Name: "m", Type: jam.HarnessClaude, Version: "2.x", Principal: jam.ModelPrincipal{Credential: "c"}, Claude: &jam.ClaudeSpec{Provider: "anthropic"}}); err != nil {
			t.Fatal(err)
		}
		snap := src.ExportConfig()
		if len(snap.ModelSpecs) != 1 || snap.ModelSpecs[0].Name != "m" {
			t.Fatalf("export ModelSpecs = %+v", snap.ModelSpecs)
		}
		if err := src.ImportConfig(snap); !errors.Is(err, jam.ErrConfigNotEmpty) {
			t.Fatalf("import into a store holding a model-spec = %v, want ErrConfigNotEmpty", err)
		}
		dst := newStore(t)
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatalf("ImportConfig: %v", err)
		}
		if got, ok := dst.GetModelSpec("m"); !ok || got.Claude == nil || got.Claude.Provider != "anthropic" {
			t.Fatalf("imported spec = %+v, %v", got, ok)
		}
	})

	t.Run("model_spec_schema_marker", func(t *testing.T) {
		s := newStore(t)
		if got := s.ModelSpecSchema(); got != 0 {
			t.Fatalf("fresh store schema = %d, want 0", got)
		}
		if err := s.SetModelSpecSchema(1); err != nil {
			t.Fatal(err)
		}
		if got := s.ModelSpecSchema(); got != 1 {
			t.Fatalf("schema = %d, want 1", got)
		}
		// The marker rides in a config backup and is restored by an import.
		snap := s.ExportConfig()
		if snap.ModelSpecSchema != 1 {
			t.Fatalf("export ModelSpecSchema = %d, want 1", snap.ModelSpecSchema)
		}
		dst := newStore(t)
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		if got := dst.ModelSpecSchema(); got != 1 {
			t.Fatalf("imported schema = %d, want 1", got)
		}
		// A pre-marker backup imports as schema 0 (still to migrate).
		old := newStore(t)
		snap.ModelSpecSchema = 0
		if err := old.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		if got := old.ModelSpecSchema(); got != 0 {
			t.Fatalf("pre-marker import schema = %d, want 0", got)
		}
	})

	t.Run("roster_and_escalation", func(t *testing.T) {
		s := newStoreWithAcme(t)
		if err := s.AddHuman("acme", jam.Human{Name: "alice", Handle: "@alice"}); err != nil {
			t.Fatalf("AddHuman: %v", err)
		}
		if err := s.AddHuman("acme", jam.Human{Name: "alice", Handle: "@alice2"}); err != nil { // upsert by name
			t.Fatalf("AddHuman (upsert): %v", err)
		}
		if err := s.AddChannel("acme", jam.RosterChannel{Name: "eng", Ref: "ACME-1"}); err != nil {
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
		tiers := []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: time.Minute}}
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
		// delivery profile + oidc identity round-trip through AddHuman (upsert by name)
		if err := s.AddHuman("acme", jam.Human{Name: "dave", Handle: "@dave", Login: "auth0|dave", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "chan-9", UserID: "123456789"}}, Identity: []jam.OIDCIdentity{{Issuer: "https://accounts.google.com", Subject: "dave-sub"}}}); err != nil {
			t.Fatalf("AddHuman with delivery: %v", err)
		}
		if r, ok := s.GetRoster("acme"); !ok {
			t.Fatal("GetRoster acme")
		} else {
			var dave jam.Human
			for _, h := range r.Humans {
				if h.Name == "dave" {
					dave = h
				}
			}
			if d, ok := dave.DeliveryFor("discord"); !ok || d.Address != "chan-9" || d.UserID != "123456789" {
				t.Fatalf("dave delivery = %+v,%v", d, ok)
			}
			if dave.Login != "auth0|dave" {
				t.Fatalf("dave login = %q, want auth0|dave", dave.Login)
			}
			if len(dave.Identity) != 1 || dave.Identity[0].Issuer != "https://accounts.google.com" || dave.Identity[0].Subject != "dave-sub" {
				t.Fatalf("dave identity = %+v", dave.Identity)
			}
		}
		// chat-service set + clear round-trips through GetProject
		if err := s.SetChatService("acme", "discord"); err != nil {
			t.Fatalf("SetChatService: %v", err)
		}
		if p, ok := s.GetProject("acme"); !ok || jam.ChatKind(s, p) != "discord" {
			t.Fatalf("ChatService = %q (ok=%v), want a discord connection", p.ChatService, ok)
		}
		if err := s.SetChatService("acme", ""); err != nil {
			t.Fatalf("SetChatService clear: %v", err)
		}
		if p, ok := s.GetProject("acme"); !ok || p.ChatService != "" {
			t.Fatalf("ChatService after clear = %q, want empty", p.ChatService)
		}
	})

	t.Run("projects_lifecycle", func(t *testing.T) {
		s := newStore(t)
		if got := s.ListProjects(); len(got) != 0 {
			t.Fatalf("ListProjects on a fresh store = %v, want none", got)
		}
		if err := s.CreateProject(""); err == nil {
			t.Fatal("CreateProject with an empty name must error")
		}
		if err := s.CreateProject("acme"); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		if err := s.CreateProject("acme"); !errors.Is(err, jam.ErrProjectExists) {
			t.Fatalf("CreateProject of an existing project = %v, want ErrProjectExists", err)
		}
		if got := s.ListProjects(); len(got) != 1 || got[0] != "acme" {
			t.Fatalf("ListProjects = %v, want [acme]", got)
		}
		if p, ok := s.GetProject("acme"); !ok || p.Name != "acme" {
			t.Fatalf("GetProject = %+v, %v", p, ok)
		}

		// Every project-scoped write into an unknown project is refused — no
		// implicit creation.
		writes := map[string]func() error{
			"PutRole":             func() error { return s.PutRole("ghost", jam.Role{Name: "r"}) },
			"AddHuman":            func() error { return s.AddHuman("ghost", jam.Human{Name: "h"}) },
			"AddChannel":          func() error { return s.AddChannel("ghost", jam.RosterChannel{Name: "c"}) },
			"SetEscalationPolicy": func() error { return s.SetEscalationPolicy("ghost", "", nil) },
			"SetChatService":      func() error { return s.SetChatService("ghost", "discord") },
			"AddActor": func() error {
				return s.AddActor(jam.Actor{ID: "a2", TokenHash: "h2", Grants: []jam.Grant{{Project: "ghost", Role: "r"}}})
			},
		}
		if err := s.AddActor(jam.Actor{ID: "a1", TokenHash: "h1"}); err != nil {
			t.Fatal(err)
		}
		writes["AddGrant"] = func() error { return s.AddGrant("a1", jam.Grant{Project: "ghost", Role: "r"}) }
		for name, w := range writes {
			if err := w(); !errors.Is(err, jam.ErrProjectNotFound) {
				t.Errorf("%s into an unknown project = %v, want ErrProjectNotFound", name, err)
			}
		}
		if got := s.ListProjects(); len(got) != 1 {
			t.Fatalf("a refused write created a project: ListProjects = %v", got)
		}
		if _, ok := s.Lookup("h2"); ok {
			t.Fatal("a refused AddActor left the actor behind")
		}

		// RemoveProject refuses while a role or a grant references the project.
		if err := s.PutRole("acme", jam.Role{Name: "worker"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveProject("acme"); !errors.Is(err, jam.ErrProjectInUse) {
			t.Fatalf("RemoveProject with a role = %v, want ErrProjectInUse", err)
		}
		if err := s.AddGrant("a1", jam.Grant{Project: "acme", Role: "worker"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveRole("acme", "worker"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveProject("acme"); !errors.Is(err, jam.ErrProjectInUse) {
			t.Fatalf("RemoveProject with a grant = %v, want ErrProjectInUse", err)
		}
		if err := s.RemoveGrant("a1", "acme", "worker"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveProject("acme"); err != nil {
			t.Fatalf("RemoveProject: %v", err)
		}
		if _, ok := s.GetProject("acme"); ok {
			t.Fatal("project still present after RemoveProject")
		}
		if got := s.ListProjects(); len(got) != 0 {
			t.Fatalf("ListProjects after RemoveProject = %v", got)
		}
		if err := s.RemoveProject("acme"); !errors.Is(err, jam.ErrProjectNotFound) {
			t.Fatalf("RemoveProject of an absent project = %v, want ErrProjectNotFound", err)
		}
	})

	// The default project is the one exception to explicit creation: a write
	// that names it (or names no project) materializes it, so a zero-config Jam
	// keeps working.
	t.Run("default_project_materializes_on_first_use", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRole("", jam.Role{Name: "worker"}); err != nil {
			t.Fatalf("PutRole into the default project: %v", err)
		}
		if got := s.ListProjects(); len(got) != 1 || got[0] != jam.DefaultProject {
			t.Fatalf("ListProjects = %v, want [%s]", got, jam.DefaultProject)
		}
		if _, ok := s.GetProject(jam.DefaultProject); !ok {
			t.Fatal("the default project has no record after first use")
		}
		s2 := newStore(t)
		if err := s2.AddActor(jam.Actor{ID: "a", TokenHash: "h", Grants: []jam.Grant{{Role: "worker"}}}); err != nil {
			t.Fatalf("AddActor granting into the default project: %v", err)
		}
		if _, ok := s2.GetProject(jam.DefaultProject); !ok {
			t.Fatal("AddActor did not materialize the default project")
		}
	})

	t.Run("failed_write_leaves_cache_unchanged", func(t *testing.T) {
		// The commit-then-cache invariant: a write that errors must not alter the
		// readable state.
		s := newStore(t)
		if err := s.AddActor(jam.Actor{ID: "cove-1", TokenHash: "h1"}); err != nil {
			t.Fatal(err)
		}
		before := s.ListActors()
		// A duplicate-id AddActor fails; the store must be unchanged afterward.
		if err := s.AddActor(jam.Actor{ID: "cove-1", TokenHash: "h2"}); err == nil {
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
