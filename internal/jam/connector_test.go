package jam

import (
	"maps"
	"path/filepath"
	"testing"
)

func connectorStore(t *testing.T, dests []Destination, roles map[string]Scope) Store {
	t.Helper()
	st, err := NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dests {
		if err := st.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
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
	legacyAnthropic = Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyXAPIKey, Apply: ApplyXAPIKey}
	legacyGit       = Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword}
	ghAPI           = Destination{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: ApplyBearer, Apply: ApplyBearer,
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
	pool.IdentityIn = ApplyBearer
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
	same := Destination{Name: "x", Route: "/x/", Upstream: "https://x", Env: map[string]string{"GH_HOST": "{host}"}}
	diff := Destination{Name: "y", Route: "/y/", Upstream: "https://y", Env: map[string]string{"GH_HOST": "other"}}
	st := connectorStore(t, []Destination{ghAPI, same, diff, legacyGit, {Name: "git2", Route: "/git2/", Upstream: "https://github.com", Git: true}},
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
	for _, env := range []map[string]string{{"lower": "x"}, {"AT_JAM_X": "x"}, {"AT_HARBOR_IDENTITY_TOKEN": "x"}, {"X": "{nope}"}} {
		if err := (Destination{Env: env}).ValidateEnv(); err == nil {
			t.Errorf("%v: want error", env)
		}
	}
}
