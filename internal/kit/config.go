package kit

import (
	"bytes"
	"fmt"
	"strings"
	"time"

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

// Loop declares a scheduled, unattended agent run for `at-cove loop`. Interval
// is a Go duration string (e.g. "5m"). Check exits 0 to trigger the agent run;
// Prompt is passed to `claude -p`. Setup, when set, overrides the kit-level
// setup for this loop's workspace; FreshWorkspace re-seeds the workspace before
// each trigger.
type Loop struct {
	Interval       string `yaml:"interval"`
	Check          string `yaml:"check"`
	Prompt         string `yaml:"prompt"`
	Setup          string `yaml:"setup"`
	FreshWorkspace bool   `yaml:"fresh-workspace"`
}

// ParsedInterval returns the loop's interval as a time.Duration. It assumes the
// config passed ParseConfig (which rejects unparseable or non-positive
// intervals), so a parse error is reported as a zero duration.
func (l Loop) ParsedInterval() time.Duration {
	d, _ := time.ParseDuration(l.Interval)
	return d
}

// ImageConfig declares additive, build-time customisations of the sandbox image.
// cove translates each field to the correct sealed mechanism; every field is
// additive to the hardened baseline and never overrides it.
type ImageConfig struct {
	SetupScript    []string          `yaml:"setup-script"`    // kit-relative scripts run as root at build, in place
	Paths          []string          `yaml:"paths"`           // appended to PATH in /etc/environment
	Env            map[string]string `yaml:"env"`             // KEY=VALUE written to /etc/environment
	AllowedDomains []string          `yaml:"allowed-domains"` // added to the squid egress allow-list
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name    string          `yaml:"name"`
	Backend string          `yaml:"backend"`
	Setup   string          `yaml:"setup"` // optional: command run once to populate an isolated workspace
	Secrets []Secret        `yaml:"secrets"`
	Loops   map[string]Loop `yaml:"loops"`
	Image   ImageConfig     `yaml:"image"`
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
	for name, lp := range cfg.Loops {
		d, err := time.ParseDuration(lp.Interval)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("config.yml: loops[%q]: interval must be a positive Go duration (e.g. 5m), got %q", name, lp.Interval)
		}
		if lp.Check == "" {
			return Config{}, fmt.Errorf("config.yml: loops[%q]: check is required", name)
		}
		if lp.Prompt == "" {
			return Config{}, fmt.Errorf("config.yml: loops[%q]: prompt is required", name)
		}
	}
	for i, s := range cfg.Image.SetupScript {
		if strings.TrimSpace(s) == "" {
			return Config{}, fmt.Errorf("config.yml: image.setup-script[%d]: must not be empty", i)
		}
	}
	for i, p := range cfg.Image.Paths {
		if strings.TrimSpace(p) == "" {
			return Config{}, fmt.Errorf("config.yml: image.paths[%d]: must not be empty", i)
		}
	}
	for k := range cfg.Image.Env {
		if strings.TrimSpace(k) == "" {
			return Config{}, fmt.Errorf("config.yml: image.env: keys must not be empty")
		}
	}
	for i, d := range cfg.Image.AllowedDomains {
		if strings.TrimSpace(d) == "" {
			return Config{}, fmt.Errorf("config.yml: image.allowed-domains[%d]: must not be empty", i)
		}
	}
	return cfg, nil
}
