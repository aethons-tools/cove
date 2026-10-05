package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
	"github.com/aethons-tools/cove/internal/kit"
)

// The model-spec types are defined in the leaf package modelspec (shared with
// the connector and the cove-side harness); these aliases keep Jam's names.
type (
	// HarnessType is a model-spec's harness family (modelspec.HarnessType).
	HarnessType = modelspec.HarnessType
	// ModelSpec is a named, harness-typed description of how a cove runs its
	// agent (modelspec.Spec). A role binds one by name (Role.ModelSpec); Jam
	// delivers the resolved spec to the cove in its connector.
	ModelSpec = modelspec.Spec
	// ModelPrincipal names who the agent authenticates as.
	ModelPrincipal = modelspec.Principal
	// ModelChoice optionally pins the model and its effort.
	ModelChoice = modelspec.Choice
	// ModelPolicy is the harness permission policy.
	ModelPolicy = modelspec.Policy
	// ClaudeSpec is the claude per-type body.
	ClaudeSpec = modelspec.Claude
)

// HarnessClaude is the Claude Code harness family.
const HarnessClaude = modelspec.HarnessClaude

// DefaultModelSpec is the model-spec a role with no binding resolves to; Jam
// seeds it at serve startup (EnsureDefaultModelSpec).
const DefaultModelSpec = modelspec.DefaultName

// PoolPrincipal is the principal.credential keyword meaning "an account from the
// subscription pool" — valid only when the serve-config enables a pool.
const PoolPrincipal = "pool"

// MaxModelSpecNote bounds ModelSpec.Note (an operator hint, like a destination's).
const MaxModelSpecNote = 300

// claudeProviders are the model providers a claude spec can target.
var claudeProviders = []string{"anthropic", "vertex", "bedrock"}

// ClaudePermissionModes lists the accepted policy.mode values (a copy of
// modelspec.PermissionModes), for forms that offer them as choices.
func ClaudePermissionModes() []string { return modelspec.PermissionModes() }

// ClaudeProviders lists the accepted claude.provider values (a copy).
func ClaudeProviders() []string { return slices.Clone(claudeProviders) }

// claudeNonPreferenceSettings are settings.json keys that are not preferences:
// env (would bypass the provider-env checks), permissions (owned by policy),
// the credential helpers (which produce secrets), hooks and statusLine (run
// commands, and hooks can override the permission policy) and MCP server
// selection (owned by the kit and the harness's generated --mcp-config).
var claudeNonPreferenceSettings = []string{
	"env", "permissions",
	"apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper",
	"hooks", "disableAllHooks", "statusLine",
	"enableAllProjectMcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "allowedMcpServers", "deniedMcpServers",
	// Plugin enablement is claude.plugins' (installed at build, enabled per
	// run): enabling an uninstalled plugin would make claude auto-install it
	// through the egress proxy at runtime.
	"enabledPlugins", "extraKnownMarketplaces",
}

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
		return bad("version is required (the exact harness CLI release the image installs, e.g. %q)", modelspec.DefaultClaudeVersion)
	}
	v, err := modelspec.ParseExactVersion(m.Version)
	if err != nil {
		return bad("%s (a range belongs in version-constraint)", err.Error())
	}
	if m.VersionConstraint != "" {
		c, err := modelspec.ParseConstraint(m.VersionConstraint)
		if err != nil {
			return bad("version-constraint: %s", err.Error())
		}
		if !c.Allows(v) {
			return bad("version-constraint %q does not admit version %s (every cove would fail its version check)", m.VersionConstraint, m.Version)
		}
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
	if err := validatePrincipalHeaders(m.Principal.Headers, bad); err != nil {
		return err
	}
	if err := modelspec.CheckPermissionMode(m.Policy.Mode); err != nil {
		return bad("%s", err.Error())
	}
	for field, rules := range map[string][]string{"allow": m.Policy.Allow, "deny": m.Policy.Deny} {
		for _, r := range rules {
			switch {
			case strings.TrimSpace(r) == "":
				return bad("policy.%s has an empty rule", field)
			case strings.TrimSpace(r) != r:
				return bad("policy.%s rule %q has leading or trailing whitespace", field, r)
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
		case modelspec.CredentialEnvKey(k):
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
		if err := modelspec.CheckClaudePlugin(p); err != nil {
			return bad("claude.plugins: %s", err.Error())
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

// ModelSpecSchemaVersion is the model-spec store schema this binary writes:
// 1 = the version split + build-time plugins (COV-242). A store (or a config
// backup) recording less still holds legacy specs.
const ModelSpecSchemaVersion = 1

// ModelSpecMigration reports a MigrateModelSpecs run: the specs it rewrote and
// the warnings (each naming its spec) for anything it dropped.
type ModelSpecMigration struct {
	Migrated []string
	Warnings []string
}

// MigrateModelSpecs is the one-time model-spec store migration (COV-242): when
// the store's schema marker is below ModelSpecSchemaVersion it rewrites EVERY
// stored spec with modelspec.MigrateLegacy — exact-version specs included, as
// only the marker tells a legacy "no plugins" from an explicit one — then
// records the marker, so it never runs again. Run at serve startup, before
// EnsureDefaultModelSpec.
func MigrateModelSpecs(store Store) (ModelSpecMigration, error) {
	modelSpecMu.Lock()
	defer modelSpecMu.Unlock()
	var rep ModelSpecMigration
	if store.ModelSpecSchema() >= ModelSpecSchemaVersion {
		return rep, nil
	}
	for _, m := range store.ListModelSpecs() {
		out, warns := modelspec.MigrateLegacy(m)
		rep.Warnings = append(rep.Warnings, warns...)
		if sameSpec(m, out) {
			continue
		}
		if err := store.PutModelSpec(out); err != nil {
			return rep, fmt.Errorf("migrate model-spec %q: %w", m.Name, err)
		}
		rep.Migrated = append(rep.Migrated, m.Name)
	}
	if err := store.SetModelSpecSchema(ModelSpecSchemaVersion); err != nil {
		return rep, fmt.Errorf("record model-spec schema: %w", err)
	}
	return rep, nil
}

// MigrateSnapshotModelSpecs applies the one-time migration to a config backup
// taken before it (ModelSpecSchema below ModelSpecSchemaVersion) and marks the
// snapshot current, returning the warnings; a current snapshot is untouched.
func MigrateSnapshotModelSpecs(snap *ConfigSnapshot) []string {
	if snap.ModelSpecSchema >= ModelSpecSchemaVersion {
		return nil
	}
	var warns []string
	for i, ms := range snap.ModelSpecs {
		var w []string
		snap.ModelSpecs[i], w = modelspec.MigrateLegacy(ms)
		warns = append(warns, w...)
	}
	snap.ModelSpecSchema = ModelSpecSchemaVersion
	return warns
}

// sameSpec reports whether a and b store identically.
func sameSpec(a, b ModelSpec) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// ErrNoDefaultPrincipal is EnsureDefaultModelSpec's refusal when it can find
// no principal for the default spec: no pool, and no anthropic destination.
var ErrNoDefaultPrincipal = errors.New("no subscription pool and no anthropic destination to take the default model-spec's principal from")

// DefaultModelSpecFor builds DefaultModelSpec as Jam seeds it
// (modelspec.Default), authenticating as the pool when one is configured, else as the anthropic destination's
// credential (the destination named "anthropic", or else the one routed at
// /anthropic/). ErrNoDefaultPrincipal when neither resolves.
func DefaultModelSpecFor(store Store, poolConfigured bool) (ModelSpec, error) {
	cred := ""
	if poolConfigured {
		cred = PoolPrincipal
	} else {
		dests := store.ListDestinations()
		for _, match := range []func(Destination) bool{
			func(d Destination) bool { return d.Name == "anthropic" },
			func(d Destination) bool { return d.Route == "/anthropic/" },
		} {
			if i := slices.IndexFunc(dests, match); i >= 0 && dests[i].CredName != "" {
				cred = dests[i].CredName
				break
			}
		}
	}
	if cred == "" {
		return ModelSpec{}, ErrNoDefaultPrincipal
	}
	return modelspec.Default(cred), nil
}

// EnsureDefaultModelSpec seeds DefaultModelSpec at serve startup when absent
// (an operator's edits to an existing one are kept). created reports a seed;
// ErrNoDefaultPrincipal means nothing was seeded — unbound roles then deliver
// no spec and their coves keep the harness's built-in defaults.
func EnsureDefaultModelSpec(store Store, poolConfigured bool) (created bool, err error) {
	modelSpecMu.Lock()
	defer modelSpecMu.Unlock()
	if _, ok := store.GetModelSpec(DefaultModelSpec); ok {
		return false, nil
	}
	m, err := DefaultModelSpecFor(store, poolConfigured)
	if err != nil {
		return false, err
	}
	if err := store.PutModelSpec(m); err != nil {
		return false, err
	}
	return true, nil
}
