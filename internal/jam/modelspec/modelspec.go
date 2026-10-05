// Package modelspec holds the model-spec wire types — the named, harness-typed
// description of how a cove runs its agent — and the harness CLI version
// constraint grammar. It is a leaf (no Jam imports) so the connector Jam
// delivers (internal/jam/snippet) and the cove-side harness (internal/agentrun)
// share one definition; internal/jam aliases these types and owns validation
// and storage.
package modelspec

import (
	"fmt"
	"slices"
	"strings"
)

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
	Name string      `json:"name"                yaml:"name"`
	Type HarnessType `json:"type"                yaml:"type"`
	// Version is the exact harness CLI release X.Y.Z (ParseExactVersion) the
	// cove's image installs — the build pins it (COV-242).
	Version string `json:"version" yaml:"version"`
	// VersionConstraint is the runtime check the harness's Validate applies to
	// the installed CLI (ParseConstraint grammar). Empty means "== Version"
	// (RuntimeConstraint).
	VersionConstraint string    `json:"version-constraint,omitempty" yaml:"version-constraint,omitempty"`
	Principal         Principal `json:"principal"           yaml:"principal"`
	Model             Choice    `json:"model,omitzero"      yaml:"model,omitempty"`
	Policy            Policy    `json:"policy,omitzero"     yaml:"policy,omitempty"`
	Note              string    `json:"note,omitempty"      yaml:"note,omitempty"`
	Claude            *Claude   `json:"claude,omitempty"    yaml:"claude,omitempty"`
}

// RuntimeConstraint is the version constraint the harness checks the installed
// CLI against: VersionConstraint when set, else Version itself (an exact
// X.Y.Z is a valid constraint matching only that release). A legacy spec whose
// Version still holds a constraint (stored before the split) checks against it.
func (s Spec) RuntimeConstraint() string {
	if strings.TrimSpace(s.VersionConstraint) != "" {
		return s.VersionConstraint
	}
	return s.Version
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
	// Mode is a Claude permission mode (PermissionModes). Empty means
	// ModeBypassPermissions — the legacy --dangerously-skip-permissions launch —
	// NOT Claude's own "default" mode.
	Mode  string   `json:"mode,omitempty"  yaml:"mode,omitempty"`
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"  yaml:"deny,omitempty"`
}

// ModeBypassPermissions is the permission mode claude-default runs under, and
// what an empty Policy.Mode means; the claude harness renders it as
// --dangerously-skip-permissions.
const ModeBypassPermissions = "bypassPermissions"

// ModePlan is Claude's plan mode, refused for a cove: a headless stream-json
// agent has nobody to approve its plan, so it could never leave plan mode.
const ModePlan = "plan"

// permissionModes are the Claude permission modes a model-spec may name.
var permissionModes = []string{"default", "acceptEdits", ModeBypassPermissions, "dontAsk"}

// PermissionModes lists the accepted Policy.Mode values (a copy) — the one
// list Jam validation, the admin UI and the claude harness all check against.
func PermissionModes() []string { return slices.Clone(permissionModes) }

// CheckPermissionMode reports why mode is not an accepted Policy.Mode (nil when
// it is, or when it is empty).
func CheckPermissionMode(mode string) error {
	switch {
	case mode == "" || slices.Contains(permissionModes, mode):
		return nil
	case mode == ModePlan:
		return fmt.Errorf("policy.mode %q cannot be used: a cove runs claude headless (-p, stream-json) with nobody to approve a plan, so the agent could never leave plan mode (want one of %s)", mode, strings.Join(permissionModes, ", "))
	}
	return fmt.Errorf("policy.mode %q is not a supported Claude permission mode (want one of %s)", mode, strings.Join(permissionModes, ", "))
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
	// Plugins are installed into the image at BUILD time by the harness layer
	// (internal/harnessinstall), as "name@marketplace" ids whose marketplace
	// is a KnownClaudeMarketplaces key (CheckClaudePlugin).
	Plugins []string `json:"plugins,omitempty"  yaml:"plugins,omitempty"`
}

// knownClaudeMarketplaces maps the plugin marketplaces a model-spec may name
// (the "@marketplace" half of a plugin id) to the source `claude plugin
// marketplace add` takes. Adding a marketplace is a code change: the source is
// fetched at image build with open network, so it is not operator input.
var knownClaudeMarketplaces = map[string]string{
	"claude-plugins-official": "anthropics/claude-plugins-official",
}

// ClaudeMarketplaceSource returns the `claude plugin marketplace add` source
// for a known marketplace name.
func ClaudeMarketplaceSource(name string) (string, bool) {
	src, ok := knownClaudeMarketplaces[name]
	return src, ok
}

// KnownClaudeMarketplaces lists the marketplace names a plugin id may name, sorted.
func KnownClaudeMarketplaces() []string {
	out := make([]string, 0, len(knownClaudeMarketplaces))
	for k := range knownClaudeMarketplaces {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// pluginPart is one half of a plugin id: it reaches a Dockerfile RUN line, so
// it is restricted to a shell-inert alphabet.
func pluginPart(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return s[0] != '-' && s[0] != '.'
}

// CheckClaudePlugin reports why id is not an installable plugin id: it must be
// "name@marketplace", both halves [A-Za-z0-9._-] (not leading '-' or '.'), and
// the marketplace one of KnownClaudeMarketplaces.
func CheckClaudePlugin(id string) error {
	name, mkt, ok := strings.Cut(id, "@")
	if !ok || !pluginPart(name) || !pluginPart(mkt) {
		return fmt.Errorf("plugin %q is not a plugin id (want name@marketplace, letters, digits, '.', '_' or '-')", id)
	}
	if _, ok := knownClaudeMarketplaces[mkt]; !ok {
		return fmt.Errorf("plugin %q names marketplace %q, which is not a known marketplace (want one of %s)", id, mkt, strings.Join(KnownClaudeMarketplaces(), ", "))
	}
	return nil
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

// DefaultClaudeVersion is THE pinned Claude Code release: claude-default's
// version, and the harness every full config.yml kit (plain at-cove Assemble)
// installs. Bump it here, and only here, to move every default image.
//
// renovate: datasource=npm depName=@anthropic-ai/claude-code
const DefaultClaudeVersion = "2.1.287"

// LegacyDefaultVersion is the version constraint claude-default was seeded with
// before the version split (COV-242); MigrateVersion recognizes it.
const LegacyDefaultVersion = ">=2.0.0"

// defaultClaudePlugins are the plugins claude-default installs — what every
// image carried before plugins became model-spec-driven (COV-242).
var defaultClaudePlugins = []string{"superpowers@claude-plugins-official"}

// DefaultClaudePlugins returns claude-default's plugin ids (a copy).
func DefaultClaudePlugins() []string { return slices.Clone(defaultClaudePlugins) }

// Default is DefaultName as Jam seeds it, authenticating as principal: claude
// on the anthropic provider, bypassPermissions, version DefaultClaudeVersion
// (runtime constraint == version), the DefaultClaudePlugins, no model, effort
// or settings — so a cove under it launches exactly as it did before
// model-specs.
func Default(principal string) Spec {
	return Spec{
		Name:      DefaultName,
		Type:      HarnessClaude,
		Version:   DefaultClaudeVersion,
		Principal: Principal{Credential: principal},
		Policy:    Policy{Mode: ModeBypassPermissions},
		Note:      "Seeded by Jam: the built-in Claude Code defaults every unbound role runs under.",
		Claude:    &Claude{Provider: "anthropic", Plugins: DefaultClaudePlugins()},
	}
}

// MigrateVersion upgrades a spec stored before the version split (COV-242),
// whose Version held a constraint rather than an exact X.Y.Z. It reports
// whether s changed. A legacy spec:
//
//   - keeps its old constraint as VersionConstraint (unless one is set) and is
//     pinned to DefaultClaudeVersion — except the untouched claude-default seed
//     (LegacyDefaultVersion), which becomes exactly a fresh seed's pin with no
//     separate constraint;
//   - with no claude.plugins gets DefaultClaudePlugins: before COV-242 every
//     image carried them regardless of the spec, so its coves keep them.
//
// A spec whose Version is already exact is returned unchanged.
func MigrateVersion(s Spec) (Spec, bool) {
	if _, err := ParseExactVersion(s.Version); err == nil {
		return s, false
	}
	old := strings.TrimSpace(s.Version)
	s.Version = DefaultClaudeVersion
	switch {
	case s.Name == DefaultName && old == LegacyDefaultVersion && s.VersionConstraint == "":
		// The untouched seed: same pin as a fresh one, constraint == version.
	case strings.TrimSpace(s.VersionConstraint) == "":
		s.VersionConstraint = old
	}
	if s.Type == HarnessClaude && s.Claude != nil && len(s.Claude.Plugins) == 0 {
		c := *s.Claude
		c.Plugins = DefaultClaudePlugins()
		s.Claude = &c
	}
	return s, true
}
