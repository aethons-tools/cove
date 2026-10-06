package jam

import (
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

// legacyState is a memState holding projects whose docs still carry humans.
func legacyState(t *testing.T, rosters map[string][]Human) *memState {
	t.Helper()
	m := newMemState()
	for name, humans := range rosters {
		p := newProject(name)
		p.Roster.Humans = humans
		m.projects[name] = p
	}
	return m
}

func planUserNamed(p humanPlan, name string) (User, bool) {
	for _, u := range p.users {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

func TestPlanHumanMigrationGrouping(t *testing.T) {
	disc := func(addr, uid string) []DeliveryProfile {
		return []DeliveryProfile{{Service: "discord", Address: addr, UserID: uid}}
	}
	for _, tc := range []struct {
		name      string
		rosters   map[string][]Human
		wantUsers []string // sorted
	}{
		{"single", map[string][]Human{"acme": {{Name: "alice"}}}, []string{"alice"}},
		{"same name, no ids → one user", map[string][]Human{"acme": {{Name: "alice"}}, "beta": {{Name: "alice"}}}, []string{"alice"}},
		{"shared login across projects and names → one user", map[string][]Human{
			"acme": {{Name: "alice", Login: "auth0|a"}}, "beta": {{Name: "al", Login: "auth0|a"}}}, []string{"alice"}},
		{"same name, conflicting logins → two users", map[string][]Human{
			"acme": {{Name: "alice", Login: "auth0|a"}}, "beta": {{Name: "alice", Login: "auth0|b"}}}, []string{"alice", "alice-beta"}},
		{"weak human joins the unique strong group of its name", map[string][]Human{
			"acme": {{Name: "alice", Delivery: disc("c1", "111")}}, "beta": {{Name: "alice"}}}, []string{"alice"}},
		{"weak human stays apart from two conflicting strong groups", map[string][]Human{
			"acme": {{Name: "alice", Login: "a"}}, "beta": {{Name: "alice", Login: "b"}}, "gamma": {{Name: "alice"}}},
			[]string{"alice", "alice-beta", "alice-gamma"}},
		{"oidc overlap groups", map[string][]Human{
			"acme": {{Name: "alice", Identity: []OIDCIdentity{{Issuer: "i", Subject: "s"}}}},
			"beta": {{Name: "ally", Identity: []OIDCIdentity{{Issuer: "i", Subject: "s"}}}}}, []string{"alice"}},
		{"invalid name sanitized", map[string][]Human{"acme": {{Name: "al ice"}}}, []string{"al-ice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := legacyState(t, tc.rosters).planHumanMigration()
			var got []string
			for _, u := range plan.users {
				got = append(got, u.Name)
				if u.ID.Kind() != ident.User || u.Status != StatusLive {
					t.Fatalf("user %+v", u)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.wantUsers) {
				t.Fatalf("users = %v, want %v (notes %v)", got, tc.wantUsers, plan.report.Notes)
			}
			humans := 0
			for _, hs := range tc.rosters {
				humans += len(hs)
			}
			if len(plan.aliases) != humans || len(plan.memberships) != humans {
				t.Fatalf("aliases %d, memberships %d; want %d each", len(plan.aliases), len(plan.memberships), humans)
			}
			for _, p := range plan.projects {
				if len(p.Roster.Humans) != 0 {
					t.Fatalf("project %s keeps humans", p.Name)
				}
			}
		})
	}
}

func TestPlanHumanMigrationFields(t *testing.T) {
	m := legacyState(t, map[string][]Human{
		"acme": {{Name: "dave", Handle: "@dave", Login: "auth0|dave",
			Delivery: []DeliveryProfile{{Service: "discord", Address: "chan-9", UserID: "123"}},
			Identity: []OIDCIdentity{{Issuer: "https://g", Subject: "dave-sub"}}}},
	})
	plan := m.planHumanMigration()
	u, ok := planUserNamed(plan, "dave")
	if !ok || !slices.Equal(u.Logins, []string{"auth0|dave"}) || len(u.OIDC) != 1 {
		t.Fatalf("dave = %+v, %v", u, ok)
	}
	if len(plan.connections) != 2 {
		t.Fatalf("connections = %+v, want implicit linear + discord", plan.connections)
	}
	kinds := map[ident.ID]string{}
	for _, c := range plan.connections {
		kinds[c.ID] = c.Kind
	}
	var linear, discord Account
	for _, a := range plan.accounts {
		switch kinds[a.ConnectionID] {
		case "linear":
			linear = a
		case "discord":
			discord = a
		}
		if a.UserID != u.ID {
			t.Fatalf("account %+v not linked to dave", a)
		}
	}
	if linear.Handle != "@dave" || discord.ServiceUID != "123" {
		t.Fatalf("accounts = %+v", plan.accounts)
	}
	ms := plan.memberships[0]
	if len(ms.Delivery) != 1 || ms.Delivery[0].Address != "chan-9" || ms.Delivery[0].UserID != "" {
		t.Fatalf("membership = %+v", ms)
	}
	if plan.aliases[0] != (legacyAlias{Project: "acme", Name: "dave", User: u.ID}) {
		t.Fatalf("alias = %+v", plan.aliases[0])
	}
}

func TestPlanHumanMigrationRenameRewritesExactRefs(t *testing.T) {
	m := legacyState(t, map[string][]Human{
		"acme": {{Name: "alice", Login: "a"}},
		"beta": {{Name: "alice", Login: "b"}},
	})
	beta := m.projects["beta"]
	beta.Escalation = []EscalationTier{{Targets: []string{"human:alice", "human:bob"}}}
	m.projects["beta"] = beta
	m.roles["beta"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:alice", "human:al*"}}}}
	m.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:alice"}}}}
	m.instances["personal-alice-1"] = Instance{ActorID: "personal-alice-1", Project: "beta", Owner: "alice"}
	m.actors["h1"] = Actor{ID: "personal-alice-1", TokenHash: "h1", Grants: []Grant{{Project: "beta", Role: "impl", Overrides: &Override{Addressing: []string{"human:alice"}}}}}

	plan := m.planHumanMigration()
	if _, ok := planUserNamed(plan, "alice-beta"); !ok {
		t.Fatalf("users = %+v", plan.users)
	}
	var betaDoc Project
	for _, p := range plan.projects {
		if p.Name == "beta" {
			betaDoc = p
		}
	}
	if got := betaDoc.Escalation[0].Targets; !slices.Equal(got, []string{"human:alice-beta", "human:bob"}) {
		t.Fatalf("beta tiers = %v", got)
	}
	if len(plan.roles) != 1 || plan.roles[0].Project != "beta" ||
		!slices.Equal(plan.roles[0].Role.Scope.Addressing, []string{"human:alice-beta", "human:al*"}) {
		t.Fatalf("roles = %+v (acme's role must be untouched)", plan.roles)
	}
	if len(plan.instances) != 1 || plan.instances[0].Owner != "alice-beta" {
		t.Fatalf("instances = %+v", plan.instances)
	}
	if len(plan.actors) != 1 || plan.actors[0].Grants[0].Overrides.Addressing[0] != "human:alice-beta" {
		t.Fatalf("actors = %+v", plan.actors)
	}
	if m.actors["h1"].Grants[0].Overrides.Addressing[0] != "human:alice" {
		t.Fatal("the planner must not mutate the cache")
	}
	var globNote bool
	for _, n := range plan.report.Notes {
		globNote = globNote || strings.Contains(n, `"human:al*"`)
	}
	if !globNote {
		t.Fatalf("notes %v must report the unrewritten glob", plan.report.Notes)
	}
}

func TestPlanHumanMigrationReusesRegistry(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": {{Name: "alice", Login: "auth0|a"}, {Name: "bob", Handle: "@b"}}})
	existing := User{ID: ident.New(ident.User), Name: "alicia", Status: StatusLive, Logins: []string{"auth0|a"}}
	m.users[existing.ID] = existing
	plan := m.planHumanMigration()
	u, ok := planUserNamed(plan, "alicia")
	if !ok || u.ID != existing.ID || plan.report.Users != 1 {
		t.Fatalf("users = %+v (report %+v); alice must reuse the existing user by login", plan.users, plan.report)
	}
}

func TestPlanHumanMigrationHandleConflictReported(t *testing.T) {
	m := legacyState(t, map[string][]Human{
		"acme": {{Name: "alice", Login: "a", Handle: "@x"}},
		"beta": {{Name: "bob", Login: "b", Handle: "@x"}},
	})
	plan := m.planHumanMigration()
	if len(plan.accounts) != 1 {
		t.Fatalf("accounts = %+v, want one (the handle stays with its first user)", plan.accounts)
	}
	alice, _ := planUserNamed(plan, "alice")
	if plan.accounts[0].UserID != alice.ID {
		t.Fatalf("account linked to %s, want alice", plan.accounts[0].UserID)
	}
	if len(plan.report.Notes) == 0 {
		t.Fatal("the skipped link must be reported")
	}
}

func TestPlanHumanMigrationNothingToDo(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil})
	if plan := m.planHumanMigration(); len(plan.users)+len(plan.projects)+len(plan.connections) != 0 {
		t.Fatalf("plan = %+v, want empty", plan)
	}
}

func TestPlanHumanMigrationLongNameCollisionTerminates(t *testing.T) {
	long := strings.Repeat("x", 64)
	m := legacyState(t, map[string][]Human{
		"acme": {{Name: long, Login: "a"}},
		"beta": {{Name: long, Login: "b"}},
		"zeta": {{Name: long, Login: "c"}},
	})
	plan := m.planHumanMigration()
	seen := map[string]bool{}
	for _, u := range plan.users {
		if seen[u.Name] || ValidateEntityName(u.Name) != nil {
			t.Fatalf("user names = %+v: duplicate or invalid %q", plan.users, u.Name)
		}
		seen[u.Name] = true
	}
	if len(seen) != 3 {
		t.Fatalf("users = %d, want 3", len(seen))
	}
}

func TestPlanHumanMigrationStrongGroupReusesIdentitylessUserByName(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": {{Name: "bob", Login: "auth0|b"}}})
	bob := User{ID: ident.New(ident.User), Name: "bob", Status: StatusLive}
	m.users[bob.ID] = bob
	plan := m.planHumanMigration()
	if len(plan.users) != 1 || plan.users[0].ID != bob.ID || !slices.Equal(plan.users[0].Logins, []string{"auth0|b"}) {
		t.Fatalf("users = %+v; the human must become the existing identity-less bob", plan.users)
	}
}

func TestPlanHumanMigrationNotesSeveralHandles(t *testing.T) {
	m := legacyState(t, map[string][]Human{
		"acme": {{Name: "alice", Login: "a", Handle: "@alice"}},
		"beta": {{Name: "alice", Login: "a", Handle: "@alice-b"}},
	})
	plan := m.planHumanMigration()
	var noted bool
	for _, n := range plan.report.Notes {
		noted = noted || strings.Contains(n, "handles")
	}
	if !noted {
		t.Fatalf("notes = %v; a user with two handles must be reported", plan.report.Notes)
	}
}

func TestPlanRegistryMigrationChatServices(t *testing.T) {
	m := legacyState(t, map[string][]Human{
		"acme": {{Name: "alice", Delivery: []DeliveryProfile{{Service: "discord", Address: "inbox", UserID: "111"}}}},
		"beta": nil,
	})
	for name, cs := range map[string]string{"acme": "discord", "beta": "slack"} {
		p := m.projects[name]
		p.ChatService = cs
		m.projects[name] = p
	}
	plan := m.planRegistryMigration(0)
	var discord []Connection
	for _, c := range plan.connections {
		if c.Kind == "discord" {
			discord = append(discord, c)
		}
	}
	if len(discord) != 1 {
		t.Fatalf("connections = %+v, want one discord connection shared by the account and the chat service", plan.connections)
	}
	docs := map[string]Project{}
	for _, p := range plan.projects {
		docs[p.Name] = p
	}
	if docs["acme"].ChatService != string(discord[0].ID) || len(docs["acme"].Roster.Humans) != 0 {
		t.Fatalf("acme = %+v", docs["acme"])
	}
	if docs["beta"].ChatService != "" {
		t.Fatalf("beta's chat service %q is no chat kind and must be cleared", docs["beta"].ChatService)
	}
	var noted bool
	for _, n := range plan.report.Notes {
		noted = noted || strings.Contains(n, `"slack"`)
	}
	if !noted {
		t.Fatalf("notes = %v; clearing slack must be reported", plan.report.Notes)
	}

	// From roster_schema 1 (humans already migrated): only chat services move.
	m2 := legacyState(t, map[string][]Human{"acme": nil})
	p := m2.projects["acme"]
	p.ChatService = "discord"
	m2.projects["acme"] = p
	c := Connection{ID: ident.New(ident.Connection), Kind: "discord", Name: "discord", Status: StatusLive}
	m2.connections[c.ID] = c
	plan = m2.planRegistryMigration(1)
	if len(plan.connections) != 0 || len(plan.users) != 0 || len(plan.projects) != 1 || plan.projects[0].ChatService != string(c.ID) {
		t.Fatalf("from 1 = %+v", plan)
	}
	if again := m2.planRegistryMigration(2); len(again.projects) != 0 {
		t.Fatalf("from 2 must plan nothing: %+v", again)
	}
}
