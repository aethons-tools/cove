package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
)

func TestUnknownServeKeys(t *testing.T) {
	// a clean config → no unknowns
	if got := unknownServeKeys([]byte("listen: \":8443\"\nadmin-listen: \"127.0.0.1:8081\"\nstore: /s.json\n")); len(got) != 0 {
		t.Fatalf("clean config unknowns = %v", got)
	}
	// a stray destinations block + a hyphen/underscore typo → both reported, sorted
	got := unknownServeKeys([]byte("listen: \":8443\"\ndestinations: [a]\nadmin_listen: x\n"))
	if want := []string{"admin_listen", "destinations"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unknowns = %v, want %v", got, want)
	}
	// every known key is accepted (guards the reflect-derived set against drift)
	known := "listen: a\nadmin-listen: b\ntls: {}\nadmin-tls: {}\nstore: s\ncredentials: {}\noperator-auth: {}\n"
	if got := unknownServeKeys([]byte(known)); len(got) != 0 {
		t.Fatalf("all-known config flagged: %v", got)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8081": true, "localhost:8081": true, "[::1]:8081": true,
		":8081": false, "0.0.0.0:8081": false, "10.0.0.5:8081": false, "harbor.example:8081": false,
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
    audience: https://harbor.acme/api
    device-client-id: NativeClientId123
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	lc := cfg.operatorLoginConfig()
	if lc == nil || lc.ClientID != "NativeClientId123" || lc.Issuer != "https://acme.us.auth0.com/" ||
		lc.Audience != "https://harbor.acme/api" || lc.Scope != "openid" {
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
store: /s.json
operator-auth:
  oidc:
    issuer: https://acme.us.auth0.com/
    audience: https://harbor.acme/api
    require-scope: harbor:admin
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.OperatorAuth.OIDC == nil || cfg.OperatorAuth.OIDC.Issuer != "https://acme.us.auth0.com/" ||
		cfg.OperatorAuth.OIDC.Audience != "https://harbor.acme/api" || cfg.OperatorAuth.OIDC.RequireScope != "harbor:admin" {
		t.Fatalf("oidc = %+v", cfg.OperatorAuth.OIDC)
	}
}

func TestParseServeConfig(t *testing.T) {
	yml := `
listen: ":8443"
admin-listen: "127.0.0.1:8081"
tls: { cert: /c.pem, key: /k.pem }
store: /var/lib/harbor/store.json
credentials:
  anthropic-key: { command: [at-mint, anthropic] }
  git-pat: { value: literal-dev-pat }
`
	cfg, err := parseServeConfig([]byte(yml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Listen != ":8443" || cfg.AdminListen != "127.0.0.1:8081" || cfg.Store != "/var/lib/harbor/store.json" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if s := cfg.credSpecs()["git-pat"]; !s.Literal || s.Value != "literal-dev-pat" {
		t.Fatalf("git-pat spec = %+v", s)
	}
}

func TestRuntimeDurationsDefaults(t *testing.T) {
	c, err := parseServeConfig([]byte("listen: \":443\"\nstore: /tmp/s.json\n"))
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
    install-manifest: /var/lib/harbor/install.json
    runtime-addr: harbor.example.com:443
    harbor-host: harbor.example.com
    identity-file: /etc/harbor/id_ed25519
    known-hosts-dir: /etc/harbor/known_hosts.d
    dns: ["1.1.1.1", "8.8.8.8"]
    docker: true
`))
	if err != nil {
		t.Fatal(err)
	}
	lc := c.Runtime.Launcher
	if lc == nil {
		t.Fatal("runtime.launcher did not parse")
	}
	if lc.InstallManifest != "/var/lib/harbor/install.json" ||
		lc.RuntimeAddr != "harbor.example.com:443" ||
		lc.HarborHost != "harbor.example.com" ||
		lc.IdentityFile != "/etc/harbor/id_ed25519" ||
		lc.KnownHostsDir != "/etc/harbor/known_hosts.d" ||
		!lc.Docker ||
		len(lc.DNS) != 2 || lc.DNS[0] != "1.1.1.1" || lc.DNS[1] != "8.8.8.8" {
		t.Fatalf("launcher config = %+v", lc)
	}
	// runtime.launcher is a known key (no unknown-key warning).
	if got := unknownServeKeys([]byte("runtime:\n  launcher:\n    harbor-host: h\n")); len(got) != 0 {
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
			InstallManifest: "/m.json",
			RuntimeAddr:     "h:443",
			HarborHost:      "h",
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
		"install-manifest": func(l *launcherConfig) { l.InstallManifest = "" },
		"runtime-addr":     func(l *launcherConfig) { l.RuntimeAddr = "" },
		"harbor-host":      func(l *launcherConfig) { l.HarborHost = "" },
	} {
		bad := serveConfig{}
		bad.Runtime.Launcher = base()
		mutate(bad.Runtime.Launcher)
		if err := bad.validateLauncher(); err == nil {
			t.Fatalf("missing %s should error", field)
		}
	}
}

func TestValidateLauncherDefaultsDontOverride(t *testing.T) {
	c := serveConfig{}
	c.Runtime.Launcher = &launcherConfig{
		InstallManifest: "/m.json", RuntimeAddr: "h:443", HarborHost: "h",
		IdentityFile: "/custom/id", KnownHostsDir: "/custom/kh",
	}
	if err := c.validateLauncher(); err != nil {
		t.Fatal(err)
	}
	if c.Runtime.Launcher.IdentityFile != "/custom/id" || c.Runtime.Launcher.KnownHostsDir != "/custom/kh" {
		t.Fatalf("explicit identity-file/known-hosts-dir must not be overridden: %+v", c.Runtime.Launcher)
	}
}

func TestRuntimeDispatcherParsed(t *testing.T) {
	c, err := parseServeConfig([]byte(`
runtime:
  dispatcher:
    role: implementer
    project: cove
    max-concurrent: 3
    poll-interval: 45s
    tracker-token:
      command: ["op", "read", "tracker-token"]
    linear:
      team: COV
      poll-interval: 60s
      states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
`))
	if err != nil {
		t.Fatal(err)
	}
	dc := c.Runtime.Dispatcher
	if dc == nil {
		t.Fatal("runtime.dispatcher did not parse")
	}
	if dc.Role != "implementer" ||
		dc.Project != "cove" ||
		dc.MaxConcurrent != 3 ||
		dc.PollInterval != "45s" {
		t.Fatalf("dispatcher config = %+v", dc)
	}
	if len(dc.TrackerToken.Command) != 3 || dc.TrackerToken.Command[0] != "op" {
		t.Fatalf("tracker-token command = %+v", dc.TrackerToken)
	}
	if dc.Linear == nil || dc.Linear.Team != "COV" {
		t.Fatalf("linear.team did not parse: %+v", dc.Linear)
	}
	// runtime.dispatcher is a known key (no unknown-key warning).
	if got := unknownServeKeys([]byte("runtime:\n  dispatcher:\n    role: r\n")); len(got) != 0 {
		t.Fatalf("unknown keys = %v", got)
	}
}

func TestValidateDispatcherRequiredFields(t *testing.T) {
	// no dispatcher block at all → no error.
	if err := (serveConfig{}).validateDispatcher(); err != nil {
		t.Fatalf("nil dispatcher should not error: %v", err)
	}
	base := func() *dispatcherConfig {
		return &dispatcherConfig{
			Role:          "implementer",
			MaxConcurrent: 1,
			Linear:        &kit.LinearTracker{Team: "COV"},
		}
	}
	// all required fields present → ok.
	c := serveConfig{}
	c.Runtime.Dispatcher = base()
	if err := c.validateDispatcher(); err != nil {
		t.Fatalf("complete dispatcher block should not error: %v", err)
	}

	for field, mutate := range map[string]func(*dispatcherConfig){
		"role":           func(d *dispatcherConfig) { d.Role = "" },
		"max-concurrent": func(d *dispatcherConfig) { d.MaxConcurrent = 0 },
		"linear":         func(d *dispatcherConfig) { d.Linear = nil },
	} {
		bad := serveConfig{}
		bad.Runtime.Dispatcher = base()
		mutate(bad.Runtime.Dispatcher)
		if err := bad.validateDispatcher(); err == nil {
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
