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

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/wakeon"
	"gopkg.in/yaml.v3"
)

// credSpec is the YAML shape for one Jam credential: either a resolver
// command or a literal value (dev only).
type credSpec struct {
	Command []string `yaml:"command"`
	Value   string   `yaml:"value"`
}

// serveConfig is the on-disk config for `at-jam serve`.
type serveConfig struct {
	Listen      string `yaml:"listen"`
	AdminListen string `yaml:"admin-listen"`
	// UIHosts are extra Host values accepted for the browser UI on a loopback
	// connection, beyond the loopback literals (127.0.0.1/::1/localhost). Set a
	// custom loopback-bound hostname here (e.g. jam.local.example); otherwise
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
	// IntercomLog is an optional path to the durable intercom JSONL log file. When set,
	// `serve` opens it and the admin UI serves the read-only Intercom view
	// (/ui/intercom). Created on first open. Empty disables the view.
	IntercomLog string `yaml:"intercom-log"`
	// StorePostgres, when set, selects the Postgres store backend and takes
	// precedence over the file `store`. The DB password is never inline — it is a
	// named credential resolved on the host in memory (see password-cred).
	StorePostgres *storePostgresConfig `yaml:"store-postgres"`
	Credentials   map[string]credSpec  `yaml:"credentials"`
	// Pool, when set, enables the subscription-OAuth account pool: the anthropic
	// destination's cred (cred-name) is resolved from the pool by cove identity,
	// coves are seeded in subscription mode, and a background refresher rotates
	// pool tokens. Absent ⇒ the anthropic destination keeps its configured
	// (x-api-key/federated) credential and coves launch in API-key mode.
	Pool         *poolConfig `yaml:"pool"`
	OperatorAuth struct {
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
		Listen            string               `yaml:"listen"`
		LeaseTTL          string               `yaml:"lease-ttl"`
		ReconcileInterval string               `yaml:"reconcile-interval"`
		Launcher          *launcherConfig      `yaml:"launcher"`
		Requisitioner     *requisitionerConfig `yaml:"requisitioner"`
		// DeprecatedDispatcher is runtime.requisitioner's pre-rename name,
		// accepted for one release with a warning (both set is an error);
		// parseServeConfig folds it into Requisitioner and clears it.
		DeprecatedDispatcher *requisitionerConfig `yaml:"dispatcher"`
		Discord              *discordConfig       `yaml:"discord"`
		Wake                 *wakeConfig          `yaml:"wake"`
	} `yaml:"runtime"`

	// deprecated lists the {old, new} key pairs parseServeConfig folded from a
	// deprecated alias (Harbor → Jam rename); serve warns once for each. See
	// docs/usage/jam/renamed-from-harbor.md.
	deprecated [][2]string
}

// wakeConfig configures the resident wake-on engine (internal/wakeon), which
// runs whenever Jam has an intercom log or a Requisitioner. Each field is
// optional and resolves runtime.wake > the matching runtime.requisitioner field
// (wake-poll-interval / wait-max / warm-timeout) > the engine default.
type wakeConfig struct {
	PollInterval string `yaml:"poll-interval"`
	WaitMax      string `yaml:"wait-max"`
	WarmTimeout  string `yaml:"warm-timeout"`
}

// wakeSettings resolves the wake-on engine's settings, per field:
// runtime.wake if set, else the runtime.requisitioner field, else zero (the
// engine's default). An invalid Requisitioner value also falls back to the
// default (its long-standing lenient behavior); runtime.wake values are
// checked by validateWake.
func (c serveConfig) wakeSettings() wakeon.Config {
	var w wakeConfig
	if c.Runtime.Wake != nil {
		w = *c.Runtime.Wake
	}
	var d requisitionerConfig
	if c.Runtime.Requisitioner != nil {
		d = *c.Runtime.Requisitioner
	}
	dur := func(s string) time.Duration {
		v, _ := time.ParseDuration(s) // "" or invalid → 0 → engine default
		return v
	}
	return wakeon.Config{
		PollInterval: dur(firstNonEmpty(w.PollInterval, d.WakePollInterval)),
		MaxWait:      dur(firstNonEmpty(w.WaitMax, d.WaitMax)),
		WarmTimeout:  dur(firstNonEmpty(w.WarmTimeout, d.WarmTimeout)),
	}
}

// validateWake checks runtime.wake's durations parse. A no-op when unset.
func (c serveConfig) validateWake() error {
	w := c.Runtime.Wake
	if w == nil {
		return nil
	}
	for name, v := range map[string]string{"poll-interval": w.PollInterval, "wait-max": w.WaitMax, "warm-timeout": w.WarmTimeout} {
		if v == "" {
			continue
		}
		if _, err := time.ParseDuration(v); err != nil {
			return fmt.Errorf("runtime.wake.%s: %w", name, err)
		}
	}
	return nil
}

// discordConfig enables the resident Discord relay engine (egress this slice).
type discordConfig struct {
	BotToken credSpec `yaml:"bot-token"` // resolved on the host; never logged/injected
}

// launcherConfig configures the real Colima-backed jam.Launcher
// (internal/jam/launcher). Present (non-nil) opts a `serve` process into
// raising real managed coves; absent keeps the placeholder launcher.
// poolConfig configures the subscription-OAuth account pool. TokenURL, ClientID,
// and Scope are optional and default to the probed Claude Code constants
// (see jam.Refresher).
type poolConfig struct {
	Store           string `yaml:"store"`            // path to the pool JSON file (required)
	CredName        string `yaml:"cred-name"`        // the anthropic destination cred that routes to the pool (required)
	RefreshInterval string `yaml:"refresh-interval"` // ticker cadence; default 5m
	RefreshMargin   string `yaml:"refresh-margin"`   // refresh when within this of expiry; default 15m
	TokenURL        string `yaml:"token-url"`        // default jam.defaultTokenURL
	ClientID        string `yaml:"client-id"`        // default jam.defaultClientID
	Scope           string `yaml:"scope"`            // default jam.defaultScope
}

// validatePool checks a set pool block. Store and CredName are required; the
// endpoint/client/scope default in the refresher, and durations parse.
func (c serveConfig) validatePool() error {
	if c.Pool == nil {
		return nil
	}
	if c.Pool.Store == "" {
		return fmt.Errorf("pool: store is required")
	}
	if c.Pool.CredName == "" {
		return fmt.Errorf("pool: cred-name is required")
	}
	if _, _, err := c.poolDurations(); err != nil {
		return err
	}
	return nil
}

// poolDurations returns the refresh interval and margin, defaulting to 5m / 15m.
func (c serveConfig) poolDurations() (interval, margin time.Duration, err error) {
	interval, margin = 5*time.Minute, 15*time.Minute
	if c.Pool == nil {
		return interval, margin, nil
	}
	if s := c.Pool.RefreshInterval; s != "" {
		if interval, err = time.ParseDuration(s); err != nil {
			return 0, 0, fmt.Errorf("pool.refresh-interval: %w", err)
		}
	}
	if s := c.Pool.RefreshMargin; s != "" {
		if margin, err = time.ParseDuration(s); err != nil {
			return 0, 0, fmt.Errorf("pool.refresh-margin: %w", err)
		}
	}
	return interval, margin, nil
}

type launcherConfig struct {
	// InstallManifest is the host path to the at-cove install manifest
	// (install.Manifest JSON) whose Image/ImageDigest the launcher raises.
	InstallManifest string `yaml:"install-manifest"`
	// RuntimeAddr is the Jam Attach-gRPC address a raised cove's cove-master
	// dials (AT_JAM_RUNTIME_ADDR), typically "<jam-host>:443".
	RuntimeAddr string `yaml:"runtime-addr"`
	// JamHost is the broker hostname injected as the connector base and
	// docker --add-host target, so a raised cove can reach Jam by name.
	JamHost string `yaml:"jam-host"`
	// DeprecatedHarborHost is jam-host's pre-rename name, accepted for one
	// release with a warning (both set is an error); parseServeConfig folds it
	// into JamHost and clears it.
	DeprecatedHarborHost string `yaml:"harbor-host"`
	// IdentityFile/KnownHostsDir are the SSH identity Jam uses to reach a
	// raised cove. They must be the same key `at-cove install` baked into the
	// image's authorized_keys. Default to the at-cove config dir's
	// id_ed25519 / known_hosts.d when empty, so a Jam host colocated with
	// at-cove needs no explicit path.
	IdentityFile  string   `yaml:"identity-file"`
	KnownHostsDir string   `yaml:"known-hosts-dir"`
	DNS           []string `yaml:"dns"`
	Docker        bool     `yaml:"docker"`
}

// validateLauncher checks runtime.launcher when present (required fields:
// install-manifest, runtime-addr, jam-host) and defaults identity-file /
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
	if lc.JamHost == "" {
		return fmt.Errorf("runtime.launcher.jam-host is required")
	}
	if lc.IdentityFile == "" {
		lc.IdentityFile = filepath.Join(atCoveConfigDir(), "id_ed25519")
	}
	if lc.KnownHostsDir == "" {
		lc.KnownHostsDir = filepath.Join(atCoveConfigDir(), "known_hosts.d")
	}
	return nil
}

// requisitionerConfig enables the Requisitioner: Jam polls the tracker and
// raises a managed cove per ready ticket, bounded by max-concurrent.
type requisitionerConfig struct {
	Role          string             `yaml:"role"`
	Project       string             `yaml:"project"`
	MaxConcurrent int                `yaml:"max-concurrent"`
	PollInterval  string             `yaml:"poll-interval"` // optional; empty/invalid ⇒ the Requisitioner's 30s default
	TrackerToken  credSpec           `yaml:"tracker-token"`
	Linear        *kit.LinearTracker `yaml:"linear"`

	// WakePollInterval, WaitMax, and WarmTimeout configure the resident wake-on
	// engine (internal/wakeon), which watches Waiting instances' tickets and
	// wakes, idles (pauses), or tears them down. All optional, and now a
	// fallback: the matching runtime.wake field wins when set (see
	// wakeSettings); both empty ⇒ the engine's own defaults. EscalationPollInterval configures the resident
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

// validateRequisitioner checks runtime.requisitioner when present (required fields:
// role, max-concurrent > 0, linear). A no-op when runtime.requisitioner is unset —
// the Requisitioner stays disabled, unchanged from before this block existed.
func (c serveConfig) validateRequisitioner() error {
	d := c.Runtime.Requisitioner
	if d == nil {
		return nil
	}
	if d.Role == "" {
		return fmt.Errorf("runtime.requisitioner.role is required")
	}
	if d.MaxConcurrent <= 0 {
		return fmt.Errorf("runtime.requisitioner.max-concurrent must be > 0")
	}
	if d.Linear == nil {
		return fmt.Errorf("runtime.requisitioner.linear is required")
	}
	return nil
}

// validateDiscord checks runtime.discord when present (required: a non-empty
// bot-token, as a command or a literal value). A no-op when runtime.discord is
// unset — the resident Discord relay engine stays disabled.
func (c serveConfig) validateDiscord() error {
	d := c.Runtime.Discord
	if d == nil {
		return nil
	}
	if len(d.BotToken.Command) == 0 && d.BotToken.Value == "" {
		return fmt.Errorf("runtime.discord.bot-token is required")
	}
	return nil
}

// atCoveConfigDir mirrors at-cove's own configDir() (cmd/at-cove/main.go):
// $XDG_CONFIG_HOME/at-cove, else ~/.config/at-cove. Duplicated rather than
// imported — at-cove's configDir is unexported in a different `main` package
// — so a Jam host colocated with `at-cove install` shares its identity key
// and known_hosts without extra config.
func atCoveConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-cove")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-cove")
}

// operatorLoginConfig builds the public device-flow client config Jam
// advertises at /admin/login-config, or nil when device login isn't configured
// (no device-client-id). Scope defaults to "openid".
func (c serveConfig) operatorLoginConfig() *jam.OperatorLoginConfig {
	o := c.OperatorAuth.OIDC
	if o == nil || o.DeviceClientID == "" {
		return nil
	}
	scope := o.DeviceScope
	if scope == "" {
		scope = "openid"
	}
	return &jam.OperatorLoginConfig{Issuer: o.Issuer, Audience: o.Audience, ClientID: o.DeviceClientID, Scope: scope}
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
	return yamlKeys(reflect.TypeOf(serveConfig{}))
}

// runtimeConfigKeys is the set of recognized keys under `runtime:`.
func runtimeConfigKeys() map[string]bool {
	f, _ := reflect.TypeOf(serveConfig{}).FieldByName("Runtime")
	return yamlKeys(f.Type)
}

// yamlKeys returns the yaml tag names of struct type t's fields.
func yamlKeys(t reflect.Type) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// unknownServeKeys returns the top-level keys in the jam.yml that serveConfig
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
	// One level into runtime: too (reported dotted), so a typo'd runtime block
	// such as `runtime.wak` is flagged rather than silently ignored.
	if rt, ok := m["runtime"].(map[string]any); ok {
		rknown := runtimeConfigKeys()
		for k := range rt {
			if !rknown[k] {
				out = append(out, "runtime."+k)
			}
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

// parseServeConfig parses the serve config YAML, folding deprecated key
// aliases into their new names (recorded in c.deprecated for serve to warn
// about). A key set under both its old and new name is an error.
func parseServeConfig(data []byte) (serveConfig, error) {
	var c serveConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return serveConfig{}, err
	}
	if lc := c.Runtime.Launcher; lc != nil && lc.DeprecatedHarborHost != "" {
		if lc.JamHost != "" {
			return serveConfig{}, fmt.Errorf("runtime.launcher: both jam-host and harbor-host are set; harbor-host is the deprecated name for jam-host — keep only jam-host (see %s)", logging.RenameDoc)
		}
		lc.JamHost, lc.DeprecatedHarborHost = lc.DeprecatedHarborHost, ""
		c.deprecated = append(c.deprecated, [2]string{"runtime.launcher.harbor-host", "runtime.launcher.jam-host"})
	}
	if c.Runtime.DeprecatedDispatcher != nil {
		if c.Runtime.Requisitioner != nil {
			return serveConfig{}, fmt.Errorf("runtime: both requisitioner and dispatcher are set; dispatcher is the deprecated name for requisitioner — keep only requisitioner (see %s)", logging.RenameDoc)
		}
		c.Runtime.Requisitioner, c.Runtime.DeprecatedDispatcher = c.Runtime.DeprecatedDispatcher, nil
		c.deprecated = append(c.deprecated, [2]string{"runtime.dispatcher", "runtime.requisitioner"})
	}
	return c, nil
}

// credSpecs maps each configured credential to a secret.Spec (literal or command).
func (c serveConfig) credSpecs() map[string]secret.Spec {
	out := make(map[string]secret.Spec, len(c.Credentials))
	for name, cs := range c.Credentials {
		// toSpec sets Name, which secret.Resolve keys its output map by; callers
		// that index the resolved map by credential name (e.g. the store-postgres
		// password path) get an empty value if Name is unset.
		out[name] = cs.toSpec(name)
	}
	return out
}
