package kit

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SecretConfig configures how a declared secret (keyed by its env var name in the
// secrets map) resolves. Command, when present, is the host argv whose stdout is the
// value; when omitted the value is supplied from ~/.config/at-cove/secrets.yml.
type SecretConfig struct {
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

// DispatchConfig declares how `at-cove dispatch` performs a unit of work: the command
// run inside the VM, and the VM-side paths where at-cove injects the task file and reads
// the result. Input/Output are absolute VM paths under the worker's .at-work/ dir; the
// dispatch command must run with a cwd such that at-work reads/writes the same files
// (see kits/reference-worker for the reference wiring).
type DispatchConfig struct {
	Command []string `yaml:"command"`
	Input   string   `yaml:"input"`  // VM path at-cove writes the task file to, e.g. /home/agent/work/.at-work/task.json
	Output  string   `yaml:"output"` // VM path at-cove reads the task-result from, e.g. /home/agent/work/.at-work/task-result.json
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name     string                  `yaml:"name"`
	Backend  string                  `yaml:"backend"`
	Setup    string                  `yaml:"setup"` // optional: command run once to populate an isolated workspace
	Secrets  map[string]SecretConfig `yaml:"secrets"`
	Loops    map[string]Loop         `yaml:"loops"`
	Image    ImageConfig             `yaml:"image"`
	Dispatch DispatchConfig          `yaml:"dispatch"`
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
	for name := range cfg.Secrets {
		if strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("config.yml: secrets: a secret name (map key) must not be empty")
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
	return cfg, nil
}
