package harbor

import "testing"

func testConfig() Config {
	return Config{Destinations: []Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyBearer, CredName: "anthropic-bearer", Apply: ApplyBearer},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword, RepoScoped: true},
	}}
}

func TestConfigMatch(t *testing.T) {
	c := testConfig()
	d, ok := c.Match("/anthropic/v1/messages")
	if !ok || d.Name != "anthropic" {
		t.Fatalf("anthropic match = %+v, %v", d, ok)
	}
	if _, ok := c.Match("/nope/x"); ok {
		t.Fatal("unexpected match for /nope/x")
	}
}

func TestRepoFromPath(t *testing.T) {
	got, ok := RepoFromPath("/git/", "/git/acme/api.git/info/refs")
	if !ok || got != "acme/api" {
		t.Fatalf("RepoFromPath = %q, %v", got, ok)
	}
	if _, ok := RepoFromPath("/git/", "/git/acme"); ok {
		t.Fatal("expected failure for missing repo segment")
	}
}
