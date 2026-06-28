package kit

import (
	"testing"
	"time"
)

func TestParseConfigValid(t *testing.T) {
	data := []byte(`
name: claude-on-myrepo
backend: colima
secrets:
  - name: GITHUB_TOKEN
    command: ["op", "read", "x"]
  - name: ANTHROPIC_API_KEY
    description: Anthropic key
    command: ["pass", "show", "y"]
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "claude-on-myrepo" || cfg.Backend != "colima" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Secrets) != 2 || cfg.Secrets[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
	if cfg.Secrets[1].Description != "Anthropic key" {
		t.Fatalf("description not parsed: %+v", cfg.Secrets[1])
	}
}

func TestParseConfigRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no name":        "backend: colima\n",
		"no backend":     "name: x\n",
		"secret no name": "name: x\nbackend: colima\nsecrets:\n  - command: [\"a\"]\n",
	}
	for label, data := range cases {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestParseConfigRejectsUnknownField(t *testing.T) {
	if _, err := ParseConfig([]byte("name: x\nbackend: colima\nbogus: 1\n")); err == nil {
		t.Error("expected error on unknown field")
	}
}

func TestParseConfigAllowsCommandlessSecret(t *testing.T) {
	data := []byte("name: x\nbackend: colima\nsecrets:\n  - name: GITHUB_TOKEN\n")
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("name-only secret should be valid: %v", err)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].Name != "GITHUB_TOKEN" || len(cfg.Secrets[0].Command) != 0 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
}

// Regression guard: literal secret values must NOT be declarable in the kit;
// they belong only in the user's ~/.config/at-cove/secrets.yml. KnownFields(true)
// rejects the unknown `value:` key, so this passes from the start.
func TestParseConfigRejectsSecretValueField(t *testing.T) {
	data := []byte("name: x\nbackend: colima\nsecrets:\n  - name: T\n    value: ghp_secret\n")
	if _, err := ParseConfig(data); err == nil {
		t.Fatal("a literal value: in config.yml must be rejected")
	}
}

func TestParseConfigSetup(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nbackend: colima\nsetup: \"git clone https://x .\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Setup != "git clone https://x ." {
		t.Fatalf("Setup = %q", cfg.Setup)
	}
}

func TestParseConfigSetupOptional(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nbackend: colima\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Setup != "" {
		t.Fatalf("Setup should default empty, got %q", cfg.Setup)
	}
}

func TestParseConfigLoops(t *testing.T) {
	data := []byte(`
name: x
backend: colima
loops:
  default:
    interval: 5m
    check: "test -e q"
    prompt: "do it"
  fresh:
    interval: 30s
    check: "c"
    prompt: "p"
    setup: "git clone https://x ."
    fresh-workspace: true
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Loops) != 2 {
		t.Fatalf("loops = %+v", cfg.Loops)
	}
	d := cfg.Loops["default"]
	if d.ParsedInterval() != 5*time.Minute {
		t.Fatalf("default interval = %v, want 5m", d.ParsedInterval())
	}
	if d.Check != "test -e q" || d.Prompt != "do it" {
		t.Fatalf("default loop = %+v", d)
	}
	f := cfg.Loops["fresh"]
	if !f.FreshWorkspace || f.Setup != "git clone https://x ." || f.ParsedInterval() != 30*time.Second {
		t.Fatalf("fresh loop = %+v", f)
	}
}

func TestParseConfigLoopValidation(t *testing.T) {
	bad := map[string]string{
		"bad interval":  "name: x\nbackend: colima\nloops:\n  a:\n    interval: nope\n    check: c\n    prompt: p\n",
		"zero interval": "name: x\nbackend: colima\nloops:\n  a:\n    interval: 0s\n    check: c\n    prompt: p\n",
		"no interval":   "name: x\nbackend: colima\nloops:\n  a:\n    check: c\n    prompt: p\n",
		"no check":      "name: x\nbackend: colima\nloops:\n  a:\n    interval: 1m\n    prompt: p\n",
		"no prompt":     "name: x\nbackend: colima\nloops:\n  a:\n    interval: 1m\n    check: c\n",
		"unknown field": "name: x\nbackend: colima\nloops:\n  a:\n    interval: 1m\n    check: c\n    prompt: p\n    bogus: 1\n",
	}
	for label, data := range bad {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestParseConfigNoLoopsOK(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: x\nbackend: colima\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Loops) != 0 {
		t.Fatalf("loops should be empty, got %+v", cfg.Loops)
	}
}
