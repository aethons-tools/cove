package main

import "testing"

func TestParseServeConfig(t *testing.T) {
	yml := `
listen: ":8443"
tls: { cert: /c.pem, key: /k.pem }
store: /var/lib/harbor/ids.json
destinations:
  - { name: anthropic, route: /anthropic/, upstream: https://api.anthropic.com, identity_in: bearer, cred_name: anthropic-bearer, apply: bearer }
  - { name: git, route: /git/, upstream: https://github.com, identity_in: basic-password, cred_name: git-pat, apply: basic-password, repo_scoped: true }
credentials:
  anthropic-bearer: { command: [at-mint, anthropic] }
  git-pat: { value: literal-dev-pat }
`
	cfg, err := parseServeConfig([]byte(yml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Listen != ":8443" || cfg.Store != "/var/lib/harbor/ids.json" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Broker.Destinations) != 2 || cfg.Broker.Destinations[1].Name != "git" {
		t.Fatalf("destinations = %+v", cfg.Broker.Destinations)
	}
	if s := cfg.credSpecs()["git-pat"]; !s.Literal || s.Value != "literal-dev-pat" {
		t.Fatalf("git-pat spec = %+v", s)
	}
}
