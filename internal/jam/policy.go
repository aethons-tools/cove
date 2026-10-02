package jam

import "strings"

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
	// OAuthBeta, when set, makes the broker ensure the `oauth-2025-04-20` beta is
	// present in the forwarded `anthropic-beta` header. Used by the subscription
	// pool: a cove on ANTHROPIC_AUTH_TOKEN sends a bearer but NOT that beta, and
	// Anthropic requires it to accept a subscription-OAuth token.
	OAuthBeta bool `json:"oauth_beta,omitempty" yaml:"oauth_beta,omitempty"`
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
