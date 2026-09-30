package kit

import (
	"encoding/json"
	"strings"
	"testing"
)

// ConfigToJSON produces JSON keyed by config.yml field names (not Go field
// names), so ParseConfig reads it straight back — the parse-YAML / store-JSON
// contract. ConfigToYAML renders it back for a human.
func TestConfigCodecRoundTrip(t *testing.T) {
	src := "name: web\nimage:\n  allowed-domains:\n    - example.com\n    - github.com\n"
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseConfig(yaml): %v", err)
	}

	// store form: JSON, keyed by config.yml names
	j, err := ConfigToJSON(cfg)
	if err != nil {
		t.Fatalf("ConfigToJSON: %v", err)
	}
	if !json.Valid(j) {
		t.Fatalf("ConfigToJSON is not valid JSON: %s", j)
	}
	if !strings.Contains(string(j), `"name":"web"`) || !strings.Contains(string(j), `"allowed-domains"`) {
		t.Fatalf("JSON must use config.yml keys: %s", j)
	}

	// stored JSON reads straight back via the YAML parser (JSON ⊂ YAML)
	back, err := ParseConfig(j)
	if err != nil {
		t.Fatalf("ParseConfig(json): %v", err)
	}
	if back.Name != "web" || len(back.Image.AllowedDomains) != 2 || back.Image.AllowedDomains[0] != "example.com" {
		t.Fatalf("round-trip lost data: %+v", back)
	}

	// determinism (safe to content-hash)
	j2, _ := ConfigToJSON(cfg)
	if string(j) != string(j2) {
		t.Fatalf("ConfigToJSON not deterministic:\n%s\n%s", j, j2)
	}

	// display: YAML for the human
	y, err := ConfigToYAML(cfg)
	if err != nil {
		t.Fatalf("ConfigToYAML: %v", err)
	}
	if !strings.Contains(string(y), "name: web") || !strings.Contains(string(y), "allowed-domains:") {
		t.Fatalf("ConfigToYAML not YAML: %s", y)
	}
}
