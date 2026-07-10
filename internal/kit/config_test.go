package kit

import (
	"strings"
	"testing"
)

func TestParseConfigValid(t *testing.T) {
	data := []byte(`
name: claude-on-myrepo
secrets:
  GITHUB_TOKEN:
    command: ["op", "read", "x"]
  ANTHROPIC_API_KEY:
    description: Anthropic key
    command: ["pass", "show", "y"]
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "claude-on-myrepo" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Secrets) != 2 || len(cfg.Secrets["GITHUB_TOKEN"].Command) != 3 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
	if cfg.Secrets["ANTHROPIC_API_KEY"].Description != "Anthropic key" {
		t.Fatalf("description not parsed: %+v", cfg.Secrets["ANTHROPIC_API_KEY"])
	}
}

func TestParseConfigRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no name":        "secrets:\n  GITHUB_TOKEN: {}\n",
		"secret no name": "name: x\nsecrets:\n  \"\":\n    command: [\"a\"]\n",
	}
	for label, data := range cases {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestParseConfigRejectsUnknownField(t *testing.T) {
	if _, err := ParseConfig([]byte("name: x\nbogus: 1\n")); err == nil {
		t.Error("expected error on unknown field")
	}
}

func TestParseConfigAllowsCommandlessSecret(t *testing.T) {
	data := []byte("name: x\nsecrets:\n  GITHUB_TOKEN: {}\n")
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("name-only secret should be valid: %v", err)
	}
	s, ok := cfg.Secrets["GITHUB_TOKEN"]
	if len(cfg.Secrets) != 1 || !ok || len(s.Command) != 0 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
}

// Regression guard: literal secret values must NOT be declarable in the kit;
// they belong only in the user's ~/.config/at-cove/secrets.yml. KnownFields(true)
// rejects the unknown `value:` key, so this passes from the start.
func TestParseConfigRejectsSecretValueField(t *testing.T) {
	data := []byte("name: x\nsecrets:\n  T:\n    value: ghp_secret\n")
	if _, err := ParseConfig(data); err == nil {
		t.Fatal("a literal value: in config.yml must be rejected")
	}
}

func TestParseConfigRejectsRemovedSetupField(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nsetup: \"git clone https://x .\"\n"))
	if err == nil {
		t.Fatal("expected error: setup is a removed/unknown field")
	}
}

func TestParseConfigImage(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
name: k
image:
  setup-scripts:
    - .install-files/install.sh
  paths:
    - /usr/local/go/bin
  env:
    GOROOT: /usr/local/go
  allowed-domains:
    - .example.com
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Image.SetupScripts) != 1 || cfg.Image.SetupScripts[0] != ".install-files/install.sh" {
		t.Fatalf("SetupScripts = %v", cfg.Image.SetupScripts)
	}
	if len(cfg.Image.Paths) != 1 || cfg.Image.Paths[0] != "/usr/local/go/bin" {
		t.Fatalf("Paths = %v", cfg.Image.Paths)
	}
	if cfg.Image.Env["GOROOT"] != "/usr/local/go" {
		t.Fatalf("Env = %v", cfg.Image.Env)
	}
	if len(cfg.Image.AllowedDomains) != 1 || cfg.Image.AllowedDomains[0] != ".example.com" {
		t.Fatalf("AllowedDomains = %v", cfg.Image.AllowedDomains)
	}
}

func TestParseConfigImageAbsent(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Image.SetupScripts) != 0 || len(cfg.Image.Paths) != 0 || len(cfg.Image.Env) != 0 || len(cfg.Image.AllowedDomains) != 0 {
		t.Fatalf("absent image must be zero-valued, got %+v", cfg.Image)
	}
}

func TestParseConfigImageRejectsEmptyScript(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  setup-scripts:\n    - \"\"\n"))
	if err == nil || !strings.Contains(err.Error(), "setup-scripts") {
		t.Fatalf("expected empty setup-scripts error, got %v", err)
	}
}

func TestParseConfigImageRejectsReservedEnvKey(t *testing.T) {
	// PATH is a base-owned key; overriding it would produce a second PATH= line.
	_, err := ParseConfig([]byte("name: k\nimage:\n  env:\n    PATH: /evil\n"))
	if err == nil {
		t.Fatal("expected error for reserved PATH env key, got nil")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("error should mention PATH, got: %v", err)
	}
	if !strings.Contains(err.Error(), "overridden") {
		t.Fatalf("error should mention 'overridden', got: %v", err)
	}

	// Proxy keys are also base-owned (egress gate).
	_, err = ParseConfig([]byte("name: k\nimage:\n  env:\n    https_proxy: http://x\n"))
	if err == nil {
		t.Fatal("expected error for reserved https_proxy env key, got nil")
	}
	if !strings.Contains(err.Error(), "https_proxy") {
		t.Fatalf("error should mention https_proxy, got: %v", err)
	}
}

func TestParseConfigImageRejectsEnvValueNewline(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  env:\n    FOO: \"a\\nb\"\n"))
	if err == nil {
		t.Fatal("expected error for env value with newline, got nil")
	}
	if !strings.Contains(err.Error(), "newline") {
		t.Fatalf("error should mention 'newline', got: %v", err)
	}
}

func TestParseConfigImageRejectsPathNewline(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  paths:\n    - \"a\\nb\"\n"))
	if err == nil {
		t.Fatal("expected error for path with newline, got nil")
	}
	if !strings.Contains(err.Error(), "newline") {
		t.Fatalf("error should mention 'newline', got: %v", err)
	}
}

func TestParseConfigImageRejectsEmptyPath(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  paths:\n    - \"\"\n"))
	if err == nil {
		t.Fatal("expected error for empty path entry, got nil")
	}
	if !strings.Contains(err.Error(), "paths") {
		t.Fatalf("error should mention 'paths', got: %v", err)
	}
}

func TestParseConfigImageRejectsEmptyEnvKey(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  env:\n    \"\": x\n"))
	if err == nil {
		t.Fatal("expected error for empty env key, got nil")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Fatalf("error should mention 'env', got: %v", err)
	}
}

func TestParseConfigImageRejectsEmptyDomain(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nimage:\n  allowed-domains:\n    - \"\"\n"))
	if err == nil {
		t.Fatal("expected error for empty domain entry, got nil")
	}
	if !strings.Contains(err.Error(), "allowed-domains") {
		t.Fatalf("error should mention 'allowed-domains', got: %v", err)
	}
}

func TestParseConfigRejectsDispatch(t *testing.T) {
	_, err := ParseConfig([]byte("name: w\ndispatch:\n  command: [\"run-worker.sh\"]\n"))
	if err == nil {
		t.Fatal("expected error for unknown field dispatch, got nil")
	}
}

func TestParseConfigWorkers(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nworkers:\n  implement:\n    prompt: do the thing\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Workers["implement"].Prompt != "do the thing" {
		t.Fatalf("workers not parsed: %+v", cfg.Workers)
	}
}

func TestParseConfigRejectsWorkerWithoutPrompt(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nworkers:\n  implement: {}\n"))
	if err == nil {
		t.Fatal("a worker class with no prompt must be rejected")
	}
}
