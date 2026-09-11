package main

import "testing"

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
