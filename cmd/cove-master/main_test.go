package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildConfigRequiresEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_RUNTIME_ADDR is missing")
	}
	env["AT_HARBOR_RUNTIME_ADDR"] = "harbor:9090"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_IDENTITY_TOKEN is missing")
	}
	env["AT_HARBOR_IDENTITY_TOKEN"] = "tok"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_HARBOR_LAUNCH_SECRET is missing")
	}
	env["AT_HARBOR_LAUNCH_SECRET"] = "sec"
	cfg, err := buildConfig(get)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "harbor:9090" || cfg.Token != "tok" || cfg.LaunchSecret != "sec" {
		t.Fatalf("config = %+v", cfg)
	}
	if len(cfg.DialOptions) == 0 {
		t.Fatal("expected transport credentials in DialOptions")
	}
}

func TestBuildAgentConfig(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("do the work"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("missing prompt file env", func(t *testing.T) {
		_, err := buildAgentConfig(func(k string) string { return "" })
		if err == nil {
			t.Fatal("want error when AT_COVE_AGENT_PROMPT_FILE unset")
		}
	})

	t.Run("unreadable prompt file", func(t *testing.T) {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": filepath.Join(dir, "nope.txt")}
		_, err := buildAgentConfig(func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("want error when prompt file is unreadable")
		}
	})

	t.Run("defaults workdir, reads prompt", func(t *testing.T) {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": promptPath}
		cfg, err := buildAgentConfig(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("buildAgentConfig: %v", err)
		}
		if cfg.WorkDir != "/home/agent/workspace" {
			t.Errorf("WorkDir default: got %q", cfg.WorkDir)
		}
		if cfg.Prompt != "do the work" {
			t.Errorf("Prompt: got %q", cfg.Prompt)
		}
	})

	t.Run("explicit workdir honored", func(t *testing.T) {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": promptPath, "AT_COVE_WORKDIR": "/tmp/work"}
		cfg, err := buildAgentConfig(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("buildAgentConfig: %v", err)
		}
		if cfg.WorkDir != "/tmp/work" {
			t.Errorf("WorkDir: got %q", cfg.WorkDir)
		}
	})
}
