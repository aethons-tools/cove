package assemble

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Every plugin enabled in managed-settings.json must have its marketplace
// declared there too, so the seeded known_marketplaces.json entry is
// declaratively backed and the reconciler will not prune it.
func TestManagedSettingsDeclaresMarketplaceForEnabledPlugins(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), Egress{}, ""); err != nil {
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
