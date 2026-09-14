package harbor

import (
	"testing"
	"time"
)

func roleScopes(t *testing.T, scopes ...Scope) []Scope { t.Helper(); return scopes }

func TestEffectiveScopeInheritsRole(t *testing.T) {
	r := Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"}}}
	got := EffectiveScope(Grant{Project: "p", Role: "guest"}, r)
	if len(got.Destinations) != 2 || got.Repos[0] != "acme/*" {
		t.Fatalf("inherited scope = %+v", got)
	}
}

func TestEffectiveScopeOverrideReplaces(t *testing.T) {
	r := Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "git"}, Repos: []string{"acme/*"}}}
	g := Grant{Project: "p", Role: "guest", Overrides: &Override{Repos: []string{"beta/*"}}}
	got := EffectiveScope(g, r)
	if len(got.Destinations) != 2 { // destinations inherited (override nil)
		t.Fatalf("destinations should inherit: %+v", got)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "beta/*" { // repos replaced
		t.Fatalf("repos should be replaced: %+v", got)
	}
}

func TestDecideAllowsWhenAGrantAuthorizes(t *testing.T) {
	dest := testConfig().Destinations[0] // anthropic
	dec, err := Decide(Actor{ID: "spider-18"}, roleScopes(t, Scope{Destinations: []string{"anthropic"}}), dest, "", time.Now())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.NeedCred || dec.CredName != "anthropic-bearer" || dec.Apply != ApplyBearer {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestDecideAdditiveAcrossGrants(t *testing.T) {
	git := testConfig().Destinations[1] // repo-scoped
	// first scope lacks git; second authorizes it — additive.
	scopes := roleScopes(t,
		Scope{Destinations: []string{"anthropic"}},
		Scope{Destinations: []string{"git"}, Repos: []string{"beta/*"}},
	)
	if _, err := Decide(Actor{ID: "x"}, scopes, git, "beta/api", time.Now()); err != nil {
		t.Fatalf("second grant should authorize: %v", err)
	}
}

func TestDecideNoCrossGrantRepoBleed(t *testing.T) {
	git := testConfig().Destinations[1]
	// grant A: git but only acme/*. grant B: beta/* but NOT git. Must deny git beta/x.
	scopes := roleScopes(t,
		Scope{Destinations: []string{"git"}, Repos: []string{"acme/*"}},
		Scope{Destinations: []string{"anthropic"}, Repos: []string{"beta/*"}},
	)
	if _, err := Decide(Actor{ID: "x"}, scopes, git, "beta/secret", time.Now()); err == nil {
		t.Fatal("expected denial: no single grant couples git with beta/*")
	}
}

func TestDecideDeniesWithNoScopes(t *testing.T) {
	if _, err := Decide(Actor{ID: "x"}, nil, testConfig().Destinations[0], "", time.Now()); err == nil {
		t.Fatal("expected denial when the actor has no resolvable grant")
	}
}

func TestDecideRejectsExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	a := Actor{ID: "x", Expiry: past}
	if _, err := Decide(a, roleScopes(t, Scope{Destinations: []string{"anthropic"}}), testConfig().Destinations[0], "", time.Now()); err == nil {
		t.Fatal("expected expired actor to be rejected")
	}
}

func TestEffectiveScopeAddressingReplaces(t *testing.T) {
	role := Role{Name: "r", Scope: Scope{Addressing: []string{"human:*"}}}
	// nil override addressing inherits the role's
	if got := EffectiveScope(Grant{Role: "r"}, role).Addressing; !sameStrings(got, []string{"human:*"}) {
		t.Fatalf("inherit: got %v", got)
	}
	// set override addressing REPLACES (no merge)
	g := Grant{Role: "r", Overrides: &Override{Addressing: []string{"channel:eng-help"}}}
	if got := EffectiveScope(g, role).Addressing; !sameStrings(got, []string{"channel:eng-help"}) {
		t.Fatalf("replace: got %v", got)
	}
}
