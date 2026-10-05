package modelspec

import (
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

func TestMigrateVersion(t *testing.T) {
	// An exact version is left alone.
	exact := Spec{Name: "x", Type: HarnessClaude, Version: "2.1.0", Claude: &Claude{Provider: "anthropic"}}
	if got, changed := MigrateVersion(exact); changed || got.Version != "2.1.0" || got.Claude.Plugins != nil {
		t.Fatalf("exact spec migrated: %+v", got)
	}
	// A legacy constraint moves to version-constraint; version gets the pin;
	// the image-wide plugins are kept explicitly.
	legacy := Spec{Name: "x", Type: HarnessClaude, Version: "2.x", Claude: &Claude{Provider: "anthropic"}}
	got, changed := MigrateVersion(legacy)
	if !changed || got.Version != DefaultClaudeVersion || got.VersionConstraint != "2.x" ||
		!slices.Equal(got.Claude.Plugins, DefaultClaudePlugins()) {
		t.Fatalf("legacy migration = %+v (%+v)", got, got.Claude)
	}
	if legacy.Claude.Plugins != nil {
		t.Fatal("MigrateVersion mutated its input's claude body")
	}
	// Declared plugins are kept as they are.
	withPlugins := Spec{Name: "x", Type: HarnessClaude, Version: "*", Claude: &Claude{Provider: "anthropic", Plugins: []string{"a@claude-plugins-official"}}}
	if got, _ := MigrateVersion(withPlugins); !slices.Equal(got.Claude.Plugins, []string{"a@claude-plugins-official"}) || got.VersionConstraint != "*" {
		t.Fatalf("declared plugins changed: %+v", got)
	}
	// The untouched claude-default seed becomes exactly a fresh seed.
	seed := Default("pool")
	seed.Version, seed.Claude.Plugins = LegacyDefaultVersion, nil
	got, changed = MigrateVersion(seed)
	if fresh := Default("pool"); !changed || got.Version != fresh.Version || got.VersionConstraint != "" || !slices.Equal(got.Claude.Plugins, fresh.Claude.Plugins) {
		t.Fatalf("legacy seed migration = %+v", got)
	}
}
