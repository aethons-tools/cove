// Package studiomanager holds the at-studio-manager binary's logic.
package studiomanager

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/colimacfg"
	"github.com/aethons-tools/cove/internal/jam"
)

// Config is the at-studio-manager config file.
type Config struct {
	Name       string       `yaml:"name"`        // required; jam.ValidManagerName, not "local"
	JamAddr    string       `yaml:"jam-addr"`    // required host:port of Jam's cove-facing TLS listener
	ServerName string       `yaml:"server-name"` // optional TLS name override; default the jam-addr host
	CAFile     string       `yaml:"ca-file"`     // optional PEM added to the system roots
	TokenFile  string       `yaml:"token-file"`  // default <dir>/token
	Capacity   int          `yaml:"capacity"`    // max studios; 0 = unlimited
	LogLevel   string       `yaml:"log-level"`   // debug|info|warn|error; default info
	Colima     ColimaConfig `yaml:"colima"`
}

// ColimaConfig configures the Colima runtime.
type ColimaConfig struct {
	RuntimeAddr   string   `yaml:"runtime-addr"`   // required: the Jam address studios dial
	JamHost       string   `yaml:"jam-host"`       // required: the Jam host name studios resolve
	DockerContext string   `yaml:"docker-context"` // "" = colima
	Docker        bool     `yaml:"docker"`
	DNS           []string `yaml:"dns"`
	IdentityFile  string   `yaml:"identity-file"`   // default <dir>/id_ed25519
	KnownHostsDir string   `yaml:"known-hosts-dir"` // default <dir>/known_hosts.d
}

// DefaultDir is $XDG_CONFIG_HOME/at-studio-manager, else $HOME/.config/at-studio-manager.
func DefaultDir(getenv func(string) string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-studio-manager")
	}
	return filepath.Join(getenv("HOME"), ".config", "at-studio-manager")
}

// Load reads path strictly, applies defaults, then validates.
func Load(path string, getenv func(string) string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	dir := DefaultDir(getenv)
	if c.TokenFile == "" {
		c.TokenFile = filepath.Join(dir, "token")
	}
	if c.Colima.IdentityFile == "" {
		c.Colima.IdentityFile = filepath.Join(dir, "id_ed25519")
	}
	if c.Colima.KnownHostsDir == "" {
		c.Colima.KnownHostsDir = filepath.Join(dir, "known_hosts.d")
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c Config) validate() error {
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if err := jam.ValidManagerName(c.Name); err != nil {
		return fmt.Errorf("name: %w", err)
	}
	if c.JamAddr == "" {
		return fmt.Errorf("jam-addr is required")
	}
	if h, p, err := net.SplitHostPort(c.JamAddr); err != nil || h == "" || p == "" {
		return fmt.Errorf("jam-addr %q must be host:port", c.JamAddr)
	}
	if c.Capacity < 0 {
		return fmt.Errorf("capacity must not be negative")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log-level %q must be debug, info, warn or error", c.LogLevel)
	}
	if c.Colima.RuntimeAddr == "" {
		return fmt.Errorf("colima.runtime-addr is required")
	}
	if c.Colima.JamHost == "" {
		return fmt.Errorf("colima.jam-host is required")
	}
	if dc := c.Colima.DockerContext; dc != "" && !colimacfg.ValidDockerContext(dc) {
		return fmt.Errorf("colima.docker-context %q is not a valid docker context name", dc)
	}
	return nil
}

// Token reads TokenFile and trims whitespace; empty is an error.
func (c Config) Token() (string, error) {
	b, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("token file %s is empty", c.TokenFile)
	}
	return t, nil
}
