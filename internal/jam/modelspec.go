package jam

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/kit"
)

// HarnessType is a model-spec's harness family; it implies the harness a cove
// runs and selects the per-type body. Only claude exists today.
type HarnessType string

// HarnessClaude is the Claude Code harness family.
const HarnessClaude HarnessType = "claude"

// PoolPrincipal is the principal.credential keyword meaning "an account from the
// subscription pool" — valid only when the serve-config enables a pool.
const PoolPrincipal = "pool"

// MaxModelSpecNote bounds ModelSpec.Note (an operator hint, like a destination's).
const MaxModelSpecNote = 300

// ModelSpec is a named, harness-typed description of how a cove runs its agent:
// the harness family and CLI version, the principal it authenticates as (a
// credential BY NAME — never a value), the model, the permission policy, and a
// per-type body. It is a common envelope plus exactly one body keyed by Type
// (a union, like kit.ModelProvider). Stored only; nothing binds it to a role or
// delivers it to a cove yet.
type ModelSpec struct {
	Name      string         `json:"name"                yaml:"name"`
	Type      HarnessType    `json:"type"                yaml:"type"`
	Version   string         `json:"version"             yaml:"version"` // required harness CLI version constraint, e.g. "2.x"
	Principal ModelPrincipal `json:"principal"           yaml:"principal"`
	Model     ModelChoice    `json:"model,omitzero"      yaml:"model,omitempty"`
	Policy    ModelPolicy    `json:"policy,omitzero"     yaml:"policy,omitempty"`
	Note      string         `json:"note,omitempty"      yaml:"note,omitempty"`
	Claude    *ClaudeSpec    `json:"claude,omitempty"    yaml:"claude,omitempty"`
}

// ModelPrincipal names who the agent authenticates as.
type ModelPrincipal struct {
	// Credential is a serve-config credential name, or PoolPrincipal.
	Credential string `json:"credential" yaml:"credential"`
}

// ModelChoice optionally pins the model and its effort; empty = harness default.
type ModelChoice struct {
	ID     string `json:"id,omitempty"     yaml:"id,omitempty"`
	Effort string `json:"effort,omitempty" yaml:"effort,omitempty"`
}

// ModelPolicy is the harness permission policy.
type ModelPolicy struct {
	Mode  string   `json:"mode,omitempty"  yaml:"mode,omitempty"` // a Claude permission mode; empty = harness default
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"  yaml:"deny,omitempty"`
}

// ClaudeSpec is the claude per-type body.
type ClaudeSpec struct {
	Provider string `json:"provider" yaml:"provider"` // anthropic | vertex | bedrock
	// ProviderEnv is non-secret provider env (e.g. Vertex project/region). It
	// may not set protected (kit.ProtectedEnvKey), reserved, or credential env.
	ProviderEnv map[string]string `json:"provider-env,omitempty" yaml:"provider-env,omitempty"`
	// Settings is a Claude settings.json fragment — preferences only.
	Settings map[string]any `json:"settings,omitempty" yaml:"settings,omitempty"`
	Plugins  []string       `json:"plugins,omitempty"  yaml:"plugins,omitempty"`
}

// claudePermissionModes are Claude Code's permission modes.
var claudePermissionModes = []string{"default", "acceptEdits", "plan", "bypassPermissions", "dontAsk"}

// claudeProviders are the model providers a claude spec can target.
var claudeProviders = []string{"anthropic", "vertex", "bedrock"}

// credentialEnvKeys carry credential values; a model-spec names credentials by
// principal only, so provider-env may never set these.
var credentialEnvKeys = map[string]bool{
	"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
	"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
	"AWS_BEARER_TOKEN_BEDROCK": true,
}

// claudeNonPreferenceSettings are settings.json keys that are not preferences:
// env (would bypass the provider-env checks), permissions (owned by policy) and
// the credential helpers (which produce secrets).
var claudeNonPreferenceSettings = []string{"env", "permissions", "apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper"}

// ValidateModelSpec checks a model-spec at write time. credExists resolves a
// serve-config credential name; poolConfigured says whether the PoolPrincipal
// keyword is available. Refusals are 400 WriteErrors and never echo an env value.
func ValidateModelSpec(m ModelSpec, credExists func(string) bool, poolConfigured bool) error {
	bad := func(format string, a ...any) error { return writeErr(http.StatusBadRequest, format, a...) }
	if m.Name == "" {
		return bad("name is required")
	}
	switch m.Type {
	case "":
		return bad("type is required (want %s)", HarnessClaude)
	case HarnessClaude:
	default:
		return bad("type %q is not a known harness family (want %s)", m.Type, HarnessClaude)
	}
	if strings.TrimSpace(m.Version) == "" {
		return bad("version is required (the harness CLI version constraint, e.g. \"2.x\")")
	}
	switch c := m.Principal.Credential; {
	case c == "":
		return bad("principal.credential is required (a credential name, or %q)", PoolPrincipal)
	case c == PoolPrincipal:
		if !poolConfigured {
			return bad("principal.credential is %q but no subscription pool is configured", PoolPrincipal)
		}
	case !credExists(c):
		return bad("principal.credential %q does not resolve to a configured credential", c)
	}
	if mode := m.Policy.Mode; mode != "" && !slices.Contains(claudePermissionModes, mode) {
		return bad("policy.mode %q is not a Claude permission mode (want one of %s)", mode, strings.Join(claudePermissionModes, ", "))
	}
	for field, rules := range map[string][]string{"allow": m.Policy.Allow, "deny": m.Policy.Deny} {
		for _, r := range rules {
			if strings.TrimSpace(r) == "" {
				return bad("policy.%s has an empty rule", field)
			}
		}
	}
	if len(m.Note) > MaxModelSpecNote {
		return bad("note is %d bytes; at most %d", len(m.Note), MaxModelSpecNote)
	}
	// The per-type body: exactly the one matching Type. claude is the only
	// family, so the union check is "claude is set".
	if m.Claude == nil {
		return bad("type %s requires a claude: body", m.Type)
	}
	return validateClaudeSpec(*m.Claude, bad)
}

func validateClaudeSpec(c ClaudeSpec, bad func(string, ...any) error) error {
	if c.Provider == "" {
		return bad("claude.provider is required (want one of %s)", strings.Join(claudeProviders, ", "))
	}
	if !slices.Contains(claudeProviders, c.Provider) {
		return bad("claude.provider %q is not supported (want one of %s)", c.Provider, strings.Join(claudeProviders, ", "))
	}
	for k := range c.ProviderEnv {
		switch {
		case !envKeyRe.MatchString(k):
			return bad("claude.provider-env key %q is not an env-var name", k)
		case strings.HasPrefix(k, "AT_JAM_") || strings.HasPrefix(k, "AT_HARBOR_"):
			return bad("claude.provider-env key %q is reserved", k)
		case kit.ProtectedEnvKey(k):
			return bad("claude.provider-env: %q is a sealed-owned/security-relevant variable and cannot be set", k)
		case credentialEnvKeys[k]:
			return bad("claude.provider-env: %q carries a credential; name credentials via principal.credential, never by value", k)
		}
	}
	for _, k := range claudeNonPreferenceSettings {
		if _, ok := c.Settings[k]; ok {
			return bad("claude.settings: %q is not a preference and cannot be set in a model-spec", k)
		}
	}
	if _, err := json.Marshal(c.Settings); err != nil {
		return bad("claude.settings is not a JSON object: %v", err)
	}
	for i, p := range c.Plugins {
		if strings.TrimSpace(p) == "" {
			return bad("claude.plugins has an empty plugin entry")
		}
		if slices.Contains(c.Plugins[:i], p) {
			return bad("claude.plugins lists %q twice", p)
		}
	}
	return nil
}

// cloneModelSpec deep-copies m (Settings is arbitrary JSON) so a store never
// shares maps/slices with its callers. Validated specs always round-trip.
func cloneModelSpec(m ModelSpec) (ModelSpec, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return ModelSpec{}, fmt.Errorf("model-spec %q: %w", m.Name, err)
	}
	var out ModelSpec
	if err := json.Unmarshal(b, &out); err != nil {
		return ModelSpec{}, fmt.Errorf("model-spec %q: %w", m.Name, err)
	}
	return out, nil
}

// modelSpecMu serializes model-spec create/update so the existence check and
// the write are one step (as destMu does for destinations).
var modelSpecMu sync.Mutex

// CreateModelSpec validates and stores a new model-spec; an existing name is a
// 409 WriteError.
func CreateModelSpec(store Store, m ModelSpec, credExists func(string) bool, poolConfigured bool) error {
	if err := ValidateModelSpec(m, credExists, poolConfigured); err != nil {
		return err
	}
	modelSpecMu.Lock()
	defer modelSpecMu.Unlock()
	if _, ok := store.GetModelSpec(m.Name); ok {
		return writeErr(http.StatusConflict, "model-spec %q already exists", m.Name)
	}
	return store.PutModelSpec(m)
}

// UpdateModelSpec validates and replaces an existing model-spec (every field;
// the name is the key); a missing one is a 404 WriteError.
func UpdateModelSpec(store Store, m ModelSpec, credExists func(string) bool, poolConfigured bool) error {
	if err := ValidateModelSpec(m, credExists, poolConfigured); err != nil {
		return err
	}
	modelSpecMu.Lock()
	defer modelSpecMu.Unlock()
	if _, ok := store.GetModelSpec(m.Name); !ok {
		return writeErr(http.StatusNotFound, "model-spec %q does not exist", m.Name)
	}
	return store.PutModelSpec(m)
}
