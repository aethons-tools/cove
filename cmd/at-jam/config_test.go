package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/wakeon"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

func TestUnknownServeKeys(t *testing.T) {
	// a clean config → no unknowns
	if got := unknownServeKeys([]byte("listen: \":8443\"\nadmin-listen: \"127.0.0.1:8081\"\nstore-postgres: {}\n")); len(got) != 0 {
		t.Fatalf("clean config unknowns = %v", got)
	}
	// a stray destinations block + a hyphen/underscore typo → both reported, sorted
	got := unknownServeKeys([]byte("listen: \":8443\"\ndestinations: [a]\nadmin_listen: x\n"))
	if want := []string{"admin_listen", "destinations"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unknowns = %v, want %v", got, want)
	}
	// every known key is accepted (guards the reflect-derived set against drift)
	known := "listen: a\nadmin-listen: b\ntls: {}\nadmin-tls: {}\nstore: s\ncredentials: {}\noperator-auth: {}\nintercom-log: m\nsession-events-dir: e\nstate-dir: d\nstore-postgres: {}\n"
	if got := unknownServeKeys([]byte(known)); len(got) != 0 {
		t.Fatalf("all-known config flagged: %v", got)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8081": true, "localhost:8081": true, "[::1]:8081": true,
		":8081": false, "0.0.0.0:8081": false, "10.0.0.5:8081": false, "jam.example:8081": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestAdminTLSAndUsesTLS(t *testing.T) {
	// only top-level tls → resolves it, but loopback stays plain
	c := serveConfig{AdminListen: "127.0.0.1:8081"}
	c.TLS.Cert, c.TLS.Key = "/t.pem", "/t.key"
	if cert, key, ok := c.adminTLS(); !ok || cert != "/t.pem" || key != "/t.key" {
		t.Fatalf("adminTLS = %q,%q,%v", cert, key, ok)
	}
	if c.adminUsesTLS() {
		t.Fatal("loopback with only top-level tls must stay plain HTTP")
	}
	// explicit admin-tls opts loopback into TLS and overrides the cert
	c.AdminTLS.Cert, c.AdminTLS.Key = "/a.pem", "/a.key"
	if cert, _, _ := c.adminTLS(); cert != "/a.pem" {
		t.Fatalf("admin-tls should override: cert=%q", cert)
	}
	if !c.adminUsesTLS() {
		t.Fatal("explicit admin-tls must enable TLS on loopback")
	}
	// off-loopback always uses TLS
	if !(serveConfig{AdminListen: "0.0.0.0:8081"}).adminUsesTLS() {
		t.Fatal("off-loopback must use TLS")
	}
	// neither cert → not ok
	if _, _, ok := (serveConfig{}).adminTLS(); ok {
		t.Fatal("no cert configured must be ok=false")
	}
}

func TestValidateAdminExposure(t *testing.T) {
	withTLS := func(c *serveConfig) { c.TLS.Cert, c.TLS.Key = "/t.pem", "/t.key" }
	withOIDC := func(c *serveConfig) {
		c.OperatorAuth.OIDC = &struct {
			Issuer          string `yaml:"issuer"`
			Audience        string `yaml:"audience"`
			RequireScope    string `yaml:"require-scope"`
			DeviceClientID  string `yaml:"device-client-id"`
			DeviceScope     string `yaml:"device-scope"`
			BrowserClientID string `yaml:"browser-client-id"`
			BrowserScope    string `yaml:"browser-scope"`
		}{Issuer: "i", Audience: "a"}
	}
	// loopback: always ok, even plain + loopback-auth (today's setup)
	if err := (serveConfig{AdminListen: "127.0.0.1:8081"}).validateAdminExposure(); err != nil {
		t.Fatalf("loopback plain should pass: %v", err)
	}
	// off-loopback, both TLS + OIDC → ok
	ok := serveConfig{AdminListen: "0.0.0.0:8081"}
	withTLS(&ok)
	withOIDC(&ok)
	if err := ok.validateAdminExposure(); err != nil {
		t.Fatalf("off-loopback+TLS+OIDC should pass: %v", err)
	}
	// off-loopback missing TLS → error
	noTLS := serveConfig{AdminListen: "0.0.0.0:8081"}
	withOIDC(&noTLS)
	if err := noTLS.validateAdminExposure(); err == nil {
		t.Fatal("off-loopback without TLS must fail")
	}
	// off-loopback missing OIDC → error
	noOIDC := serveConfig{AdminListen: "0.0.0.0:8081"}
	withTLS(&noOIDC)
	if err := noOIDC.validateAdminExposure(); err == nil {
		t.Fatal("off-loopback without OIDC must fail")
	}
}

func TestOperatorLoginConfig(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
operator-auth:
  oidc:
    issuer: https://acme.us.auth0.com/
    audience: https://jam.acme/api
    device-client-id: NativeClientId123
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	lc := cfg.operatorLoginConfig()
	if lc == nil || lc.ClientID != "NativeClientId123" || lc.Issuer != "https://acme.us.auth0.com/" ||
		lc.Audience != "https://jam.acme/api" || lc.Scope != "openid" {
		t.Fatalf("login config = %+v (want default scope openid)", lc)
	}

	// no device-client-id → no login config (login disabled)
	none, _ := parseServeConfig([]byte("operator-auth:\n  oidc:\n    issuer: x\n    audience: y\n"))
	if none.operatorLoginConfig() != nil {
		t.Fatal("expected nil login config without device-client-id")
	}
}

func TestParseServeConfigOIDC(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
listen: ":8443"
admin-listen: "127.0.0.1:8081"
store-postgres: {}
operator-auth:
  oidc:
    issuer: https://acme.us.auth0.com/
    audience: https://jam.acme/api
    require-scope: jam:admin
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.OperatorAuth.OIDC == nil || cfg.OperatorAuth.OIDC.Issuer != "https://acme.us.auth0.com/" ||
		cfg.OperatorAuth.OIDC.Audience != "https://jam.acme/api" || cfg.OperatorAuth.OIDC.RequireScope != "jam:admin" {
		t.Fatalf("oidc = %+v", cfg.OperatorAuth.OIDC)
	}
}

func TestParseServeConfig(t *testing.T) {
	yml := `
listen: ":8443"
admin-listen: "127.0.0.1:8081"
tls: { cert: /c.pem, key: /k.pem }
state-dir: /var/lib/jam/state
credentials:
  anthropic-key: {}
  git-pat: {}
`
	cfg, err := parseServeConfig([]byte(yml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Listen != ":8443" || cfg.AdminListen != "127.0.0.1:8081" || cfg.StateDir != "/var/lib/jam/state" {
		t.Fatalf("cfg = %+v", cfg)
	}
	// Credentials are name-only demands; values come from the credentials file.
	names := map[string]bool{}
	for _, n := range cfg.demandedCredentials() {
		names[n] = true
	}
	if !names["git-pat"] || !names["anthropic-key"] {
		t.Fatalf("demandedCredentials = %v, want git-pat and anthropic-key", names)
	}
}

func TestRuntimeDurationsDefaults(t *testing.T) {
	c, err := parseServeConfig([]byte("listen: \":443\"\nstate-dir: /tmp/s\n"))
	if err != nil {
		t.Fatal(err)
	}
	ttl, rec, err := c.runtimeDurations()
	if err != nil {
		t.Fatal(err)
	}
	if ttl != 60*time.Second || rec != 30*time.Second {
		t.Fatalf("defaults = %s / %s", ttl, rec)
	}
}

func TestRuntimeDurationsParsedAndValidated(t *testing.T) {
	c, err := parseServeConfig([]byte("runtime:\n  lease-ttl: 2m\n  reconcile-interval: 40s\n"))
	if err != nil {
		t.Fatal(err)
	}
	ttl, rec, err := c.runtimeDurations()
	if err != nil || ttl != 2*time.Minute || rec != 40*time.Second {
		t.Fatalf("parsed = %s / %s err=%v", ttl, rec, err)
	}

	bad, _ := parseServeConfig([]byte("runtime:\n  lease-ttl: 30s\n  reconcile-interval: 60s\n"))
	if _, _, err := bad.runtimeDurations(); err == nil {
		t.Fatal("expected error when reconcile-interval >= lease-ttl")
	}
}

func TestRuntimeIsAKnownServeKey(t *testing.T) {
	// runtime: must not be reported as an unknown key.
	if got := unknownServeKeys([]byte("runtime:\n  lease-ttl: 1m\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v, want none", got)
	}
}

func TestRuntimeListenParsed(t *testing.T) {
	c, err := parseServeConfig([]byte("runtime:\n  listen: 127.0.0.1:9090\n  lease-ttl: 1m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime.Listen != "127.0.0.1:9090" {
		t.Fatalf("runtime.listen = %q", c.Runtime.Listen)
	}
	// runtime is still a known key (no unknown-key warning).
	if got := unknownServeKeys([]byte("runtime:\n  listen: :9090\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v", got)
	}
}

func TestRuntimeLauncherParsed(t *testing.T) {
	c, err := parseServeConfig([]byte(`
runtime:
  launcher:
    runtime-addr: jam.example.com:443
    jam-host: jam.example.com
    identity-file: /etc/jam/id_ed25519
    known-hosts-dir: /etc/jam/known_hosts.d
    dns: ["1.1.1.1", "8.8.8.8"]
    docker: true
    docker-context: colima-jam-b
`))
	if err != nil {
		t.Fatal(err)
	}
	lc := c.Runtime.Launcher
	if lc == nil {
		t.Fatal("runtime.launcher did not parse")
	}
	if lc.RuntimeAddr != "jam.example.com:443" ||
		lc.JamHost != "jam.example.com" ||
		lc.IdentityFile != "/etc/jam/id_ed25519" ||
		lc.KnownHostsDir != "/etc/jam/known_hosts.d" ||
		!lc.Docker || lc.DockerContext != "colima-jam-b" ||
		len(lc.DNS) != 2 || lc.DNS[0] != "1.1.1.1" || lc.DNS[1] != "8.8.8.8" {
		t.Fatalf("launcher config = %+v", lc)
	}
	// runtime.launcher is a known key (no unknown-key warning).
	if got := unknownServeKeys([]byte("runtime:\n  launcher:\n    jam-host: h\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v", got)
	}
}

func TestValidateLauncherRequiredFields(t *testing.T) {
	// no launcher block at all → no error.
	if err := (serveConfig{}).validateLauncher(); err != nil {
		t.Fatalf("nil launcher should not error: %v", err)
	}
	base := func() *launcherConfig {
		return &launcherConfig{
			RuntimeAddr: "h:443",
			JamHost:     "h",
		}
	}
	// all required fields present → ok, and defaults get filled in.
	c := serveConfig{}
	c.Runtime.Launcher = base()
	if err := c.validateLauncher(); err != nil {
		t.Fatalf("complete launcher block should not error: %v", err)
	}
	if c.Runtime.Launcher.IdentityFile == "" || c.Runtime.Launcher.KnownHostsDir == "" {
		t.Fatalf("expected identity-file/known-hosts-dir to default, got %+v", c.Runtime.Launcher)
	}

	for field, mutate := range map[string]func(*launcherConfig){
		"runtime-addr": func(l *launcherConfig) { l.RuntimeAddr = "" },
		"jam-host":     func(l *launcherConfig) { l.JamHost = "" },
	} {
		bad := serveConfig{}
		bad.Runtime.Launcher = base()
		mutate(bad.Runtime.Launcher)
		if err := bad.validateLauncher(); err == nil {
			t.Fatalf("missing %s should error", field)
		}
	}
}

// install-manifest is retired (no shim): a launcher block with only runtime-addr
// and jam-host now validates.
func TestValidateLauncherNoLongerRequiresInstallManifest(t *testing.T) {
	c := serveConfig{}
	c.Runtime.Launcher = &launcherConfig{RuntimeAddr: "jam:443", JamHost: "jam"}
	if err := c.validateLauncher(); err != nil {
		t.Fatalf("launcher without install-manifest must validate: %v", err)
	}
}

func TestValidateLauncherDefaultsDontOverride(t *testing.T) {
	c := serveConfig{}
	c.Runtime.Launcher = &launcherConfig{
		RuntimeAddr: "h:443", JamHost: "h",
		IdentityFile: "/custom/id", KnownHostsDir: "/custom/kh",
	}
	if err := c.validateLauncher(); err != nil {
		t.Fatal(err)
	}
	if c.Runtime.Launcher.IdentityFile != "/custom/id" || c.Runtime.Launcher.KnownHostsDir != "/custom/kh" {
		t.Fatalf("explicit identity-file/known-hosts-dir must not be overridden: %+v", c.Runtime.Launcher)
	}
}

func TestRuntimeRequisitionerParsed(t *testing.T) {
	c, err := parseServeConfig([]byte(`
runtime:
  requisitioner:
    role: implementer
    project: cove
    max-concurrent: 3
    poll-interval: 45s
    wake-poll-interval: 15s
    wait-max: 24h
    warm-timeout: 5m
    escalation-poll-interval: 45s
    tracker-token-cred: linear-bot
    linear:
      team: COV
      poll-interval: 60s
      states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
`))
	if err != nil {
		t.Fatal(err)
	}
	dc := c.Runtime.Requisitioner
	if dc == nil {
		t.Fatal("runtime.requisitioner did not parse")
	}
	if dc.Role != "implementer" ||
		dc.Project != "cove" ||
		dc.MaxConcurrent != 3 ||
		dc.PollInterval != "45s" ||
		dc.WakePollInterval != "15s" ||
		dc.WaitMax != "24h" ||
		dc.WarmTimeout != "5m" ||
		dc.EscalationPollInterval != "45s" {
		t.Fatalf("Requisitioner config = %+v", dc)
	}
	if dc.TrackerTokenCred != "linear-bot" {
		t.Fatalf("tracker-token-cred = %q", dc.TrackerTokenCred)
	}
	if dc.Linear == nil || dc.Linear.Team != "COV" {
		t.Fatalf("linear.team did not parse: %+v", dc.Linear)
	}
	// runtime.requisitioner is a known key (no unknown-key warning).
	if got := unknownServeKeys([]byte("runtime:\n  requisitioner:\n    role: r\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v", got)
	}
}

func TestValidateRequisitionerRequiredFields(t *testing.T) {
	// no Requisitioner block at all → no error.
	if err := (serveConfig{}).validateRequisitioner(); err != nil {
		t.Fatalf("nil Requisitioner should not error: %v", err)
	}
	base := func() *requisitionerConfig {
		return &requisitionerConfig{
			Role:             "implementer",
			MaxConcurrent:    1,
			Linear:           &kit.LinearTracker{Team: "COV"},
			TrackerTokenCred: "linear-bot",
		}
	}
	// all required fields present → ok.
	c := serveConfig{Credentials: map[string]credSpec{"linear-bot": {}}}
	c.Runtime.Requisitioner = base()
	if err := c.validateRequisitioner(); err != nil {
		t.Fatalf("complete Requisitioner block should not error: %v", err)
	}

	for field, mutate := range map[string]func(*requisitionerConfig){
		"role":               func(d *requisitionerConfig) { d.Role = "" },
		"max-concurrent":     func(d *requisitionerConfig) { d.MaxConcurrent = 0 },
		"linear":             func(d *requisitionerConfig) { d.Linear = nil },
		"tracker-token-cred": func(d *requisitionerConfig) { d.TrackerTokenCred = "" },
	} {
		bad := serveConfig{Credentials: map[string]credSpec{"linear-bot": {}}}
		bad.Runtime.Requisitioner = base()
		mutate(bad.Runtime.Requisitioner)
		if err := bad.validateRequisitioner(); err == nil {
			t.Fatalf("missing/invalid %s should error", field)
		}
	}
}

func TestBrowserAuthConfig(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
operator-auth:
  oidc:
    issuer: https://idp/
    audience: aud
    browser-client-id: bcid
`))
	if err != nil {
		t.Fatal(err)
	}
	bc := cfg.browserAuthConfig()
	if bc == nil || bc.ClientID != "bcid" || bc.Audience != "aud" || bc.Issuer != "https://idp/" || bc.Scope != "openid profile email" {
		t.Fatalf("browserAuthConfig = %+v, want bcid/aud/default-scope", bc)
	}

	none, _ := parseServeConfig([]byte("operator-auth:\n  oidc:\n    issuer: x\n    audience: y\n"))
	if none.browserAuthConfig() != nil {
		t.Error("no browser-client-id should yield nil")
	}
}

func TestBrowserLoginAdminsEveryone(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml string
		want bool
	}{
		"login, empty scope": {"    browser-client-id: b\n", true},
		"login, scope set":   {"    browser-client-id: b\n    require-scope: jam:admin\n", false},
		"no login, empty":    {"", false},
	} {
		cfg, err := parseServeConfig([]byte("operator-auth:\n  oidc:\n    issuer: https://idp/\n    audience: aud\n" + tc.yaml))
		if err != nil {
			t.Fatal(name, err)
		}
		if got := cfg.browserLoginAdminsEveryone(); got != tc.want {
			t.Errorf("%s: = %v, want %v", name, got, tc.want)
		}
	}
}

// The /app login asks for the operator scope (require-scope) so its access
// token can make the viewer an admin; openid is always present, once.
func TestBrowserAuthConfigRequestsOperatorScope(t *testing.T) {
	for name, extra := range map[string]string{
		"default scope":        "",
		"browser-scope set":    "    browser-scope: profile jam:admin\n",
		"browser-scope openid": "    browser-scope: openid email\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseServeConfig([]byte("operator-auth:\n  oidc:\n    issuer: https://idp/\n    audience: aud\n    browser-client-id: bcid\n    require-scope: jam:admin\n" + extra))
			if err != nil {
				t.Fatal(err)
			}
			n := map[string]int{}
			for _, s := range strings.Fields(cfg.browserAuthConfig().Scope) {
				n[s]++
			}
			if n["openid"] != 1 || n["jam:admin"] != 1 {
				t.Fatalf("scope = %q, want openid and jam:admin exactly once each", cfg.browserAuthConfig().Scope)
			}
		})
	}
}

func TestUIHostsParsed(t *testing.T) {
	cfg, err := parseServeConfig([]byte("ui-hosts:\n  - jam.local.aethons.tools\n  - jam.internal\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.UIHosts) != 2 || cfg.UIHosts[0] != "jam.local.aethons.tools" || cfg.UIHosts[1] != "jam.internal" {
		t.Fatalf("UIHosts = %v, want the two configured hosts", cfg.UIHosts)
	}
	// ui-hosts is a known key (not flagged as unknown).
	if got := unknownServeKeys([]byte("ui-hosts: [a]\n")); len(got) != 0 {
		t.Fatalf("ui-hosts flagged as unknown: %v", got)
	}
}

func TestUIOriginsParsedAndValidated(t *testing.T) {
	cfg, err := parseServeConfig([]byte("ui-origins:\n  - http://localhost:8090\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.UIOrigins) != 1 || cfg.UIOrigins[0] != "http://localhost:8090" {
		t.Fatalf("UIOrigins = %v, want [http://localhost:8090]", cfg.UIOrigins)
	}
	if got := unknownServeKeys([]byte("ui-origins: [a]\n")); len(got) != 0 {
		t.Fatalf("ui-origins flagged as unknown: %v", got)
	}
	// An origin is scheme://host[:port] — no path, no bare host, no other scheme.
	for _, bad := range []string{"localhost:8090", "http://localhost:8090/ui", "ftp://x", "http://"} {
		if _, err := parseServeConfig([]byte("ui-origins: [\"" + bad + "\"]\n")); err == nil {
			t.Errorf("ui-origins %q accepted, want an error", bad)
		}
	}
}

func TestDevIdentityParsedAndLoopbackOnly(t *testing.T) {
	cfg, err := parseServeConfig([]byte("admin-listen: 127.0.0.1:8081\ndev-identity:\n  user: you\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DevIdentity == nil || cfg.DevIdentity.User != "you" {
		t.Fatalf("DevIdentity = %+v, want user you", cfg.DevIdentity)
	}
	// The pre-registry form {project, human} still parses, as the user named human.
	cfg, err = parseServeConfig([]byte("admin-listen: 127.0.0.1:8081\ndev-identity:\n  project: test\n  human: you\n"))
	if err != nil || cfg.DevIdentity.DeprecatedHuman != "you" || len(cfg.deprecated) == 0 {
		t.Fatalf("deprecated form = %+v (deprecated %v), %v", cfg.DevIdentity, cfg.deprecated, err)
	}
	if cfg.DevIdentity.DeprecatedProject != "test" {
		t.Fatalf("the deprecated form must keep its project for the legacy alias: %+v", cfg.DevIdentity)
	}
	for name, bad := range map[string]string{
		"off-loopback admin": "admin-listen: 0.0.0.0:8081\ndev-identity:\n  user: you\n",
		"missing user":       "admin-listen: 127.0.0.1:8081\ndev-identity:\n  project: test\n",
		"both forms":         "admin-listen: 127.0.0.1:8081\ndev-identity:\n  user: you\n  human: you\n",
		"human, no project":  "admin-listen: 127.0.0.1:8081\ndev-identity:\n  human: you\n",
	} {
		if _, err := parseServeConfig([]byte(bad)); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

func TestServeConfigStorePostgres(t *testing.T) {
	y := "store-postgres:\n  host: db\n  port: 5432\n  database: jam\n  user: jam\n  sslmode: verify-full\n  password-cred: jam-db\n"
	var c serveConfig
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatal(err)
	}
	if c.StorePostgres == nil {
		t.Fatal("StorePostgres is nil")
	}
	if c.StorePostgres.Host != "db" || c.StorePostgres.Port != 5432 || c.StorePostgres.Database != "jam" ||
		c.StorePostgres.User != "jam" || c.StorePostgres.SSLMode != "verify-full" || c.StorePostgres.PasswordCred != "jam-db" {
		t.Fatalf("StorePostgres = %+v", c.StorePostgres)
	}
	// Absent block => nil.
	var empty serveConfig
	if err := yaml.Unmarshal([]byte("listen: \":443\"\n"), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.StorePostgres != nil {
		t.Fatalf("StorePostgres default = %+v, want nil", empty.StorePostgres)
	}
}

func TestValidateStorePostgres(t *testing.T) {
	// nil block passes validateStorePostgres; presence is enforced by validateStorage.
	if err := (serveConfig{}).validateStorePostgres(); err != nil {
		t.Fatalf("nil store-postgres should be valid: %v", err)
	}
	// missing required fields error.
	c := serveConfig{StorePostgres: &storePostgresConfig{Host: "db"}}
	if err := c.validateStorePostgres(); err == nil {
		t.Fatal("expected error for missing required fields")
	}
	// password-cred must resolve to a configured credential.
	c = serveConfig{
		Credentials:   map[string]credSpec{},
		StorePostgres: &storePostgresConfig{Host: "db", Database: "h", User: "u", SSLMode: "require", PasswordCred: "missing"},
	}
	if err := c.validateStorePostgres(); err == nil {
		t.Fatal("expected error when password-cred is not a configured credential")
	}
	c.Credentials["jam-db"] = credSpec{Command: []string{"echo", "pw"}}
	c.StorePostgres.PasswordCred = "jam-db"
	if err := c.validateStorePostgres(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateDiscord(t *testing.T) {
	var c serveConfig
	c.Runtime.Discord = &discordConfig{} // no bot-token-cred
	if err := c.validateDiscord(); err == nil {
		t.Fatal("expected error for missing bot-token-cred")
	}
	c.Credentials = map[string]credSpec{"discord-bot": {}}
	c.Runtime.Discord = &discordConfig{BotTokenCred: "discord-bot"}
	if err := c.validateDiscord(); err != nil {
		t.Fatalf("valid discord: %v", err)
	}
	var empty serveConfig // Discord unset → no-op
	if err := empty.validateDiscord(); err != nil {
		t.Fatalf("unset discord must be a no-op: %v", err)
	}
}

// runtime.wake parses, and is a known runtime key; an unknown runtime sub-key
// is reported (dotted) so a typo'd block doesn't vanish silently.
func TestRuntimeWakeParsedAndKnown(t *testing.T) {
	c, err := parseServeConfig([]byte("runtime:\n  wake:\n    poll-interval: 5s\n    wait-max: 2h\n    warm-timeout: 90s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if w := c.Runtime.Wake; w == nil || w.PollInterval != "5s" || w.WaitMax != "2h" || w.WarmTimeout != "90s" {
		t.Fatalf("runtime.wake = %+v", c.Runtime.Wake)
	}
	if got := unknownServeKeys([]byte("runtime:\n  wake:\n    wait-max: 2h\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v, want none", got)
	}
	if got := unknownServeKeys([]byte("runtime:\n  wak:\n    wait-max: 2h\n")); len(got) != 1 || got[0] != "runtime.wak" {
		t.Fatalf("unknown keys = %v, want [runtime.wak]", got)
	}
}

// Each wake-on setting resolves runtime.wake > runtime.requisitioner > the engine
// default (zero), independently per field.
func TestWakeSettingsPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want wakeon.Config
	}{
		{"defaults", "runtime: {}\n", wakeon.Config{}},
		{"Requisitioner fallback", "runtime:\n  requisitioner:\n    wake-poll-interval: 15s\n    wait-max: 24h\n    warm-timeout: 5m\n",
			wakeon.Config{PollInterval: 15 * time.Second, MaxWait: 24 * time.Hour, WarmTimeout: 5 * time.Minute}},
		{"wake wins", "runtime:\n  wake:\n    poll-interval: 5s\n    wait-max: 2h\n    warm-timeout: 90s\n  requisitioner:\n    wake-poll-interval: 15s\n    wait-max: 24h\n    warm-timeout: 5m\n",
			wakeon.Config{PollInterval: 5 * time.Second, MaxWait: 2 * time.Hour, WarmTimeout: 90 * time.Second}},
		{"per field", "runtime:\n  wake:\n    wait-max: 2h\n  requisitioner:\n    wake-poll-interval: 15s\n    wait-max: 24h\n",
			wakeon.Config{PollInterval: 15 * time.Second, MaxWait: 2 * time.Hour}},
		{"wake only", "runtime:\n  wake:\n    warm-timeout: 90s\n", wakeon.Config{WarmTimeout: 90 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := parseServeConfig([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got := c.wakeSettings(); got != tc.want {
				t.Fatalf("wakeSettings = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// An invalid runtime.wake duration is a config error (the Requisitioner's legacy
// fields stay lenient: invalid ⇒ engine default).
func TestValidateWake(t *testing.T) {
	if err := (serveConfig{}).validateWake(); err != nil {
		t.Fatalf("unset runtime.wake: %v", err)
	}
	for _, y := range []string{"poll-interval: soon", "wait-max: x", "warm-timeout: 1parsec"} {
		c, err := parseServeConfig([]byte("runtime:\n  wake:\n    " + y + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.validateWake(); err == nil {
			t.Fatalf("%s: want a validation error", y)
		}
	}
	c, _ := parseServeConfig([]byte("runtime:\n  wake:\n    wait-max: 2h\n"))
	if err := c.validateWake(); err != nil {
		t.Fatalf("valid runtime.wake: %v", err)
	}
}

func TestParseServeConfigPool(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
pool:
  store: /var/lib/jam/pool.json
  cred-name: anthropic-sub
  refresh-interval: 5m
  refresh-margin: 15m
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Pool == nil || cfg.Pool.CredName != "anthropic-sub" || cfg.Pool.Store != "/var/lib/jam/pool.json" {
		t.Fatalf("pool block not parsed: %+v", cfg.Pool)
	}
	iv, mg, err := cfg.poolDurations()
	if err != nil {
		t.Fatalf("poolDurations: %v", err)
	}
	if iv != 5*time.Minute || mg != 15*time.Minute {
		t.Fatalf("durations = %v / %v", iv, mg)
	}
}

func TestValidatePoolRequiresStoreAndCred(t *testing.T) {
	c := serveConfig{Pool: &poolConfig{Store: "", CredName: "x"}}
	if err := c.validatePool(); err == nil {
		t.Fatal("missing store should fail validation")
	}
	c = serveConfig{Pool: &poolConfig{Store: "/p.json", CredName: ""}}
	if err := c.validatePool(); err == nil {
		t.Fatal("missing cred-name should fail validation")
	}
	c = serveConfig{Pool: &poolConfig{Store: "/p.json", CredName: "anthropic-sub"}}
	if err := c.validatePool(); err != nil {
		t.Fatalf("valid pool block should pass: %v", err)
	}
}

func TestPoolDurationsDefault(t *testing.T) {
	c := serveConfig{Pool: &poolConfig{Store: "/p.json", CredName: "x"}}
	iv, mg, err := c.poolDurations()
	if err != nil {
		t.Fatalf("poolDurations: %v", err)
	}
	if iv != 5*time.Minute || mg != 15*time.Minute {
		t.Fatalf("defaults = %v / %v, want 5m/15m", iv, mg)
	}
}

func TestParseClaudeAiOauth(t *testing.T) {
	data := []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-AAA","refreshToken":"sk-ant-ort01-BBB","expiresAt":1893456000000,"subscriptionType":"pro"}}`)
	acct, err := parseClaudeAiOauth("pool-a", data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if acct.Name != "pool-a" || acct.AccessToken != "sk-ant-oat01-AAA" || acct.RefreshToken != "sk-ant-ort01-BBB" {
		t.Fatalf("account = %+v", acct)
	}
	if acct.ExpiresAt.UnixMilli() != 1893456000000 {
		t.Fatalf("expiresAt = %v", acct.ExpiresAt)
	}
}

func TestParseClaudeAiOauthMissingTokens(t *testing.T) {
	if _, err := parseClaudeAiOauth("x", []byte(`{"claudeAiOauth":{"accessToken":"a"}}`)); err == nil {
		t.Fatal("missing refreshToken should error")
	}
	if _, err := parseClaudeAiOauth("x", []byte(`not json`)); err == nil {
		t.Fatal("bad json should error")
	}
}

func TestCredConfiguredIncludesPoolCred(t *testing.T) {
	c := serveConfig{
		Credentials: map[string]credSpec{"git-pat": {}},
		Pool:        &poolConfig{Store: "/p.json", CredName: "anthropic-sub"},
	}
	if !c.credConfigured("git-pat") {
		t.Fatal("a credentials: entry should be configured")
	}
	if !c.credConfigured("anthropic-sub") {
		t.Fatal("the pool cred-name should be accepted (the pool resolves it)")
	}
	if c.credConfigured("nope") {
		t.Fatal("an unknown name must not be configured")
	}
	// No pool block ⇒ the pool cred name is not accepted.
	c2 := serveConfig{Credentials: map[string]credSpec{"git-pat": {}}}
	if c2.credConfigured("anthropic-sub") {
		t.Fatal("without a pool block, the pool cred must not resolve")
	}
}

func TestValidateCredentials_InlineStrategyRejected(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  anthropic-key:
    command: ["at-mint", "anthropic"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateCredentials(); err == nil {
		t.Fatal("want error for an inline command under credentials:, got nil")
	}
}

func TestValidateCredentials_NameOnlyOK(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  anthropic-key:
  git-pat:
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateCredentials(); err != nil {
		t.Fatalf("name-only credentials should validate: %v", err)
	}
	got := cfg.demandedCredentials()
	if len(got) != 2 || got[0] != "anthropic-key" || got[1] != "git-pat" {
		t.Fatalf("demandedCredentials = %v", got)
	}
}

func TestValidateCredentials_ExchangeGCP(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  git-pat:
  vertex-gcp: { exchange: gcp }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateCredentials(); err != nil {
		t.Fatalf("exchange: gcp should validate: %v", err)
	}
	if got := cfg.gcpCredentials(); len(got) != 1 || got[0] != "vertex-gcp" {
		t.Fatalf("gcpCredentials = %v", got)
	}
	if got := cfg.demandedCredentials(); len(got) != 2 {
		t.Fatalf("an exchange credential is still demanded: %v", got)
	}
	for name, yml := range map[string]string{
		"unknown exchange": "credentials:\n  c: { exchange: aws }\n",
		"pool credential":  "pool: { store: /tmp/p.json, cred-name: c }\ncredentials:\n  c: { exchange: gcp }\n",
	} {
		cfg, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := cfg.validateCredentials(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCredentialsFilePath_DefaultUnderXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	var cfg serveConfig
	if got := cfg.credentialsFilePath(); got != "/tmp/xdg/at-jam/credentials.yml" {
		t.Fatalf("default path = %q", got)
	}
	cfg.CredentialsFile = "/etc/jam/creds.yml"
	if got := cfg.credentialsFilePath(); got != "/etc/jam/creds.yml" {
		t.Fatalf("explicit path = %q", got)
	}
}

func TestValidateDiscord_InlineBotTokenRejected(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  discord-bot:
runtime:
  discord:
    bot-token: { value: "x" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateDiscord(); err == nil {
		t.Fatal("want error for inline runtime.discord.bot-token, got nil")
	}
}

func TestValidateDiscord_CredReferenceOK(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  discord-bot:
runtime:
  discord:
    bot-token-cred: discord-bot
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateDiscord(); err != nil {
		t.Fatalf("bot-token-cred reference should validate: %v", err)
	}
}

func TestValidateDiscord_CredMustBeDemanded(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
runtime:
  discord:
    bot-token-cred: nope
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateDiscord(); err == nil {
		t.Fatal("want error: bot-token-cred names an undemanded credential")
	}
}

func TestValidateRequisitioner_InlineTrackerTokenRejected(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  linear-bot:
runtime:
  requisitioner:
    role: worker
    max-concurrent: 1
    linear: { team: T }
    tracker-token: { value: "x" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateRequisitioner(); err == nil {
		t.Fatal("want error for inline runtime.requisitioner.tracker-token, got nil")
	}
}

func TestValidateRequisitioner_CredReferenceOK(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
credentials:
  linear-bot:
runtime:
  requisitioner:
    role: worker
    max-concurrent: 1
    linear: { team: T }
    tracker-token-cred: linear-bot
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.validateRequisitioner(); err != nil {
		t.Fatalf("tracker-token-cred reference should validate: %v", err)
	}
}

func TestValidateDiscord_PoolCredNotAccepted(t *testing.T) {
	cfg := serveConfig{}
	cfg.Pool = &poolConfig{CredName: "anthropic-sub"}
	cfg.Runtime.Discord = &discordConfig{BotTokenCred: "anthropic-sub"}
	if err := cfg.validateDiscord(); err == nil {
		t.Fatal("want error: the pool cred is not a demanded credentials: key")
	}
}

func TestValidateRequisitioner_PoolCredNotAccepted(t *testing.T) {
	cfg := serveConfig{}
	cfg.Pool = &poolConfig{CredName: "anthropic-sub"}
	cfg.Runtime.Requisitioner = &requisitionerConfig{Role: "w", MaxConcurrent: 1, Linear: &kit.LinearTracker{}, TrackerTokenCred: "anthropic-sub"}
	if err := cfg.validateRequisitioner(); err == nil {
		t.Fatal("want error: the pool cred is not a demanded credentials: key")
	}
}

func TestParseServeConfigSessionEvents(t *testing.T) {
	c, err := parseServeConfig([]byte("session-events-retention: 30d\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.SessionEventsRetention != "30d" {
		t.Fatalf("%+v", c)
	}
	if err := c.validateSessionEvents(); err != nil {
		t.Fatal(err)
	}
	bad, _ := parseServeConfig([]byte("session-events-retention: forever\n"))
	if err := bad.validateSessionEvents(); err == nil {
		t.Fatal("want a validation error")
	}
}

func TestValidateRemovedStorageKeys(t *testing.T) {
	pg := "store-postgres: {host: h, port: 5432, database: d, user: u, password-cred: p, sslmode: disable}\n"
	for _, key := range []string{"store: /var/lib/jam/store.json", "intercom-log: /var/lib/jam/log.jsonl", "session-events-dir: /var/lib/jam/events"} {
		name := strings.SplitN(key, ":", 2)[0]
		// Without store-postgres: export/import migration hint.
		c, err := parseServeConfig([]byte(key + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		err = c.validateStorage()
		if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "at-jam export") || !strings.Contains(err.Error(), "not migrated") {
			t.Errorf("%s: want removed-key error naming it with the export/import hint, got %v", name, err)
		}
		// With store-postgres: the key was ignored, so just delete it.
		c, err = parseServeConfig([]byte(pg + key + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		err = c.validateStorage()
		if err == nil || !strings.Contains(err.Error(), name+" is no longer supported; remove it") || !strings.Contains(err.Error(), "no migration") || strings.Contains(err.Error(), "at-jam export") {
			t.Errorf("%s (postgres set): want remove-it/no-migration error without export hint, got %v", name, err)
		}
	}
}

func TestValidateStorageRequiresPostgres(t *testing.T) {
	c, _ := parseServeConfig([]byte("listen: \":443\"\n"))
	if err := c.validateStorage(); err == nil || !strings.Contains(err.Error(), "store-postgres") {
		t.Fatalf("want store-postgres required, got %v", err)
	}
	ok, _ := parseServeConfig([]byte("store-postgres: {host: h, port: 5432, database: d, user: u, password-cred: p, sslmode: disable}\n"))
	if err := ok.validateStorage(); err != nil {
		t.Fatalf("valid postgres config: %v", err)
	}
}

func TestStateDirDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got, err := (serveConfig{}).stateDir(); err != nil || got != "/xdg/state/at-jam" {
		t.Fatalf("XDG: got %q, %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, _ := os.UserHomeDir()
	if got, err := (serveConfig{}).stateDir(); err != nil || got != filepath.Join(home, ".local", "state", "at-jam") {
		t.Fatalf("home default: got %q, %v", got, err)
	}
	c, _ := parseServeConfig([]byte("state-dir: /srv/jam-state\n"))
	if got, err := c.stateDir(); err != nil || got != "/srv/jam-state" {
		t.Fatalf("explicit: got %q, %v", got, err)
	}
}

func TestStateDirRejectsRelative(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "rel/state")
	if _, err := (serveConfig{}).stateDir(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative XDG: want error, got %v", err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	c, _ := parseServeConfig([]byte("state-dir: rel/state\n"))
	if _, err := c.stateDir(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative explicit: want error, got %v", err)
	}
	t.Setenv("HOME", "")
	if _, err := (serveConfig{}).stateDir(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("HOME unset: want error, got %v", err)
	}
}

func TestRelayStatePaths(t *testing.T) {
	c, m, r := relayStatePaths("/srv/state")
	if c != "/srv/state/relay-cursors.json" || m != "/srv/state/relay-markers.json" || r != "/srv/state/relay-receipts.json" {
		t.Fatalf("%s %s %s", c, m, r)
	}
}

// credNames (what the UI suggests) lists exactly what credConfigured accepts.
func TestCredNamesMatchCredConfigured(t *testing.T) {
	c := serveConfig{Credentials: map[string]credSpec{"gh-pat": {}, "anth-key": {}}, Pool: &poolConfig{CredName: "pool-cred"}}
	got := c.credNames()
	want := []string{"anth-key", "gh-pat", "pool-cred"}
	if !slices.Equal(got, want) {
		t.Fatalf("credNames = %v, want %v", got, want)
	}
	for _, n := range got {
		if !c.credConfigured(n) {
			t.Errorf("suggested %q is not configured", n)
		}
	}
	// a pool reusing a listed credential isn't listed twice
	c.Pool.CredName = "gh-pat"
	if got := c.credNames(); !slices.Equal(got, []string{"anth-key", "gh-pat"}) {
		t.Errorf("credNames with shared pool cred = %v", got)
	}
}

// The escalation poll interval: runtime.escalation-poll-interval wins over
// runtime.requisitioner's key (kept for compatibility); neither = default.
func TestEscalationPollInterval(t *testing.T) {
	for yml, want := range map[string]time.Duration{
		"runtime:\n  escalation-poll-interval: 10s\n  requisitioner:\n    escalation-poll-interval: 20s\n": 10 * time.Second,
		"runtime:\n  requisitioner:\n    escalation-poll-interval: 20s\n":                                  20 * time.Second,
		"runtime:\n  listen: x\n": 0,
	} {
		cfg, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%q: %v", yml, err)
		}
		if got := cfg.escalationPollInterval(); got != want {
			t.Errorf("%q = %v, want %v", yml, got, want)
		}
	}
	bad, err := parseServeConfig([]byte("runtime:\n  escalation-poll-interval: soon\n"))
	if err == nil && bad.validateWake() == nil {
		t.Fatal("an invalid runtime.escalation-poll-interval must be refused")
	}
}

func TestSessionWakeLimit(t *testing.T) {
	for yml, want := range map[string]int{
		"runtime:\n  listen: x\n":                        8,
		"runtime:\n  wake:\n    session-wake-limit: 3\n": 3,
		"runtime:\n  wake:\n    session-wake-limit: 0\n": 0,
	} {
		cfg, err := parseServeConfig([]byte(yml))
		if err != nil || cfg.validateWake() != nil {
			t.Fatalf("%q: %v", yml, err)
		}
		if got := cfg.sessionWakeLimit(); got != want {
			t.Errorf("%q = %d, want %d", yml, got, want)
		}
	}
	bad, _ := parseServeConfig([]byte("runtime:\n  wake:\n    session-wake-limit: -1\n"))
	if bad.validateWake() == nil {
		t.Fatal("a negative limit must be refused")
	}
}

// display-name is optional, one line, at most 64 characters; it is trimmed.
func TestDisplayNameConfig(t *testing.T) {
	cfg, err := parseServeConfig([]byte("display-name: \"  Aethon  \"\nlisten: 127.0.0.1:8443\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.validateDisplayName(); err != nil || cfg.displayName() != "Aethon" {
		t.Fatalf("display-name = %q, %v", cfg.displayName(), err)
	}
	if (serveConfig{}).validateDisplayName() != nil || (serveConfig{}).displayName() != "" {
		t.Error("an unset display-name is valid and empty")
	}
	if (serveConfig{DisplayName: "   "}).displayName() != "" {
		t.Error("a whitespace-only display-name is unset")
	}
	for _, bad := range []string{"two\nlines", "tab\there", "line\u2028sep", "zero\u200bwidth", "bidi\u202eoverride", strings.Repeat("x", 65)} {
		if err := (serveConfig{DisplayName: bad}).validateDisplayName(); err == nil {
			t.Errorf("display-name %q should be refused", bad)
		}
	}
	if err := (serveConfig{DisplayName: strings.Repeat("é", 64)}).validateDisplayName(); err != nil {
		t.Errorf("64 characters (not bytes) is fine: %v", err)
	}
}

func TestValidateMetrics(t *testing.T) {
	ok, err := parseServeConfig([]byte("credentials:\n  prom-scrape:\nmetrics:\n  token-cred: prom-scrape\n  alertmanager-url: http://localhost:9093\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.validateMetrics(); err != nil {
		t.Fatalf("valid metrics refused: %v", err)
	}
	for name, yml := range map[string]string{
		"no token-cred":    "metrics: {}\n",
		"undemanded cred":  "metrics:\n  token-cred: nope\n",
		"bad alertmanager": "credentials:\n  t:\nmetrics:\n  token-cred: t\n  alertmanager-url: ftp://x\n",
		"gcp cred":         "credentials:\n  g: { exchange: gcp }\nmetrics:\n  token-cred: g\n",
		"pool cred":        "credentials:\n  sub:\npool: { store: /tmp/p.json, cred-name: sub }\nmetrics:\n  token-cred: sub\n",
	} {
		c, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := c.validateMetrics(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	none, _ := parseServeConfig([]byte("listen: :443\n"))
	if err := none.validateMetrics(); err != nil {
		t.Fatalf("absent metrics must validate: %v", err)
	}
}

func TestValidateSSH(t *testing.T) {
	ok, err := parseServeConfig([]byte("credentials:\n  jam-ssh-ca:\nssh:\n  ca-cred: jam-ssh-ca\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.validateSSH(); err != nil {
		t.Fatalf("valid ssh refused: %v", err)
	}
	if ok.SSH.CACred != "jam-ssh-ca" {
		t.Fatalf("ca-cred = %q", ok.SSH.CACred)
	}
	for name, yml := range map[string]string{
		"no ca-cred":      "ssh: {}\n",
		"undemanded cred": "ssh:\n  ca-cred: nope\n",
		"gcp cred":        "credentials:\n  g: { exchange: gcp }\nssh:\n  ca-cred: g\n",
		"pool cred":       "credentials:\n  sub:\npool: { store: /tmp/p.json, cred-name: sub }\nssh:\n  ca-cred: sub\n",
	} {
		c, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := c.validateSSH(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	none, _ := parseServeConfig([]byte("listen: :443\n"))
	if err := none.validateSSH(); err != nil {
		t.Fatalf("absent ssh must validate: %v", err)
	}
}

// resolveSSHCA builds the CA from the demanded credential; absent ssh is nil;
// a bad key fails without echoing the key.
func TestResolveSSHCA(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	// The credential value arrives trimmed of its trailing newline (command
	// sources) — it must still parse.
	keyPEM := strings.TrimSuffix(string(pem.EncodeToMemory(block)), "\n")
	cfg := serveConfig{SSH: &sshConfig{CACred: "jam-ssh-ca"}}
	ca, err := resolveSSHCA(cfg, map[string]secret.Spec{"jam-ssh-ca": {Name: "jam-ssh-ca", Literal: true, Value: keyPEM}})
	if err != nil || ca == nil {
		t.Fatalf("resolveSSHCA = %v, %v", ca, err)
	}
	want, _ := ssh.NewPublicKey(priv.Public())
	if !bytes.Equal(ca.PublicKey(), ssh.MarshalAuthorizedKey(want)) {
		t.Fatalf("public key = %q", ca.PublicKey())
	}

	if ca, err := resolveSSHCA(serveConfig{}, nil); ca != nil || err != nil {
		t.Fatalf("absent ssh = %v, %v; want nil, nil", ca, err)
	}

	bad := "-----BEGIN OPENSSH PRIVATE KEY-----\nc2VjcmV0LWtleS1tYXRlcmlhbA==\n-----END OPENSSH PRIVATE KEY-----"
	_, err = resolveSSHCA(cfg, map[string]secret.Spec{"jam-ssh-ca": {Name: "jam-ssh-ca", Literal: true, Value: bad}})
	if err == nil || strings.Contains(err.Error(), "c2VjcmV0") || !strings.Contains(err.Error(), "jam-ssh-ca") {
		t.Fatalf("err = %v; want a failure naming the cred, never the key", err)
	}
}

func TestCredFixHint(t *testing.T) {
	c, _ := parseServeConfig([]byte("credentials:\n  vertex-gcp: { exchange: gcp }\n  git-pat:\npool: { store: /tmp/p.json, cred-name: anthropic-sub }\n"))
	if h := c.credFixHint("vertex-gcp"); !strings.Contains(h, "gcloud auth application-default login") {
		t.Errorf("gcp hint = %q", h)
	}
	if h := c.credFixHint("anthropic-sub"); !strings.Contains(h, "at-jam pool list") {
		t.Errorf("pool hint = %q", h)
	}
	if h := c.credFixHint("git-pat"); !strings.Contains(h, "git-pat") || !strings.Contains(h, "credentials") {
		t.Errorf("default hint = %q", h)
	}
}

// docker-context must be a valid docker context name: it rides as a
// `--context` argv value, so reject anything that could read as a flag.
func TestValidateLauncherDockerContext(t *testing.T) {
	for ctx, ok := range map[string]bool{
		"":             true, // unset → the default colima context
		"colima":       true,
		"colima-jam-b": true,
		"my_ctx.2":     true,
		"-x":           false,
		"a b":          false,
		"colima/x":     false,
	} {
		c := serveConfig{}
		c.Runtime.Launcher = &launcherConfig{RuntimeAddr: "h:443", JamHost: "h", DockerContext: ctx}
		if err := c.validateLauncher(); (err == nil) != ok {
			t.Errorf("docker-context %q: err=%v, want ok=%v", ctx, err, ok)
		}
	}
}

func TestValidatePreviews(t *testing.T) {
	const base = "admin-listen: 0.0.0.0:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\noperator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n"
	const block = "previews:\n  domain: preview.jam.example\n  tls: { cert: /p.crt, key: /p.key }\n"
	ok, err := parseServeConfig([]byte(base + block))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.validatePreviews(); err != nil {
		t.Fatalf("valid previews refused: %v", err)
	}
	if ok.Previews == nil || ok.Previews.Domain != "preview.jam.example" || ok.Previews.TLS == nil || ok.Previews.TLS.Cert != "/p.crt" || ok.Previews.TLS.Key != "/p.key" {
		t.Fatalf("previews parsed as %+v", ok.Previews)
	}
	for name, yml := range map[string]string{
		"missing tls":          base + "previews:\n  domain: preview.jam.example\n",
		"missing tls key":      base + "previews:\n  domain: preview.jam.example\n  tls: { cert: /p.crt }\n",
		"admin not TLS":        "admin-listen: 127.0.0.1:8080\noperator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n" + block,
		"no admin listener":    "operator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n" + block,
		"no browser-client-id": "admin-listen: 0.0.0.0:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\noperator-auth:\n  oidc: { issuer: https://idp.example, device-client-id: cli }\n" + block,
		"no oidc":              "admin-listen: 127.0.0.1:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\n" + block,
		"uppercase domain":     base + "previews:\n  domain: Preview.Jam.Example\n  tls: { cert: /p.crt, key: /p.key }\n",
		"single label":         base + "previews:\n  domain: example\n  tls: { cert: /p.crt, key: /p.key }\n",
		"empty label":          base + "previews:\n  domain: a..b\n  tls: { cert: /p.crt, key: /p.key }\n",
		"no domain":            base + "previews:\n  tls: { cert: /p.crt, key: /p.key }\n",
		"hyphen edge":          base + "previews:\n  domain: -a.example\n  tls: { cert: /p.crt, key: /p.key }\n",
		"admin host is login":  "admin-listen: jam.preview.jam.example:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\noperator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n" + block,
		"admin host under":     "admin-listen: Admin.Preview.Jam.Example:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\noperator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n" + block,
		"admin host is domain": "admin-listen: preview.jam.example:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\noperator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n" + block,
		"ui-host under":        base + "ui-hosts: [ui.preview.jam.example]\n" + block,
	} {
		c, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := c.validatePreviews(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	named, _ := parseServeConfig([]byte("admin-listen: jam.example:8443\nadmin-tls: { cert: /a.crt, key: /a.key }\noperator-auth:\n  oidc: { issuer: https://idp.example, browser-client-id: web }\n" + block))
	if err := named.validatePreviews(); err != nil {
		t.Fatalf("admin host outside the domain refused: %v", err)
	}
	none, _ := parseServeConfig([]byte("listen: :443\n"))
	if err := none.validatePreviews(); err != nil {
		t.Fatalf("absent previews must validate: %v", err)
	}
	if unknown := unknownServeKeys([]byte(base + block)); len(unknown) != 0 {
		t.Fatalf("previews flagged unknown: %v", unknown)
	}
}

func TestValidateKits(t *testing.T) {
	ok, err := parseServeConfig([]byte("credentials:\n  gh-kits:\n  gl:\nkits:\n  git:\n    - url: https://github.com/acme/\n      cred: gh-kits\n    - url: http://127.0.0.1:3000/\n      cred: gl\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.validateKits(); err != nil {
		t.Fatalf("valid kits refused: %v", err)
	}
	if len(ok.Kits.Git) != 2 || ok.Kits.Git[0].Cred != "gh-kits" {
		t.Fatalf("kits = %+v", ok.Kits)
	}
	for name, yml := range map[string]string{
		"http url":        "credentials:\n  c:\nkits:\n  git:\n    - {url: \"http://git.example.com/a/\", cred: c}\n",
		"ftp url":         "credentials:\n  c:\nkits:\n  git:\n    - {url: \"ftp://git.example.com/a/\", cred: c}\n",
		"userinfo":        "credentials:\n  c:\nkits:\n  git:\n    - {url: \"https://u:p@git.example.com/a/\", cred: c}\n",
		"no trailing /":   "credentials:\n  c:\nkits:\n  git:\n    - {url: \"https://git.example.com/a\", cred: c}\n",
		"single-label":    "credentials:\n  c:\nkits:\n  git:\n    - {url: \"https://gitserver/a/\", cred: c}\n",
		"empty url":       "credentials:\n  c:\nkits:\n  git:\n    - {cred: c}\n",
		"no cred":         "kits:\n  git:\n    - {url: \"https://git.example.com/a/\"}\n",
		"undemanded cred": "kits:\n  git:\n    - {url: \"https://git.example.com/a/\", cred: nope}\n",
		"gcp cred":        "credentials:\n  g: { exchange: gcp }\nkits:\n  git:\n    - {url: \"https://git.example.com/a/\", cred: g}\n",
		"pool cred":       "credentials:\n  sub:\npool: { store: /tmp/p.json, cred-name: sub }\nkits:\n  git:\n    - {url: \"https://git.example.com/a/\", cred: sub}\n",
		"duplicate":       "credentials:\n  c:\nkits:\n  git:\n    - {url: \"https://git.example.com/a/\", cred: c}\n    - {url: \"https://GIT.example.com/a/\", cred: c}\n",
	} {
		c, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		err = c.validateKits()
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "u:p") {
			t.Errorf("%s: error echoes userinfo: %v", name, err)
		}
	}
	none, _ := parseServeConfig([]byte("listen: :443\n"))
	if err := none.validateKits(); err != nil {
		t.Fatalf("absent kits must validate: %v", err)
	}
}

func TestValidateBlobs(t *testing.T) {
	const creds = "credentials:\n  bk:\n  bs:\n"
	ok, err := parseServeConfig([]byte(creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  region: us-east-1\n  access-key-cred: bk\n  secret-key-cred: bs\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.validateBlobs(); err != nil {
		t.Fatalf("valid blobs refused: %v", err)
	}
	if got := ok.Blobs.maxBytes(); got != 25<<20 {
		t.Fatalf("default max = %d", got)
	}
	sized, _ := parseServeConfig([]byte(creds + "blobs:\n  endpoint: http://minio.internal:9000\n  bucket: jam\n  path-style: true\n  max-size: 100MB\n  access-key-cred: bk\n  secret-key-cred: bs\n"))
	if err := sized.validateBlobs(); err != nil || sized.Blobs.maxBytes() != 100<<20 || !sized.Blobs.PathStyle {
		t.Fatalf("sized = %+v, %v", sized.Blobs, err)
	}
	base := "  access-key-cred: bk\n  secret-key-cred: bs\n"
	for name, yml := range map[string]string{
		"no endpoint":       creds + "blobs:\n  bucket: jam\n" + base,
		"bad endpoint":      creds + "blobs:\n  endpoint: s3.example.com\n  bucket: jam\n" + base,
		"userinfo endpoint": creds + "blobs:\n  endpoint: https://u:p@s3.example.com\n  bucket: jam\n" + base,
		"no bucket":         creds + "blobs:\n  endpoint: https://s3.example.com\n" + base,
		"undemanded cred":   creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  access-key-cred: nope\n  secret-key-cred: bs\n",
		"no secret cred":    creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  access-key-cred: bk\n",
		"bad size":          creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  max-size: lots\n" + base,
		"too big":           creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  max-size: 2GB\n" + base,
		"overflow":          creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  max-size: 8589934592GB\n" + base,
		"wraps to zero":     creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  max-size: 17179869184GB\n" + base,
		"zero":              creds + "blobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  max-size: 0MB\n" + base,
		"gcp cred":          "credentials:\n  g: { exchange: gcp }\n  bs:\nblobs:\n  endpoint: https://s3.example.com\n  bucket: jam\n  access-key-cred: g\n  secret-key-cred: bs\n",
	} {
		c, err := parseServeConfig([]byte(yml))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := c.validateBlobs(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	none, _ := parseServeConfig([]byte("listen: :443\n"))
	if err := none.validateBlobs(); err != nil {
		t.Fatalf("absent blobs must validate: %v", err)
	}
}

// The dev sample config parses, and its blobs block (dev/docker-compose.yml's
// Garage) validates.
func TestDevSampleConfigValidates(t *testing.T) {
	b, err := os.ReadFile("../../dev/jam.dev.yml.example")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseServeConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.validateBlobs(); err != nil || c.Blobs == nil {
		t.Fatalf("blobs: %+v %v", c.Blobs, err)
	}
}

func ghBlock() *kit.GitHubTracker {
	return &kit.GitHubTracker{Repo: "acme/board", States: kit.StateMap{Ready: "status:ready", InProgress: "status:in-progress",
		InReview: "status:in-review", NeedsInput: "status:needs-input", Blocked: "status:blocked"}}
}

func TestValidateRequisitionerTrackerChoice(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*requisitionerConfig)
		want   string // "" = valid
	}{
		"github":          {func(d *requisitionerConfig) { d.GitHub = ghBlock() }, ""},
		"linear":          {func(d *requisitionerConfig) { d.Linear = &kit.LinearTracker{Team: "T"} }, ""},
		"neither":         {func(d *requisitionerConfig) {}, "runtime.requisitioner: set exactly one of linear or github"},
		"both":            {func(d *requisitionerConfig) { d.Linear, d.GitHub = &kit.LinearTracker{Team: "T"}, ghBlock() }, "runtime.requisitioner: set exactly one of linear or github"},
		"padded repo":     {func(d *requisitionerConfig) { d.GitHub = ghBlock(); d.GitHub.Repo = " acme/board " }, ""}, // checked trimmed, as NewJam uses it
		"bad repo":        {func(d *requisitionerConfig) { d.GitHub = ghBlock(); d.GitHub.Repo = "acme" }, "runtime.requisitioner.github.repo: github repo must be owner/name"},
		"dot-dot repo":    {func(d *requisitionerConfig) { d.GitHub = ghBlock(); d.GitHub.Repo = "acme/.." }, "github repo must be owner/name"},
		"missing state":   {func(d *requisitionerConfig) { d.GitHub = ghBlock(); d.GitHub.States.Blocked = "" }, "runtime.requisitioner.github.states.blocked is required"},
		"github + legacy": {func(d *requisitionerConfig) { d.GitHub, d.Connection, d.TrackerTokenCred = ghBlock(), "", "gh-bot" }, "tracker-token-cred is Linear-only"},
	} {
		t.Run(name, func(t *testing.T) {
			c := serveConfig{Credentials: map[string]credSpec{"gh-bot": {}}}
			c.Runtime.Requisitioner = &requisitionerConfig{Role: "w", MaxConcurrent: 1, Connection: "gh-acme"}
			tc.mutate(c.Runtime.Requisitioner)
			err := c.validateRequisitioner()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validate = %v; want %q", err, tc.want)
			}
			if strings.HasPrefix(tc.want, "runtime.requisitioner: ") && err.Error() != tc.want {
				t.Fatalf("validate = %q; want exactly %q", err, tc.want)
			}
		})
	}
}

func TestParseRequisitionerGitHubBlock(t *testing.T) {
	cfg, err := parseServeConfig([]byte(`
runtime:
  requisitioner:
    role: worker
    max-concurrent: 1
    connection: gh-acme
    github:
      repo: acme/board
      poll-interval: 2m
      dispatch-label-prefix: "go:"
      states: { ready: "status:ready", in-progress: "status:in-progress", in-review: "status:in-review", needs-input: "status:needs-input", blocked: "status:blocked" }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := cfg.Runtime.Requisitioner
	if d.GitHub == nil || d.GitHub.Repo != "acme/board" || d.GitHub.DispatchLabelPrefix != "go:" || d.trackerKind() != "github" || d.pollInterval() != "2m" {
		t.Fatalf("github block = %+v", d.GitHub)
	}
	if err := cfg.validateRequisitioner(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	d.PollInterval = "45s"
	if d.pollInterval() != "45s" {
		t.Fatal("requisitioner poll-interval wins over the tracker block's")
	}
}

// F10: a Linear requisitioner keeps its effective poll interval — its own
// poll-interval only; linear.poll-interval stays unhonoured.
func TestPollIntervalLinearUnchanged(t *testing.T) {
	d := &requisitionerConfig{Linear: &kit.LinearTracker{Team: "T", PollInterval: "5m"}}
	if d.trackerKind() != "linear" || d.pollInterval() != "" {
		t.Fatalf("linear: kind %q, poll %q; want linear and the Requisitioner default", d.trackerKind(), d.pollInterval())
	}
	d.PollInterval = "45s"
	if d.pollInterval() != "45s" {
		t.Fatalf("linear with its own poll-interval = %q", d.pollInterval())
	}
}
