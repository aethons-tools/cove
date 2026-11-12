package jam

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

func connectorStore(t *testing.T, dests []Destination, roles map[string]Scope) Store {
	t.Helper()
	st := NewMemStore()
	for _, d := range dests {
		if err := st.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	mustCreateProject(t, st, "p")
	for name, s := range roles {
		if err := st.PutRole("p", Role{Name: name, Scope: s}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func actorWith(roles ...string) Actor {
	a := Actor{ID: "a"}
	for _, r := range roles {
		a.Grants = append(a.Grants, Grant{Project: "p", Role: r})
	}
	return a
}

var (
	legacyAnthropic = Destination{Name: "anthropic", Endpoints: []Endpoint{{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyXAPIKey, Apply: ApplyXAPIKey}}}
	legacyGit       = Destination{Name: "git", Endpoints: []Endpoint{{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword}}}
	ghAPI           = Destination{Name: "github-api", Endpoints: []Endpoint{{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: ApplyBearer, Apply: ApplyBearer}},
		Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"}}
)

func TestConnectorForLegacyDestinations(t *testing.T) {
	st := connectorStore(t, []Destination{legacyAnthropic, legacyGit}, map[string]Scope{"w": {Destinations: []string{"anthropic", "git"}}})
	c, err := ConnectorFor(st, actorWith("w"))
	want := map[string]string{"ANTHROPIC_BASE_URL": "{base}/anthropic", "ANTHROPIC_API_KEY": "{token}"}
	if err != nil || !maps.Equal(c.Env, want) || c.GitRoute != "/git/" {
		t.Fatalf("connector = %+v, %v", c, err)
	}
}

func TestConnectorForPoolAnthropicUsesAuthToken(t *testing.T) {
	pool := legacyAnthropic
	pool.Endpoints = slices.Clone(pool.Endpoints)
	pool.Endpoints[0].IdentityIn = ApplyBearer
	st := connectorStore(t, []Destination{pool}, map[string]Scope{"w": {Destinations: []string{"anthropic"}}})
	c, err := ConnectorFor(st, actorWith("w"))
	if err != nil || c.Env["ANTHROPIC_AUTH_TOKEN"] != "{token}" || c.Env["ANTHROPIC_API_KEY"] != "" || c.GitRoute != "" {
		t.Fatalf("connector = %+v, %v", c, err)
	}
}

func TestConnectorForDeclaredEnvAndURL(t *testing.T) {
	d := ghAPI
	d.Env = map[string]string{"GH_HOST": "{host}", "API": "{url}"}
	st := connectorStore(t, []Destination{d, legacyGit}, map[string]Scope{"w": {Destinations: []string{"github-api"}}})
	c, err := ConnectorFor(st, actorWith("w"))
	if err != nil || c.Env["GH_HOST"] != "{host}" || c.Env["API"] != "{base}/api/v3" || c.GitRoute != "" {
		t.Fatalf("connector = %+v, %v (git out of scope must not route)", c, err)
	}
}

func TestConnectorForUnionsGrantsAndSkipsUnknown(t *testing.T) {
	st := connectorStore(t, []Destination{legacyAnthropic, ghAPI}, map[string]Scope{
		"a": {Destinations: []string{"anthropic", "ghost"}},
		"b": {Destinations: []string{"github-api"}},
	})
	c, err := ConnectorFor(st, actorWith("a", "b"))
	if err != nil || c.Env["ANTHROPIC_API_KEY"] == "" || c.Env["GH_HOST"] == "" {
		t.Fatalf("connector = %+v, %v", c, err)
	}
}

func TestConnectorForConflicts(t *testing.T) {
	same := Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x"}}, Env: map[string]string{"GH_HOST": "{host}"}}
	diff := Destination{Name: "y", Endpoints: []Endpoint{{Name: "y", Route: "/y/", Upstream: "https://y"}}, Env: map[string]string{"GH_HOST": "other"}}
	st := connectorStore(t, []Destination{ghAPI, same, diff, legacyGit, {Name: "git2", Endpoints: []Endpoint{{Name: "git2", Route: "/git2/", Upstream: "https://github.com", Git: true}}}},
		map[string]Scope{"ok": {Destinations: []string{"github-api", "x"}}, "env": {Destinations: []string{"github-api", "y"}}, "git": {Destinations: []string{"git", "git2"}}})
	if _, err := ConnectorFor(st, actorWith("ok")); err != nil {
		t.Fatalf("equal values must not conflict: %v", err)
	}
	if _, err := ConnectorFor(st, actorWith("env")); err == nil {
		t.Fatal("differing values for one variable must error")
	}
	if _, err := ConnectorFor(st, actorWith("git")); err == nil {
		t.Fatal("two git routes must error")
	}
}

func TestDestinationValidateEnv(t *testing.T) {
	if err := ghAPI.ValidateEnv(); err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{{"lower": "x"}, {"JAM_X": "x"}, {"X": "{nope}"}} {
		if err := (Destination{Env: env}).ValidateEnv(); err == nil {
			t.Errorf("%v: want error", env)
		}
	}
}

// Env reaching the store other than through the admin API (config import,
// direct writes) is still validated before it can reach a studio.
func TestConnectorForRejectsInvalidEnv(t *testing.T) {
	bad := Destination{Name: "x", Endpoints: []Endpoint{{Name: "x", Route: "/x/", Upstream: "https://x"}}, Env: map[string]string{"NOT-A-NAME": "v"}}
	st := connectorStore(t, []Destination{bad}, map[string]Scope{"w": {Destinations: []string{"x"}}})
	if _, err := ConnectorFor(st, actorWith("w")); err == nil {
		t.Fatal("a malformed env key must fail the connector")
	}
	// A reserved key never reaches the connector (it would clobber the
	// identity token): dropped (TestConnectorForDropsAStoredReservedEnvKey).
	clobber := Destination{Name: "y", Endpoints: []Endpoint{{Name: "y", Route: "/y/", Upstream: "https://y"}}, Env: map[string]string{"JAM_IDENTITY_TOKEN": "pwned"}}
	st = connectorStore(t, []Destination{clobber}, map[string]Scope{"w": {Destinations: []string{"y"}}})
	if c, err := ConnectorFor(st, actorWith("w")); err != nil || len(c.Env) != 0 {
		t.Fatalf("connector = %+v, %v", c, err)
	}
}

// A git destination's rewrite target is its upstream host; github.com stays
// implicit (GitTarget "") so a pre-target client applies it unchanged.
func TestConnectorForGitTargetFromUpstream(t *testing.T) {
	gitlab := Destination{Name: "gitlab-git", Endpoints: []Endpoint{{Name: "gitlab-git", Route: "/gitlab-git/", Upstream: "https://gitlab.example.com", Git: true, IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword}}}
	st := connectorStore(t, []Destination{legacyGit, gitlab},
		map[string]Scope{"gh": {Destinations: []string{"git"}}, "gl": {Destinations: []string{"gitlab-git"}}})
	if c, err := ConnectorFor(st, actorWith("gl")); err != nil || c.GitRoute != "/gitlab-git/" || c.GitTarget != "https://gitlab.example.com/" {
		t.Fatalf("gitlab connector = %+v, %v", c, err)
	}
	if c, err := ConnectorFor(st, actorWith("gh")); err != nil || c.GitRoute != "/git/" || c.GitTarget != "" {
		t.Fatalf("github connector = %+v, %v", c, err)
	}
}

// withDestinationsDomain sets the destinations domain for one test.
func withDestinationsDomain(t *testing.T, domain string) {
	t.Helper()
	prev := DestinationsDomain()
	SetDestinationsDomain(domain)
	t.Cleanup(func() { SetDestinationsDomain(prev) })
}

// githubEndpoints is one service on several endpoints: REST on a subdomain
// route, GraphQL route-only on the Jam host, git on a subdomain of its own.
var githubEndpoints = Destination{Name: "github", Endpoints: []Endpoint{
	{Name: "rest", Subdomain: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: ApplyBearer, Apply: ApplyBearer},
	{Name: "graphql", Route: "/api/graphql/", Upstream: "https://api.github.com/graphql", IdentityIn: ApplyBearer, Apply: ApplyBearer},
	{Name: "git", Subdomain: "git", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword, Git: true},
}, Env: map[string]string{
	"GH_HOST": "{host:rest}", "GH_API": "{url:rest}", "GH_GQL": "{url:graphql}", "GQL_HOST": "{host:graphql}",
	"GIT_BASE": "{url:git}", "GH_ENTERPRISE_TOKEN": "{token}",
}}

func TestConnectorForEndpointPlaceholders(t *testing.T) {
	withDestinationsDomain(t, "jam.example.com")
	st := connectorStore(t, []Destination{githubEndpoints}, map[string]Scope{"w": {Destinations: []string{"github"}}})
	c, err := ConnectorFor(st, actorWith("w"))
	want := map[string]string{
		"GH_HOST": "gh.jam.example.com", "GH_API": "https://gh.jam.example.com/api/v3",
		// Route-only endpoints stay client-side placeholders, as {url} does.
		"GH_GQL": "{base}/api/graphql", "GQL_HOST": "{host}",
		"GIT_BASE": "https://git.jam.example.com", "GH_ENTERPRISE_TOKEN": "{token}",
	}
	if err != nil || !maps.Equal(c.Env, want) {
		t.Fatalf("connector = %+v, %v\nwant env %v", c, err, want)
	}
	if c.GitRoute != "/" || c.GitHost != "git.jam.example.com" || c.GitTarget != "" {
		t.Fatalf("git = %q on %q → %q, want / on git.jam.example.com", c.GitRoute, c.GitHost, c.GitTarget)
	}
}

// {url} reads the first endpoint, so a destination whose one endpoint is on a
// subdomain gets that subdomain's URL.
func TestConnectorForURLOnSubdomain(t *testing.T) {
	withDestinationsDomain(t, "jam.example.com")
	d := Destination{Name: "linear", Endpoints: []Endpoint{{Name: "linear", Subdomain: "linear", Upstream: "https://api.linear.app"}},
		Env: map[string]string{"LINEAR_URL": "{url}", "LINEAR_HOST": "{host:linear}"}}
	st := connectorStore(t, []Destination{d}, map[string]Scope{"w": {Destinations: []string{"linear"}}})
	c, err := ConnectorFor(st, actorWith("w"))
	if err != nil || c.Env["LINEAR_URL"] != "https://linear.jam.example.com" || c.Env["LINEAR_HOST"] != "linear.jam.example.com" {
		t.Fatalf("connector = %+v, %v", c, err)
	}
}

// An endpoint on a subdomain stored on a Jam without a destinations domain
// (e.g. imported from a backup) cannot be named: the connector fails closed.
func TestConnectorForSubdomainWithoutDomainFails(t *testing.T) {
	withDestinationsDomain(t, "")
	for _, d := range []Destination{
		{Name: "env", Endpoints: []Endpoint{{Name: "env", Subdomain: "gh", Upstream: "https://x"}}, Env: map[string]string{"H": "{host:env}"}},
		{Name: "git", Endpoints: []Endpoint{{Name: "git", Subdomain: "git", Upstream: "https://github.com", Git: true}}},
	} {
		st := connectorStore(t, []Destination{d}, map[string]Scope{"w": {Destinations: []string{d.Name}}})
		if c, err := ConnectorFor(st, actorWith("w")); err == nil {
			t.Errorf("%s: connector = %+v, want an error", d.Name, c)
		}
	}
}

func TestDestinationValidateEnvEndpointPlaceholders(t *testing.T) {
	if err := githubEndpoints.ValidateEnv(); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"{host:nope}", "{url:nope}", "{url:}", "{port:rest}"} {
		d := githubEndpoints
		d.Env = map[string]string{"X": v}
		if err := d.ValidateEnv(); err == nil {
			t.Errorf("%s: want error", v)
		}
	}
	// The admin/config write path refuses an unknown endpoint name too.
	d := githubEndpoints
	d.Env = map[string]string{"X": "{host:nope}"}
	if err := ValidateDestination(d, func(string) bool { return true }, true); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("ValidateDestination = %v, want the unknown endpoint named", err)
	}
}

// Conflicts stay fail-closed with endpoints: one variable set differently
// (an endpoint placeholder resolving to another host), or two git routes —
// for one upstream host — on different addresses.
func TestConnectorForEndpointConflicts(t *testing.T) {
	withDestinationsDomain(t, "jam.example.com")
	other := Destination{Name: "ghe", Endpoints: []Endpoint{{Name: "rest", Subdomain: "ghe", Upstream: "https://ghe.example.com"}},
		Env: map[string]string{"GH_HOST": "{host:rest}"}}
	same := Destination{Name: "same", Endpoints: []Endpoint{{Name: "rest", Subdomain: "gh", Route: "/x/", Upstream: "https://x"}},
		Env: map[string]string{"GH_HOST": "{host:rest}"}}
	git2 := Destination{Name: "git2", Endpoints: []Endpoint{{Name: "git2", Subdomain: "git2", Upstream: "https://github.com", Git: true}}}
	st := connectorStore(t, []Destination{githubEndpoints, other, same, git2, legacyGit}, map[string]Scope{
		"ok": {Destinations: []string{"github", "same"}}, "env": {Destinations: []string{"github", "ghe"}},
		"git": {Destinations: []string{"github", "git2"}}, "gitlegacy": {Destinations: []string{"github", "git"}},
	})
	if _, err := ConnectorFor(st, actorWith("ok")); err != nil {
		t.Fatalf("one host from two destinations must not conflict: %v", err)
	}
	for _, role := range []string{"env", "git", "gitlegacy"} {
		if c, err := ConnectorFor(st, actorWith(role)); err == nil {
			t.Errorf("%s: connector = %+v, want a conflict", role, c)
		}
	}
}

// Today's destinations — route-only, with or without env — deliver exactly
// the connector they did before endpoints could be named, byte for byte (the
// fingerprint a running cove reported stays ok), whatever the domain.
func TestConnectorForExistingDestinationsUnchanged(t *testing.T) {
	gitlab := Destination{Name: "gitlab-git", Endpoints: []Endpoint{{Name: "gitlab-git", Route: "/gitlab-git/", Upstream: "https://gitlab.example.com", Git: true}}}
	vertex := Destination{Name: "vertex", Endpoints: []Endpoint{{Name: "vertex", Route: "/vertex/", Upstream: "https://x"}},
		Env: map[string]string{"ANTHROPIC_VERTEX_BASE_URL": "{url}/v1", "CLAUDE_CODE_USE_VERTEX": "1"}}
	for _, tc := range []struct {
		scope []string
		json  string
	}{
		{[]string{"anthropic", "git"}, `{"env":{"ANTHROPIC_API_KEY":"{token}","ANTHROPIC_BASE_URL":"{base}/anthropic"},"git_route":"/git/"}`},
		{[]string{"github-api"}, `{"env":{"GH_ENTERPRISE_TOKEN":"{token}","GH_HOST":"{host}"}}`},
		{[]string{"gitlab-git"}, `{"git_route":"/gitlab-git/","git_target":"https://gitlab.example.com/"}`},
		{[]string{"vertex"}, `{"env":{"ANTHROPIC_VERTEX_BASE_URL":"{base}/vertex/v1","CLAUDE_CODE_USE_VERTEX":"1"}}`},
	} {
		for _, domain := range []string{"", "jam.example.com"} {
			withDestinationsDomain(t, domain)
			st := connectorStore(t, []Destination{legacyAnthropic, legacyGit, ghAPI, gitlab, vertex}, map[string]Scope{"w": {Destinations: tc.scope}})
			c, err := ConnectorFor(st, actorWith("w"))
			b, _ := json.Marshal(c)
			if err != nil || string(b) != tc.json {
				t.Errorf("%v (domain %q): connector = %s, %v\nwant %s", tc.scope, domain, b, err, tc.json)
			}
		}
	}
}
