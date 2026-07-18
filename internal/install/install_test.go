package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
)

func sampleConfig() kit.Config {
	return kit.Config{
		Name:    "box",
		Secrets: map[string]kit.SecretConfig{"FOO": {Description: "a foo"}},
		Image:   kit.ImageConfig{Base: "ghcr.io/x/y:1", AllowedDomains: []string{"example.com"}},
		Workers: map[string]kit.Worker{"implementor": {Prompt: "do it", Concurrency: 2}},
		Dispatch: &kit.Dispatch{
			Concurrency:      3,
			ReaperTimeout:    "30m",
			DispatchOverhead: "15m",
		},
		Collaborators: map[string]kit.Collaborator{"brent": {Prompt: "hi", Default: true}},
	}
}

func sampleBuild() ResolvedBuild {
	return ResolvedBuild{
		Image:        "at-cove-for-box",
		BaseRef:      "ghcr.io/x/y:1",
		BaseDigest:   "sha256:deadbeef",
		CurrencyHash: "abc123",
		InstalledAt:  "2026-07-18T00:00:00Z",
	}
}

// Compile is pure: it freezes the config and the resolved build outputs into a
// manifest with the schema version stamped.
func TestCompileFreezesConfigAndBuild(t *testing.T) {
	cfg := sampleConfig()
	rb := sampleBuild()
	m := Compile(cfg, rb)

	if m.SchemaVersion != schemaVersion {
		t.Errorf("schemaVersion = %d, want %d", m.SchemaVersion, schemaVersion)
	}
	if m.Name != cfg.Name {
		t.Errorf("Name = %q, want %q", m.Name, cfg.Name)
	}
	if m.Image != rb.Image || m.BaseRef != rb.BaseRef || m.BaseDigest != rb.BaseDigest {
		t.Errorf("build outputs not frozen: %+v", m)
	}
	if m.CurrencyHash != rb.CurrencyHash {
		t.Errorf("CurrencyHash = %q, want %q", m.CurrencyHash, rb.CurrencyHash)
	}
	if m.InstalledAt != rb.InstalledAt {
		t.Errorf("InstalledAt = %q, want %q", m.InstalledAt, rb.InstalledAt)
	}
	// The resolved run-config is materialized in full.
	if m.RunConfig.Name != cfg.Name ||
		m.RunConfig.Image.Base != cfg.Image.Base ||
		m.RunConfig.Workers["implementor"].Prompt != "do it" ||
		m.RunConfig.Collaborators["brent"].Default != true {
		t.Errorf("run-config not materialized: %+v", m.RunConfig)
	}
}

// Stale is the currency check: equal inputs are current; any changed build input
// makes the install stale.
func TestStale(t *testing.T) {
	inputs := CurrencyInputs{
		KitSourceTree:       "kit-hash",
		AtCoveBuildIdentity: "id-hash",
		BaseRef:             "ghcr.io/x/y:1",
	}
	m := Compile(sampleConfig(), ResolvedBuild{CurrencyHash: CurrencyHash(inputs)})

	if m.Stale(inputs) {
		t.Error("unchanged kit → must be current")
	}
	edited := inputs
	edited.KitSourceTree = "kit-hash-2"
	if !m.Stale(edited) {
		t.Error("edited config → must be stale")
	}
	bumped := inputs
	bumped.AtCoveBuildIdentity = "id-hash-2"
	if !m.Stale(bumped) {
		t.Error("at-cove identity bump → must be stale")
	}
	rebased := inputs
	rebased.BaseRef = "ghcr.io/x/y:2"
	if !m.Stale(rebased) {
		t.Error("base ref change → must be stale")
	}
}

func TestSaveLoadExists(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Fatal("should not exist yet")
	}
	if _, err := Load(dir); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Load missing: want ErrNotInstalled, got %v", err)
	}

	want := Compile(sampleConfig(), sampleBuild())
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("should exist after save")
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("schemaVersion = %d, want %d", got.SchemaVersion, schemaVersion)
	}
	if got.Image != want.Image || got.BaseDigest != want.BaseDigest || got.CurrencyHash != want.CurrencyHash {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.RunConfig.Workers["implementor"].Concurrency != 2 ||
		got.RunConfig.Secrets["FOO"].Description != "a foo" ||
		got.RunConfig.Dispatch == nil || got.RunConfig.Dispatch.Concurrency != 3 {
		t.Errorf("run-config not round-tripped: %+v", got.RunConfig)
	}

	// The manifest lives at .state/install.json.
	if _, err := os.Stat(filepath.Join(dir, ".state", "install.json")); err != nil {
		t.Fatalf("expected .state/install.json: %v", err)
	}
}

// Save writes the kit's managed .gitignore so the machine-generated manifest
// never leaks into git (the .state/ entry covers install.json).
func TestSaveEnsuresGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Compile(sampleConfig(), sampleBuild())); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore written by Save: %v", err)
	}
	if !strings.Contains(string(b), ".state/") {
		t.Fatalf(".gitignore missing .state/:\n%s", string(b))
	}
}
