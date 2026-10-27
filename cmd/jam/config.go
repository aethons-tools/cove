package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/colimacfg"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/preview"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/logging"
	"github.com/aethons-tools/cove/internal/ticketkey"
	"github.com/aethons-tools/cove/internal/tracker"
	"github.com/aethons-tools/cove/internal/wakeon"
	"gopkg.in/yaml.v3"
)

// credSpec is the YAML shape for one serve-config credentials: entry. The
// strategy (Command/Value) is the removed inline form, kept only so
// validateCredentials can refuse it; Exchange is the one setting a demand may
// carry — how Jam uses the supplied value (credExchangeGCP), never the value.
type credSpec struct {
	Command  []string `yaml:"command"`
	Value    string   `yaml:"value"`
	Exchange string   `yaml:"exchange"`
}

// credExchangeGCP marks a credential whose supplied value is a Google
// credentials JSON: the broker exchanges it for short-lived GCP access tokens
// (jam.GCPTokenResolver), and that token is what a destination applies.
const credExchangeGCP = "gcp"

// devIdentityConfig names the user dev-identity impersonates. The
// pre-registry form {project, human} is accepted as the user named human.
type devIdentityConfig struct {
	User              string `yaml:"user"`
	DeprecatedProject string `yaml:"project"`
	DeprecatedHuman   string `yaml:"human"`
}

// serveConfig is the on-disk config for `jam serve`.
type serveConfig struct {
	// DisplayName names this Jam in the admin UI: the title bar reads
	// "<name> Jam" (its brand). Optional; one line, at
	// most 64 characters, trimmed.
	DisplayName string `yaml:"display-name"`
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
	// DevIdentity — DEV ONLY: loopback browser requests to /app act as
	// this user with no login (see browserauth.DevIdentity). serve
	// refuses it unless admin-listen is loopback.
	DevIdentity *devIdentityConfig `yaml:"dev-identity"`
	TLS         tlsFiles           `yaml:"tls"`
	AdminTLS    tlsFiles           `yaml:"admin-tls"`
	// Store, IntercomLog and SessionEventsDir are REMOVED file backends
	// (Postgres-only Jam, docs/usage/jam/serve.md). They are kept only so
	// validateStorage can reject a config that still sets them.
	Store            string `yaml:"store"`
	IntercomLog      string `yaml:"intercom-log"`
	SessionEventsDir string `yaml:"session-events-dir"`
	// StateDir holds Jam's remaining file state (relay cursors, markers,
	// receipts — no Postgres equivalent yet). Empty → jamStateDir().
	StateDir string `yaml:"state-dir"`
	// SessionEventsRetention bounds how long session events are kept: "<N>d"
	// or a Go duration; empty keeps forever.
	SessionEventsRetention string `yaml:"session-events-retention"`
	// StorePostgres is required: Postgres is Jam's only store backend. The DB password is never inline — it is a
	// named credential resolved on the host in memory (see password-cred).
	StorePostgres *storePostgresConfig `yaml:"store-postgres"`
	// CredentialsFile is the protected supply file that resolves each demanded
	// credential's strategy (value/command/global/mint). Empty => the XDG default
	// (~/.config/jam/credentials.yml). Strategies never live in this serve
	// config — see docs/usage/jam/credentials.md.
	CredentialsFile string              `yaml:"credentials-file"`
	Credentials     map[string]credSpec `yaml:"credentials"`
	// Pool, when set, enables the subscription-OAuth account pool: the anthropic
	// destination's cred (cred-name) is resolved from the pool by cove identity,
	// coves are seeded in subscription mode, and a background refresher rotates
	// pool tokens. Absent ⇒ the anthropic destination keeps its configured
	// (x-api-key/federated) credential and coves launch in API-key mode.
	Pool *poolConfig `yaml:"pool"`

	// Metrics, when set, serves the operator-attention exposition at /metrics
	// on the broker listener, gated on a scrape token (a demanded credential).
	// See docs/usage/jam/monitoring.md.
	Metrics *metricsConfig `yaml:"metrics"`
	// SSH, when set, enables studio SSH collaboration: Jam's SSH user CA (an
	// ed25519 private key, a demanded credential) issues short-lived certs
	// that every raised studio's sshd trusts. Absent ⇒ SSH collaboration off.
	// See docs/usage/jam/serve.md.
	SSH *sshConfig `yaml:"ssh"`
	// Kits configures git access for kit sources: credentials by URL prefix.
	// Absent ⇒ only anonymous public repos can be imported. See
	// docs/usage/jam/serve.md.
	Kits *kitsConfig `yaml:"kits"`
	// Blobs enables attachments: an S3-compatible bucket (docs/usage/jam/attachments.md).
	Blobs *blobsConfig `yaml:"blobs"`
	// Previews, when set, serves studio previews at <name>--<label>.<domain>
	// (and the login host jam.<domain>) on the admin listener, with its own
	// wildcard cert picked by SNI. Absent ⇒ previews off. See
	// docs/usage/jam/previews.md.
	Previews     *previewsConfig `yaml:"previews"`
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
		StudioManagers    bool                 `yaml:"studio-managers"` // accept remote Studio Manager connections
		Requisitioner     *requisitionerConfig `yaml:"requisitioner"`
		// DeprecatedDispatcher is runtime.requisitioner's pre-rename name,
		// accepted for one release with a warning (both set is an error);
		// parseServeConfig folds it into Requisitioner and clears it.
		DeprecatedDispatcher *requisitionerConfig `yaml:"dispatcher"`
		Discord              *discordConfig       `yaml:"discord"`
		Push                 *pushConfig          `yaml:"push"`
		Wake                 *wakeConfig          `yaml:"wake"`
		// EscalationPollInterval is how often the escalation engine checks
		// waiting sessions; it wins over runtime.requisitioner's key.
		EscalationPollInterval string `yaml:"escalation-poll-interval"`
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
	// SessionWakeLimit is the agent-to-agent loop breaker's run: how many
	// session posts in a row a channel takes, since a person last posted
	// there, before session posts stop waking sessions in it. Unset = 8;
	// 0 = session posts never wake sessions.
	SessionWakeLimit *int `yaml:"session-wake-limit"`
}

// defaultSessionWakeLimit is runtime.wake.session-wake-limit's default.
const defaultSessionWakeLimit = 8

// sessionWakeLimit resolves runtime.wake.session-wake-limit (0: off).
func (c serveConfig) sessionWakeLimit() int {
	if c.Runtime.Wake != nil && c.Runtime.Wake.SessionWakeLimit != nil {
		return *c.Runtime.Wake.SessionWakeLimit
	}
	return defaultSessionWakeLimit
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

// alertmanagerURL is the configured Alertmanager's base URL, or "".
func alertmanagerURL(c serveConfig) string {
	if c.Metrics == nil {
		return ""
	}
	return c.Metrics.AlertmanagerURL
}

// displayName is the trimmed display-name ("" when unset).
func (c serveConfig) displayName() string { return strings.TrimSpace(c.DisplayName) }

// validateDisplayName refuses a display-name that spans lines, holds control,
// invisible or bidi-override characters (anything but printable text and plain
// spaces), or runs past 64 characters.
func (c serveConfig) validateDisplayName() error {
	n := c.displayName()
	if utf8.RuneCountInString(n) > 64 {
		return fmt.Errorf("display-name: at most 64 characters, got %d", utf8.RuneCountInString(n))
	}
	for _, r := range n {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("display-name: must be one line of printable text (no control, invisible or line-separator characters)")
		}
	}
	return nil
}

// validateWake checks runtime.wake's durations parse. A no-op when unset.
func (c serveConfig) validateWake() error {
	if v := c.Runtime.EscalationPollInterval; v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			return fmt.Errorf("runtime.escalation-poll-interval: want a positive duration, got %q", v)
		}
	}
	w := c.Runtime.Wake
	if w == nil {
		return nil
	}
	if w.SessionWakeLimit != nil && *w.SessionWakeLimit < 0 {
		return fmt.Errorf("runtime.wake.session-wake-limit: want 0 (off) or more, got %d", *w.SessionWakeLimit)
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
	// Connection names the discord connection the relay uses (its credential
	// is the connection's). Exclusive with the deprecated BotTokenCred.
	Connection string `yaml:"connection"`
	// BotTokenCred (deprecated: use connection) names a demanded credential;
	// it binds the implicit connection named "discord".
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

// metricsConfig enables /metrics. TokenCred names a demanded credential (the
// scrape token's value comes from the credentials file). AlertmanagerURL, when
// set, is linked from the admin UI's Health tab.
type metricsConfig struct {
	TokenCred       string `yaml:"token-cred"`
	AlertmanagerURL string `yaml:"alertmanager-url"`
}

// validateMetrics checks a set metrics block: token-cred is required and
// demanded; alertmanager-url, if set, is an http(s) URL.
func (c serveConfig) validateMetrics() error {
	m := c.Metrics
	if m == nil {
		return nil
	}
	if m.TokenCred == "" {
		return fmt.Errorf("metrics.token-cred is required")
	}
	if _, ok := c.Credentials[m.TokenCred]; !ok {
		return fmt.Errorf("metrics.token-cred %q is not a demanded credential", m.TokenCred)
	}
	if slices.Contains(c.gcpCredentials(), m.TokenCred) || (c.Pool != nil && m.TokenCred == c.Pool.CredName) {
		return fmt.Errorf("metrics.token-cred %q must be a plain static credential, not a gcp-exchange or pool credential", m.TokenCred)
	}
	if m.AlertmanagerURL != "" {
		u, err := url.Parse(m.AlertmanagerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("metrics.alertmanager-url %q must be an http(s) URL", m.AlertmanagerURL)
		}
	}
	return nil
}

// sshConfig enables studio SSH collaboration. CACred names a demanded
// credential whose value is Jam's ed25519 SSH CA private key.
type sshConfig struct {
	CACred string `yaml:"ca-cred"`
}

// validateSSH checks a set ssh block: ca-cred is required, demanded, and a
// plain static credential (the rules validateMetrics applies to token-cred).
func (c serveConfig) validateSSH() error {
	s := c.SSH
	if s == nil {
		return nil
	}
	if s.CACred == "" {
		return fmt.Errorf("ssh.ca-cred is required")
	}
	if _, ok := c.Credentials[s.CACred]; !ok {
		return fmt.Errorf("ssh.ca-cred %q is not a demanded credential", s.CACred)
	}
	if slices.Contains(c.gcpCredentials(), s.CACred) || (c.Pool != nil && s.CACred == c.Pool.CredName) {
		return fmt.Errorf("ssh.ca-cred %q must be a plain static credential, not a gcp-exchange or pool credential", s.CACred)
	}
	return nil
}

// blobsConfig is the attachments bucket: S3-compatible (MinIO, AWS S3, R2,
// GCS interop). The keys are demanded credentials, resolved in memory.
type blobsConfig struct {
	Endpoint      string `yaml:"endpoint"`
	Bucket        string `yaml:"bucket"`
	Region        string `yaml:"region"`
	PathStyle     bool   `yaml:"path-style"`
	MaxSize       string `yaml:"max-size"`
	AccessKeyCred string `yaml:"access-key-cred"`
	SecretKeyCred string `yaml:"secret-key-cred"`
}

// defaultBlobMax and maxBlobMax bound an attachment's size.
const (
	defaultBlobMax = 25 << 20
	maxBlobMax     = 1 << 30
)

// maxBytes is the per-file cap (max-size, default 25 MB). Call after
// validateBlobs.
func (b *blobsConfig) maxBytes() int64 {
	n, _ := parseByteSize(b.MaxSize)
	return n
}

// parseByteSize parses "25MB" (KB, MB, GB; binary multiples); "" is the default.
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return defaultBlobMax, nil
	}
	for suffix, mult := range map[string]int64{"KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30} {
		if num, ok := strings.CutSuffix(s, suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
			if err != nil || n <= 0 {
				break
			}
			if n > maxBlobMax/mult {
				return 0, fmt.Errorf("over 1GB")
			}
			return n * mult, nil
		}
	}
	return 0, fmt.Errorf("want a size like 25MB")
}

// validateBlobs checks a set blobs block: an http(s) endpoint with a host and
// no userinfo, a bucket, a size within bounds, and both keys demanded, plain
// static credentials (the rules validateSSH applies).
func (c serveConfig) validateBlobs() error {
	b := c.Blobs
	if b == nil {
		return nil
	}
	u, err := url.Parse(b.Endpoint)
	if b.Endpoint == "" || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("blobs.endpoint must be http(s)://host[:port]")
	}
	if b.Bucket == "" {
		return fmt.Errorf("blobs.bucket is required")
	}
	n, err := parseByteSize(b.MaxSize)
	if err != nil {
		return fmt.Errorf("blobs.max-size: %w", err)
	}
	if n > maxBlobMax {
		return fmt.Errorf("blobs.max-size is over 1GB")
	}
	for key, cred := range map[string]string{"access-key-cred": b.AccessKeyCred, "secret-key-cred": b.SecretKeyCred} {
		switch {
		case cred == "":
			return fmt.Errorf("blobs.%s is required", key)
		case !c.demands(cred):
			return fmt.Errorf("blobs.%s %q is not a demanded credential", key, cred)
		case slices.Contains(c.gcpCredentials(), cred) || (c.Pool != nil && cred == c.Pool.CredName):
			return fmt.Errorf("blobs.%s %q must be a plain static credential, not a gcp-exchange or pool credential", key, cred)
		}
	}
	return nil
}

func (c serveConfig) demands(cred string) bool {
	_, ok := c.Credentials[cred]
	return ok
}

// kitsConfig configures kit git sources.
type kitsConfig struct {
	Git []kitGitCred `yaml:"git"`
}

// kitGitCred supplies a credential for the git repos under URL: a demanded
// credential whose value is `token` or `user:token` (the longest matching
// prefix wins).
type kitGitCred struct {
	URL  string `yaml:"url"`
	Cred string `yaml:"cred"`
}

// validateKits checks the kits block: each url is a valid repo-URL prefix
// (gitsource.ValidatePrefix: https or loopback http, trailing '/', no userinfo,
// dotted host or loopback), unique, and each cred is demanded and a plain
// static credential (the rules validateSSH applies to ca-cred). Errors name the
// entry by index, never echoing a url.
func (c serveConfig) validateKits() error {
	if c.Kits == nil {
		return nil
	}
	seen := map[string]bool{}
	for i, g := range c.Kits.Git {
		if g.URL == "" {
			return fmt.Errorf("kits.git[%d].url is required", i)
		}
		if err := gitsource.ValidatePrefix(g.URL); err != nil {
			return fmt.Errorf("kits.git[%d].url: %w", i, err)
		}
		key := g.URL
		if u, err := url.Parse(g.URL); err == nil {
			key = strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.Path
		}
		if seen[key] {
			return fmt.Errorf("kits.git[%d].url duplicates an earlier entry", i)
		}
		seen[key] = true
		if g.Cred == "" {
			return fmt.Errorf("kits.git[%d].cred is required", i)
		}
		if _, ok := c.Credentials[g.Cred]; !ok {
			return fmt.Errorf("kits.git[%d].cred %q is not a demanded credential", i, g.Cred)
		}
		if slices.Contains(c.gcpCredentials(), g.Cred) || (c.Pool != nil && g.Cred == c.Pool.CredName) {
			return fmt.Errorf("kits.git[%d].cred %q must be a plain static credential, not a gcp-exchange or pool credential", i, g.Cred)
		}
	}
	return nil
}

// tlsFiles names a PEM certificate (chain) and its private key on disk.
type tlsFiles struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// previewsConfig enables studio previews under Domain, served with TLS's
// certificate (which must cover *.<domain>).
type previewsConfig struct {
	Domain string    `yaml:"domain"`
	TLS    *tlsFiles `yaml:"tls"`
}

// dnsLabelRE is one lowercase DNS label (LDH, no leading/trailing '-').
var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validatePreviews checks a set previews block: domain is a lowercase DNS name
// of at least two labels; tls.cert/tls.key are set; the admin listener serves
// TLS (previews ride it); and browser login is configured (viewers sign in).
func (c serveConfig) validatePreviews() error {
	p := c.Previews
	if p == nil {
		return nil
	}
	labels := strings.Split(p.Domain, ".")
	if len(labels) < 2 || len(p.Domain) > 253 || slices.ContainsFunc(labels, func(l string) bool { return !dnsLabelRE.MatchString(l) }) {
		return fmt.Errorf("previews.domain %q must be a lowercase DNS name of at least two labels (e.g. preview.jam.example)", p.Domain)
	}
	if p.TLS == nil || p.TLS.Cert == "" || p.TLS.Key == "" {
		return fmt.Errorf("previews.tls.cert and previews.tls.key are required (a certificate covering *.%s)", p.Domain)
	}
	if c.AdminListen == "" || !c.adminUsesTLS() {
		return fmt.Errorf("previews requires the admin listener on TLS (set admin-listen and admin-tls)")
	}
	if c.browserAuthConfig() == nil {
		return fmt.Errorf("previews requires browser login (set operator-auth.oidc.browser-client-id)")
	}
	// The admin API must not be reached by a name under the domain: those
	// requests all go to the preview handler. Only names the config states
	// can be checked (an IP or wildcard admin-listen says nothing).
	if h, _, err := net.SplitHostPort(c.AdminListen); err == nil && h != "" && net.ParseIP(h) == nil && preview.UnderDomain(h, p.Domain) {
		return fmt.Errorf("admin-listen host %q is under previews.domain %q; serve the admin API at a name outside it", h, p.Domain)
	}
	for _, h := range c.UIHosts {
		if preview.UnderDomain(h, p.Domain) {
			return fmt.Errorf("ui-hosts entry %q is under previews.domain %q; use a name outside it", h, p.Domain)
		}
	}
	return nil
}

// credFixHint is the remedy a cred.unavailable condition shows for name.
func (c serveConfig) credFixHint(name string) string {
	switch {
	case slices.Contains(c.gcpCredentials(), name):
		return "if it supplies a user ADC: run `gcloud auth application-default login` on the Jam host (Jam re-reads it within 10s); otherwise replace the Google credentials JSON for " + name
	case c.Pool != nil && name == c.Pool.CredName:
		return "check the pool's accounts: `jam pool list --store " + c.Pool.Store + "`"
	}
	return "check the credentials file entry " + name + " (" + c.credentialsFilePath() + ")"
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
	// RuntimeAddr is the Jam Attach-gRPC address a raised cove's jam-studio-agent
	// dials (JAM_RUNTIME_ADDR), typically "<jam-host>:443".
	RuntimeAddr string `yaml:"runtime-addr"`
	// JamHost is the broker hostname injected as the connector base and
	// docker --add-host target, so a raised cove can reach Jam by name.
	JamHost string `yaml:"jam-host"`
	// DeprecatedHarborHost is jam-host's pre-rename name, accepted for one
	// release with a warning (both set is an error); parseServeConfig folds it
	// into JamHost and clears it.
	DeprecatedHarborHost string `yaml:"harbor-host"`
	// IdentityFile/KnownHostsDir are the SSH identity Jam uses to reach a
	// raised cove; serve generates the key on first use and bakes its public
	// half into every studio image. Default to the jam config dir's
	// id_ed25519 / known_hosts.d when empty (configDir; a pre-rename launcher
	// key is copied there by migrateDirs).
	IdentityFile  string   `yaml:"identity-file"`
	KnownHostsDir string   `yaml:"known-hosts-dir"`
	DNS           []string `yaml:"dns"`
	Docker        bool     `yaml:"docker"`
	// DockerContext is the docker context the launcher pins every docker call
	// to, selecting the colima instance its studios run in: colima-<profile>
	// for `colima start --profile <profile>`. Empty ⇒ colima (the default
	// profile).
	DockerContext string `yaml:"docker-context"`
}

// validateLauncher checks runtime.launcher when present (required fields:
// runtime-addr, jam-host) and defaults identity-file / known-hosts-dir to the
// jam config dir's id_ed25519 / known_hosts.d. A no-op when runtime.launcher
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
	if lc.DockerContext != "" && !colimacfg.ValidDockerContext(lc.DockerContext) {
		return fmt.Errorf("runtime.launcher.docker-context %q is not a valid docker context name", lc.DockerContext)
	}
	if lc.IdentityFile == "" {
		lc.IdentityFile = filepath.Join(configDir(), "id_ed25519")
	}
	if lc.KnownHostsDir == "" {
		lc.KnownHostsDir = filepath.Join(configDir(), "known_hosts.d")
	}
	return nil
}

// requisitionerConfig enables the Requisitioner: Jam polls the tracker and
// raises a managed cove per ready ticket, bounded by max-concurrent.
type requisitionerConfig struct {
	Role          string `yaml:"role"`
	Project       string `yaml:"project"`
	MaxConcurrent int    `yaml:"max-concurrent"`
	PollInterval  string `yaml:"poll-interval"` // optional; empty/invalid ⇒ the Requisitioner's 30s default
	// Connection names the tracker connection the Requisitioner, escalation
	// and the tracker relay use: kind linear or github, matching the block
	// set below. Exclusive with the deprecated TrackerTokenCred (Linear only),
	// which binds the implicit connection named "linear".
	Connection       string `yaml:"connection"`
	TrackerTokenCred string `yaml:"tracker-token-cred"`
	// DeprecatedTrackerToken detects the removed inline form (see discordConfig).
	DeprecatedTrackerToken *credSpec          `yaml:"tracker-token"`
	Linear                 *kit.LinearTracker `yaml:"linear"`
	// GitHub dispatches from one repo's Issues instead (exactly one of
	// linear or github): status labels for states, done = closed.
	GitHub *kit.GitHubTracker `yaml:"github"`

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

// trackerKind is the kind of the Requisitioner's tracker (and of its
// connection and relay service).
func (d *requisitionerConfig) trackerKind() string {
	if d.GitHub != nil {
		return tracker.KindGitHub
	}
	return tracker.KindLinear
}

// pollInterval is the Requisitioner's own poll-interval, else (GitHub only)
// its tracker block's; "" ⇒ the Requisitioner's 30s default. A Linear
// block's poll-interval stays unread, as before GitHub.
func (d *requisitionerConfig) pollInterval() string {
	if d.PollInterval == "" && d.GitHub != nil {
		return d.GitHub.PollInterval
	}
	return d.PollInterval
}

const removedStorageHint = "Jam is Postgres-only: set store-postgres instead. To keep an existing file Jam's config, run `jam export` against it (on the old version) and `jam import` into the Postgres Jam (docs/usage/jam/backup.md); squawk history and session events in the old files are not migrated"

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

// jamStateDir is $XDG_STATE_HOME/jam, else ~/.local/state/jam.
func jamStateDir() (string, error) {
	s := xdgStateHome()
	if s == "" {
		return "", errStateDirNotAbsolute
	}
	return filepath.Join(s, "jam"), nil
}

// stateDir is where Jam keeps its remaining file state (relay cursors,
// markers, receipts; the jam rename's studio-migration record). It must resolve to an absolute path.
func (c serveConfig) stateDir() (string, error) {
	dir := c.StateDir
	if dir == "" {
		var err error
		if dir, err = jamStateDir(); err != nil {
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
// role, max-concurrent > 0, exactly one of linear or github). A no-op when runtime.requisitioner is unset —
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
	if (d.Linear == nil) == (d.GitHub == nil) {
		return errors.New("runtime.requisitioner: set exactly one of linear or github")
	}
	if d.DeprecatedTrackerToken != nil {
		return fmt.Errorf("runtime.requisitioner.tracker-token is no longer inline — set runtime.requisitioner.tracker-token-cred: <name> and %s", credentialsFileHint)
	}
	if g := d.GitHub; g != nil {
		if err := ticketkey.ValidRepo(strings.TrimSpace(g.Repo)); err != nil { // as NewJam uses it
			return fmt.Errorf("runtime.requisitioner.github.repo: %w", err)
		}
		for _, s := range []struct{ name, label string }{
			{"ready", g.States.Ready}, {"in-progress", g.States.InProgress}, {"in-review", g.States.InReview},
			{"needs-input", g.States.NeedsInput}, {"blocked", g.States.Blocked},
		} { // done = the issue is closed: no label
			if strings.TrimSpace(s.label) == "" {
				return fmt.Errorf("runtime.requisitioner.github.states.%s is required", s.name)
			}
		}
		if d.TrackerTokenCred != "" {
			return fmt.Errorf("runtime.requisitioner.tracker-token-cred is Linear-only; set runtime.requisitioner.connection to a github connection")
		}
	}
	return checkConnectionOrCred(c, "runtime.requisitioner", d.Connection, "tracker-token-cred", d.TrackerTokenCred)
}

// checkConnectionOrCred checks a block's exactly-one-of `connection` / its
// deprecated credential key (which must name a demanded credential).
func checkConnectionOrCred(c serveConfig, field, conn, credKey, cred string) error {
	switch {
	case conn != "" && cred != "":
		return fmt.Errorf("%s: both connection and %s are set; %s is deprecated — keep only connection", field, credKey, credKey)
	case conn == "" && cred == "":
		return fmt.Errorf("%s.connection is required", field)
	case cred != "":
		if _, ok := c.Credentials[cred]; !ok {
			return fmt.Errorf("%s.%s %q is not a demanded credential", field, credKey, cred)
		}
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
	return checkConnectionOrCred(c, "runtime.discord", d.Connection, "bot-token-cred", d.BotTokenCred)
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
	// The login asks for openid (the ID token names the roster user) and the
	// operator scope (the access token makes the viewer an admin).
	return &browserauth.RawConfig{Issuer: o.Issuer, ClientID: o.BrowserClientID, Scope: withScopes(scope, "openid", o.RequireScope), Audience: o.Audience}
}

// browserLoginAdminsEveryone reports whether browser login is configured with
// no require-scope: any access token for the audience then verifies, so every
// signed-in user (Inbox members included) is an admin.
func (c serveConfig) browserLoginAdminsEveryone() bool {
	return c.browserAuthConfig() != nil && c.OperatorAuth.OIDC.RequireScope == ""
}

// withScopes adds each non-empty want missing from the space-separated scope:
// openid in front, the rest at the end.
func withScopes(scope string, want ...string) string {
	have := strings.Fields(scope)
	for _, w := range want {
		if w == "" || slices.Contains(have, w) {
			continue
		}
		if w == "openid" {
			have = append([]string{w}, have...)
		} else {
			have = append(have, w)
		}
	}
	return strings.Join(have, " ")
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
	if d := c.Runtime.Requisitioner; d != nil && d.TrackerTokenCred != "" && d.Connection == "" {
		c.deprecated = append(c.deprecated, [2]string{"runtime.requisitioner.tracker-token-cred", "runtime.requisitioner.connection"})
	}
	if d := c.Runtime.Discord; d != nil && d.BotTokenCred != "" && d.Connection == "" {
		c.deprecated = append(c.deprecated, [2]string{"runtime.discord.bot-token-cred", "runtime.discord.connection"})
	}
	if d := c.DevIdentity; d != nil {
		switch {
		case d.User != "" && (d.DeprecatedHuman != "" || d.DeprecatedProject != ""):
			return serveConfig{}, fmt.Errorf("dev-identity: both user and project/human are set; project/human is the deprecated form — keep only user")
		case d.DeprecatedHuman != "":
			if d.DeprecatedProject == "" {
				return serveConfig{}, fmt.Errorf("dev-identity: the deprecated human needs its project (or use user)")
			}
			c.deprecated = append(c.deprecated, [2]string{"dev-identity.project/human", "dev-identity.user"})
		case d.User == "":
			return serveConfig{}, fmt.Errorf("dev-identity: user is required")
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
const credentialsFileHint = "supply its strategy in the jam credentials file (see docs/usage/jam/credentials.md)"

// validateCredentials enforces the demand/supply split: a serve-config
// credentials: entry names a credential only; an inline command:/value: (the old
// form) is a hard error pointing at the credentials file. An exchange must be a
// known one.
func (c serveConfig) validateCredentials() error {
	for _, name := range c.demandedCredentials() {
		cs := c.Credentials[name]
		if len(cs.Command) > 0 || cs.Value != "" {
			return fmt.Errorf("credentials.%s: an inline command/value is no longer allowed — list the name only and %s", name, credentialsFileHint)
		}
		switch cs.Exchange {
		case "":
		case credExchangeGCP:
			if c.Pool != nil && name == c.Pool.CredName {
				return fmt.Errorf("credentials.%s: the pool's credential cannot also be exchange: %s", name, credExchangeGCP)
			}
		default:
			return fmt.Errorf("credentials.%s: exchange %q is not supported (want %s)", name, cs.Exchange, credExchangeGCP)
		}
	}
	return nil
}

// gcpCredentials is the sorted set of demanded credentials with exchange: gcp.
func (c serveConfig) gcpCredentials() []string {
	var out []string
	for _, name := range c.demandedCredentials() {
		if c.Credentials[name].Exchange == credExchangeGCP {
			out = append(out, name)
		}
	}
	return out
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
// when set, else the XDG default ~/.config/jam/credentials.yml.
func (c serveConfig) credentialsFilePath() string {
	if c.CredentialsFile != "" {
		return c.CredentialsFile
	}
	return filepath.Join(configDir(), "credentials.yml")
}

// escalationPollInterval resolves the escalation engine's poll interval:
// runtime.escalation-poll-interval > runtime.requisitioner's key > 0 (the
// engine default). An unparsable value is the default.
func (c serveConfig) escalationPollInterval() time.Duration {
	v := c.Runtime.EscalationPollInterval
	if v == "" && c.Runtime.Requisitioner != nil {
		v = c.Runtime.Requisitioner.EscalationPollInterval
	}
	d, _ := time.ParseDuration(v)
	return d
}
