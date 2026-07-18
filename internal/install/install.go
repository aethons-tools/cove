// Package install owns install.json — the compiled artifact of a kit (mirroring
// internal/state's ownership of state.json). config.yml is the source; running
// `at-cove install` resolves + gates the base, builds the hardened image, and
// freezes the result into install.json. Every run command then reads this
// manifest — never config.yml — verifies the install is still current, and
// consumes the pre-built image (COV-38).
//
// This package is the pure core: the manifest schema, Compile (freeze config +
// resolved build into a manifest), the currency hash (currency.go), and
// read/write against kitDir/.state/install.json. It performs no docker and no
// network; the build itself lives in the backend and is wired in a later step.
package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/kit"
)

const schemaVersion = 1

// ResolvedBuild is install's build output that Compile freezes into a manifest:
// the tagged image ref, the resolved base (ref + digest), the currency hash over
// the build inputs (CurrencyHash), and the install timestamp. The command layer
// produces this from the backend's build op; Compile stays pure by taking it as
// plain data rather than performing the build itself.
type ResolvedBuild struct {
	Image        string // the built, tagged image ref (the stable at-cove-for-<kit> identity)
	BaseRef      string // the base as configured (or the blessed default)
	BaseDigest   string // the digest the base resolved to
	CurrencyHash string // sha256 over the build-affecting inputs (§5)
	InstalledAt  string // RFC3339 timestamp, stamped by the caller (Compile stays pure)
}

// Manifest is the frozen, resolved runtime contract for one installed kit. Run
// commands read it instead of config.yml, so they cannot diverge in how they
// interpret the kit.
type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Name          string `json:"name"`
	InstalledAt   string `json:"installedAt"`
	Image         string `json:"image"`
	BaseRef       string `json:"baseRef"`
	BaseDigest    string `json:"baseDigest"`
	CurrencyHash  string `json:"currencyHash"`
	// RunConfig is the resolved run-config materialized from config.yml (workspace
	// defaults, collaborators, workers, tracker, source-control, dispatch, secret
	// demands — names only, never values — and allowed-domains): whatever a run
	// command needs, resolved once. kit.Load has already applied its defaults.
	RunConfig kit.Config `json:"runConfig"`
}

// Compile freezes the resolved config and the resolved build outputs into a
// manifest, stamping the schema version. Pure: no I/O, no docker — it unit-tests
// with plain structs. The base is expected to be already resolved + gated by the
// caller; Compile only records the result.
func Compile(cfg kit.Config, resolved ResolvedBuild) Manifest {
	return Manifest{
		SchemaVersion: schemaVersion,
		Name:          cfg.Name,
		InstalledAt:   resolved.InstalledAt,
		Image:         resolved.Image,
		BaseRef:       resolved.BaseRef,
		BaseDigest:    resolved.BaseDigest,
		CurrencyHash:  resolved.CurrencyHash,
		RunConfig:     cfg,
	}
}

// Stale reports whether the install is out of date: the current build-affecting
// inputs no longer hash to the frozen CurrencyHash. A run command computes
// `current` from the live kit source, its own embedded identity, and the
// configured base ref, and re-installs when this is true.
func (m Manifest) Stale(current CurrencyInputs) bool {
	return m.CurrencyHash != CurrencyHash(current)
}

// ErrNotInstalled is returned by Load when no install.json exists for the kit.
var ErrNotInstalled = errors.New("no install for this kit (run `at-cove install` first)")

// Dir returns the .state directory inside the kit (shared with state.json).
func Dir(kitDir string) string { return filepath.Join(kitDir, ".state") }

// Path returns the install.json path inside the kit.
func Path(kitDir string) string { return filepath.Join(Dir(kitDir), "install.json") }

// Exists reports whether the kit has an install.json.
func Exists(kitDir string) bool {
	_, err := os.Stat(Path(kitDir))
	return err == nil
}

// Save writes the kit's install.json (creating .state/), stamping the schema
// version. It refreshes the kit's managed .gitignore first, so the
// machine-generated, host-specific manifest never leaks into git (the .state/
// entry covers it).
func Save(kitDir string, m Manifest) error {
	if err := kit.EnsureGitignore(kitDir); err != nil {
		return err
	}
	m.SchemaVersion = schemaVersion
	if err := os.MkdirAll(Dir(kitDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(kitDir), append(b, '\n'), 0o600)
}

// Load reads the kit's install.json. Returns ErrNotInstalled if absent.
func Load(kitDir string) (Manifest, error) {
	b, err := os.ReadFile(Path(kitDir))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrNotInstalled
	}
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
