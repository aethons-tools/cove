package kit

import "testing"

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
		"no name":           "backend: colima\n",
		"no backend":        "name: x\n",
		"secret no name":    "name: x\nbackend: colima\nsecrets:\n  - command: [\"a\"]\n",
		"secret no command": "name: x\nbackend: colima\nsecrets:\n  - name: T\n",
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
