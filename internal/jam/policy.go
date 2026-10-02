package jam

import (
	"fmt"
	"regexp"
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
	// OAuthBeta, when set, makes the broker ensure the `oauth-2025-04-20` beta is
	// present in the forwarded `anthropic-beta` header. Used by the subscription
	// pool: a cove on ANTHROPIC_AUTH_TOKEN sends a bearer but NOT that beta, and
	// Anthropic requires it to accept a subscription-OAuth token.
	OAuthBeta bool `json:"oauth_beta,omitempty" yaml:"oauth_beta,omitempty"`
	// Env is the client env a studio sets to use this destination: values are
	// templates over {url} (broker base + this route), {base}, {host} and
	// {token} — see snippet.Connector. nil = the legacy default (ClientEnv).
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// Git routes a studio's https://github.com/ through this destination's route.
	Git bool `json:"git,omitempty" yaml:"git,omitempty"`
	// Note is an optional human-written usage hint shown to sessions granted
	// this destination (the Studio layer of their session context), e.g. how a
	// tool must be pointed at the route. Not a secret; ≤ MaxDestinationNote bytes.
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
}

// ClientEnv is the env a studio sets for this destination, with {url} resolved
// to {base}<route>. A destination without Env keeps the pre-env contract: the
// /anthropic/ route sets ANTHROPIC_BASE_URL plus the identity on
// ANTHROPIC_API_KEY (identity-in x-api-key) or ANTHROPIC_AUTH_TOKEN (bearer —
// the subscription pool's configuration).
func (d Destination) ClientEnv() map[string]string {
	env := d.Env
	if env == nil && d.Route == "/anthropic/" {
		env = map[string]string{"ANTHROPIC_BASE_URL": "{url}"}
		switch d.IdentityIn {
		case ApplyXAPIKey:
			env["ANTHROPIC_API_KEY"] = "{token}"
		case ApplyBearer:
			env["ANTHROPIC_AUTH_TOKEN"] = "{token}"
		}
	}
	url := "{base}" + strings.TrimSuffix(d.Route, "/")
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = strings.ReplaceAll(v, "{url}", url)
	}
	return out
}

// GitRouted reports whether studios route github.com through this destination:
// set explicitly, or implied by the legacy /git/ route on a destination without Env.
func (d Destination) GitRouted() bool {
	return d.Git || (d.Env == nil && d.Route == "/git/")
}

var (
	envKeyRe       = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	envPlaceholder = regexp.MustCompile(`\{[^}]*\}`)
)

// ValidateEnv checks Env at write time: keys are env-var names outside the
// reserved AT_JAM_/AT_HARBOR_ namespace, and templates use only the known
// placeholders.
func (d Destination) ValidateEnv() error {
	for k, v := range d.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("env key %q is not an env-var name", k)
		}
		if strings.HasPrefix(k, "AT_JAM_") || strings.HasPrefix(k, "AT_HARBOR_") {
			return fmt.Errorf("env key %q is reserved", k)
		}
		for _, ph := range envPlaceholder.FindAllString(v, -1) {
			switch ph {
			case "{url}", "{base}", "{host}", "{token}":
			default:
				return fmt.Errorf("env %s: unknown placeholder %s (want {url}, {base}, {host} or {token})", k, ph)
			}
		}
	}
	return nil
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
