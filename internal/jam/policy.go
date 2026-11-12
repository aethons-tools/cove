package jam

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// Destination is one configured upstream service the broker will proxy to,
// reached at one or more Endpoints. Anthropic and git are simply two
// Destinations; the engine has no service-specific branches. The credential,
// env, note and operator notes belong to the destination: every endpoint
// shares them. A destination written before endpoints existed (the flat form,
// destwire.go) is one endpoint named after the destination.
type Destination struct {
	Name     string `json:"name"      yaml:"name"`
	CredName string `json:"cred_name" yaml:"cred_name"`
	// Endpoints are where the destination is reached, at most MaxEndpoints;
	// the broker serves a request on the most specific one (Config.Match).
	Endpoints []Endpoint `json:"endpoints,omitempty" yaml:"endpoints,omitempty"`
	// LegacyOAuthBeta is the removed oauth_beta flag (COV-241), kept LOAD-ONLY
	// so a destination stored or backed up before the removal still decodes.
	// The broker never reads it (it ensures the beta for every pool
	// credential); the legacy-flag scan MigrateModelSpecs runs at every startup
	// (and MigrateSnapshotModelSpecs on import) clears it, adding OAuthBetaRule
	// to the specs naming a flagged non-pool credential. ValidateDestination
	// refuses a write that sets it.
	LegacyOAuthBeta bool `json:"oauth_beta,omitempty" yaml:"oauth_beta,omitempty"`
	// Env is the client env a studio sets to use this destination: values are
	// templates over {host:<endpoint>} and {url:<endpoint>} (an endpoint's
	// host, and its host and route), {url} (the first endpoint's), {base},
	// {host} and {token} — see ClientEnv and snippet.Connector. nil = the
	// legacy default (ClientEnv).
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// Note is an optional human-written usage hint shown to sessions granted
	// this destination (the Studio layer of their session context), e.g. how a
	// tool must be pointed at the route. Not a secret; ≤ MaxDestinationNote bytes.
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
	// OperatorNotes is free markdown for operators only: never shown to
	// sessions (unlike Note) and of no effect on the broker.
	OperatorNotes string `json:"operator_notes,omitempty" yaml:"operator-notes,omitempty"`
}

// Endpoint is one address of a destination: where Jam answers for it
// (Subdomain and/or Route), the upstream it forwards to, and how identity
// and credential travel. Name is unique in the destination.
type Endpoint struct {
	Name string `json:"name" yaml:"name"`
	// Subdomain is the endpoint's host, one label under the destinations
	// domain (serve config destinations.domain); validation refuses it on a
	// Jam without one.
	Subdomain  string      `json:"subdomain,omitempty" yaml:"subdomain,omitempty"`
	Route      string      `json:"route,omitempty"       yaml:"route,omitempty"`
	Upstream   string      `json:"upstream"              yaml:"upstream"`
	IdentityIn ApplyMethod `json:"identity_in,omitempty" yaml:"identity_in,omitempty"`
	Apply      ApplyMethod `json:"apply,omitempty"       yaml:"apply,omitempty"`
	// IdentityInSpec / ApplySpec are the header specs used when IdentityIn /
	// Apply is "custom" (and only then); presets expand in headerspec.go.
	IdentityInSpec *InboundSpec  `json:"identity_in_spec,omitempty" yaml:"identity_in_spec,omitempty"`
	ApplySpec      *OutboundSpec `json:"apply_spec,omitempty"       yaml:"apply_spec,omitempty"`
	// Git routes a studio's https://github.com/ through this endpoint's route.
	Git bool `json:"git,omitempty" yaml:"git,omitempty"`
	// AllowPaths, when set, is the only request paths the broker forwards on
	// this endpoint: path.Match patterns over the path after the route
	// prefix (so "*" never crosses a "/"). Anything else is refused 403 before
	// the credential is resolved. Empty = any path (the default). It bounds what
	// a broad upstream credential (e.g. a GCP cloud-platform token) can be used
	// for; see ValidateAllowPaths.
	AllowPaths []string `json:"allow_paths,omitempty" yaml:"allow_paths,omitempty"`
	// LogHeadersIn / LogHeadersOut turn on troubleshooting header logs for
	// this endpoint (headerlog.go); "" = none. Secrets are always redacted.
	LogHeadersIn  HeaderLog `json:"log_headers_in,omitempty"  yaml:"log_headers_in,omitempty"`
	LogHeadersOut HeaderLog `json:"log_headers_out,omitempty" yaml:"log_headers_out,omitempty"`
}

// MaxEndpoints bounds Destination.Endpoints.
const MaxEndpoints = 10

// Primary is the destination's first endpoint (the only one of a destination
// in the flat form), or the zero Endpoint when it has none. The client env's
// {url} and its legacy default read it.
func (d Destination) Primary() Endpoint {
	if len(d.Endpoints) == 0 {
		return Endpoint{}
	}
	return d.Endpoints[0]
}

// Endpoint returns the endpoint named name.
func (d Destination) Endpoint(name string) (Endpoint, bool) {
	for _, e := range d.Endpoints {
		if e.Name == name {
			return e, true
		}
	}
	return Endpoint{}, false
}

// normalize names a lone unnamed endpoint after the destination, as the flat
// form (and an endpoints: list of one without a name) means.
func (d *Destination) normalize() {
	if len(d.Endpoints) == 1 && d.Endpoints[0].Name == "" {
		d.Endpoints[0].Name = d.Name
	}
}

// ClientEnv is the env a studio sets for this destination, its endpoint
// placeholders resolved (clientEnv) on this Jam's destinations domain. A
// destination without Env keeps the pre-env contract: the /anthropic/ route
// sets ANTHROPIC_BASE_URL plus the identity on ANTHROPIC_API_KEY (identity-in
// x-api-key) or ANTHROPIC_AUTH_TOKEN (bearer — the subscription pool's
// configuration). Other presets and custom specs imply no token env. A
// placeholder clientEnv cannot resolve is left as written.
func (d Destination) ClientEnv() map[string]string {
	out, _ := d.clientEnv(DestinationsDomain())
	return out
}

// endpointPlaceholder matches {host:<endpoint>} and {url:<endpoint>} in a
// client-env template.
var endpointPlaceholder = regexp.MustCompile(`\{(host|url):([^}]*)\}`)

// clientEnv is ClientEnv on the destinations domain domain ("" = none):
// {url} is {url:<first endpoint>}; {host:<e>} is e's host and {url:<e>} is
// https://<that host><e's route> without a trailing "/". For an endpoint on
// the Jam host they stay client-side placeholders ({host}, {base}<route>), as
// {url} always resolved; for one on a subdomain the host is <sub>.<domain>.
// An unknown endpoint, or a subdomain without a domain, is an error (that
// placeholder is left as written).
func (d Destination) clientEnv(domain string) (map[string]string, error) {
	env, p := d.Env, d.Primary()
	if env == nil && p.Route == "/anthropic/" {
		env = map[string]string{"ANTHROPIC_BASE_URL": "{url}"}
		switch p.IdentityIn {
		case ApplyXAPIKey:
			env["ANTHROPIC_API_KEY"] = "{token}"
		case ApplyBearer:
			env["ANTHROPIC_AUTH_TOKEN"] = "{token}"
		}
	}
	var err error
	resolve := func(kind string, e Endpoint, as string) string {
		host, herr := e.hostOn(domain)
		switch {
		case herr != nil:
			err = herr
			return as
		case host == "" && kind == "host":
			return "{host}"
		case host == "":
			return "{base}" + strings.TrimSuffix(e.Route, "/")
		case kind == "host":
			return host
		}
		return "https://" + host + strings.TrimSuffix(e.Route, "/")
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		v = endpointPlaceholder.ReplaceAllStringFunc(v, func(ph string) string {
			m := endpointPlaceholder.FindStringSubmatch(ph)
			e, ok := d.Endpoint(m[2])
			if !ok {
				err = fmt.Errorf("env %s: %s names no endpoint of destination %q", k, ph, d.Name)
				return ph
			}
			return resolve(m[1], e, ph)
		})
		if strings.Contains(v, "{url}") {
			v = strings.ReplaceAll(v, "{url}", resolve("url", p, "{url}"))
		}
		out[k] = v
	}
	return out, err
}

// hostOn is e's host on the destinations domain domain: "" for the Jam host
// (no subdomain), else <sub>.<domain> — an error when domain is "".
func (e Endpoint) hostOn(domain string) (string, error) {
	switch {
	case e.Subdomain == "":
		return "", nil
	case domain == "":
		return "", fmt.Errorf("endpoint %q is on subdomain %q, but this Jam has no destinations domain", e.Name, e.Subdomain)
	}
	return e.Subdomain + "." + domain, nil
}

// RoutesGit reports whether studios route git for e's upstream host
// (snippet.GitTargetFor; github.com for the legacy route) through e, an
// endpoint of d: set explicitly, or implied by the legacy /git/ route on a
// destination without Env.
func (d Destination) RoutesGit(e Endpoint) bool {
	return e.Git || (d.Env == nil && e.Route == "/git/")
}

// GitEndpoints are d's endpoints that route git (RoutesGit).
func (d Destination) GitEndpoints() []Endpoint {
	var out []Endpoint
	for _, e := range d.Endpoints {
		if d.RoutesGit(e) {
			out = append(out, e)
		}
	}
	return out
}

// GitRouted reports whether any endpoint of d routes git.
func (d Destination) GitRouted() bool { return len(d.GitEndpoints()) > 0 }

// envPlaceholder matches a {placeholder} in a client-env template. Env key
// names follow modelspec.ValidEnvKey / ReservedEnvKey (one grammar).
var envPlaceholder = regexp.MustCompile(`\{[^}]*\}`)

// ValidateEnv checks Env at write time: keys are env-var names outside the
// reserved JAM_ namespace, and templates use only the known placeholders,
// {host:<endpoint>} and {url:<endpoint>} naming an endpoint of d.
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
				m := endpointPlaceholder.FindStringSubmatch(ph)
				if m == nil {
					return fmt.Errorf("env %s: unknown placeholder %s (want {host:<endpoint>}, {url:<endpoint>}, {url}, {base}, {host} or {token})", k, ph)
				}
				if _, ok := d.Endpoint(m[2]); !ok {
					return fmt.Errorf("env %s: %s names no endpoint of destination %q", k, ph, d.Name)
				}
			}
		}
	}
	return nil
}

// MaxAllowPaths bounds Endpoint.AllowPaths.
const MaxAllowPaths = 32

// ValidateAllowPaths checks AllowPaths at write time: at most MaxAllowPaths
// patterns, each a clean absolute path and a valid path.Match pattern.
func (e Endpoint) ValidateAllowPaths() error {
	if len(e.AllowPaths) > MaxAllowPaths {
		return fmt.Errorf("allow_paths has %d patterns; at most %d", len(e.AllowPaths), MaxAllowPaths)
	}
	for _, p := range e.AllowPaths {
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
// after the route prefix — on e: always when AllowPaths is empty, else only a
// clean path matching one of the patterns (a path with "..", "//" or a
// trailing "/" segment never matches, so a pattern can't be escaped).
func (e Endpoint) PathAllowed(p string) bool {
	if len(e.AllowPaths) == 0 {
		return true
	}
	if path.Clean(p) != p {
		return false
	}
	for _, pat := range e.AllowPaths {
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

// Match returns the endpoint that serves reqPath on the host sub names (""
// for the Jam host, else a subdomain of the destinations domain), and its
// destination. On the Jam host, the endpoint without a subdomain whose Route
// is the longest prefix of reqPath wins, across every destination. On a
// subdomain, only its endpoints match: the one whose Route is the longest
// prefix, else the one without a route, which takes every path.
func (c Config) Match(sub, reqPath string) (Destination, Endpoint, bool) {
	best := -1
	var bestDest Destination
	var bestEp Endpoint
	for _, d := range c.Destinations {
		for _, e := range d.Endpoints {
			if e.Subdomain != sub {
				continue
			}
			score := -1
			switch {
			case e.Route == "":
				if sub != "" {
					score = 0
				}
			case strings.HasPrefix(reqPath, e.Route):
				score = len(e.Route)
			}
			if score > best {
				best = score
				bestDest, bestEp = d, e
			}
		}
	}
	return bestDest, bestEp, best >= 0
}
