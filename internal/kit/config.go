package kit

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Secret declares an environment variable the sandbox needs. Command is
// optional: when omitted, the secret is a demand to be supplied by the user's
// ~/.config/at-cove/secrets.yml at connect time (or it warns and is left unset).
// When present, Command is the host argv that produces the value (trusted today,
// pre-.local).
type Secret struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Command     []string `yaml:"command"`
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name    string   `yaml:"name"`
	Backend string   `yaml:"backend"`
	Setup   string   `yaml:"setup"` // optional: command run once to populate an isolated workspace
	Secrets []Secret `yaml:"secrets"`
}

// ParseConfig unmarshals and validates config.yml bytes. Unknown fields are
// rejected to catch typos early.
func ParseConfig(data []byte) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config.yml: %w", err)
	}
	if cfg.Name == "" {
		return Config{}, fmt.Errorf("config.yml: name is required")
	}
	if cfg.Backend == "" {
		return Config{}, fmt.Errorf("config.yml: backend is required")
	}
	for i, s := range cfg.Secrets {
		if s.Name == "" {
			return Config{}, fmt.Errorf("config.yml: secrets[%d]: name is required", i)
		}
	}
	return cfg, nil
}
