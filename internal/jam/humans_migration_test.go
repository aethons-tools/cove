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
	if plan.aliases[0] != (LegacyAlias{Project: "acme", Name: "dave", UserID: u.ID}) {
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
	m.roles[m.projects["beta"].ID] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:alice", "human:al*"}}}}
	m.roles[m.projects["acme"].ID] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:alice"}}}}
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
	if len(plan.roles) != 1 || plan.roles[0].Project != m.projects["beta"].ID ||
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

func TestPlanRegistryMigrationPolicyRefs(t *testing.T) {
	// A fresh migration: step 1 creates the users, step 3 must see them.
	m := legacyState(t, map[string][]Human{"acme": {{Name: "alice", Login: "a"}, {Name: "bob"}}})
	acme := m.projects["acme"]
	acme.Escalation = []EscalationTier{{Targets: []string{"human:alice", "human:ghost", "channel:eng"}}}
	m.projects["acme"] = acme
	m.roles[m.projects["acme"].ID] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*", "human:bob", "channel:eng", "user:carol"}}}}
	m.actors["h1"] = Actor{ID: "personal-alice-1", TokenHash: "h1", Grants: []Grant{{Project: "acme", Role: "impl", Overrides: &Override{Addressing: []string{"human:alice"}}}}}
	m.instances["personal-alice-1"] = Instance{ActorID: "personal-alice-1", Project: "acme", Owner: "alice", SessionKind: SessionKindPersonal}

	plan := m.planRegistryMigration(0)
	alice, _ := planUserNamed(plan, "alice")
	bob, _ := planUserNamed(plan, "bob")
	if len(plan.instances) != 1 || plan.instances[0].OwnerID != alice.ID {
		t.Fatalf("instances = %+v, want alice's session to carry her id", plan.instances)
	}
	if len(plan.roles) != 1 || !slices.Equal(plan.roles[0].Role.Scope.Addressing, []string{"user:*", "user:" + string(bob.ID), "channel:eng", "user:carol"}) {
		t.Fatalf("roles = %+v", plan.roles)
	}
	if len(plan.actors) != 1 || !slices.Equal(plan.actors[0].Grants[0].Overrides.Addressing, []string{"user:" + string(alice.ID)}) {
		t.Fatalf("actors = %+v", plan.actors)
	}
	var tiers []string
	for _, p := range plan.projects {
		if p.Name == "acme" {
			tiers = p.Escalation[0].Targets
		}
	}
	if !slices.Equal(tiers, []string{"user:" + string(alice.ID), "user:ghost", "channel:eng"}) {
		t.Fatalf("tiers = %v", tiers)
	}

	// From roster_schema 2 (users exist): owners resolve through the legacy
	// alias first, so a collision-renamed human maps to the right user.
	m2 := legacyState(t, map[string][]Human{"beta": nil})
	u := User{ID: ident.New(ident.User), Name: "alice-beta", Status: StatusLive}
	m2.users[u.ID] = u
	m2.aliases["beta"] = map[string]ident.ID{"alice": u.ID}
	m2.instances["p2"] = Instance{ActorID: "p2", Project: "beta", Owner: "alice", SessionKind: SessionKindPersonal}
	if plan := m2.planRegistryMigration(2); len(plan.instances) != 1 || plan.instances[0].OwnerID != u.ID {
		t.Fatalf("from 2 = %+v", plan.instances)
	}
	if again := m2.planRegistryMigration(3); len(again.roles)+len(again.projects) != 0 || len(again.instances) != 1 || again.instances[0].OwnerID != "" {
		t.Fatalf("from 3 must plan only step 6's project id: %+v", again)
	}
	if again := m2.planRegistryMigration(3); again.instances[0].Project != string(m2.projects["beta"].ID) {
		t.Fatalf("step 6 names the project by id: %+v", again.instances)
	}
}

// Past step 1, a "human:<name>" names the live user of that name now; the
// legacy alias is only the fallback for a name no live user has.
func TestPlanPolicyRefsPrefersLiveNameOverAlias(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil})
	alice := User{ID: ident.New(ident.User), Name: "alice", Status: StatusLive}
	renamed := User{ID: ident.New(ident.User), Name: "alice-acme", Status: StatusLive}
	m.users[alice.ID], m.users[renamed.ID] = alice, renamed
	m.aliases["acme"] = map[string]ident.ID{"alice": renamed.ID}
	m.instances["p1"] = Instance{ActorID: "p1", Project: "acme", Owner: "alice", SessionKind: SessionKindPersonal}
	if plan := m.planRegistryMigration(2); len(plan.instances) != 1 || plan.instances[0].OwnerID != alice.ID {
		t.Fatalf("owner = %+v, want the live alice", plan.instances)
	}
}

// Step 4 seeds the standing-session map with the ids live sessions already
// run under, so their state volumes and inboxes carry over untouched.
func TestPlanRegistryMigrationSeedsStandingSessions(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil})
	m.roles[m.projects["acme"].ID] = map[string]Role{"impl": {Name: "impl", Allocation: RoleAllocation{Standing: []StandingSession{{Name: "spider", Prompt: "p"}, {Name: "ant", Prompt: "p"}}}}}
	acme := m.projects["acme"].ID
	m.standing[standingKey{acme, "impl", "ant"}] = "ses-already"
	plan := m.planRegistryMigration(3)
	if len(plan.standing) != 1 || plan.standing[0] != (StandingSessionRef{ProjectID: acme, Role: "impl", Name: "spider", SessionID: StandingActorID("acme", "impl", "spider")}) {
		t.Fatalf("standing = %+v", plan.standing)
	}
	if again := m.planRegistryMigration(4); len(again.standing) != 0 {
		t.Fatalf("from 4 = %+v", again.standing)
	}
}

// Two declarations whose pre-registry ids coincide (names differing only in
// characters the old id mapped to "-") never share one: the second is left
// for the reconciler to start with a minted id.
func TestPlanStandingSessionsSkipsCollidingIDs(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil})
	m.roles[m.projects["acme"].ID] = map[string]Role{"impl": {Name: "impl", Allocation: RoleAllocation{Standing: []StandingSession{{Name: "a b", Prompt: "p"}, {Name: "a-b", Prompt: "p"}}}}}
	plan := m.planRegistryMigration(3)
	if len(plan.standing) != 1 {
		t.Fatalf("standing = %+v, want one seeded entry", plan.standing)
	}
}

// Step 5: roster channels become rooms bound on the connection of their
// service; a ref two projects shared stays with the first project's room and
// the second only posts there; the docs drop their channels.
func TestPlanRegistryMigrationRosterChannelsToRooms(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil, "zeta": nil})
	acme, zeta := m.projects["acme"], m.projects["zeta"]
	acme.Roster.Channels = []RosterChannel{{Name: "eng", Service: "linear", Ref: "ACME-1"}, {Name: "chat", Service: "discord", Ref: "42"}}
	zeta.Roster.Channels = []RosterChannel{{Name: "eng", Ref: "ACME-1"}, {Name: "bad", Service: "carrier-pigeon", Ref: "x"}}
	m.projects["acme"], m.projects["zeta"] = acme, zeta

	plan := m.planRegistryMigration(4)
	if len(plan.connections) != 2 {
		t.Fatalf("connections = %+v, want one linear and one discord", plan.connections)
	}
	if len(plan.channels) != 3 {
		t.Fatalf("channels = %+v, want 3 rooms", plan.channels)
	}
	byKey := map[string][]Channel{}
	for _, c := range plan.channels {
		if c.Kind != SourceRoom || c.Status != StatusLive || c.ID.Kind() != ident.Channel {
			t.Fatalf("room = %+v", c)
		}
		byKey[c.Key] = append(byKey[c.Key], c)
	}
	if e := byKey["eng"]; len(e) != 2 || e[0].ProjectID != acme.ID || e[0].Bindings[0].Mode != BindBoth || e[1].ProjectID != zeta.ID || e[1].Bindings[0].Mode != BindEgress {
		t.Fatalf("eng rooms = %+v", e)
	}
	if len(plan.report.Notes) != 2 {
		t.Fatalf("notes = %q, want the shared ref and the unknown service", plan.report.Notes)
	}
	if len(plan.projects) != 2 || len(plan.projects[0].Roster.Channels) != 0 || len(plan.projects[1].Roster.Channels) != 1 {
		t.Fatalf("projects = %+v, want acme's doc cleared and zeta's keeping its unplaceable channel", plan.projects)
	}

	m.applyHumanPlan(plan)
	if again := m.planRegistryMigration(5); len(again.channels)+len(again.projects) != 0 {
		t.Fatalf("from 5 = %+v", again)
	}
	if got := roomChannels(m, acme.ID); len(got) != 2 || got[0] != (RosterChannel{Name: "chat", Service: "discord", Ref: "42"}) {
		t.Fatalf("rooms = %+v", got)
	}
}

// Step 5 is lenient about what older Jams stored: a service in any case maps
// to its kind, a name given twice is one room (the last wins, as the doc's
// upsert did), and a channel it can't place stays in the doc rather than
// being lost.
func TestPlanRoomsKeepsWhatItCannotPlace(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil})
	p := m.projects["acme"]
	p.Roster.Channels = []RosterChannel{
		{Name: "chat", Service: " Discord ", Ref: "42"},
		{Name: "eng", Ref: "ACME-1"}, {Name: "eng", Ref: "ACME-2"},
		{Name: "pigeon", Service: "carrier-pigeon", Ref: "x"},
	}
	m.projects["acme"] = p
	plan := m.planRegistryMigration(4)
	if len(plan.channels) != 2 {
		t.Fatalf("channels = %+v, want chat and one eng", plan.channels)
	}
	m.applyHumanPlan(plan)
	want := []RosterChannel{{Name: "chat", Service: "discord", Ref: "42"}, {Name: "eng", Service: "linear", Ref: "ACME-2"}}
	if got := roomChannels(m, p.ID); !slices.Equal(got, want) {
		t.Fatalf("rooms = %+v, want %+v", got, want)
	}
	if kept := m.projects["acme"].Roster.Channels; len(kept) != 1 || kept[0].Name != "pigeon" {
		t.Fatalf("doc channels = %+v, want the unplaceable one kept", kept)
	}
}

// A fresh migration (from 0) moves humans and channels together: step 5
// starts from the doc step 1 rewrote, so neither change is lost.
func TestPlanRegistryMigrationRoomsKeepEarlierSteps(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": {{Name: "alice"}}})
	p := m.projects["acme"]
	p.Roster.Channels = []RosterChannel{{Name: "eng", Ref: "ACME-1"}}
	m.projects["acme"] = p
	plan := m.planRegistryMigration(0)
	if len(plan.projects) != 1 || len(plan.projects[0].Roster.Humans)+len(plan.projects[0].Roster.Channels) != 0 {
		t.Fatalf("projects = %+v", plan.projects)
	}
	if len(plan.users) != 1 || len(plan.channels) != 1 {
		t.Fatalf("users %+v, channels %+v", plan.users, plan.channels)
	}
}

// roomChannels lists project's live rooms in the legacy roster-channel shape
// (name, connection kind, ingress ref), sorted by name.
func roomChannels(m *memState, project ident.ID) []RosterChannel {
	var out []RosterChannel
	for _, ch := range m.ListChannels(project, SourceRoom) {
		rc := RosterChannel{Name: ch.Key}
		if len(ch.Bindings) > 0 {
			if c, ok := m.GetConnection(ch.Bindings[0].ConnectionID); ok {
				rc.Service = c.Kind
			}
			rc.Ref = ch.Bindings[0].Ref
		}
		out = append(out, rc)
	}
	return out
}

// Step 6 names every grant's and instance's project by id; an unknown name is
// kept (and noted), an id is left alone.
func TestPlanProjectRefs(t *testing.T) {
	m := legacyState(t, map[string][]Human{"acme": nil, DefaultProject: nil})
	acme := m.projects["acme"].ID
	m.actors["h1"] = Actor{ID: "a1", TokenHash: "h1", Grants: []Grant{{Project: "acme", Role: "r"}, {Project: "", Role: "r"}, {Project: "ghost", Role: "r"}}}
	m.actors["h2"] = Actor{ID: "a2", TokenHash: "h2", Grants: []Grant{{Project: string(acme), Role: "r"}}}
	m.instances["i1"] = Instance{ActorID: "i1", Project: "acme"}
	m.instances["i2"] = Instance{ActorID: "i2", Project: string(acme)}
	plan := m.planRegistryMigration(5)
	if len(plan.actors) != 1 || plan.actors[0].Grants[0].Project != string(acme) ||
		plan.actors[0].Grants[1].Project != string(m.projects[DefaultProject].ID) || plan.actors[0].Grants[2].Project != "ghost" {
		t.Fatalf("actors = %+v", plan.actors)
	}
	if len(plan.instances) != 1 || plan.instances[0].ActorID != "i1" || plan.instances[0].Project != string(acme) {
		t.Fatalf("instances = %+v", plan.instances)
	}
	if len(plan.report.Notes) != 1 {
		t.Fatalf("notes = %q", plan.report.Notes)
	}
	if again := m.planRegistryMigration(6); len(again.actors)+len(again.instances) != 0 {
		t.Fatalf("from 6 = %+v", again)
	}
}
