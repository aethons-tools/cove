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
)

// Destination is one configured upstream the broker will proxy to. Anthropic and
// git are simply two Destinations; the engine has no service-specific branches.
type Destination struct {
	Name       string      `yaml:"name"`        // "anthropic", "git"
	Route      string      `yaml:"route"`       // inbound path prefix, e.g. "/anthropic/" or "/git/"
	Upstream   string      `yaml:"upstream"`    // "https://api.anthropic.com", "https://github.com"
	IdentityIn ApplyMethod `yaml:"identity_in"` // how the caller presents its identity token
	CredName   string      `yaml:"cred_name"`   // harbor credential to inject; "" = no credential
	Apply      ApplyMethod `yaml:"apply"`       // how to apply the real credential upstream
	RepoScoped bool        `yaml:"repo_scoped"` // path is <route><owner>/<repo>/...; checked against Identity.Repos
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
