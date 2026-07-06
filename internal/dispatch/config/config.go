// Package config defines and loads the at-dispatch configuration: the tracker
// wiring, the repo, per-class dispatch commands, secrets, and the DISPATCH_*
// command contract. It is at-cove-agnostic — a class's command is the only seam.
package config

import (
	"bytes"
	"fmt"
	"os"

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
