package jam

import "testing"

func testConfig() Config {
	return Config{Destinations: []Destination{
		{Name: "anthropic", Endpoints: []Endpoint{{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: ApplyBearer, Apply: ApplyBearer}}, CredName: "anthropic-bearer"},
		{Name: "git", Endpoints: []Endpoint{{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword}}, CredName: "git-pat"},
	}}
}

func TestConfigMatch(t *testing.T) {
	c := testConfig()
	d, e, ok := c.Match("/anthropic/v1/messages")
	if !ok || d.Name != "anthropic" || e.Name != "anthropic" {
		t.Fatalf("anthropic match = %+v, %+v, %v", d, e, ok)
	}
	if _, _, ok := c.Match("/nope/x"); ok {
		t.Fatal("unexpected match for /nope/x")
	}
}

// The most specific endpoint wins across every destination's endpoints; an
// endpoint without a route, or on a subdomain, is never matched by path.
func TestConfigMatchMostSpecificEndpoint(t *testing.T) {
	c := Config{Destinations: []Destination{
		{Name: "github", Endpoints: []Endpoint{
			{Name: "graphql", Route: "/api/", Upstream: "https://api.github.com"},
			{Name: "rest", Route: "/api/v3/", Upstream: "https://api.github.com"},
			{Name: "host", Subdomain: "gh", Upstream: "https://api.github.com"},
		}},
		{Name: "uploads", Endpoints: []Endpoint{
			{Name: "uploads", Route: "/api/v3/uploads/", Upstream: "https://uploads.github.com"},
			{Name: "sub", Subdomain: "up", Route: "/x/", Upstream: "https://uploads.github.com"},
		}},
	}}
	for path, want := range map[string]string{
		"/api/graphql":            "github/graphql",
		"/api/v3/repos/x":         "github/rest",
		"/api/v3/uploads/assets/": "uploads/uploads",
	} {
		d, e, ok := c.Match(path)
		if got := d.Name + "/" + e.Name; !ok || got != want {
			t.Errorf("Match(%s) = %s, %v; want %s", path, got, ok, want)
		}
	}
	for _, path := range []string{"/", "/x/y", "/other"} {
		if d, e, ok := c.Match(path); ok {
			t.Errorf("Match(%s) = %s/%s; want none", path, d.Name, e.Name)
		}
	}
}
