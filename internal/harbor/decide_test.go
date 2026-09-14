package harbor

import (
	"errors"
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

func TestDecideSend(t *testing.T) {
	roles := map[string]map[string]Role{
		"acme": {
			"impl":   {Name: "impl", Scope: Scope{Addressing: []string{"human:*", "channel:eng-help"}}},
			"noaddr": {Name: "noaddr"},
		},
	}
	rosters := map[string]Roster{
		"acme": {
			Humans:   []Human{{Name: "alice", Handle: "alice.h"}},
			Channels: []Channel{{Name: "eng-help", Service: "linear", Ref: "ACME-1"}},
		},
	}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	now := time.Unix(1_000, 0)

	actor := Actor{ID: "a", Grants: []Grant{{Project: "acme", Role: "impl"}}}

	// authorized human → resolves handle
	st, err := DecideSend(actor, getRole, getRoster, "human:alice", now)
	if err != nil || st.Kind != "human" || st.Handle != "alice.h" || st.Project != "acme" {
		t.Fatalf("human: %+v err=%v", st, err)
	}
	// authorized channel → resolves ref
	st, err = DecideSend(actor, getRole, getRoster, "channel:eng-help", now)
	if err != nil || st.Kind != "channel" || st.Ref != "ACME-1" {
		t.Fatalf("channel: %+v err=%v", st, err)
	}
	// glob does not authorize channel:other → denied (403), and never leaks existence
	if _, err := DecideSend(actor, getRole, getRoster, "channel:other", now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expected denied, got %v", err)
	}
	// authorized-in-form (human:*) but not in roster → unresolved (404)
	if _, err := DecideSend(actor, getRole, getRoster, "human:bob", now); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("expected unresolved, got %v", err)
	}
	// malformed target → denied
	if _, err := DecideSend(actor, getRole, getRoster, "alice", now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expected denied for malformed, got %v", err)
	}
	// no-addressing role → denied
	na := Actor{ID: "n", Grants: []Grant{{Project: "acme", Role: "noaddr"}}}
	if _, err := DecideSend(na, getRole, getRoster, "human:alice", now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("expected denied for no addressing, got %v", err)
	}
	// expired actor → denied
	exp := Actor{ID: "e", Expiry: now.Add(-time.Hour), Grants: []Grant{{Project: "acme", Role: "impl"}}}
	if _, err := DecideSend(exp, getRole, getRoster, "human:alice", now); err == nil {
		t.Fatal("expected expired actor denied")
	}
}

func TestDecideSendPerGrantExistential(t *testing.T) {
	// grant A authorizes humans in acme; grant B authorizes channels in beta.
	roles := map[string]map[string]Role{
		"acme": {"a": {Name: "a", Scope: Scope{Addressing: []string{"human:*"}}}},
		"beta": {"b": {Name: "b", Scope: Scope{Addressing: []string{"channel:*"}}}},
	}
	rosters := map[string]Roster{
		"acme": {Humans: []Human{{Name: "alice", Handle: "h"}}},
		"beta": {Channels: []Channel{{Name: "ops", Ref: "BETA-9"}}},
	}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	a := Actor{ID: "x", Grants: []Grant{{Project: "acme", Role: "a"}, {Project: "beta", Role: "b"}}}
	now := time.Unix(1, 0)
	if st, err := DecideSend(a, getRole, getRoster, "channel:ops", now); err != nil || st.Ref != "BETA-9" {
		t.Fatalf("beta channel via grant B: %+v %v", st, err)
	}
	// a human that only exists in beta's project is not addressable (acme grant authorizes humans but acme has no bob; beta grant doesn't authorize humans)
	if _, err := DecideSend(a, getRole, getRoster, "human:ops", now); err == nil {
		t.Fatal("expected cross-project recombination to fail")
	}
}

func TestListTargets(t *testing.T) {
	roles := map[string]map[string]Role{"acme": {"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}}
	rosters := map[string]Roster{"acme": {Humans: []Human{{Name: "alice", Handle: "h"}, {Name: "bob", Handle: "h2"}}, Channels: []Channel{{Name: "eng", Ref: "R"}}}}
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
