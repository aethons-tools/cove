package harbor

import (
	"path"
	"strings"
)

// ApplyMethod is how a credential (the inbound identity token, or the outbound
// real credential) is carried on an HTTP request.
type ApplyMethod string

const (
	ApplyBearer        ApplyMethod = "bearer"         // Authorization: Bearer <value>
	ApplyBasicPassword ApplyMethod = "basic-password" // HTTP basic auth, <value> as the password
	ApplyXAPIKey       ApplyMethod = "x-api-key"      // X-Api-Key: <value> (Anthropic API keys)
)

// Destination is one configured upstream the broker will proxy to. Anthropic and
// git are simply two Destinations; the engine has no service-specific branches.
type Destination struct {
	Name       string      `json:"name"        yaml:"name"`
	Route      string      `json:"route"       yaml:"route"`
	Upstream   string      `json:"upstream"    yaml:"upstream"`
	IdentityIn ApplyMethod `json:"identity_in" yaml:"identity_in"`
	CredName   string      `json:"cred_name"   yaml:"cred_name"`
	Apply      ApplyMethod `json:"apply"       yaml:"apply"`
	RepoScoped bool        `json:"repo_scoped" yaml:"repo_scoped"`
}

// Config is the broker's destination table.
type Config struct {
	Destinations []Destination `yaml:"destinations"`
}

// Match returns the destination whose Route prefixes reqPath (longest wins).
func (c Config) Match(reqPath string) (Destination, bool) {
	best := -1
	var bestDest Destination
	for _, d := range c.Destinations {
		if strings.HasPrefix(reqPath, d.Route) && len(d.Route) > best {
			best = len(d.Route)
			bestDest = d
		}
	}
	return bestDest, best >= 0
}

// RepoFromPath extracts "owner/repo" from a repo-scoped path after the route:
// route "/git/", path "/git/acme/api.git/info/refs" → "acme/api".
func RepoFromPath(route, reqPath string) (string, bool) {
	rest := strings.TrimPrefix(reqPath, route)
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), true
}

// repoAllowed reports whether ownerRepo matches any glob in allowed.
func repoAllowed(ownerRepo string, allowed []string) bool {
	for _, g := range allowed {
		if ok, _ := path.Match(g, ownerRepo); ok {
			return true
		}
	}
	return false
}
