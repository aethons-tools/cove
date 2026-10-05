package snippet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// Connector is what a client sets up to reach the destinations in its scope:
// env-var templates plus, optionally, git routing. Templates may use {base}
// (the broker base URL, e.g. https://jam.example), {host} (the Jam host) and
// {token} (the identity token). Jam resolves each destination's {url} to
// {base}<route> before handing a Connector out, so the token never sits in a
// template — the client substitutes it in memory (Expand) or references
// $AT_JAM_IDENTITY_TOKEN in a rendered shell snippet (Render).
//
// For a cove, ModelSpec is its role's resolved model-spec (names only — never
// a secret value): cove-master re-reads it with every connector refresh, so an
// edit takes effect at the cove's next episode. nil = none (a Jam predating
// model-specs, or the default spec not seeded): the harness's built-in defaults.
type Connector struct {
	Env       map[string]string `json:"env,omitempty"`
	GitRoute  string            `json:"git_route,omitempty"` // e.g. "/git/"; "" = no git routing
	ModelSpec *modelspec.Spec   `json:"model_spec,omitempty"`
}

// ErrNoConnectorEndpoint is Fetch's error for a Jam that predates GET /connector;
// callers fall back to Legacy.
var ErrNoConnectorEndpoint = errors.New("jam has no /connector endpoint")

// Legacy is the built-in contract a Jam without destination env implies:
// Anthropic on x-api-key (or, for a subscription-pool cove, a static bearer on
// ANTHROPIC_AUTH_TOKEN) plus git routed through /git/.
func Legacy(subscription bool) Connector {
	key := "ANTHROPIC_API_KEY"
	if subscription {
		key = "ANTHROPIC_AUTH_TOKEN"
	}
	return Connector{
		Env:      map[string]string{"ANTHROPIC_BASE_URL": "{base}" + anthropicPath, key: "{token}"},
		GitRoute: gitPath,
	}
}

// Expand returns the env a client sets in memory: each template resolved, plus
// the identity token under tokenVar and its deprecated name.
func (c Connector) Expand(baseURL, token string) map[string]string {
	baseURL = strings.TrimRight(baseURL, "/")
	r := strings.NewReplacer("{base}", baseURL, "{host}", hostOf(baseURL), "{token}", token)
	out := map[string]string{tokenVar: token, legacyTokenVar: token}
	for k, v := range c.Env {
		out[k] = r.Replace(v)
	}
	return out
}

// Render returns a sourceable shell snippet: the token exported once, every
// other value double-quoted with {token} as a ${AT_JAM_IDENTITY_TOKEN} reference,
// then the git config when the connector routes git.
func (c Connector) Render(baseURL, token string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "export %s=%s\n", tokenVar, token)
	fmt.Fprintf(&b, "export %s=\"$%s\"\n", legacyTokenVar, tokenVar)
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	lit := strings.NewReplacer("{base}", baseURL, "{host}", hostOf(baseURL))
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		parts := strings.Split(c.Env[k], "{token}")
		for i, p := range parts {
			parts[i] = esc.Replace(lit.Replace(p))
		}
		// Braced, so name characters after {token} aren't absorbed into the variable.
		fmt.Fprintf(&b, "export %s=\"%s\"\n", k, strings.Join(parts, "${"+tokenVar+"}"))
	}
	b.WriteString(c.GitConfig(baseURL))
	return b.String()
}

// GitConfig returns the token-free `git config --global` commands routing
// github.com through the connector's git route, or "" when it routes no git.
// The keys come from the builders execGit (cove-master's per-turn refresh) sets
// via argv, so the two cannot drift; here the subsection is shell-quoted.
func (c Connector) GitConfig(baseURL string) string {
	if c.GitRoute == "" {
		return ""
	}
	baseURL = strings.TrimRight(baseURL, "/")
	quote := func(sub string) string { return fmt.Sprintf("%q", sub) }
	var b strings.Builder
	fmt.Fprintf(&b, "git config --global %s %s\n", gitRewriteKey(quote(baseURL+c.GitRoute)), GitRewriteTarget)
	fmt.Fprintf(&b, "git config --global %s %s\n", gitHelperKey(quote(baseURL)), gitHelper)
	return b.String()
}

// GitRewriteTarget is the URL prefix a git route rewrites (the insteadOf value).
const GitRewriteTarget = "https://github.com/"

// GitRewriteKey is the git config key whose insteadOf rewrites GitRewriteTarget
// to baseURL+route; GitHelperKey is the key holding GitHelper for baseURL. Both
// unquoted, for callers that set them via argv.
func GitRewriteKey(baseURL, route string) string {
	return gitRewriteKey(strings.TrimRight(baseURL, "/") + route)
}

// GitHelperKey: see GitRewriteKey.
func GitHelperKey(baseURL string) string { return gitHelperKey(strings.TrimRight(baseURL, "/")) }

func gitRewriteKey(sub string) string { return "url." + sub + ".insteadOf" }
func gitHelperKey(sub string) string  { return "credential." + sub + ".helper" }

// Fetch asks the Jam at baseURL for the identity's connector (GET /connector,
// identity as a bearer). A 404 — a Jam that predates the endpoint — returns
// ErrNoConnectorEndpoint.
func Fetch(hc *http.Client, baseURL, token string) (Connector, error) {
	return FetchContext(context.Background(), hc, baseURL, token)
}

// FetchContext is Fetch bounded by ctx.
func FetchContext(ctx context.Context, hc *http.Client, baseURL, token string) (Connector, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/connector", nil)
	if err != nil {
		return Connector{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hc.Do(req)
	if err != nil {
		return Connector{}, fmt.Errorf("fetch jam connector: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Connector{}, ErrNoConnectorEndpoint
	case resp.StatusCode != http.StatusOK:
		return Connector{}, fmt.Errorf("fetch jam connector: %s", resp.Status)
	}
	var c Connector
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return Connector{}, fmt.Errorf("fetch jam connector: %w", err)
	}
	return c, nil
}

// Fingerprint identifies a connector's content (templates, git route and the
// delivered model-spec, never a token — a Connector holds none): sha256 of its
// JSON, whose map keys encoding/json sorts, so it is order-independent; nil and
// empty Env coincide (omitempty). A model-spec edit therefore shows the cove's
// connector as stale until its next episode applies it. cove-master reports it after applying a connector and Jam compares
// it with the role's current one, so both sides must use this function.
func Fingerprint(c Connector) string {
	b, _ := json.Marshal(c) // a validated spec's settings always marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// GitHelper is the git credential-helper value GitConfig installs, unquoted —
// for callers that set it via argv (cove-master's per-turn git refresh).
func GitHelper() string { return gitHelperValue }

func hostOf(baseURL string) string {
	if _, rest, ok := strings.Cut(baseURL, "://"); ok {
		baseURL = rest
	}
	host, _, _ := strings.Cut(baseURL, "/")
	return host
}
