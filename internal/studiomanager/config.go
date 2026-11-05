// Package studiomanager is the generic Studio Manager host runtime — config,
// the connect loop, serving Jam's ops — that the jam-colima binary runs with
// its Colima wiring.
package studiomanager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/colimacfg"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/legacynames"
)

// Config is the jam-colima config file (docs/usage/jam-colima.md).
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
	RuntimeAddr   string   `yaml:"runtime-addr"` // required: the Jam address studios dial
	JamHost       string   `yaml:"jam-host"`     // required: the Jam host name studios resolve
	Profile       string   `yaml:"profile"`      // the colima VM; default "default"
	Docker        bool     `yaml:"docker"`
	DNS           []string `yaml:"dns"`
	IdentityFile  string   `yaml:"identity-file"`   // default <dir>/id_ed25519
	KnownHostsDir string   `yaml:"known-hosts-dir"` // default <dir>/known_hosts.d
}

// DockerContext is the docker context of the profile's VM: colima for the
// default profile, else colima-<profile>. Every docker call is pinned to it.
func (c ColimaConfig) DockerContext() string { return colimacfg.ProfileContext(c.Profile) }

// dirName is the config directory's name under $XDG_CONFIG_HOME.
const dirName = "jam-colima"

// DefaultDir is $XDG_CONFIG_HOME/jam-colima, else $HOME/.config/jam-colima.
func DefaultDir(getenv func(string) string) string {
	return filepath.Join(configHome(getenv), dirName)
}

func configHome(getenv func(string) string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	return filepath.Join(getenv("HOME"), ".config")
}

// MigrateDir renames the config directory the binary used before jam-colima
// (legacynames.StudioManager, same parent) to DefaultDir when only the old one
// exists, so the config, token and identity key — and with it the image fingerprints — carry over. It
// returns the old path when it moved it, "" when there was nothing to do.
func MigrateDir(getenv func(string) string) (string, error) {
	to := DefaultDir(getenv)
	from := filepath.Join(configHome(getenv), legacynames.StudioManager)
	if _, err := os.Lstat(to); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return "", nil // the new dir exists (or cannot be checked: Load reports it)
	}
	if info, err := os.Stat(from); err != nil || !info.IsDir() {
		return "", nil
	}
	if err := os.Rename(from, to); err != nil {
		return "", fmt.Errorf("move config dir %s to %s: %w", from, to, err)
	}
	return from, nil
}

// errDockerContext refuses the key colima.profile replaced.
var errDockerContext = errors.New("colima.docker-context is replaced by colima.profile (the VM name; its docker context is colima-<profile>)")

// hasDockerContext reports whether raw still sets colima.docker-context. The
// strict decode would only say "field not found", so it is probed first.
func hasDockerContext(raw []byte) bool {
	var probe struct {
		Colima map[string]yaml.Node `yaml:"colima"`
	}
	if yaml.Unmarshal(raw, &probe) != nil {
		return false // the strict decode reports it
	}
	_, ok := probe.Colima["docker-context"]
	return ok
}

// Load reads path strictly, applies defaults, then validates.
func Load(path string, getenv func(string) string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	if hasDockerContext(raw) {
		return Config{}, fmt.Errorf("%s: %w", path, errDockerContext)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
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
	if c.Colima.Profile == "" {
		c.Colima.Profile = colimacfg.DefaultProfile
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
	if p := c.Colima.Profile; !colimacfg.ValidProfile(p) {
		return fmt.Errorf("colima.profile %q is not a valid colima profile name (letters, digits, '-', '_', '.')", p)
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
