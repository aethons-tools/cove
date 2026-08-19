package assemble

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude stands in for the real `claude` CLI: it simulates
// `plugin marketplace add` / `plugin install` by writing the same state files
// (with absolute paths under $CLAUDE_CONFIG_DIR, exactly like the real CLI) so
// the seed script's path-rewrite + fold logic can be exercised hermetically —
// no network, no real Claude Code.
const fakeClaude = `#!/usr/bin/env bash
set -e
cfg="${CLAUDE_CONFIG_DIR:?CLAUDE_CONFIG_DIR unset}"
case "$1 $2 $3" in
  "plugin marketplace add")
    mkdir -p "$cfg/plugins/marketplaces/claude-plugins-official"
    printf '{"claude-plugins-official":{"installLocation":"%s/plugins/marketplaces/claude-plugins-official"}}\n' \
      "$cfg" > "$cfg/plugins/known_marketplaces.json"
    ;;
  "plugin install "*|"plugin install")
    mkdir -p "$cfg/plugins/cache/claude-plugins-official/superpowers/6.1.0"
    printf '{"version":2,"plugins":{"superpowers@claude-plugins-official":[{"installPath":"%s/plugins/cache/claude-plugins-official/superpowers/6.1.0"}]}}\n' \
      "$cfg" > "$cfg/plugins/installed_plugins.json"
    ;;
  "plugin list "*|"plugin list") echo "superpowers@claude-plugins-official enabled" ;;
esac
`

// The seed script must install the marketplace + plugins under a throwaway
// config dir, rewrite the recorded absolute paths to the runtime
// CLAUDE_CONFIG_DIR, and fold the result into the first-boot seed — so that
// after the entrypoint copies the seed into /agent-data the registries resolve.
func TestSeedPluginsFoldsIntoSeedWithRuntimePaths(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()

	binDir := filepath.Join(dir, "bin")
	mustWrite(t, filepath.Join(binDir, "claude"), fakeClaude)
	if err := os.Chmod(filepath.Join(binDir, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	buildCfg := filepath.Join(dir, "buildcfg")
	seed := filepath.Join(dir, "seed")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/seed-plugins.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"COVE_PLUGIN_BUILD_CFG="+buildCfg,
		"COVE_PLUGIN_SEED="+seed,
		"COVE_PLUGIN_RUNTIME_CFG=/agent-data",
		"COVE_PLUGIN_RUN_AS=", // run claude inline as the test user, no `su`
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed-plugins.sh failed: %v\n%s", err, out)
	}

	// The marketplace clone and plugin cache are folded into the seed.
	for _, p := range []string{
		"plugins/marketplaces/claude-plugins-official",
		"plugins/cache/claude-plugins-official/superpowers/6.1.0",
	} {
		if _, err := os.Stat(filepath.Join(seed, p)); err != nil {
			t.Fatalf("expected %s in seed: %v", p, err)
		}
	}

	// The recorded absolute paths point at the runtime config dir, not the
	// throwaway build dir (else they dangle after the seed copy).
	for _, f := range []string{"known_marketplaces.json", "installed_plugins.json"} {
		got := read(t, filepath.Join(seed, "plugins", f))
		if strings.Contains(got, buildCfg) {
			t.Fatalf("%s still references the build dir %q:\n%s", f, buildCfg, got)
		}
		if !strings.Contains(got, "/agent-data/plugins") {
			t.Fatalf("%s does not reference the runtime dir:\n%s", f, got)
		}
	}

	// The throwaway build config dir is cleaned up.
	if _, err := os.Stat(buildCfg); !os.IsNotExist(err) {
		t.Fatalf("build config dir not cleaned up (stat err = %v)", err)
	}
}

// Every plugin enabled in managed-settings.json must have its marketplace
// declared there too, so the seeded known_marketplaces.json entry is
// declaratively backed and the reconciler will not prune it.
func TestManagedSettingsDeclaresMarketplaceForEnabledPlugins(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), nil, ""); err != nil {
		t.Fatal(err)
	}
	raw := read(t, filepath.Join(buildDir, "image-files/etc/claude-code/managed-settings.json"))

	var ms struct {
		EnabledPlugins         map[string]bool            `json:"enabledPlugins"`
		ExtraKnownMarketplaces map[string]json.RawMessage `json:"extraKnownMarketplaces"`
	}
	if err := json.Unmarshal([]byte(raw), &ms); err != nil {
		t.Fatalf("managed-settings.json is not valid JSON: %v", err)
	}
	if len(ms.EnabledPlugins) == 0 {
		t.Fatal("managed-settings.json declares no enabledPlugins")
	}
	for plugin := range ms.EnabledPlugins {
		at := strings.LastIndex(plugin, "@")
		if at < 0 {
			t.Fatalf("enabled plugin %q is not in name@marketplace form", plugin)
		}
		mkt := plugin[at+1:]
		if _, ok := ms.ExtraKnownMarketplaces[mkt]; !ok {
			t.Fatalf("enabled plugin %q references marketplace %q not declared in extraKnownMarketplaces", plugin, mkt)
		}
	}
}
