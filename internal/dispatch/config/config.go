// Package config defines and loads the at-cove dispatch configuration: the tracker
// wiring and per-class dispatch kits. It is at-cove-agnostic — a class's kit
// is the only seam. The repo is not named here: it is resolved from each
// class's kit origin at dispatch time.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the parsed contents of an at-cove dispatch config file.
type Config struct {
	Tracker          TrackerConfig    `yaml:"tracker"`
	Classes          map[string]Class `yaml:"classes"`
	Concurrency      int              `yaml:"concurrency"`
	ReaperTimeout    string           `yaml:"reaper-timeout"`
	DispatchOverhead string           `yaml:"dispatch-overhead"` // build+boot+teardown margin added to a class's work timeout
}

// TrackerConfig wires at-cove dispatch to one tracker team.
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

// SecretRef is a resolver: Command's stdout is the value, produced in memory.
type SecretRef struct {
	Command []string `yaml:"command"`
}

// Class maps a handler class to how at-cove dispatch runs it.
type Class struct {
	Mode        string `yaml:"mode"`    // "autonomous" | "interactive"
	Kit         string `yaml:"kit"`     // path to the class's .at-cove kit (autonomous); relative resolves against the config dir
	Timeout     string `yaml:"timeout"` // Go duration; autonomous
	Concurrency int    `yaml:"concurrency"`
}

const defaultClassLabelPrefix = "class:"

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
	cfg, err := ParseConfig(data)
	if err != nil {
		return Config{}, err
	}
	// Resolve each class's kit relative to the config file's directory.
	base := filepath.Dir(path)
	for name, cl := range cfg.Classes {
		if cl.Kit != "" && !filepath.IsAbs(cl.Kit) {
			cl.Kit = filepath.Join(base, cl.Kit)
			cfg.Classes[name] = cl
		}
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Tracker.ClassLabelPrefix == "" {
		c.Tracker.ClassLabelPrefix = defaultClassLabelPrefix
	}
	if c.DispatchOverhead == "" {
		c.DispatchOverhead = "15m"
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
	if err := checkDuration("dispatch-overhead", c.DispatchOverhead); err != nil {
		return err
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("config: concurrency must be >= 1, got %d", c.Concurrency)
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
			if strings.TrimSpace(cl.Kit) == "" {
				return fmt.Errorf("config: classes[%q]: autonomous class requires a kit", name)
			}
			if err := checkDuration(fmt.Sprintf("classes[%q].timeout", name), cl.Timeout); err != nil {
				return err
			}
		case "interactive":
			if strings.TrimSpace(cl.Kit) != "" {
				return fmt.Errorf("config: classes[%q]: interactive class must not set a kit", name)
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
