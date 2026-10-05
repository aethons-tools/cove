package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
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
const MaxModelSpecNote = modelspec.MaxNote

// ClaudePermissionModes lists the accepted policy.mode values (a copy of
// modelspec.PermissionModes), for forms that offer them as choices.
func ClaudePermissionModes() []string { return modelspec.PermissionModes() }

// ClaudeProviders lists the accepted claude.provider values (a copy).
func ClaudeProviders() []string { return modelspec.ClaudeProviders() }

// ValidateModelSpec checks a model-spec at write time with the one shared
// validator (modelspec.Validate — the kit's model-spec: block uses it too),
// giving the principal Jam's meaning: credExists resolves a serve-config
// credential name; poolConfigured says whether the PoolPrincipal keyword is
// available; the header rules are checked (validatePrincipalHeaders).
// Refusals are 400 WriteErrors and never echo an env value.
func ValidateModelSpec(m ModelSpec, credExists func(string) bool, poolConfigured bool) error {
	bad := func(format string, a ...any) error { return writeErr(http.StatusBadRequest, format, a...) }
	err := modelspec.Validate(m, func(p ModelPrincipal) error {
		switch c := p.Credential; {
		case c == "":
			return bad("principal.credential is required (a credential name, or %q)", PoolPrincipal)
		case c == PoolPrincipal:
			if !poolConfigured {
				return bad("principal.credential is %q but no subscription pool is configured", PoolPrincipal)
			}
		case !credExists(c):
			return bad("principal.credential %q does not resolve to a configured credential", c)
		}
		return validatePrincipalHeaders(p.Headers, bad)
	})
	var we *WriteError
	if err == nil || errors.As(err, &we) {
		return err
	}
	return bad("%s", err.Error())
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

// ModelSpecSchemaVersion is the model-spec store schema this binary writes —
// each one-time migration step a store (or a config backup) recording less has
// not had (specMigration):
//
//  1. the version split + build-time plugins (COV-242): modelspec.MigrateLegacy
//     on every stored spec;
//  2. the Claude preferences moved out of the sealed managed settings into
//     claude-default (COV-245): modelspec.MigrateSettings — a stored
//     claude-default gains only the preference keys it lacks, and every spec
//     drops the managed sandbox-policy keys Jam now refuses (with warnings).
//
// The removed destination oauth_beta flag (COV-241) is not a marker step: it
// is an idempotent scan run on every migration call (legacyOAuthBetaCreds).
const ModelSpecSchemaVersion = 2

// OAuthBetaRule is the principal header rule that carries Anthropic's
// subscription-OAuth beta. The broker already ensures that beta for every pool
// credential (subscriptionOAuthBeta); the rule is only for a subscription
// token that is NOT the pool's — the legacy-flag scan adds it to the specs
// whose principal is a flagged non-pool destination's credential.
func OAuthBetaRule() ModelHeaderRule {
	return ModelHeaderRule{Name: "anthropic-beta", EnsureListItem: subscriptionOAuthBeta}
}

// ModelSpecMigration reports a MigrateModelSpecs run: the specs it rewrote,
// the destinations whose removed oauth_beta flag it cleared, and the warnings
// (each naming its spec) for anything it dropped.
type ModelSpecMigration struct {
	Migrated     []string
	Destinations []string
	Warnings     []string
}

// legacyOAuthBetaCreds returns the credentials of the destinations still
// carrying the removed oauth_beta flag — the principals whose specs gain
// OAuthBetaRule — or nil when none does. Pool-backed destinations need no rule
// (the broker ensures the beta for a pool credential), but the migration does
// not know the pool's cred-name, so a spec naming it literally gets a
// redundant rule: harmless, as the beta is ensured, never duplicated. A spec
// whose principal is the `pool` keyword is never touched.
func legacyOAuthBetaCreds(dests []Destination) map[string]bool {
	var creds map[string]bool
	for _, d := range dests {
		if !d.LegacyOAuthBeta || d.CredName == "" || d.CredName == PoolPrincipal {
			continue
		}
		if creds == nil {
			creds = map[string]bool{}
		}
		creds[d.CredName] = true
	}
	return creds
}

// specMigration applies to a spec every schema step above `from` (the store's
// or snapshot's recorded marker), then the legacy oauth_beta rule for the
// principals in oauthBeta (always — not marker-gated).
type specMigration struct {
	from      int
	oauthBeta map[string]bool
}

// newSpecMigration builds the migration from `from` for a store or snapshot
// holding dests.
func newSpecMigration(from int, dests []Destination) specMigration {
	return specMigration{from: from, oauthBeta: legacyOAuthBetaCreds(dests)}
}

func (sm specMigration) apply(m ModelSpec) (ModelSpec, []string) {
	var warns []string
	if sm.from < 1 {
		m, warns = modelspec.MigrateLegacy(m)
	}
	if sm.from < 2 {
		var w []string
		m, w = modelspec.MigrateSettings(m)
		warns = append(warns, w...)
	}
	if sm.oauthBeta[m.Principal.Credential] {
		rule := OAuthBetaRule()
		switch {
		case slices.ContainsFunc(m.Principal.Headers, func(r ModelHeaderRule) bool {
			return http.CanonicalHeaderKey(r.Name) == http.CanonicalHeaderKey(rule.Name) && r.EnsureListItem == rule.EnsureListItem
		}):
		case len(m.Principal.Headers) >= MaxPrincipalHeaderRules:
			warns = append(warns, fmt.Sprintf("model-spec %q: could not add the %s %s rule replacing the removed destination oauth_beta flag: it already has %d header rules", m.Name, rule.Name, rule.EnsureListItem, MaxPrincipalHeaderRules))
		default:
			// Appended: the flag used to apply after every rule.
			m.Principal.Headers = append(slices.Clone(m.Principal.Headers), rule)
		}
	}
	return m, warns
}

// MigrateModelSpecs is the model-spec store migration, run at every serve
// startup before EnsureDefaultModelSpec:
//
//   - one-time steps: when the store's schema marker is below
//     ModelSpecSchemaVersion it rewrites EVERY stored spec with the steps it
//     has not had (specMigration) — step 1 covers exact-version specs too, as
//     only the marker tells a legacy "no plugins" from an explicit one — then
//     records the marker, so no step ever runs twice (an operator's later
//     edits are never undone);
//   - every time: the legacy oauth_beta scan — when a destination still
//     carries the removed flag (an old store, or a row an older binary wrote
//     during a rolling deploy), the specs naming a flagged destination's
//     credential gain OAuthBetaRule and the flag is cleared. Idempotent.
func MigrateModelSpecs(store Store) (ModelSpecMigration, error) {
	modelSpecMu.Lock()
	defer modelSpecMu.Unlock()
	var rep ModelSpecMigration
	from := store.ModelSpecSchema()
	dests := store.ListDestinations()
	sm := newSpecMigration(from, dests)
	if from < ModelSpecSchemaVersion || sm.oauthBeta != nil {
		for _, m := range store.ListModelSpecs() {
			out, warns := sm.apply(m)
			rep.Warnings = append(rep.Warnings, warns...)
			if sameSpec(m, out) {
				continue
			}
			if err := store.PutModelSpec(out); err != nil {
				return rep, fmt.Errorf("migrate model-spec %q: %w", m.Name, err)
			}
			rep.Migrated = append(rep.Migrated, m.Name)
		}
	}
	for _, d := range dests {
		if !d.LegacyOAuthBeta {
			continue
		}
		d.LegacyOAuthBeta = false
		if err := store.AddDestination(d); err != nil {
			return rep, fmt.Errorf("migrate destination %q: %w", d.Name, err)
		}
		rep.Destinations = append(rep.Destinations, d.Name)
	}
	if from < ModelSpecSchemaVersion {
		if err := store.SetModelSpecSchema(ModelSpecSchemaVersion); err != nil {
			return rep, fmt.Errorf("record model-spec schema: %w", err)
		}
	}
	return rep, nil
}

// MigrateSnapshotModelSpecs migrates a config backup on import: the one-time
// steps its ModelSpecSchema has not had (then marked current), and — always —
// the legacy oauth_beta scan over its destinations (flags cleared). Returns the
// warnings; a current snapshot without flags is untouched.
func MigrateSnapshotModelSpecs(snap *ConfigSnapshot) []string {
	sm := newSpecMigration(snap.ModelSpecSchema, snap.Destinations)
	var warns []string
	if snap.ModelSpecSchema < ModelSpecSchemaVersion || sm.oauthBeta != nil {
		for i, ms := range snap.ModelSpecs {
			var w []string
			snap.ModelSpecs[i], w = sm.apply(ms)
			warns = append(warns, w...)
		}
	}
	for i := range snap.Destinations {
		snap.Destinations[i].LegacyOAuthBeta = false
	}
	if snap.ModelSpecSchema < ModelSpecSchemaVersion {
		snap.ModelSpecSchema = ModelSpecSchemaVersion
	}
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
// (modelspec.Default), authenticating as the pool when one is configured,
// else as the anthropic destination's
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
