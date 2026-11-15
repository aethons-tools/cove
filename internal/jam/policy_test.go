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
	d, e, ok := c.Match("", "/anthropic/v1/messages")
	if !ok || d.Name != "anthropic" || e.Name != "anthropic" {
		t.Fatalf("anthropic match = %+v, %+v, %v", d, e, ok)
	}
	if _, _, ok := c.Match("", "/nope/x"); ok {
		t.Fatal("unexpected match for /nope/x")
	}
}

// The most specific endpoint wins: on the Jam host (sub ""), the longest
// route among endpoints without a subdomain, across every destination; on a
// subdomain, the longest route among its endpoints, else its endpoint
// without a route. Neither reaches the other's endpoints.
func TestConfigMatchMostSpecificEndpoint(t *testing.T) {
	c := Config{Destinations: []Destination{
		{Name: "github", Endpoints: []Endpoint{
			{Name: "graphql", Route: "/api/", Upstream: "https://api.github.com"},
			{Name: "rest", Route: "/api/v3/", Upstream: "https://api.github.com"},
			{Name: "host", Subdomain: "gh", Upstream: "https://api.github.com"},
			{Name: "ghrest", Subdomain: "gh", Route: "/api/v3/", Upstream: "https://api.github.com"},
		}},
		{Name: "uploads", Endpoints: []Endpoint{
			{Name: "uploads", Route: "/api/v3/uploads/", Upstream: "https://uploads.github.com"},
			{Name: "sub", Subdomain: "up", Route: "/x/", Upstream: "https://uploads.github.com"},
		}},
	}}
	for _, tc := range []struct{ sub, path, want string }{
		{"", "/api/graphql", "github/graphql"},
		{"", "/api/v3/repos/x", "github/rest"},
		{"", "/api/v3/uploads/assets/", "uploads/uploads"},
		{"gh", "/api/v3/repos/x", "github/ghrest"},
		{"gh", "/api/graphql", "github/host"},
		{"gh", "/", "github/host"},
		{"up", "/x/y", "uploads/sub"},
	} {
		d, e, ok := c.Match(tc.sub, tc.path)
		if got := d.Name + "/" + e.Name; !ok || got != tc.want {
			t.Errorf("Match(%q, %s) = %s, %v; want %s", tc.sub, tc.path, got, ok, tc.want)
		}
	}
	for _, tc := range []struct{ sub, path string }{
		{"", "/"}, {"", "/x/y"}, {"", "/other"},
		{"up", "/api/v3/uploads/a"}, {"up", "/other"}, {"nope", "/api/v3/x"}, {"nope", "/"},
	} {
		if d, e, ok := c.Match(tc.sub, tc.path); ok {
			t.Errorf("Match(%q, %s) = %s/%s; want none", tc.sub, tc.path, d.Name, e.Name)
		}
	}
}

func TestSubdomainOf(t *testing.T) {
	for _, tc := range []struct {
		host, domain, sub string
		under             bool
	}{
		{"gh.d.example", "d.example", "gh", true},
		{"GH.D.Example.:443", "d.example", "gh", true},
		{"d.example", "d.example", "", false},
		{"jam.example", "d.example", "", false},
		{"xd.example", "d.example", "", false},
		{"a.gh.d.example", "d.example", "", true},
		{"x--y.d.example", "d.example", "", true},
		{"-x.d.example", "d.example", "", true},
		{"gh.d.example", "", "", false},
	} {
		if sub, under := SubdomainOf(tc.host, tc.domain); sub != tc.sub || under != tc.under {
			t.Errorf("SubdomainOf(%q, %q) = %q, %v; want %q, %v", tc.host, tc.domain, sub, under, tc.sub, tc.under)
		}
	}
}
