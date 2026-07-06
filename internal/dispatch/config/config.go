// Package config defines and loads the at-dispatch configuration: the tracker
// wiring, the repo, per-class dispatch commands, secrets, and the DISPATCH_*
// command contract. It is at-cove-agnostic — a class's command is the only seam.
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the parsed contents of an at-dispatch config file.
type Config struct {
	Tracker       TrackerConfig    `yaml:"tracker"`
	Repo          RepoConfig       `yaml:"repo"`
	Secrets       []Secret         `yaml:"secrets"`
	Classes       map[string]Class `yaml:"classes"`
	Concurrency   int              `yaml:"concurrency"`
	ReaperTimeout string           `yaml:"reaper-timeout"`
}

// TrackerConfig wires at-dispatch to one tracker team.
type TrackerConfig struct {
	Provider         string    `yaml:"provider"`
	Team             string    `yaml:"team"`
	Token            SecretRef `yaml:"token"`
	WebhookSecret    SecretRef `yaml:"webhook-secret"`
	PollInterval     string    `yaml:"poll-interval"`
	States           StateMap  `yaml:"states"`
	ClassLabelPrefix string    `yaml:"class-label-prefix"`
}

// StateMap binds the design's lifecycle roles to a team's real state names.
type StateMap struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in-progress"`
	InReview   string `yaml:"in-review"`
	Done       string `yaml:"done"`
	NeedsInput string `yaml:"needs-input"`
	Blocked    string `yaml:"blocked"`
}

// RepoConfig names the single repo this instance serves.
type RepoConfig struct {
	Slug string `yaml:"slug"`
}

// SecretRef is a resolver: Command's stdout is the value, produced in memory.
type SecretRef struct {
	Command []string `yaml:"command"`
}

// Secret is a named resolver injected as env into every dispatch command.
type Secret struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
}

// Class maps a handler class to how at-dispatch runs it.
type Class struct {
	Mode        string   `yaml:"mode"`    // "autonomous" | "interactive"
	Command     []string `yaml:"command"` // required iff autonomous
	Timeout     string   `yaml:"timeout"` // Go duration; autonomous
	Concurrency int      `yaml:"concurrency"`
}

const defaultClassLabelPrefix = "class:"

// Env var names at-dispatch sets for every dispatch command.
const (
	EnvIssue   = "DISPATCH_ISSUE"
	EnvClass   = "DISPATCH_CLASS"
	EnvRepo    = "DISPATCH_REPO"
	EnvTimeout = "DISPATCH_TIMEOUT"
	EnvBrief   = "DISPATCH_BRIEF"
	EnvResult  = "DISPATCH_RESULT"
)

// reservedEnvNames are the env names at-dispatch owns; a secret may not use one.
var reservedEnvNames = map[string]bool{
	EnvIssue: true, EnvClass: true, EnvRepo: true,
	EnvTimeout: true, EnvBrief: true, EnvResult: true,
}

// ParseConfig strict-decodes config bytes and applies defaults. Validation is
// applied here too once Validate exists (see Validate).
func ParseConfig(data []byte) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadConfig reads a config file and parses it.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return ParseConfig(data)
}

func (c *Config) applyDefaults() {
	if c.Tracker.ClassLabelPrefix == "" {
		c.Tracker.ClassLabelPrefix = defaultClassLabelPrefix
	}
}

// Validate checks the config for internal consistency. It is pure (no I/O).
func (c Config) Validate() error {
	if c.Tracker.Provider != "linear" {
		return fmt.Errorf("config: tracker.provider must be \"linear\", got %q", c.Tracker.Provider)
	}
	if c.Tracker.Team == "" {
		return fmt.Errorf("config: tracker.team is required")
	}
	if len(c.Tracker.Token.Command) == 0 {
		return fmt.Errorf("config: tracker.token.command is required")
	}
	if len(c.Tracker.WebhookSecret.Command) == 0 {
		return fmt.Errorf("config: tracker.webhook-secret.command is required")
	}
	roles := map[string]string{
		"states.ready": c.Tracker.States.Ready, "states.in-progress": c.Tracker.States.InProgress,
		"states.in-review": c.Tracker.States.InReview, "states.done": c.Tracker.States.Done,
		"states.needs-input": c.Tracker.States.NeedsInput, "states.blocked": c.Tracker.States.Blocked,
	}
	for name, v := range roles {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("config: tracker.%s is required", name)
		}
	}
	if err := checkDuration("tracker.poll-interval", c.Tracker.PollInterval); err != nil {
		return err
	}
	if err := checkDuration("reaper-timeout", c.ReaperTimeout); err != nil {
		return err
	}
	// Reachable only when Validate is called on a Config that skipped applyDefaults;
	// ParseConfig always fills this first.
	if c.Tracker.ClassLabelPrefix == "" {
		return fmt.Errorf("config: tracker.class-label-prefix must not be empty")
	}
	if parts := strings.Split(c.Repo.Slug, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("config: repo.slug must be \"owner/name\", got %q", c.Repo.Slug)
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("config: concurrency must be >= 1, got %d", c.Concurrency)
	}
	seen := map[string]bool{}
	for i, s := range c.Secrets {
		if s.Name == "" {
			return fmt.Errorf("config: secrets[%d].name is required", i)
		}
		if reservedEnvNames[s.Name] {
			return fmt.Errorf("config: secrets[%d].name %q is a reserved DISPATCH_* name", i, s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("config: secrets[%d].name %q is duplicated", i, s.Name)
		}
		seen[s.Name] = true
		if len(s.Command) == 0 {
			return fmt.Errorf("config: secrets[%d] (%s).command is required", i, s.Name)
		}
	}
	if len(c.Classes) == 0 {
		return fmt.Errorf("config: at least one class is required")
	}
	for name, cl := range c.Classes {
		if name == "" {
			return fmt.Errorf("config: a class name must not be empty")
		}
		switch cl.Mode {
		case "autonomous":
			if len(cl.Command) == 0 {
				return fmt.Errorf("config: classes[%q]: autonomous class requires a command", name)
			}
			if err := checkDuration(fmt.Sprintf("classes[%q].timeout", name), cl.Timeout); err != nil {
				return err
			}
		case "interactive":
			if len(cl.Command) != 0 {
				return fmt.Errorf("config: classes[%q]: interactive class must not set a command", name)
			}
		default:
			return fmt.Errorf("config: classes[%q].mode must be \"autonomous\" or \"interactive\", got %q", name, cl.Mode)
		}
		if cl.Concurrency < 0 {
			return fmt.Errorf("config: classes[%q].concurrency must be >= 0", name)
		}
	}
	return nil
}

func checkDuration(field, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("config: %s must be a positive Go duration (e.g. 30m), got %q", field, v)
	}
	return nil
}
