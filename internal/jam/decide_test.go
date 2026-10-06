package jam

import (
	"testing"
	"time"
)

func roleScopes(t *testing.T, scopes ...Scope) []Scope { t.Helper(); return scopes }

func TestEffectiveScopeInheritsRole(t *testing.T) {
	r := Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "git"}, Credentials: map[string]string{"git": "pat-a"}}}
	got := EffectiveScope(Grant{Project: "p", Role: "guest"}, r)
	if len(got.Destinations) != 2 || got.Credentials["git"] != "pat-a" {
		t.Fatalf("inherited scope = %+v", got)
	}
}

func TestEffectiveScopeOverrideReplaces(t *testing.T) {
	r := Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "git"}}}
	g := Grant{Project: "p", Role: "guest", Overrides: &Override{Destinations: []string{"git"}}}
	got := EffectiveScope(g, r)
	if len(got.Destinations) != 1 || got.Destinations[0] != "git" {
		t.Fatalf("destinations should be replaced: %+v", got)
	}
}

func TestDecideAllowsWhenAGrantAuthorizes(t *testing.T) {
	dest := testConfig().Destinations[0] // anthropic
	dec, err := Decide(Actor{ID: "spider-18"}, roleScopes(t, Scope{Destinations: []string{"anthropic"}}), dest, time.Now())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.NeedCred || dec.CredName != "anthropic-bearer" || dec.Apply != ApplyBearer {
		t.Fatalf("decision = %+v", dec)
	}
}

func TestDecideAdditiveAcrossGrants(t *testing.T) {
	git := testConfig().Destinations[1]
	// first scope lacks git; second authorizes it — additive.
	scopes := roleScopes(t,
		Scope{Destinations: []string{"anthropic"}},
		Scope{Destinations: []string{"git"}},
	)
	if _, err := Decide(Actor{ID: "x"}, scopes, git, time.Now()); err != nil {
		t.Fatalf("second grant should authorize: %v", err)
	}
}

func TestDecideNoRepoGate(t *testing.T) {
	git := testConfig().Destinations[1]
	if _, err := Decide(Actor{ID: "x"}, roleScopes(t, Scope{Destinations: []string{"git"}}), git, time.Now()); err != nil {
		t.Fatalf("git with no repo policy should be allowed: %v", err)
	}
}

func TestDecideDeniesWithNoScopes(t *testing.T) {
	if _, err := Decide(Actor{ID: "x"}, nil, testConfig().Destinations[0], time.Now()); err == nil {
		t.Fatal("expected denial when the actor has no resolvable grant")
	}
}

func TestDecideRejectsExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	a := Actor{ID: "x", Expiry: past}
	if _, err := Decide(a, roleScopes(t, Scope{Destinations: []string{"anthropic"}}), testConfig().Destinations[0], time.Now()); err == nil {
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

func TestListTargets(t *testing.T) {
	roles := map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}}
	rosters := map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "h"}, {Name: "bob", Handle: "h2"}}, Channels: []RosterChannel{{Name: "eng", Ref: "R"}}}}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	a := Actor{ID: "a", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	got := ListTargets(a, getRole, getRoster, time.Unix(1, 0))
	// only humans are addressable (channel not in addressing)
	names := map[string]bool{}
	for _, tg := range got {
		names[tg.Kind+":"+tg.Name] = true
	}
	if !names["human:alice"] || !names["human:bob"] || names["channel:eng"] {
		t.Fatalf("unexpected targets: %+v", got)
	}
}

func TestDecideUsesRoleMappedCredential(t *testing.T) {
	git := testConfig().Destinations[1] // default cred git-pat
	s := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat-cove"}}
	dec, err := Decide(Actor{ID: "x"}, roleScopes(t, s), git, time.Now())
	if err != nil || dec.CredName != "git-pat-cove" || !dec.NeedCred {
		t.Fatalf("decision = %+v, %v", dec, err)
	}
}

func TestDecideUnmappedFallsBackToDestinationDefault(t *testing.T) {
	git := testConfig().Destinations[1]
	s := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"anthropic": "other"}}
	dec, err := Decide(Actor{ID: "x"}, roleScopes(t, s), git, time.Now())
	if err != nil || dec.CredName != "git-pat" {
		t.Fatalf("decision = %+v, %v", dec, err)
	}
}

func TestDecideConflictingCredentialsDenied(t *testing.T) {
	git := testConfig().Destinations[1]
	scopes := roleScopes(t,
		Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "pat-a"}},
		Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "pat-b"}},
	)
	if _, err := Decide(Actor{ID: "x"}, scopes, git, time.Now()); err == nil {
		t.Fatal("conflicting credentials across grants must deny")
	}
}

func TestEffectiveScopeCredentialsOverride(t *testing.T) {
	r := Role{Name: "w", Scope: Scope{Destinations: []string{"git", "anthropic"}, Credentials: map[string]string{"git": "pat-a"}}}
	inherit := EffectiveScope(Grant{Overrides: &Override{Destinations: []string{"git"}}}, r)
	if inherit.Credentials["git"] != "pat-a" {
		t.Fatalf("credentials should inherit when override nil: %+v", inherit)
	}
	repl := EffectiveScope(Grant{Overrides: &Override{Credentials: map[string]string{"git": "pat-b"}}}, r)
	if repl.Credentials["git"] != "pat-b" {
		t.Fatalf("credentials should be replaced: %+v", repl)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
