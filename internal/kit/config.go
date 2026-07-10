package kit

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretConfig configures how a declared secret (keyed by its env var name in the
// secrets map) resolves. Command, when present, is the host argv whose stdout is the
// value; when omitted the value is supplied from ~/.config/at-cove/secrets.yml.
type SecretConfig struct {
	Description string   `yaml:"description"`
	Command     []string `yaml:"command"`
}

// baseEnvKeys are the /etc/environment variables the sealed hardening layer
// owns. A kit's image.env may not set these: overriding them would breach the
// additive guarantee (e.g. an image.env PATH would, since pam_env is last-wins,
// produce a second PATH= line and clobber the base PATH; a proxy var would
// weaken the egress gate). Keep in sync with the Dockerfile's /etc/environment
// block.
var baseEnvKeys = map[string]bool{
	"PATH":              true,
	"CLAUDE_CONFIG_DIR": true,
	"http_proxy":        true,
	"https_proxy":       true,
	"HTTP_PROXY":        true,
	"HTTPS_PROXY":       true,
	"no_proxy":          true,
	"NO_PROXY":          true,
}

// ImageConfig declares additive, build-time customisations of the sandbox image.
// cove translates each field to the correct sealed mechanism; every field is
// additive to the hardened baseline and never overrides it.
type ImageConfig struct {
	SetupScripts   []string          `yaml:"setup-scripts"`   // kit-relative scripts run as root at build, in place
	Paths          []string          `yaml:"paths"`           // appended to PATH in /etc/environment
	Env            map[string]string `yaml:"env"`             // KEY=VALUE written to /etc/environment
	AllowedDomains []string          `yaml:"allowed-domains"` // added to the squid egress allow-list
}

// Worker declares a dispatch worker class: the prompt at-cove sends the agent when
// at-cove dispatch runs this class. at-cove wraps it in the standard at-work bracket.
type Worker struct {
	Prompt string `yaml:"prompt"`
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name    string                  `yaml:"name"`
	Secrets map[string]SecretConfig `yaml:"secrets"`
	Image   ImageConfig             `yaml:"image"`
	Workers map[string]Worker       `yaml:"workers"`
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
	for name := range cfg.Secrets {
		if strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("config.yml: secrets: a secret name (map key) must not be empty")
		}
	}
	for i, s := range cfg.Image.SetupScripts {
		if strings.TrimSpace(s) == "" {
			return Config{}, fmt.Errorf("config.yml: image.setup-scripts[%d]: must not be empty", i)
		}
	}
	for i, p := range cfg.Image.Paths {
		if strings.TrimSpace(p) == "" {
			return Config{}, fmt.Errorf("config.yml: image.paths[%d]: must not be empty", i)
		}
		if strings.Contains(p, "\n") {
			return Config{}, fmt.Errorf("config.yml: image.paths[%d]: must not contain a newline", i)
		}
	}
	for k, v := range cfg.Image.Env {
		if strings.TrimSpace(k) == "" {
			return Config{}, fmt.Errorf("config.yml: image.env: keys must not be empty")
		}
		if strings.ContainsAny(k, "=\n") {
			return Config{}, fmt.Errorf("config.yml: image.env: key %q must not contain '=' or a newline", k)
		}
		if baseEnvKeys[k] {
			return Config{}, fmt.Errorf("config.yml: image.env: %q is owned by the base image and cannot be overridden", k)
		}
		if strings.Contains(v, "\n") {
			return Config{}, fmt.Errorf("config.yml: image.env: value for %q must not contain a newline", k)
		}
	}
	for i, d := range cfg.Image.AllowedDomains {
		if strings.TrimSpace(d) == "" {
			return Config{}, fmt.Errorf("config.yml: image.allowed-domains[%d]: must not be empty", i)
		}
	}
	for class, w := range cfg.Workers {
		if strings.TrimSpace(class) == "" {
			return Config{}, fmt.Errorf("config.yml: workers: a class name (map key) must not be empty")
		}
		if strings.TrimSpace(w.Prompt) == "" {
			return Config{}, fmt.Errorf("config.yml: workers[%q]: prompt is required", class)
		}
	}
	return cfg, nil
}
