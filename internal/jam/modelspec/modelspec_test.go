package modelspec

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestCheckPermissionMode(t *testing.T) {
	for _, mode := range append(PermissionModes(), "") {
		if err := CheckPermissionMode(mode); err != nil {
			t.Errorf("mode %q refused: %v", mode, err)
		}
	}
	for mode, want := range map[string]string{"yolo": `"yolo"`, ModePlan: "never leave plan mode", "Default": `"Default"`} {
		if err := CheckPermissionMode(mode); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("mode %q: err = %v, want one mentioning %q", mode, err, want)
		}
	}
}

func TestPermissionModesIsACopy(t *testing.T) {
	m := PermissionModes()
	m[0] = "mutated"
	if PermissionModes()[0] == "mutated" {
		t.Fatal("PermissionModes shares its backing array")
	}
}

// The runtime constraint defaults to == version; an explicit one wins.
func TestRuntimeConstraint(t *testing.T) {
	s := Spec{Version: "2.1.287"}
	if got := s.RuntimeConstraint(); got != "2.1.287" {
		t.Fatalf("default runtime constraint = %q, want the version", got)
	}
	c, err := ParseConstraint(s.RuntimeConstraint())
	if err != nil || !c.Allows(Version{2, 1, 287}) || c.Allows(Version{2, 1, 288}) {
		t.Fatalf("default constraint must match exactly the version: %v", err)
	}
	s.VersionConstraint = "2.x"
	if got := s.RuntimeConstraint(); got != "2.x" {
		t.Fatalf("explicit runtime constraint = %q, want 2.x", got)
	}
}

// claude-default is pinned to the one constant and carries the plugins every
// image had before plugins became model-spec-driven.
func TestDefaultIsPinned(t *testing.T) {
	d := Default("pool")
	if d.Version != DefaultClaudeVersion || d.VersionConstraint != "" {
		t.Fatalf("default version = %q / %q, want %q with no separate constraint", d.Version, d.VersionConstraint, DefaultClaudeVersion)
	}
	if _, err := ParseExactVersion(DefaultClaudeVersion); err != nil {
		t.Fatalf("DefaultClaudeVersion is not exact: %v", err)
	}
	if d.Claude == nil || !slices.Equal(d.Claude.Plugins, []string{"superpowers@claude-plugins-official"}) {
		t.Fatalf("default plugins = %+v", d.Claude)
	}
	for _, p := range d.Claude.Plugins {
		if err := CheckClaudePlugin(p); err != nil {
			t.Fatalf("default plugin %q invalid: %v", p, err)
		}
	}
}

func TestCheckClaudePlugin(t *testing.T) {
	if err := CheckClaudePlugin("superpowers@claude-plugins-official"); err != nil {
		t.Fatal(err)
	}
	if src, ok := ClaudeMarketplaceSource("claude-plugins-official"); !ok || src != "anthropics/claude-plugins-official" {
		t.Fatalf("claude-plugins-official source = %q, %v", src, ok)
	}
	for id, want := range map[string]string{
		"superpowers":                     "name@marketplace",
		"@claude-plugins-official":        "name@marketplace",
		"x@y@z":                           "name@marketplace",
		"a b@claude-plugins-official":     "name@marketplace",
		"a';id;'@claude-plugins-official": "name@marketplace",
		"-rf@claude-plugins-official":     "name@marketplace",
		"superpowers@unknown":             `"unknown"`,
	} {
		if err := CheckClaudePlugin(id); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one mentioning %q", id, err, want)
		}
	}
}

func TestMigrateLegacyVersion(t *testing.T) {
	claude := func(v string, plugins ...string) Spec {
		return Spec{Name: "x", Type: HarnessClaude, Version: v, Claude: &Claude{Provider: "anthropic", Plugins: plugins}}
	}
	// An exact version is kept; a range that admits the pin is preserved as the
	// runtime constraint, so coves on images built by the old hardening (latest
	// claude) keep passing their check.
	for _, c := range []struct{ in, wantVersion, wantConstraint string }{
		{"2.1.0", "2.1.0", ""},
		{"2.x", DefaultClaudeVersion, "2.x"},
		{"*", DefaultClaudeVersion, "*"},
		{LegacyDefaultVersion, DefaultClaudeVersion, LegacyDefaultVersion},
	} {
		got, warns := MigrateLegacy(claude(c.in, "a@claude-plugins-official"))
		if got.Version != c.wantVersion || got.VersionConstraint != c.wantConstraint || len(warns) != 0 {
			t.Errorf("%q → %q / %q (warnings %v), want %q / %q", c.in, got.Version, got.VersionConstraint, warns, c.wantVersion, c.wantConstraint)
		}
	}
	// A range that does not admit the pin (or does not parse) is dropped for the
	// pin itself — never a spec validation would refuse — with a warning naming
	// the spec and the dropped constraint.
	for _, old := range []string{"2.0.x", "1.x", ">=3.0.0", "latest"} {
		got, warns := MigrateLegacy(claude(old, "a@claude-plugins-official"))
		if got.Version != DefaultClaudeVersion || got.VersionConstraint != DefaultClaudeVersion {
			t.Errorf("%q → %q / %q, want the pin for both", old, got.Version, got.VersionConstraint)
		}
		if len(warns) != 1 || !strings.Contains(warns[0], `"x"`) || !strings.Contains(warns[0], old) {
			t.Errorf("%q: warnings = %v, want one naming the spec and %q", old, warns, old)
		}
	}
}

func TestMigrateLegacyPlugins(t *testing.T) {
	// No plugins: before COV-242 every image carried the default ones.
	got, warns := MigrateLegacy(Spec{Name: "x", Type: HarnessClaude, Version: "2.1.0", Claude: &Claude{Provider: "anthropic"}})
	if !slices.Equal(got.Claude.Plugins, DefaultClaudePlugins()) || len(warns) != 0 {
		t.Fatalf("backfill = %v, %v", got.Claude.Plugins, warns)
	}
	// Unknown-marketplace / malformed ids are dropped with a warning naming the
	// spec and the plugin; valid ones are kept.
	in := Spec{Name: "x", Type: HarnessClaude, Version: "2.1.0", Claude: &Claude{Provider: "anthropic",
		Plugins: []string{"keep@claude-plugins-official", "superpowers@official", "bare"}}}
	got, warns = MigrateLegacy(in)
	if !slices.Equal(got.Claude.Plugins, []string{"keep@claude-plugins-official"}) || len(warns) != 2 ||
		!strings.Contains(strings.Join(warns, "\n"), "superpowers@official") || !strings.Contains(warns[0], `"x"`) {
		t.Fatalf("drop = %v, %v", got.Claude.Plugins, warns)
	}
	if len(in.Claude.Plugins) != 3 {
		t.Fatal("MigrateLegacy mutated its input")
	}
	// All dropped → the default ones (the image carried them before).
	got, _ = MigrateLegacy(Spec{Name: "x", Type: HarnessClaude, Version: "2.1.0", Claude: &Claude{Provider: "anthropic", Plugins: []string{"a@nowhere"}}})
	if !slices.Equal(got.Claude.Plugins, DefaultClaudePlugins()) {
		t.Fatalf("all dropped = %v", got.Claude.Plugins)
	}
}

func TestMigrateLegacyDropsPluginSettings(t *testing.T) {
	in := Spec{Name: "x", Type: HarnessClaude, Version: "2.1.0", Claude: &Claude{Provider: "anthropic",
		Settings: map[string]any{"theme": "dark", "enabledPlugins": map[string]any{"a@b": true}}}}
	got, warns := MigrateLegacy(in)
	if _, ok := got.Claude.Settings["enabledPlugins"]; ok || got.Claude.Settings["theme"] != "dark" || len(warns) != 1 {
		t.Fatalf("settings = %v, warnings %v", got.Claude.Settings, warns)
	}
	if _, ok := in.Claude.Settings["enabledPlugins"]; !ok {
		t.Fatal("MigrateLegacy mutated its input's settings")
	}
}

// claude-default carries the Claude preferences the sealed managed settings
// used to force (COV-245), exactly those values, and nothing that is sandbox
// policy (that stays in the harness layer's managed settings).
func TestDefaultClaudeSettings(t *testing.T) {
	want := map[string]any{
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
	d := Default("pool")
	if !maps.Equal(d.Claude.Settings, want) {
		t.Fatalf("default settings = %v, want %v", d.Claude.Settings, want)
	}
	if !maps.Equal(DefaultClaudeSettings(), want) {
		t.Fatalf("DefaultClaudeSettings = %v", DefaultClaudeSettings())
	}
	d.Claude.Settings["theme"] = "light"
	if Default("pool").Claude.Settings["theme"] != "dark" || DefaultClaudeSettings()["theme"] != "dark" {
		t.Fatal("Default shares its settings map")
	}
	for _, k := range []string{"disableAutoMode", "permissions", "env", "autoUpdates", "remoteControlAtStartup",
		"disableRemoteControl", "skipDangerousModePermissionPrompt", "bypassPermissionsModeAccepted"} {
		if _, ok := want[k]; ok {
			t.Errorf("%q is sandbox policy, not a claude-default preference", k)
		}
	}
}

// Schema step 2 (COV-245): a stored claude-default gains only the preference
// keys it lacks — an operator's values are kept — and any other spec, or a
// claude-default for another harness/without a body, gains nothing.
func TestMigrateSettingsAddsDefaultPreferences(t *testing.T) {
	stored := Default("pool")
	stored.Claude.Settings = map[string]any{"theme": "light", "model": "opus"}
	got, warns := MigrateSettings(stored)
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	if got.Claude.Settings["theme"] != "light" || got.Claude.Settings["model"] != "opus" {
		t.Fatalf("operator values overwritten: %v", got.Claude.Settings)
	}
	for k, v := range DefaultClaudeSettings() {
		if k != "theme" && got.Claude.Settings[k] != v {
			t.Errorf("missing key %q = %v, want %v", k, got.Claude.Settings[k], v)
		}
	}
	if len(stored.Claude.Settings) != 2 {
		t.Fatal("MigrateSettings mutated its input")
	}
	none := Default("pool")
	none.Claude.Settings = nil
	if got, _ := MigrateSettings(none); !maps.Equal(got.Claude.Settings, DefaultClaudeSettings()) {
		t.Fatalf("no settings → %v", got.Claude.Settings)
	}
	other := Default("pool")
	other.Name = "custom"
	other.Claude.Settings = nil
	if got, _ := MigrateSettings(other); got.Claude.Settings != nil {
		t.Fatalf("a non-default spec gained preferences: %v", got.Claude.Settings)
	}
	bodiless := Spec{Name: DefaultName, Type: HarnessClaude}
	if got, _ := MigrateSettings(bodiless); got.Claude != nil {
		t.Fatal("a claude-default with no claude body was given one")
	}
}

// Schema step 2 also drops, from EVERY claude spec, the claude.settings keys
// that became managed sandbox policy (ManagedPolicySettings), each with a
// warning naming the spec and key — so stored specs stay updatable.
func TestMigrateSettingsDropsManagedPolicyKeys(t *testing.T) {
	in := Spec{Name: "custom", Type: HarnessClaude, Version: "2.1.0", Claude: &Claude{Provider: "anthropic",
		Settings: map[string]any{"theme": "light", "autoUpdates": true, "disableAutoMode": "disable"}}}
	got, warns := MigrateSettings(in)
	if !maps.Equal(got.Claude.Settings, map[string]any{"theme": "light"}) {
		t.Fatalf("settings = %v", got.Claude.Settings)
	}
	joined := strings.Join(warns, "\n")
	if len(warns) != 2 || !strings.Contains(joined, `"custom"`) || !strings.Contains(joined, "autoUpdates") || !strings.Contains(joined, "disableAutoMode") {
		t.Fatalf("warnings = %v", warns)
	}
	if len(in.Claude.Settings) != 3 {
		t.Fatal("MigrateSettings mutated its input")
	}
	def := Default("pool")
	def.Claude.Settings["remoteControlAtStartup"] = false
	got, warns = MigrateSettings(def)
	if _, ok := got.Claude.Settings["remoteControlAtStartup"]; ok || len(warns) != 1 || !maps.Equal(got.Claude.Settings, DefaultClaudeSettings()) {
		t.Fatalf("claude-default = %v, %v", got.Claude.Settings, warns)
	}
	for _, k := range []string{"autoUpdates", "disableRemoteControl", "remoteControlAtStartup",
		"skipDangerousModePermissionPrompt", "bypassPermissionsModeAccepted", "disableAutoMode"} {
		if !slices.Contains(ManagedPolicySettings(), k) {
			t.Errorf("ManagedPolicySettings lacks %q", k)
		}
	}
}
