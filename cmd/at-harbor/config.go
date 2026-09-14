package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/browserauth"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/secret"
	"gopkg.in/yaml.v3"
)

// credSpec is the YAML shape for one harbor credential: either a resolver
// command or a literal value (dev only).
type credSpec struct {
	Command []string `yaml:"command"`
	Value   string   `yaml:"value"`
}

// serveConfig is the on-disk config for `at-harbor serve`.
type serveConfig struct {
	Listen      string `yaml:"listen"`
	AdminListen string `yaml:"admin-listen"`
	// UIHosts are extra Host values accepted for the browser UI on a loopback
	// connection, beyond the loopback literals (127.0.0.1/::1/localhost). Set a
	// custom loopback-bound hostname here (e.g. harbor.local.example); otherwise
	// the UI refuses it, defeating DNS-rebinding attempts.
	UIHosts []string `yaml:"ui-hosts"`
	TLS     struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	AdminTLS struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"admin-tls"`
	Store string `yaml:"store"`
	// MessageLog is an optional path to the durable msglog JSONL file. When set,
	// `serve` opens it and the admin UI serves the read-only Messages view
	// (/ui/messages). Created on first open. Empty disables the view.
	MessageLog string `yaml:"message-log"`
	// StorePostgres, when set, selects the Postgres store backend and takes
	// precedence over the file `store`. The DB password is never inline — it is a
	// named credential resolved on the host in memory (see password-cred).
	StorePostgres *storePostgresConfig `yaml:"store-postgres"`
	Credentials   map[string]credSpec  `yaml:"credentials"`
	OperatorAuth  struct {
		OIDC *struct {
			Issuer          string `yaml:"issuer"`
			Audience        string `yaml:"audience"`
			RequireScope    string `yaml:"require-scope"`
			DeviceClientID  string `yaml:"device-client-id"`
			DeviceScope     string `yaml:"device-scope"`
			BrowserClientID string `yaml:"browser-client-id"`
			BrowserScope    string `yaml:"browser-scope"`
		} `yaml:"oidc"`
	} `yaml:"operator-auth"`
	Runtime struct {
		Listen            string            `yaml:"listen"`
		LeaseTTL          string            `yaml:"lease-ttl"`
		ReconcileInterval string            `yaml:"reconcile-interval"`
		Launcher          *launcherConfig   `yaml:"launcher"`
		Dispatcher        *dispatcherConfig `yaml:"dispatcher"`
	} `yaml:"runtime"`
}

// launcherConfig configures the real Colima-backed harbor.Launcher
// (internal/harbor/launcher). Present (non-nil) opts a `serve` process into
// raising real managed coves; absent keeps the placeholder launcher.
type launcherConfig struct {
	// InstallManifest is the host path to the at-cove install manifest
	// (install.Manifest JSON) whose Image/ImageDigest the launcher raises.
	InstallManifest string `yaml:"install-manifest"`
	// RuntimeAddr is the harbor Attach-gRPC address a raised cove's cove-master
	// dials (AT_HARBOR_RUNTIME_ADDR), typically "<harbor-host>:443".
	RuntimeAddr string `yaml:"runtime-addr"`
	// HarborHost is the broker hostname injected as the connector base and
	// docker --add-host target, so a raised cove can reach harbor by name.
	HarborHost string `yaml:"harbor-host"`
	// IdentityFile/KnownHostsDir are the SSH identity harbor uses to reach a
	// raised cove. They must be the same key `at-cove install` baked into the
	// image's authorized_keys. Default to the at-cove config dir's
	// id_ed25519 / known_hosts.d when empty, so a harbor host colocated with
	// at-cove needs no explicit path.
	IdentityFile  string   `yaml:"identity-file"`
	KnownHostsDir string   `yaml:"known-hosts-dir"`
	DNS           []string `yaml:"dns"`
	Docker        bool     `yaml:"docker"`
}

// validateLauncher checks runtime.launcher when present (required fields:
// install-manifest, runtime-addr, harbor-host) and defaults identity-file /
// known-hosts-dir to the at-cove config dir's id_ed25519 / known_hosts.d.
// A no-op when runtime.launcher is unset — the placeholder launcher stays in
// effect, unchanged from before this block existed.
func (c serveConfig) validateLauncher() error {
	lc := c.Runtime.Launcher
	if lc == nil {
		return nil
	}
	if lc.InstallManifest == "" {
		return fmt.Errorf("runtime.launcher.install-manifest is required")
	}
	if lc.RuntimeAddr == "" {
		return fmt.Errorf("runtime.launcher.runtime-addr is required")
	}
	if lc.HarborHost == "" {
		return fmt.Errorf("runtime.launcher.harbor-host is required")
	}
	if lc.IdentityFile == "" {
		lc.IdentityFile = filepath.Join(atCoveConfigDir(), "id_ed25519")
	}
	if lc.KnownHostsDir == "" {
		lc.KnownHostsDir = filepath.Join(atCoveConfigDir(), "known_hosts.d")
	}
	return nil
}

// dispatcherConfig enables the resident dispatcher: harbor polls the tracker and
// raises a managed cove per ready ticket, bounded by max-concurrent.
type dispatcherConfig struct {
	Role          string             `yaml:"role"`
	Project       string             `yaml:"project"`
	MaxConcurrent int                `yaml:"max-concurrent"`
	PollInterval  string             `yaml:"poll-interval"` // optional; empty/invalid ⇒ the dispatcher's 30s default
	TrackerToken  credSpec           `yaml:"tracker-token"`
	Linear        *kit.LinearTracker `yaml:"linear"`

	// WakePollInterval, WaitMax, and WarmTimeout configure the resident wake-on
	// engine (internal/wakeon), which watches Waiting instances' tickets and
	// wakes, idles (pauses), or tears them down. All optional; empty ⇒ the
	// engine's own defaults. EscalationPollInterval configures the resident
	// escalation engine (internal/escalate), which pings ordered human tiers of
	// a Waiting instance's Project escalation policy on per-tier timers. Also
	// optional; empty ⇒ the engine's own default.
	WakePollInterval       string `yaml:"wake-poll-interval"`
	WaitMax                string `yaml:"wait-max"`
	WarmTimeout            string `yaml:"warm-timeout"`
	EscalationPollInterval string `yaml:"escalation-poll-interval"`
}

// storePostgresConfig selects and configures the Postgres store backend. The
// password is never inline: PasswordCred names an entry in `credentials`,
// resolved on the host in memory when serve assembles the DSN.
type storePostgresConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	Database     string `yaml:"database"`
	User         string `yaml:"user"`
	SSLMode      string `yaml:"sslmode"`
	PasswordCred string `yaml:"password-cred"`
}

// validateStorePostgres checks store-postgres when present: required fields set,
// and password-cred names a configured credential. A no-op when unset (the file
// backend is used).
func (c serveConfig) validateStorePostgres() error {
	p := c.StorePostgres
	if p == nil {
		return nil
	}
	for name, v := range map[string]string{"host": p.Host, "database": p.Database, "user": p.User, "sslmode": p.SSLMode, "password-cred": p.PasswordCred} {
		if v == "" {
			return fmt.Errorf("store-postgres.%s is required", name)
		}
	}
	if _, ok := c.Credentials[p.PasswordCred]; !ok {
		return fmt.Errorf("store-postgres.password-cred %q is not a configured credential", p.PasswordCred)
	}
	return nil
}

// toSpec converts this credential to a named secret.Spec (literal or command).
func (cs credSpec) toSpec(name string) secret.Spec {
	if cs.Value != "" {
		return secret.Spec{Name: name, Value: cs.Value, Literal: true}
	}
	return secret.Spec{Name: name, Command: cs.Command}
}

// validateDispatcher checks runtime.dispatcher when present (required fields:
// role, max-concurrent > 0, linear). A no-op when runtime.dispatcher is unset —
// the resident dispatcher stays disabled, unchanged from before this block existed.
func (c serveConfig) validateDispatcher() error {
	d := c.Runtime.Dispatcher
	if d == nil {
		return nil
	}
	if d.Role == "" {
		return fmt.Errorf("runtime.dispatcher.role is required")
	}
	if d.MaxConcurrent <= 0 {
		return fmt.Errorf("runtime.dispatcher.max-concurrent must be > 0")
	}
	if d.Linear == nil {
		return fmt.Errorf("runtime.dispatcher.linear is required")
	}
	return nil
}

// atCoveConfigDir mirrors at-cove's own configDir() (cmd/at-cove/main.go):
// $XDG_CONFIG_HOME/at-cove, else ~/.config/at-cove. Duplicated rather than
// imported — at-cove's configDir is unexported in a different `main` package
// — so a harbor host colocated with `at-cove install` shares its identity key
// and known_hosts without extra config.
func atCoveConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-cove")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-cove")
}

// operatorLoginConfig builds the public device-flow client config harbor
// advertises at /admin/login-config, or nil when device login isn't configured
// (no device-client-id). Scope defaults to "openid".
func (c serveConfig) operatorLoginConfig() *harbor.OperatorLoginConfig {
	o := c.OperatorAuth.OIDC
	if o == nil || o.DeviceClientID == "" {
		return nil
	}
	scope := o.DeviceScope
	if scope == "" {
		scope = "openid"
	}
	return &harbor.OperatorLoginConfig{Issuer: o.Issuer, Audience: o.Audience, ClientID: o.DeviceClientID, Scope: scope}
}

// browserAuthConfig builds the browser (Authorization Code + PKCE) login config
// when OIDC and a browser-client-id are set; nil disables browser login (the
// off-loopback UI is then refused, loopback still works). Scope defaults to
// "openid profile email".
func (c serveConfig) browserAuthConfig() *browserauth.RawConfig {
	o := c.OperatorAuth.OIDC
	if o == nil || o.BrowserClientID == "" {
		return nil
	}
	scope := o.BrowserScope
	if scope == "" {
		scope = "openid profile email"
	}
	return &browserauth.RawConfig{Issuer: o.Issuer, ClientID: o.BrowserClientID, Scope: scope, Audience: o.Audience}
}

// adminTLS resolves the admin listener's cert/key: admin-tls if set, else the
// top-level tls. ok is false when neither is configured.
func (c serveConfig) adminTLS() (cert, key string, ok bool) {
	if c.AdminTLS.Cert != "" && c.AdminTLS.Key != "" {
		return c.AdminTLS.Cert, c.AdminTLS.Key, true
	}
	if c.TLS.Cert != "" && c.TLS.Key != "" {
		return c.TLS.Cert, c.TLS.Key, true
	}
	return "", "", false
}

// adminUsesTLS reports whether to serve the admin API over TLS: off-loopback
// (required by validateAdminExposure) or an explicit admin-tls block (opt-in on
// loopback). Reusing the broker's tls: does not upgrade a loopback listener.
func (c serveConfig) adminUsesTLS() bool {
	if !isLoopbackAddr(c.AdminListen) {
		return true
	}
	return c.AdminTLS.Cert != "" && c.AdminTLS.Key != ""
}

// validateAdminExposure refuses an off-loopback admin listener that lacks TLS or
// OIDC — either would expose the admin API to token interception or no real auth.
func (c serveConfig) validateAdminExposure() error {
	if c.AdminListen == "" || isLoopbackAddr(c.AdminListen) {
		return nil
	}
	if _, _, ok := c.adminTLS(); !ok {
		return fmt.Errorf("admin-listen %q is off-loopback but no TLS is configured (set `tls` or `admin-tls`)", c.AdminListen)
	}
	if c.OperatorAuth.OIDC == nil {
		return fmt.Errorf("admin-listen %q is off-loopback but operator auth is loopback-only (configure `operator-auth.oidc`)", c.AdminListen)
	}
	return nil
}

// isLoopbackAddr reports whether a host:port listen address binds only loopback.
// Empty host, 0.0.0.0 and :: are all-interfaces (non-loopback).
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serveConfigKeys is the set of recognized top-level YAML keys, derived from
// serveConfig's yaml tags so it can't drift as fields are added.
func serveConfigKeys() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(serveConfig{})
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// unknownServeKeys returns the top-level keys in the harbor.yml that serveConfig
// does not recognize, sorted. The bootstrap config is parsed leniently (unknown
// keys are silently dropped), so a stray `destinations:` block — natural to write
// but now managed via the admin API — would otherwise vanish without a word.
func unknownServeKeys(data []byte) []string {
	var m map[string]any
	if yaml.Unmarshal(data, &m) != nil {
		return nil // a genuine parse error surfaces from parseServeConfig instead
	}
	known := serveConfigKeys()
	var out []string
	for k := range m {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// runtimeDurations resolves the supervisor's lease-ttl and reconcile-interval,
// defaulting to 60s and 30s. reconcile-interval must be strictly less than
// lease-ttl, so a live owner always renews before its own lease expires.
func (c serveConfig) runtimeDurations() (ttl, reconcile time.Duration, err error) {
	ttl, reconcile = 60*time.Second, 30*time.Second
	if c.Runtime.LeaseTTL != "" {
		if ttl, err = time.ParseDuration(c.Runtime.LeaseTTL); err != nil {
			return 0, 0, fmt.Errorf("runtime.lease-ttl: %w", err)
		}
	}
	if c.Runtime.ReconcileInterval != "" {
		if reconcile, err = time.ParseDuration(c.Runtime.ReconcileInterval); err != nil {
			return 0, 0, fmt.Errorf("runtime.reconcile-interval: %w", err)
		}
	}
	if reconcile >= ttl {
		return 0, 0, fmt.Errorf("runtime.reconcile-interval (%s) must be less than lease-ttl (%s)", reconcile, ttl)
	}
	return ttl, reconcile, nil
}

// parseServeConfig parses the serve config YAML.
func parseServeConfig(data []byte) (serveConfig, error) {
	var c serveConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return serveConfig{}, err
	}
	return c, nil
}

// credSpecs maps each configured credential to a secret.Spec (literal or command).
func (c serveConfig) credSpecs() map[string]secret.Spec {
	out := make(map[string]secret.Spec, len(c.Credentials))
	for name, cs := range c.Credentials {
		if cs.Value != "" {
			out[name] = secret.Spec{Value: cs.Value, Literal: true}
		} else {
			out[name] = secret.Spec{Command: cs.Command}
		}
	}
	return out
}
