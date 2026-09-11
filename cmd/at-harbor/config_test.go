package main

import "testing"

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
			Issuer         string `yaml:"issuer"`
			Audience       string `yaml:"audience"`
			RequireScope   string `yaml:"require-scope"`
			DeviceClientID string `yaml:"device-client-id"`
			DeviceScope    string `yaml:"device-scope"`
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
