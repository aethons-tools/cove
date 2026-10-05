// Package modelspec holds the model-spec wire types — the named, harness-typed
// description of how a cove runs its agent — its one validator (Validate), and
// the harness CLI version constraint grammar. It is a leaf (no Jam or kit
// imports) so the connector Jam delivers (internal/jam/snippet), the cove-side
// harness (internal/agentrun) and a plain at-cove kit's model-spec: block
// (internal/kit) share one definition; internal/jam aliases these types and
// owns storage and the Jam meaning of a principal.
package modelspec

import (
	"fmt"
	"maps"
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
// (a union). It never carries a secret value, so it may be delivered to a cove
// as is. Jam stores it, and a plain at-cove kit may author one in its
// model-spec: block — one schema, one validator (Validate).
type Spec struct {
	Name string      `json:"name"                yaml:"name"`
	Type HarnessType `json:"type"                yaml:"type"`
	// Version is the exact harness CLI release X.Y.Z (ParseExactVersion) the
	// cove's image installs — the build pins it (COV-242).
	// A plain at-cove kit may omit it (kit.Config.EffectiveModelSpec defaults
	// it to DefaultClaudeVersion); Jam requires it.
	Version string `json:"version" yaml:"version,omitempty"`
	// VersionConstraint is the runtime check the harness's Validate applies to
	// the installed CLI (ParseConstraint grammar). Empty means "== Version"
	// (RuntimeConstraint).
	VersionConstraint string    `json:"version-constraint,omitempty" yaml:"version-constraint,omitempty"`
	Principal         Principal `json:"principal"           yaml:"principal,omitempty"`
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
	// Headers are extra header rules the broker applies, after the credential,
	// to this principal's requests to the destination serving the spec's
	// provider route. Values are not secrets.
	Headers []HeaderRule `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// HeaderRule is one principal header rule: exactly one of Set (replace the
// header's value) or EnsureListItem (idempotently append an item to the
// header's comma-separated list) is non-empty.
type HeaderRule struct {
	Name           string `json:"name"                       yaml:"name"`
	Set            string `json:"set,omitempty"              yaml:"set,omitempty"`
	EnsureListItem string `json:"ensure-list-item,omitempty" yaml:"ensure-list-item,omitempty"`
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
	// may not set protected (ProtectedEnvKey), reserved, or credential env
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

// DefaultClaudeVersion is THE pinned Claude Code release the code defaults
// to: the version Jam seeds a NEW claude-default with, the harness a full
// config.yml kit without a model-spec: block installs, and the install a raise
// uses when no spec is delivered (harnessinstall.Default). Bumping it does NOT
// change a claude-default already stored in a Jam — an operator moves that one
// with `at-jam model-spec update` (or the admin UI).
//
// renovate: datasource=npm depName=@anthropic-ai/claude-code
const DefaultClaudeVersion = "2.1.287"

// LegacyDefaultVersion is the version constraint claude-default was seeded with
// before the version split (COV-242); MigrateLegacy keeps it as a migrated
// claude-default's version-constraint (it admits the pin).
const LegacyDefaultVersion = ">=2.0.0"

// defaultClaudePlugins are the plugins claude-default installs — what every
// image carried before plugins became model-spec-driven (COV-242).
var defaultClaudePlugins = []string{"superpowers@claude-plugins-official"}

// DefaultClaudePlugins returns claude-default's plugin ids (a copy).
func DefaultClaudePlugins() []string { return slices.Clone(defaultClaudePlugins) }

// defaultClaudeSettings are claude-default's claude.settings: the Claude Code
// PREFERENCES the sealed managed settings used to force on every image
// (COV-245), with the same values, so a cove bound to claude-default behaves
// as before. Classification (docs/usage/jam/model-spec-harness.md owns it):
//
//   - preferences (here): display and notification choices, plus
//     disableAgentView — it only hides Claude Code's agent-view UI, and grants
//     or withholds nothing;
//   - sandbox policy (the harness layer's managed settings, never here):
//     update control, remote control, the bypass-mode acceptance, the
//     permissions default for interactive sessions, and disableAutoMode — it
//     governs which permission modes exist, which is permission policy (a Jam
//     cove's mode comes from policy.mode, which never offers auto).
var defaultClaudeSettings = map[string]any{
	"agentPushNotifEnabled":   true,
	"alwaysThinkingEnabled":   true,
	"disableAgentView":        true,
	"inputNeededNotifEnabled": true,
	"prefersReducedMotion":    true,
	"showThinkingSummaries":   true,
	"showTurnDuration":        true,
	"spinnerTipsEnabled":      false,
	"theme":                   "dark",
}

// DefaultClaudeSettings returns claude-default's claude.settings (a copy).
func DefaultClaudeSettings() map[string]any { return maps.Clone(defaultClaudeSettings) }

// Default is DefaultName as Jam seeds it, authenticating as principal: claude
// on the anthropic provider, bypassPermissions, version DefaultClaudeVersion
// (runtime constraint == version), the DefaultClaudePlugins and the
// DefaultClaudeSettings preferences, no model or effort — so a cove under it
// launches exactly as it did before model-specs.
func Default(principal string) Spec {
	return Spec{
		Name:      DefaultName,
		Type:      HarnessClaude,
		Version:   DefaultClaudeVersion,
		Principal: Principal{Credential: principal},
		Policy:    Policy{Mode: ModeBypassPermissions},
		Note:      "Seeded by Jam: the built-in Claude Code defaults every unbound role runs under.",
		Claude:    &Claude{Provider: "anthropic", Plugins: DefaultClaudePlugins(), Settings: DefaultClaudeSettings()},
	}
}

// MigrateLegacy upgrades a spec stored before the version split (COV-242) —
// Jam's one-time model-spec store migration applies it to every stored spec,
// whatever its version — and returns warnings (each naming the spec) for
// anything it had to drop. The result always passes the version and plugin
// checks of Jam's write validation:
//
//   - a non-exact Version (a legacy constraint) becomes DefaultClaudeVersion.
//     The old range is kept as VersionConstraint when it admits the pin, so
//     coves on images the old hardening built (latest claude) keep passing
//     their runtime check; otherwise the constraint becomes the pin and a
//     warning names the dropped range. A set VersionConstraint that does not
//     admit Version is treated the same way;
//   - plugin ids that are malformed or name an unknown marketplace are dropped
//     with a warning (a legacy spec must never make every raise fail);
//   - a claude spec left with no plugins gets DefaultClaudePlugins: before
//     COV-242 every image carried them regardless of the spec;
//   - claude.settings' enabledPlugins / extraKnownMarketplaces are dropped
//     with a warning (claude.plugins owns enablement now).
func MigrateLegacy(s Spec) (Spec, []string) {
	var warns []string
	if _, err := ParseExactVersion(s.Version); err != nil {
		old := strings.TrimSpace(s.Version)
		s.Version = DefaultClaudeVersion
		if strings.TrimSpace(s.VersionConstraint) == "" {
			s.VersionConstraint = old
		}
	}
	if s.VersionConstraint != "" {
		v, _ := ParseExactVersion(s.Version)
		if c, err := ParseConstraint(s.VersionConstraint); err != nil || !c.Allows(v) {
			warns = append(warns, fmt.Sprintf("model-spec %q: version constraint %q does not admit the pinned version %s; dropped (the runtime check is now exactly %s)", s.Name, s.VersionConstraint, s.Version, s.Version))
			s.VersionConstraint = s.Version
		}
	}
	if s.Type == HarnessClaude && s.Claude != nil {
		c := *s.Claude
		c.Plugins = nil
		for _, p := range s.Claude.Plugins {
			if err := CheckClaudePlugin(p); err != nil {
				warns = append(warns, fmt.Sprintf("model-spec %q: dropped plugin %q: %v", s.Name, p, err))
				continue
			}
			if !slices.Contains(c.Plugins, p) {
				c.Plugins = append(c.Plugins, p)
			}
		}
		if len(c.Plugins) == 0 {
			c.Plugins = DefaultClaudePlugins()
		}
		for _, k := range []string{"enabledPlugins", "extraKnownMarketplaces"} {
			if _, ok := c.Settings[k]; ok {
				warns = append(warns, fmt.Sprintf("model-spec %q: dropped claude.settings %q (plugin enablement follows claude.plugins)", s.Name, k))
				c.Settings = maps.Clone(c.Settings)
				delete(c.Settings, k)
			}
		}
		s.Claude = &c
	}
	return s, warns
}

// managedPolicySettings are the Claude settings the harness layer's managed
// settings own as sandbox-wide policy (COV-245; they outrank --settings, so a
// spec could never change them): update control, remote control, the
// bypass-mode acceptance, and disableAutoMode — which permission modes exist
// is permission policy (policy.mode owns a cove's mode). Jam refuses them in
// claude.settings; MigrateSettings drops them from stored specs.
var managedPolicySettings = []string{
	"autoUpdates", "disableRemoteControl", "remoteControlAtStartup",
	"skipDangerousModePermissionPrompt", "bypassPermissionsModeAccepted", "disableAutoMode",
}

// ManagedPolicySettings returns the managed sandbox-policy setting keys (a copy).
func ManagedPolicySettings() []string { return slices.Clone(managedPolicySettings) }

// MigrateSettings is schema step 2 (COV-245) of Jam's one-time model-spec
// store migration, for a claude spec with a body (any other is returned as is):
//
//   - every spec's claude.settings loses the ManagedPolicySettings keys —
//     accepted before, refused now — with a warning naming the spec and key,
//     so a stored spec or an old backup stays updatable and importable;
//   - a DefaultName spec gains each DefaultClaudeSettings key its
//     claude.settings lacks (the preferences moved out of the sealed managed
//     settings); a value it already holds is never overwritten.
//
// s is never mutated.
func MigrateSettings(s Spec) (Spec, []string) {
	if s.Type != HarnessClaude || s.Claude == nil {
		return s, nil
	}
	var warns []string
	settings := maps.Clone(s.Claude.Settings)
	for _, k := range managedPolicySettings {
		if _, ok := settings[k]; ok {
			warns = append(warns, fmt.Sprintf("model-spec %q: dropped claude.settings %q (sandbox policy, set by the image's managed settings)", s.Name, k))
			delete(settings, k)
		}
	}
	if s.Name == DefaultName {
		if settings == nil {
			settings = map[string]any{}
		}
		for k, v := range defaultClaudeSettings {
			if _, ok := settings[k]; !ok {
				settings[k] = v
			}
		}
	}
	c := *s.Claude
	c.Settings = settings
	s.Claude = &c
	return s, warns
}
