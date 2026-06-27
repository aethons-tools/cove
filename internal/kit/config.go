package kit

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Secret declares an environment variable the sandbox needs and the host
// command that produces its value. Command is trusted today (pre-.local).
type Secret struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Command     []string `yaml:"command"`
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name    string   `yaml:"name"`
	Backend string   `yaml:"backend"`
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
		if len(s.Command) == 0 {
			return Config{}, fmt.Errorf("config.yml: secret %q: command is required", s.Name)
		}
	}
	return cfg, nil
}
