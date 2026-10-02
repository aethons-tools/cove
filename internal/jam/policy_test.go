package jam

import "testing"

func testConfig() Config {
	return Config{Destinations: []Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyBearer, CredName: "anthropic-bearer", Apply: ApplyBearer},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword},
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
