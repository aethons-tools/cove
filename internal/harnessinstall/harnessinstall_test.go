package harnessinstall

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// Default is claude-default's install: the one pinned version constant and the
// plugins every image carried before plugins became model-spec-driven.
func TestDefault(t *testing.T) {
	d := Default()
	if d.Type != modelspec.HarnessClaude || d.Version != modelspec.DefaultClaudeVersion ||
		!slices.Equal(d.Plugins, []string{"superpowers@claude-plugins-official"}) {
		t.Fatalf("Default() = %+v", d)
	}
	if got := FromSpec(nil); !equal(got, d) {
		t.Fatalf("FromSpec(nil) = %+v, want Default", got)
	}
}

func equal(a, b Install) bool {
	return a.Type == b.Type && a.Version == b.Version && slices.Equal(a.Plugins, b.Plugins)
}

// FromSpec takes the spec's exact version and its plugins (normalized), and
// reads a legacy constraint-version spec as its migrated form.
func TestFromSpec(t *testing.T) {
	s := modelspec.Spec{Type: modelspec.HarnessClaude, Version: "2.1.100", VersionConstraint: "2.x",
		Claude: &modelspec.Claude{Provider: "anthropic", Plugins: []string{"b@claude-plugins-official", "a@claude-plugins-official", "b@claude-plugins-official"}}}
	got := FromSpec(&s)
	if got.Version != "2.1.100" || !slices.Equal(got.Plugins, []string{"a@claude-plugins-official", "b@claude-plugins-official"}) {
		t.Fatalf("FromSpec = %+v", got)
	}
	if len(s.Claude.Plugins) != 3 {
		t.Fatal("FromSpec mutated the spec's plugins")
	}
	none := FromSpec(&modelspec.Spec{Type: modelspec.HarnessClaude, Version: "2.1.100", Claude: &modelspec.Claude{Provider: "anthropic"}})
	if none.Plugins == nil || len(none.Plugins) != 0 {
		t.Fatalf("no plugins must normalize to an empty list: %+v", none)
	}
	legacy := FromSpec(&modelspec.Spec{Type: modelspec.HarnessClaude, Version: ">=2.0.0", Claude: &modelspec.Claude{Provider: "anthropic"}})
	if !equal(legacy, Default()) {
		t.Fatalf("legacy spec install = %+v, want the migrated (default) install", legacy)
	}
}

func TestValidateRefuses(t *testing.T) {
	for name, in := range map[string]Install{
		"type":        {Type: "codex", Version: "1.0.0"},
		"range":       {Type: modelspec.HarnessClaude, Version: "2.x"},
		"injection":   {Type: modelspec.HarnessClaude, Version: "2.1.0;id"},
		"plugin":      {Type: modelspec.HarnessClaude, Version: "2.1.0", Plugins: []string{"x';id;'@claude-plugins-official"}},
		"marketplace": {Type: modelspec.HarnessClaude, Version: "2.1.0", Plugins: []string{"x@elsewhere"}},
	} {
		if err := in.Validate(); err == nil {
			t.Errorf("%s: %+v accepted", name, in)
		}
		if _, err := Stage(t.TempDir(), in); err == nil {
			t.Errorf("%s: Stage rendered an invalid install", name)
		}
	}
}

// The claude stage opens FROM ${BASE} AS harness, installs the exact version
// with the native installer, symlinks it, disables the auto-updater, and seeds
// the spec's marketplace + plugins from the staged payload.
func TestStageClaude(t *testing.T) {
	dir := t.TempDir()
	in := Install{Type: modelspec.HarnessClaude, Version: "2.1.100", Plugins: []string{"superpowers@claude-plugins-official"}}
	df, err := Stage(dir, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ARG BASE\n",
		"FROM ${BASE} AS harness\n",
		"RUN su - agent -c 'curl -fsSL https://claude.ai/install.sh | bash -s 2.1.100'\n",
		"RUN ln -sf /home/agent/.local/bin/claude /usr/local/bin/claude\n",
		"ENV DISABLE_AUTOUPDATER=1\n",
		"COPY harness/seed-plugins.sh /tmp/cove-seed/\n",
		"seed-plugins.sh -m 'claude-plugins-official=anthropics/claude-plugins-official' -p 'superpowers@claude-plugins-official'",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("stage missing %q:\n%s", want, df)
		}
	}
	if strings.Index(df, "ARG BASE") > strings.Index(df, "FROM ${BASE} AS") {
		t.Errorf("ARG BASE must precede the FROM:\n%s", df)
	}
	if strings.Index(df, "install.sh") > strings.Index(df, "seed-plugins.sh") {
		t.Errorf("plugins must be seeded after the CLI is installed:\n%s", df)
	}
	if fi, err := os.Stat(filepath.Join(dir, ContextDir, "seed-plugins.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("seed-plugins.sh not staged executable: %v", err)
	}
}

// No plugins → no seed step (the script refuses an empty list).
func TestStageClaudeNoPlugins(t *testing.T) {
	df, err := Stage(t.TempDir(), Install{Type: modelspec.HarnessClaude, Version: "2.1.100", Plugins: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(df, "seed-plugins.sh") || !strings.Contains(df, "bash -s 2.1.100") {
		t.Fatalf("stage without plugins:\n%s", df)
	}
}

// The claude stage (COV-245) runs, in this order and each in its OWN
// COPY/RUN layer: the plugin seed (network-bound; before the managed settings
// exist, so their DISABLE_UPDATES etc. never apply to build-time `claude
// plugin` commands), the baseline user preferences, then the managed settings
// at /etc/claude-code/managed-settings.json, root-owned 0644. Each COPY names
// only its own files, so editing one never invalidates another's layer.
func TestStageClaudeLayers(t *testing.T) {
	for _, plugins := range [][]string{{}, {"superpowers@claude-plugins-official"}} {
		dir := t.TempDir()
		df, err := Stage(dir, Install{Type: modelspec.HarnessClaude, Version: "2.1.100", Plugins: plugins})
		if err != nil {
			t.Fatal(err)
		}
		layers := []string{
			"COPY harness/merge-baseline-settings.sh harness/baseline-settings.json /tmp/cove-baseline/\n",
			"RUN bash /tmp/cove-baseline/merge-baseline-settings.sh /tmp/cove-baseline/baseline-settings.json \\\n && rm -rf /tmp/cove-baseline\n",
			"COPY harness/managed-settings.json /tmp/cove-managed/\n",
			"RUN install -D -o root -g root -m 0644 /tmp/cove-managed/managed-settings.json /etc/claude-code/managed-settings.json \\\n && rm -rf /tmp/cove-managed\n",
		}
		if len(plugins) > 0 {
			layers = append([]string{
				"COPY harness/seed-plugins.sh /tmp/cove-seed/\n",
				"RUN bash /tmp/cove-seed/seed-plugins.sh -m 'claude-plugins-official=anthropics/claude-plugins-official' -p 'superpowers@claude-plugins-official' \\\n && rm -rf /tmp/cove-seed\n",
			}, layers...)
		} else if strings.Contains(df, "seed-plugins.sh") {
			t.Errorf("no plugins → no seed layer:\n%s", df)
		}
		at := strings.Index(df, "install.sh")
		for _, want := range layers {
			i := strings.Index(df, want)
			if i < 0 {
				t.Errorf("plugins=%v: stage missing %q:\n%s", plugins, want, df)
				continue
			}
			if i < at {
				t.Errorf("plugins=%v: %q is out of order (want CLI → seed → baseline → managed):\n%s", plugins, want, df)
			}
			at = i
		}
		if strings.Contains(df, "COPY harness/ ") {
			t.Errorf("a whole-payload COPY ties every layer to every payload file:\n%s", df)
		}
		for _, f := range []string{"managed-settings.json", "baseline-settings.json", "merge-baseline-settings.sh"} {
			if _, err := os.Stat(filepath.Join(dir, ContextDir, f)); err != nil {
				t.Fatalf("%s not staged: %v", f, err)
			}
		}
	}
}

// The baseline is rendered from the one source, modelspec.DefaultClaudeSettings
// — the preferences the sealed managed settings used to force — and is part
// of at-cove's build identity (BaselineSettings).
func TestStagedBaselineIsDefaultClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	if _, err := Stage(dir, Default()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ContextDir, "baseline-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(BaselineSettings()) {
		t.Fatalf("staged baseline %s != BaselineSettings %s", raw, BaselineSettings())
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(modelspec.DefaultClaudeSettings())
	if string(gj) != string(wj) {
		t.Fatalf("baseline = %s, want %s", gj, wj)
	}
}

// The managed settings hold ONLY sandbox-wide, non-preference policy:
// preferences moved to claude-default's claude.settings (COV-245), plugin
// enablement follows the spec (COV-242), and permissions keeps only the
// bypassPermissions default interactive (non-harness) sessions rely on.
func TestManagedSettingsAreSandboxPolicyOnly(t *testing.T) {
	raw, err := fs.ReadFile(PayloadFS(), "payload/claude/managed-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("managed-settings.json is not JSON: %v", err)
	}
	want := map[string]any{
		"$schema":                           "https://json.schemastore.org/claude-code-settings.json",
		"autoUpdates":                       false,
		"disableAutoMode":                   "disable",
		"disableRemoteControl":              false,
		"remoteControlAtStartup":            true,
		"env":                               map[string]any{"DISABLE_AUTOUPDATER": "1", "DISABLE_UPDATES": "1"},
		"permissions":                       map[string]any{"allow": []any{}, "deny": []any{}, "defaultMode": "bypassPermissions"},
		"skipDangerousModePermissionPrompt": true,
		"bypassPermissionsModeAccepted":     true,
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("managed settings:\n got %s\nwant %s", gj, wj)
	}
	for k := range modelspec.DefaultClaudeSettings() {
		if _, ok := got[k]; ok {
			t.Errorf("preference %q must live in claude-default's claude.settings, not managed settings", k)
		}
	}
	if strings.Contains(string(raw), "forceLoginMethod") {
		t.Error("managed settings must not force a login method (env-driven auth)")
	}
}
