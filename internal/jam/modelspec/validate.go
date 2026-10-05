package modelspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// MaxNote bounds Spec.Note (an operator hint).
const MaxNote = 300

// claudeProviders are the model providers a claude spec can target.
var claudeProviders = []string{"anthropic", "vertex", "bedrock"}

// ClaudeProviders lists the accepted claude.provider values (a copy).
func ClaudeProviders() []string { return slices.Clone(claudeProviders) }

// envKeyRe is the env-var name grammar provider-env keys must match.
var envKeyRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// protectedEnvKeys are sealed-owned or security-relevant variables that
// operator-authored, non-secret env (a model-spec's provider-env, in a Jam or
// in a kit's model-spec: block) must never set. The per-session env is
// *sourced* in the session shell, so an unchecked value would shadow the
// sealed /etc/environment (e.g. the proxy vars) and could defeat egress.
// Rejected at validation and dropped defensively at injection ("additive,
// sealed-wins" for env).
var protectedEnvKeys = map[string]bool{
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"CLAUDE_CONFIG_DIR":              true,
	"GOOGLE_APPLICATION_CREDENTIALS": true,
	"PATH":                           true,
}

// ProtectedEnvKey reports whether key is a sealed-owned or security-relevant
// variable a model-spec's provider-env must never set. One list, shared by Jam
// and the kit loader (kit.ProtectedEnvKey delegates here).
func ProtectedEnvKey(key string) bool { return protectedEnvKeys[key] }

// reservedEnvKey reports whether key is in the Jam-owned env namespace.
func reservedEnvKey(key string) bool {
	return strings.HasPrefix(key, "AT_JAM_") || strings.HasPrefix(key, "AT_HARBOR_")
}

// claudeNonPreferenceSettings are settings.json keys that are not preferences:
// env (would bypass the provider-env checks), permissions (owned by policy),
// the harness layer's managed sandbox policy keys, the credential helpers
// (which produce secrets), hooks and statusLine (run commands, and hooks can
// override the permission policy) and MCP server selection (owned by the kit
// and the harness's generated --mcp-config).
var claudeNonPreferenceSettings = append([]string{
	"env", "permissions",
	"apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper",
	"hooks", "disableAllHooks", "statusLine",
	"enableAllProjectMcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "allowedMcpServers", "deniedMcpServers",
	// Plugin enablement is claude.plugins' (installed at build, enabled per
	// run): enabling an uninstalled plugin would make claude auto-install it
	// through the egress proxy at runtime.
	"enabledPlugins", "extraKnownMarketplaces",
	// + ManagedPolicySettings: sandbox policy, set image-wide by the harness
	// layer's managed settings (COV-245).
}, managedPolicySettings...)

// Validate is THE model-spec validator — one schema, one validator, for both
// loaders: Jam's write path (internal/jam.ValidateModelSpec) and a kit's
// model-spec: block (internal/kit). The two differ only in what a principal
// means, so checkPrincipal decides that part: Jam resolves the credential
// name (or the pool keyword) and checks the header rules; a plain at-cove kit
// has no broker, so it refuses any principal. checkPrincipal's error is
// returned as is; every other refusal is a plain error that never echoes a
// provider-env value.
func Validate(m Spec, checkPrincipal func(Principal) error) error {
	if m.Name == "" {
		return errors.New("name is required")
	}
	switch m.Type {
	case "":
		return fmt.Errorf("type is required (want %s)", HarnessClaude)
	case HarnessClaude:
	default:
		return fmt.Errorf("type %q is not a known harness family (want %s)", m.Type, HarnessClaude)
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("version is required (the exact harness CLI release the image installs, e.g. %q)", DefaultClaudeVersion)
	}
	v, err := ParseExactVersion(m.Version)
	if err != nil {
		return fmt.Errorf("%s (a range belongs in version-constraint)", err.Error())
	}
	if m.VersionConstraint != "" {
		c, err := ParseConstraint(m.VersionConstraint)
		if err != nil {
			return fmt.Errorf("version-constraint: %s", err.Error())
		}
		if !c.Allows(v) {
			return fmt.Errorf("version-constraint %q does not admit version %s (every cove would fail its version check)", m.VersionConstraint, m.Version)
		}
	}
	if checkPrincipal != nil {
		if err := checkPrincipal(m.Principal); err != nil {
			return err
		}
	}
	if err := CheckPermissionMode(m.Policy.Mode); err != nil {
		return err
	}
	for _, field := range []string{"allow", "deny"} {
		rules := m.Policy.Allow
		if field == "deny" {
			rules = m.Policy.Deny
		}
		for _, r := range rules {
			switch {
			case strings.TrimSpace(r) == "":
				return fmt.Errorf("policy.%s has an empty rule", field)
			case strings.TrimSpace(r) != r:
				return fmt.Errorf("policy.%s rule %q has leading or trailing whitespace", field, r)
			}
		}
	}
	if len(m.Note) > MaxNote {
		return fmt.Errorf("note is %d bytes; at most %d", len(m.Note), MaxNote)
	}
	// The per-type body: exactly the one matching Type. claude is the only
	// family, so the union check is "claude is set".
	if m.Claude == nil {
		return fmt.Errorf("type %s requires a claude: body", m.Type)
	}
	return validateClaude(*m.Claude)
}

func validateClaude(c Claude) error {
	if c.Provider == "" {
		return fmt.Errorf("claude.provider is required (want one of %s)", strings.Join(claudeProviders, ", "))
	}
	if !slices.Contains(claudeProviders, c.Provider) {
		return fmt.Errorf("claude.provider %q is not supported (want one of %s)", c.Provider, strings.Join(claudeProviders, ", "))
	}
	for _, k := range slices.Sorted(maps.Keys(c.ProviderEnv)) {
		switch {
		case !envKeyRe.MatchString(k):
			return fmt.Errorf("claude.provider-env key %q is not an env-var name", k)
		case reservedEnvKey(k):
			return fmt.Errorf("claude.provider-env key %q is reserved", k)
		case ProtectedEnvKey(k):
			return fmt.Errorf("claude.provider-env: %q is a sealed-owned/security-relevant variable and cannot be set", k)
		case CredentialEnvKey(k):
			return fmt.Errorf("claude.provider-env: %q carries a credential; name credentials via principal.credential, never by value", k)
		}
	}
	for _, k := range claudeNonPreferenceSettings {
		if _, ok := c.Settings[k]; ok {
			return fmt.Errorf("claude.settings: %q is not a preference and cannot be set in a model-spec", k)
		}
	}
	if _, err := json.Marshal(c.Settings); err != nil {
		return fmt.Errorf("claude.settings is not a JSON object: %v", err)
	}
	for i, p := range c.Plugins {
		if strings.TrimSpace(p) == "" {
			return errors.New("claude.plugins has an empty plugin entry")
		}
		if slices.Contains(c.Plugins[:i], p) {
			return fmt.Errorf("claude.plugins lists %q twice", p)
		}
		if err := CheckClaudePlugin(p); err != nil {
			return fmt.Errorf("claude.plugins: %s", err.Error())
		}
	}
	return nil
}

// ProviderEnv is the provider env a spec implies — the one rendering the
// cove-side harness (internal/agentrun) and a plain at-cove session
// (kit.Config.SessionEnv) both use: vertex sets CLAUDE_CODE_USE_VERTEX=1,
// bedrock CLAUDE_CODE_USE_BEDROCK=1, anthropic nothing; then
// claude.provider-env. A protected, reserved or credential-carrying key is
// dropped (defense in depth: Validate refuses them). nil when there is
// nothing to set (or no claude body).
func ProviderEnv(s *Spec) map[string]string {
	if s == nil || s.Claude == nil {
		return nil
	}
	env := map[string]string{}
	for k, v := range s.Claude.ProviderEnv {
		if ProtectedEnvKey(k) || CredentialEnvKey(k) || reservedEnvKey(k) {
			continue
		}
		env[k] = v
	}
	switch s.Claude.Provider {
	case "vertex":
		env["CLAUDE_CODE_USE_VERTEX"] = "1"
	case "bedrock":
		env["CLAUDE_CODE_USE_BEDROCK"] = "1"
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
