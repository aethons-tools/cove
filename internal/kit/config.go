package kit

import (
	"bytes"
	"errors"
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

const commonKey = "<common>"

// Worker declares an autonomous handler class: the role prompt at-cove sends the
// agent (own-only, required) plus scheduling attrs that may be inherited from the
// workers <common> base.
type Worker struct {
	Prompt      string `yaml:"prompt,omitempty"`
	Timeout     string `yaml:"timeout,omitempty"` // Go duration
	Concurrency int    `yaml:"concurrency,omitempty"`
}

// ResolvedWorker returns the named worker with the workers <common> base merged
// in (own overrides <common>; prompt is own-only). It errors if class is empty,
// the reserved <common> key, or absent.
func (c Config) ResolvedWorker(class string) (Worker, error) {
	if class == "" || class == commonKey {
		return Worker{}, fmt.Errorf("kit %q: %q is not a dispatchable worker class", c.Name, class)
	}
	own, ok := c.Workers[class]
	if !ok {
		return Worker{}, fmt.Errorf("kit %q declares no worker class %q", c.Name, class)
	}
	base := c.Workers[commonKey] // zero value if absent
	if own.Timeout == "" {
		own.Timeout = base.Timeout
	}
	if own.Concurrency == 0 {
		own.Concurrency = base.Concurrency
	}
	return own, nil
}

// SourceControl names the code host + repo the kit targets — a tagged union
// (exactly one host; github only today). It is the single source of the repo
// identity and the host kind.
type SourceControl struct {
	GitHub *GitHubSource `yaml:"github,omitempty"`
}

type GitHubSource struct {
	Project    string `yaml:"project"`               // "owner/name"
	MainBranch string `yaml:"main-branch,omitempty"` // base branch; default "main"
}

// Active returns the set host, or an error if not exactly one.
func (s *SourceControl) Active() (string, error) {
	n, name := 0, ""
	if s.GitHub != nil {
		n, name = n+1, "github"
	}
	if n != 1 {
		return "", errors.New("must set exactly one host (github)")
	}
	return name, nil
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name          string                  `yaml:"name"`
	Secrets       map[string]SecretConfig `yaml:"secrets"`
	Image         ImageConfig             `yaml:"image"`
	Workers       map[string]Worker       `yaml:"workers"`
	SourceControl *SourceControl          `yaml:"source-control,omitempty"`
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
		if isReservedAngleKey(class) {
			return Config{}, fmt.Errorf("config.yml: workers: %q is not a valid key (only %q is reserved)", class, commonKey)
		}
		if class == commonKey {
			// base: prompt not allowed; scalars validated below
			if strings.TrimSpace(w.Prompt) != "" {
				return Config{}, fmt.Errorf("config.yml: workers[%q]: the base must not set a prompt", commonKey)
			}
		} else if strings.TrimSpace(w.Prompt) == "" {
			return Config{}, fmt.Errorf("config.yml: workers[%q]: prompt is required", class)
		}
		if w.Timeout != "" {
			if err := checkKitDuration(fmt.Sprintf("workers[%q].timeout", class), w.Timeout); err != nil {
				return Config{}, err
			}
		}
		if w.Concurrency < 0 {
			return Config{}, fmt.Errorf("config.yml: workers[%q].concurrency must be >= 0", class)
		}
	}
	if cfg.SourceControl != nil {
		if _, err := cfg.SourceControl.Active(); err != nil {
			return Config{}, fmt.Errorf("config.yml: source-control: %w", err)
		}
		if gh := cfg.SourceControl.GitHub; gh != nil {
			if parts := strings.Split(gh.Project, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return Config{}, fmt.Errorf("config.yml: source-control.github.project must be \"owner/name\", got %q", gh.Project)
			}
			if gh.MainBranch == "" {
				gh.MainBranch = "main"
			}
		}
	}
	return cfg, nil
}

// isReservedAngleKey reports whether key is <…>-wrapped but not the one allowed
// reserved base key <common>.
func isReservedAngleKey(key string) bool {
	if !strings.HasPrefix(key, "<") || !strings.HasSuffix(key, ">") {
		return false
	}
	return key != commonKey
}

func checkKitDuration(field, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("config.yml: %s must be a positive Go duration (e.g. 30m), got %q", field, v)
	}
	return nil
}
