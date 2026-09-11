package main

import "testing"

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
