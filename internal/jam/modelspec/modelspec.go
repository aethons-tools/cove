// Package modelspec holds the model-spec wire types — the named, harness-typed
// description of how a cove runs its agent — and the harness CLI version
// constraint grammar. It is a leaf (no Jam imports) so the connector Jam
// delivers (internal/jam/snippet) and the cove-side harness (internal/agentrun)
// share one definition; internal/jam aliases these types and owns validation
// and storage.
package modelspec

// HarnessType is a model-spec's harness family; it implies the harness a cove
// runs and selects the per-type body. Only claude exists today.
type HarnessType string

// HarnessClaude is the Claude Code harness family.
const HarnessClaude HarnessType = "claude"

// DefaultName is the model-spec a role with no binding resolves to.
const DefaultName = "claude-default"

// Spec is a named, harness-typed description of how a cove runs its agent:
// the harness family and CLI version, the principal it authenticates as (a
// credential BY NAME — never a value), the model, the permission policy, and a
// per-type body. It is a common envelope plus exactly one body keyed by Type
// (a union, like kit.ModelProvider). It never carries a secret value, so it
// may be delivered to a cove as is.
type Spec struct {
	Name      string      `json:"name"                yaml:"name"`
	Type      HarnessType `json:"type"                yaml:"type"`
	Version   string      `json:"version"             yaml:"version"` // harness CLI version constraint (ParseConstraint), e.g. "2.x"
	Principal Principal   `json:"principal"           yaml:"principal"`
	Model     Choice      `json:"model,omitzero"      yaml:"model,omitempty"`
	Policy    Policy      `json:"policy,omitzero"     yaml:"policy,omitempty"`
	Note      string      `json:"note,omitempty"      yaml:"note,omitempty"`
	Claude    *Claude     `json:"claude,omitempty"    yaml:"claude,omitempty"`
}

// Principal names who the agent authenticates as.
type Principal struct {
	// Credential is a serve-config credential name, or the pool keyword.
	Credential string `json:"credential" yaml:"credential"`
}

// Choice optionally pins the model and its effort; empty = harness default.
type Choice struct {
	ID     string `json:"id,omitempty"     yaml:"id,omitempty"`
	Effort string `json:"effort,omitempty" yaml:"effort,omitempty"`
}

// Policy is the harness permission policy.
type Policy struct {
	Mode  string   `json:"mode,omitempty"  yaml:"mode,omitempty"` // a Claude permission mode; empty = harness default
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"  yaml:"deny,omitempty"`
}

// Claude is the claude per-type body.
type Claude struct {
	Provider string `json:"provider" yaml:"provider"` // anthropic | vertex | bedrock
	// ProviderEnv is non-secret provider env (e.g. Vertex project/region). It
	// may not set protected (kit.ProtectedEnvKey), reserved, or credential env
	// (CredentialEnvKey).
	ProviderEnv map[string]string `json:"provider-env,omitempty" yaml:"provider-env,omitempty"`
	// Settings is a Claude settings.json fragment — preferences only.
	Settings map[string]any `json:"settings,omitempty" yaml:"settings,omitempty"`
	Plugins  []string       `json:"plugins,omitempty"  yaml:"plugins,omitempty"`
}

// credentialEnvKeys carry credential values; a model-spec names credentials by
// principal only, so provider-env may never set these.
var credentialEnvKeys = map[string]bool{
	"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
	"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
	"AWS_BEARER_TOKEN_BEDROCK": true,
}

// CredentialEnvKey reports whether key carries a credential value and so may
// never be set by a model-spec's provider-env.
func CredentialEnvKey(key string) bool { return credentialEnvKeys[key] }

// DefaultVersion is the default spec's CLI version constraint: permissive —
// any Claude Code 2.0.0 or later, which today's images install.
const DefaultVersion = ">=2.0.0"

// Default is DefaultName as Jam seeds it, authenticating as principal: claude
// on the anthropic provider, bypassPermissions, DefaultVersion, no model,
// effort, settings or plugins — so a cove under it launches exactly as it did
// before model-specs (only the version check is new).
func Default(principal string) Spec {
	return Spec{
		Name:      DefaultName,
		Type:      HarnessClaude,
		Version:   DefaultVersion,
		Principal: Principal{Credential: principal},
		Policy:    Policy{Mode: "bypassPermissions"},
		Note:      "Seeded by Jam: the built-in Claude Code defaults every unbound role runs under.",
		Claude:    &Claude{Provider: "anthropic"},
	}
}
