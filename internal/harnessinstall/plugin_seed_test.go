package harnessinstall

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

	cmd := exec.Command("bash", "payload/claude/seed-plugins.sh",
		"-m", "claude-plugins-official=anthropics/claude-plugins-official", "-p", "superpowers@claude-plugins-official")
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

// The seed enables what it installed in the first-boot user settings
// (/agent-data/settings.json), merged over the base image's seeded settings —
// so interactive sessions (no per-run --settings) get exactly the harness
// layer's plugins, with their marketplace declared. It never reads the
// managed settings, which no longer enable any plugin (COV-242).
func TestSeedPluginsEnablesInSeedSettings(t *testing.T) {
	requireBash(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	mustWrite(t, filepath.Join(binDir, "claude"), fakeClaude)
	if err := os.Chmod(filepath.Join(binDir, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "seed")
	mustWrite(t, filepath.Join(seed, "settings.json"), `{"theme": "dark", "enabledPlugins": {"old@x": true}}`)
	cmd := exec.Command("bash", "payload/claude/seed-plugins.sh",
		"-m", "claude-plugins-official=anthropics/claude-plugins-official", "-p", "superpowers@claude-plugins-official")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"COVE_PLUGIN_BUILD_CFG="+filepath.Join(dir, "buildcfg"),
		"COVE_PLUGIN_SEED="+seed,
		"COVE_PLUGIN_RUN_AS=",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed-plugins.sh failed: %v\n%s", err, out)
	}
	var got struct {
		Theme                  string          `json:"theme"`
		EnabledPlugins         map[string]bool `json:"enabledPlugins"`
		ExtraKnownMarketplaces map[string]struct {
			Source struct{ Source, Repo string } `json:"source"`
		} `json:"extraKnownMarketplaces"`
	}
	raw := read(t, filepath.Join(seed, "settings.json"))
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("seed settings.json: %v\n%s", err, raw)
	}
	m := got.ExtraKnownMarketplaces["claude-plugins-official"]
	if got.Theme != "dark" || len(got.EnabledPlugins) != 1 || !got.EnabledPlugins["superpowers@claude-plugins-official"] ||
		m.Source.Source != "github" || m.Source.Repo != "anthropics/claude-plugins-official" {
		t.Fatalf("seed settings.json = %s", raw)
	}
	script := read(t, "payload/claude/seed-plugins.sh")
	if strings.Contains(script, "managed-settings") {
		t.Fatal("seed-plugins.sh must not read the managed settings")
	}
}

// The script refuses to run with nothing to seed (the stage only invokes it
// when the spec names plugins) and on an unknown argument.
func TestSeedPluginsRefusesBadArgs(t *testing.T) {
	requireBash(t)
	for _, args := range [][]string{{}, {"-x", "y"}} {
		cmd := exec.Command("bash", append([]string{"payload/claude/seed-plugins.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "COVE_PLUGIN_BUILD_CFG="+filepath.Join(t.TempDir(), "b"), "COVE_PLUGIN_RUN_AS=")
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("args %q accepted:\n%s", args, out)
		}
	}
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
