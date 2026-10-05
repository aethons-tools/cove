package assemble

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
)

// Claude Code's managed settings are Claude-specific, so they live in the
// HARNESS layer (COV-245), not the sealed hardening layer: the assembled build
// context stages them in the harness context dir and the harness stage — not
// a hardening step — installs them at /etc/claude-code/managed-settings.json.
// The hardening embed carries no managed settings at all.
func TestManagedSettingsInHarnessStageNotHardening(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, "", harnessinstall.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "image-files/etc/claude-code/managed-settings.json")); err == nil {
		t.Fatal("the hardening image-files still carry managed-settings.json")
	}
	if _, err := fs.Stat(hardeningFS, "hardening/image-files/etc/claude-code/managed-settings.json"); err == nil {
		t.Fatal("the hardening embed still carries managed-settings.json")
	}
	raw := read(t, filepath.Join(buildDir, harnessinstall.ContextDir, "managed-settings.json"))
	var ms map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &ms); err != nil {
		t.Fatalf("managed-settings.json is not valid JSON: %v", err)
	}
	// Plugin enablement follows the model-spec (COV-242): a managed enablement
	// would make every image auto-install it through the egress proxy at
	// runtime when its spec seeded none. Preferences are the spec's (COV-245).
	for _, k := range []string{"enabledPlugins", "extraKnownMarketplaces", "theme", "forceLoginMethod"} {
		if _, ok := ms[k]; ok {
			t.Errorf("managed-settings.json must not set %q:\n%s", k, raw)
		}
	}
	df := read(t, filepath.Join(buildDir, "Dockerfile"))
	install := strings.Index(df, "/etc/claude-code/managed-settings.json")
	if install < 0 || install > strings.Index(df, "FROM harness\n") {
		t.Fatalf("the harness stage (before FROM harness) must install the managed settings:\n%s", df)
	}
	if strings.Count(df, "managed-settings.json") != strings.Count(df[:strings.Index(df, "FROM harness\n")], "managed-settings.json") {
		t.Fatalf("the hardening stage must not touch managed settings:\n%s", df)
	}
}
