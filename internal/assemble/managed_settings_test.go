package assemble

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Plugin enablement follows the model-spec (COV-242): the harness layer seeds
// and enables the spec's plugins, and the claude harness enables them per run.
// The sealed managed settings must not enable (or declare a marketplace for)
// any plugin — a managed enablement would make every image auto-install it
// through the egress proxy at runtime when its spec seeded none.
func TestManagedSettingsEnablesNoPlugins(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, ""); err != nil {
		t.Fatal(err)
	}
	raw := read(t, filepath.Join(buildDir, "image-files/etc/claude-code/managed-settings.json"))
	var ms map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &ms); err != nil {
		t.Fatalf("managed-settings.json is not valid JSON: %v", err)
	}
	for _, k := range []string{"enabledPlugins", "extraKnownMarketplaces"} {
		if _, ok := ms[k]; ok {
			t.Errorf("managed-settings.json must not set %q:\n%s", k, raw)
		}
	}
	if _, ok := ms["permissions"]; !ok {
		t.Error("managed-settings.json lost its other keys (only the plugin keys move out)")
	}
}
