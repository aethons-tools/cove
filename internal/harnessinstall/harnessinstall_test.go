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
		"COPY harness/ /tmp/cove-harness/\n",
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

// The claude stage installs Claude Code's managed settings (COV-245, moved out
// of the sealed hardening layer: they are Claude-specific) at
// /etc/claude-code/managed-settings.json, root-owned 0644, whether or not the
// spec has plugins — and after the CLI install.
func TestStageClaudeManagedSettings(t *testing.T) {
	for _, plugins := range [][]string{{}, {"superpowers@claude-plugins-official"}} {
		dir := t.TempDir()
		df, err := Stage(dir, Install{Type: modelspec.HarnessClaude, Version: "2.1.100", Plugins: plugins})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"COPY harness/ /tmp/cove-harness/\n",
			"install -D -o root -g root -m 0644 /tmp/cove-harness/managed-settings.json /etc/claude-code/managed-settings.json",
			"rm -rf /tmp/cove-harness",
		} {
			if !strings.Contains(df, want) {
				t.Errorf("plugins=%v: stage missing %q:\n%s", plugins, want, df)
			}
		}
		if strings.Index(df, "install.sh") > strings.Index(df, "managed-settings.json") {
			t.Errorf("managed settings must follow the CLI install:\n%s", df)
		}
		if _, err := os.Stat(filepath.Join(dir, ContextDir, "managed-settings.json")); err != nil {
			t.Fatalf("managed-settings.json not staged: %v", err)
		}
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
