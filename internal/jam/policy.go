package jam

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
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
	// IdentityInSpec / ApplySpec are the header specs used when IdentityIn /
	// Apply is "custom" (and only then); presets expand in headerspec.go.
	IdentityInSpec *InboundSpec  `json:"identity_in_spec,omitempty" yaml:"identity_in_spec,omitempty"`
	ApplySpec      *OutboundSpec `json:"apply_spec,omitempty"       yaml:"apply_spec,omitempty"`
	// LegacyOAuthBeta is the removed oauth_beta flag (COV-241), kept LOAD-ONLY
	// so a destination stored or backed up before the removal still decodes.
	// The broker never reads it (it ensures the beta for every pool
	// credential); the legacy-flag scan MigrateModelSpecs runs at every startup
	// (and MigrateSnapshotModelSpecs on import) clears it, adding OAuthBetaRule
	// to the specs naming a flagged non-pool credential. ValidateDestination
	// refuses a write that sets it.
	LegacyOAuthBeta bool `json:"oauth_beta,omitempty" yaml:"oauth_beta,omitempty"`
	// Env is the client env a studio sets to use this destination: values are
	// templates over {url} (broker base + this route), {base}, {host} and
	// {token} — see snippet.Connector. nil = the legacy default (ClientEnv).
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// Git routes a studio's https://github.com/ through this destination's route.
	Git bool `json:"git,omitempty" yaml:"git,omitempty"`
	// AllowPaths, when set, is the only request paths the broker forwards on
	// this destination: path.Match patterns over the path after the route
	// prefix (so "*" never crosses a "/"). Anything else is refused 403 before
	// the credential is resolved. Empty = any path (the default). It bounds what
	// a broad upstream credential (e.g. a GCP cloud-platform token) can be used
	// for; see ValidateAllowPaths.
	AllowPaths []string `json:"allow_paths,omitempty" yaml:"allow_paths,omitempty"`
	// Note is an optional human-written usage hint shown to sessions granted
	// this destination (the Studio layer of their session context), e.g. how a
	// tool must be pointed at the route. Not a secret; ≤ MaxDestinationNote bytes.
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
}

// ClientEnv is the env a studio sets for this destination, with {url} resolved
// to {base}<route>. A destination without Env keeps the pre-env contract: the
// /anthropic/ route sets ANTHROPIC_BASE_URL plus the identity on
// ANTHROPIC_API_KEY (identity-in x-api-key) or ANTHROPIC_AUTH_TOKEN (bearer —
// the subscription pool's configuration). Other presets and custom specs imply
// no token env.
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

// envPlaceholder matches a {placeholder} in a client-env template. Env key
// names follow modelspec.ValidEnvKey / ReservedEnvKey (one grammar).
var envPlaceholder = regexp.MustCompile(`\{[^}]*\}`)

// ValidateEnv checks Env at write time: keys are env-var names outside the
// reserved AT_JAM_/AT_HARBOR_ namespace, and templates use only the known
// placeholders.
func (d Destination) ValidateEnv() error {
	for k, v := range d.Env {
		if !modelspec.ValidEnvKey(k) {
			return fmt.Errorf("env key %q is not an env-var name", k)
		}
		if modelspec.ReservedEnvKey(k) {
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

// MaxAllowPaths bounds Destination.AllowPaths.
const MaxAllowPaths = 32

// ValidateAllowPaths checks AllowPaths at write time: at most MaxAllowPaths
// patterns, each a clean absolute path and a valid path.Match pattern.
func (d Destination) ValidateAllowPaths() error {
	if len(d.AllowPaths) > MaxAllowPaths {
		return fmt.Errorf("allow_paths has %d patterns; at most %d", len(d.AllowPaths), MaxAllowPaths)
	}
	for _, p := range d.AllowPaths {
		if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
			return fmt.Errorf("allow_paths %q must be a clean absolute path (the path after the route, e.g. /v1/...)", p)
		}
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("allow_paths %q is not a valid pattern: %v", p, err)
		}
	}
	return nil
}

// PathAllowed reports whether the broker may forward p — the request path
// after the route prefix — on d: always when AllowPaths is empty, else only a
// clean path matching one of the patterns (a path with "..", "//" or a
// trailing "/" segment never matches, so a pattern can't be escaped).
func (d Destination) PathAllowed(p string) bool {
	if len(d.AllowPaths) == 0 {
		return true
	}
	if path.Clean(p) != p {
		return false
	}
	for _, pat := range d.AllowPaths {
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
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
