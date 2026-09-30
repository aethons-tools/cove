package kit

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// The kit registry follows the config rule of engagement: human-input config is
// parsed as YAML (JSON is valid YAML, so both are accepted), stored and used as
// JSON, and rendered back as YAML for display. ConfigToJSON is the stored form;
// ParseConfig reads it back (a YAML parser accepts JSON and legacy YAML alike);
// ConfigToYAML is the human display.

// ConfigToJSON encodes cfg as the registry's canonical stored form: JSON keyed by
// config.yml field names (not Go field names), so ParseConfig reads it straight
// back. It routes through the YAML tags (yaml.Marshal → generic map → JSON) so the
// keys match what ParseConfig expects. json.Marshal sorts map keys, so the output
// is deterministic — safe to content-hash.
func ConfigToJSON(cfg Config) ([]byte, error) {
	y, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("kit config to json: marshal yaml: %w", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(y, &m); err != nil {
		return nil, fmt.Errorf("kit config to json: transcode: %w", err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("kit config to json: marshal json: %w", err)
	}
	return b, nil
}

// ConfigToYAML encodes cfg as config.yml text for display to a human.
func ConfigToYAML(cfg Config) ([]byte, error) { return yaml.Marshal(cfg) }
