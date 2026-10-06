package jam

import (
	"errors"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
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

func TestDecideSendUserTargets(t *testing.T) {
	alice, bob := ident.New(ident.User), ident.New(ident.User)
	roles := map[string]map[string]Role{"acme": {
		"any":    {Name: "any", Scope: Scope{Addressing: []string{"user:*"}}},
		"legacy": {Name: "legacy", Scope: Scope{Addressing: []string{"human:alice"}}},
		"byid":   {Name: "byid", Scope: Scope{Addressing: []string{"user:" + string(alice)}}},
		"byname": {Name: "byname", Scope: Scope{Addressing: []string{"user:alice"}}},
	}}
	rosters := map[string]Roster{"acme": {Humans: []Human{
		{Name: "alice", UserID: alice, Handle: "alice.h"},
		{Name: "bob", UserID: bob},
	}}}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	now := time.Unix(1, 0)
	as := func(role string) Actor { return Actor{ID: role, Grants: []Grant{{Project: "acme", Role: role}}} }

	for _, tc := range []struct {
		role, target string
		ok           bool
	}{
		{"any", "user:alice", true},
		{"any", "user:" + string(alice), true},
		{"any", "human:alice", true}, // alias
		{"legacy", "user:alice", true},
		{"legacy", "user:" + string(alice), true},
		{"legacy", "user:bob", false},
		{"byid", "user:alice", true},
		{"byid", "user:bob", false},
		{"byname", "user:" + string(alice), true},
		{"byname", "user:" + string(bob), false}, // an id never widens what a name glob grants
	} {
		st, err := DecideSend(as(tc.role), getRole, getRoster, tc.target, now)
		if tc.ok != (err == nil) {
			t.Errorf("%s → %s: err = %v, want ok=%v", tc.role, tc.target, err, tc.ok)
			continue
		}
		if tc.ok && (st.Kind != "human" || st.Name != "alice" || st.UserID != alice || st.Handle != "alice.h") {
			t.Errorf("%s → %s resolved to %+v", tc.role, tc.target, st)
		}
	}
	if _, err := DecideSend(as("any"), getRole, getRoster, "user:"+string(ident.New(ident.User)), now); !errors.Is(err, ErrSendUnresolved) {
		t.Errorf("unknown user id: %v, want ErrSendUnresolved", err)
	}
	got := ListTargets(as("legacy"), getRole, getRoster, now)
	if len(got) != 1 || got[0].Name != "alice" {
		t.Errorf("ListTargets under a legacy human: glob = %+v", got)
	}
}

// A name glob never matches a user id: every id is "usr_…", so a glob like
// user:u* or *r* would otherwise reach every member.
func TestNameGlobsNeverMatchIDs(t *testing.T) {
	bob := ident.New(ident.User)
	roles := map[string]map[string]Role{"acme": {
		"u":    {Name: "u", Scope: Scope{Addressing: []string{"user:u*"}}},
		"star": {Name: "star", Scope: Scope{Addressing: []string{"user:*_*", "user:*0*"}}},
		"all":  {Name: "all", Scope: Scope{Addressing: []string{"user:*"}}},
		"any":  {Name: "any", Scope: Scope{Addressing: []string{"*"}}},
	}}
	rosters := map[string]Roster{"acme": {Humans: []Human{{Name: "bob", UserID: bob}}}}
	getRole := func(p, r string) (Role, bool) { rr, ok := roles[p][r]; return rr, ok }
	getRoster := func(p string) (Roster, bool) { rr, ok := rosters[p]; return rr, ok }
	as := func(role string) Actor { return Actor{ID: role, Grants: []Grant{{Project: "acme", Role: role}}} }
	now := time.Unix(1, 0)
	for _, role := range []string{"u", "star"} {
		for _, target := range []string{"user:bob", "user:" + string(bob)} {
			if _, err := DecideSend(as(role), getRole, getRoster, target, now); !errors.Is(err, ErrSendDenied) {
				t.Errorf("%s → %s: %v, want denied", role, target, err)
			}
		}
		if got := ListTargets(as(role), getRole, getRoster, now); len(got) != 0 {
			t.Errorf("%s lists %+v, want nobody", role, got)
		}
	}
	for _, role := range []string{"all", "any"} {
		if _, err := DecideSend(as(role), getRole, getRoster, "user:"+string(bob), now); err != nil {
			t.Errorf("%s → bob's id: %v, want allowed", role, err)
		}
	}
}
