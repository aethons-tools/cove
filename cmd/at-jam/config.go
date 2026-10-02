package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/wakeon"
	"gopkg.in/yaml.v3"
)

// credSpec is the YAML shape for one Jam credential: either a resolver
// command or a literal value (dev only).
type credSpec struct {
	Command []string `yaml:"command"`
	Value   string   `yaml:"value"`
}

// devIdentityConfig names the roster human dev-identity impersonates.
type devIdentityConfig struct {
	Project string `yaml:"project"`
	Human   string `yaml:"human"`
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
	// UIOrigins are extra exact origins (scheme://host[:port]) the browser UI's
	// CSRF write check accepts besides the request's own Host — e.g. the
	// `just dev-watch` proxy (http://localhost:8090) fronting the admin listener.
	UIOrigins []string `yaml:"ui-origins"`
	// DevIdentity — DEV ONLY: loopback browser requests to /ui and /me act as
	// this roster human with no login (see browserauth.DevIdentity). serve
	// refuses it unless admin-listen is loopback.
	DevIdentity *devIdentityConfig `yaml:"dev-identity"`
	TLS         struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	AdminTLS struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"admin-tls"`
	// Store, IntercomLog and SessionEventsDir are REMOVED file backends
	// (Postgres-only Jam, docs/usage/jam/serve.md). They are kept only so
	// validateStorage can reject a config that still sets them.
	Store            string `yaml:"store"`
	IntercomLog      string `yaml:"intercom-log"`
	SessionEventsDir string `yaml:"session-events-dir"`
	// StateDir holds Jam's remaining file state (relay cursors, markers,
	// receipts — no Postgres equivalent yet). Empty → atJamStateDir().
	StateDir string `yaml:"state-dir"`
	// SessionEventsRetention bounds how long session events are kept: "<N>d"
	// or a Go duration; empty keeps forever.
	SessionEventsRetention string `yaml:"session-events-retention"`
	// StorePostgres is required: Postgres is Jam's only store backend. The DB password is never inline — it is a
	// named credential resolved on the host in memory (see password-cred).
	StorePostgres *storePostgresConfig `yaml:"store-postgres"`
	// CredentialsFile is the protected supply file that resolves each demanded
	// credential's strategy (value/command/global/mint). Empty => the XDG default
	// (~/.config/at-jam/credentials.yml). Strategies never live in this serve
	// config — see docs/usage/jam/credentials.md.
	CredentialsFile string              `yaml:"credentials-file"`
	Credentials     map[string]credSpec `yaml:"credentials"`
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
// always runs (the Postgres message log is always present).
// Each field is optional and resolves runtime.wake > the matching runtime.requisitioner field
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

// validateSessionEvents checks session-events-retention parses.
func (c serveConfig) validateSessionEvents() error {
	_, err := sessionevents.ParseRetention(c.SessionEventsRetention)
	return err
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
	// BotTokenCred names a demanded credential; the token is supplied by the
	// at-jam credentials file, never inline here.
	BotTokenCred string `yaml:"bot-token-cred"`
	// DeprecatedBotToken detects the removed inline form; a set value is a hard
	// error pointing at the credentials file.
	DeprecatedBotToken *credSpec `yaml:"bot-token"`
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
// runtime-addr, jam-host) and defaults identity-file / known-hosts-dir to the
// at-cove config dir's id_ed25519 / known_hosts.d. A no-op when runtime.launcher
// is unset — the placeholder launcher stays in effect, unchanged from before
// this block existed.
func (c serveConfig) validateLauncher() error {
	lc := c.Runtime.Launcher
	if lc == nil {
		return nil
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
	Role             string `yaml:"role"`
	Project          string `yaml:"project"`
	MaxConcurrent    int    `yaml:"max-concurrent"`
	PollInterval     string `yaml:"poll-interval"` // optional; empty/invalid ⇒ the Requisitioner's 30s default
	TrackerTokenCred string `yaml:"tracker-token-cred"`
	// DeprecatedTrackerToken detects the removed inline form (see discordConfig).
	DeprecatedTrackerToken *credSpec          `yaml:"tracker-token"`
	Linear                 *kit.LinearTracker `yaml:"linear"`

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

const removedStorageHint = "Jam is Postgres-only: set store-postgres instead. To keep an existing file Jam's config, run `at-jam export` against it (on the old version) and `at-jam import` into the Postgres Jam (docs/usage/jam/backup.md); squawk history and session events in the old files are not migrated"

// validateStorage requires store-postgres and rejects the removed file-backend
// keys with a migration hint.
func (c serveConfig) validateStorage() error {
	for _, k := range []struct{ name, val string }{
		{"store", c.Store}, {"intercom-log", c.IntercomLog}, {"session-events-dir", c.SessionEventsDir},
	} {
		if k.val != "" {
			if c.StorePostgres != nil {
				return fmt.Errorf("%s is no longer supported; remove it — it was ignored because store-postgres is set, so no migration is needed", k.name)
			}
			return fmt.Errorf("%s is no longer supported. %s", k.name, removedStorageHint)
		}
	}
	if c.StorePostgres == nil {
		return fmt.Errorf("store-postgres is required (Jam is Postgres-only; see docs/usage/jam/serve.md)")
	}
	return nil
}

// errStateDirNotAbsolute is returned when no absolute state directory can be
// resolved, so relay files are never written relative to the working directory.
var errStateDirNotAbsolute = errors.New("state-dir: cannot resolve an absolute directory (set state-dir: in the serve config)")

// atJamStateDir is $XDG_STATE_HOME/at-jam, else ~/.local/state/at-jam.
func atJamStateDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "at-jam"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errStateDirNotAbsolute
	}
	return filepath.Join(home, ".local", "state", "at-jam"), nil
}

// stateDir is where Jam keeps its remaining file state (relay cursors,
// markers, receipts). It must resolve to an absolute path.
func (c serveConfig) stateDir() (string, error) {
	dir := c.StateDir
	if dir == "" {
		var err error
		if dir, err = atJamStateDir(); err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(dir) {
		return "", errStateDirNotAbsolute
	}
	return dir, nil
}

// relayStatePaths returns the relay cursor, marker and receipt files under dir.
func relayStatePaths(dir string) (cursors, markers, receipts string) {
	return filepath.Join(dir, "relay-cursors.json"), filepath.Join(dir, "relay-markers.json"), filepath.Join(dir, "relay-receipts.json")
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
// and password-cred names a configured credential. A no-op when unset (use
// validateStorage to require it).
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
	if d.DeprecatedTrackerToken != nil {
		return fmt.Errorf("runtime.requisitioner.tracker-token is no longer inline — set runtime.requisitioner.tracker-token-cred: <name> and %s", credentialsFileHint)
	}
	if d.TrackerTokenCred == "" {
		return fmt.Errorf("runtime.requisitioner.tracker-token-cred is required")
	}
	if _, ok := c.Credentials[d.TrackerTokenCred]; !ok {
		return fmt.Errorf("runtime.requisitioner.tracker-token-cred %q is not a demanded credential", d.TrackerTokenCred)
	}
	return nil
}

// validateDiscord checks runtime.discord when present (required: a non-empty
// bot-token-cred, naming a demanded credential). A no-op when runtime.discord is
// unset — the resident Discord relay engine stays disabled.
func (c serveConfig) validateDiscord() error {
	d := c.Runtime.Discord
	if d == nil {
		return nil
	}
	if d.DeprecatedBotToken != nil {
		return fmt.Errorf("runtime.discord.bot-token is no longer inline — set runtime.discord.bot-token-cred: <name> and %s", credentialsFileHint)
	}
	if d.BotTokenCred == "" {
		return fmt.Errorf("runtime.discord.bot-token-cred is required")
	}
	if _, ok := c.Credentials[d.BotTokenCred]; !ok {
		return fmt.Errorf("runtime.discord.bot-token-cred %q is not a demanded credential", d.BotTokenCred)
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
	if d := c.DevIdentity; d != nil {
		if d.Project == "" || d.Human == "" {
			return serveConfig{}, fmt.Errorf("dev-identity: project and human are both required")
		}
		if !isLoopbackAddr(c.AdminListen) {
			return serveConfig{}, fmt.Errorf("dev-identity: admin-listen %q is off-loopback; dev-identity skips login and is only allowed on a loopback admin listener", c.AdminListen)
		}
	}
	for _, o := range c.UIOrigins {
		if err := validateOrigin(o); err != nil {
			return serveConfig{}, fmt.Errorf("ui-origins: %w", err)
		}
	}
	return c, nil
}

// validateOrigin checks o is a bare web origin, scheme://host[:port] with an
// http(s) scheme and no path — the exact form a browser sends in Origin.
func validateOrigin(o string) error {
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("%q is not an origin (want scheme://host[:port], e.g. http://localhost:8090)", o)
	}
	return nil
}

// credConfigured reports whether n names a credential the broker can resolve: a
// `credentials:` entry, or (when the pool is enabled) the pool's cred-name, which
// the ChainResolver serves from the subscription account pool by identity rather
// than from `credentials:`. Used to validate a destination's cred_name at add time.
func (c serveConfig) credConfigured(n string) bool {
	if _, ok := c.Credentials[n]; ok {
		return true
	}
	return c.Pool != nil && n == c.Pool.CredName
}

// credentialsFileHint is the shared tail for every "an inline secret is no longer
// allowed here" error — it points the operator at the supply file + its doc.
const credentialsFileHint = "supply its strategy in the at-jam credentials file (see docs/usage/jam/credentials.md)"

// validateCredentials enforces the demand/supply split: a serve-config
// credentials: entry names a credential only; an inline command:/value: (the old
// form) is a hard error pointing at the credentials file.
func (c serveConfig) validateCredentials() error {
	for _, name := range c.demandedCredentials() {
		cs := c.Credentials[name]
		if len(cs.Command) > 0 || cs.Value != "" {
			return fmt.Errorf("credentials.%s: an inline command/value is no longer allowed — list the name only and %s", name, credentialsFileHint)
		}
	}
	return nil
}

// demandedCredentials is the sorted set of credential names the serve config
// demands (the credentials: keys). The supply file must resolve every one.
func (c serveConfig) demandedCredentials() []string {
	out := make([]string, 0, len(c.Credentials))
	for name := range c.Credentials {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// credNames lists every configured credential name — exactly what
// credConfigured accepts (the credentials: keys plus the pool's credential) —
// for the admin UI's type-ahead. Names only; never values.
func (c serveConfig) credNames() []string {
	out := c.demandedCredentials()
	if c.Pool != nil && c.Pool.CredName != "" && !slices.Contains(out, c.Pool.CredName) {
		out = append(out, c.Pool.CredName)
		sort.Strings(out)
	}
	return out
}

// credentialsFilePath is the supply file to load: the explicit credentials-file
// when set, else the XDG default ~/.config/at-jam/credentials.yml.
func (c serveConfig) credentialsFilePath() string {
	if c.CredentialsFile != "" {
		return c.CredentialsFile
	}
	return filepath.Join(atJamConfigDir(), "credentials.yml")
}

// atJamConfigDir mirrors atCoveConfigDir: $XDG_CONFIG_HOME/at-jam, else
// ~/.config/at-jam.
func atJamConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "at-jam")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "at-jam")
}
