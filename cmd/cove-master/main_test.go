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
		t.Fatal("expected error when AT_JAM_RUNTIME_ADDR is missing")
	}
	env["AT_JAM_RUNTIME_ADDR"] = "jam:9090"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_JAM_IDENTITY_TOKEN is missing")
	}
	env["AT_JAM_IDENTITY_TOKEN"] = "tok"
	if _, err := buildConfig(get); err == nil {
		t.Fatal("expected error when AT_JAM_LAUNCH_SECRET is missing")
	}
	env["AT_JAM_LAUNCH_SECRET"] = "sec"
	cfg, err := buildConfig(get)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "jam:9090" || cfg.Token != "tok" || cfg.LaunchSecret != "sec" {
		t.Fatalf("config = %+v", cfg)
	}
	if len(cfg.DialOptions) == 0 {
		t.Fatal("expected transport credentials in DialOptions")
	}
}

func TestClientTransportCredsIsTLS(t *testing.T) {
	got := clientTransportCreds().Info().SecurityProtocol
	if got != "tls" {
		t.Fatalf("transport security = %q, want tls", got)
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

	t.Run("resident from AT_COVE_RESIDENT", func(t *testing.T) {
		for v, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true} {
			env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": promptPath, "AT_COVE_RESIDENT": v}
			cfg, err := buildAgentConfig(func(k string) string { return env[k] })
			if err != nil {
				t.Fatalf("buildAgentConfig: %v", err)
			}
			if cfg.Resident != want {
				t.Errorf("AT_COVE_RESIDENT=%q: Resident = %v, want %v", v, cfg.Resident, want)
			}
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

func TestBuildAgentConfigConnector(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt")
	os.WriteFile(pf, []byte("p"), 0o600)
	env := map[string]string{
		"AT_COVE_AGENT_PROMPT_FILE": pf,
		"AT_JAM_IDENTITY_TOKEN":     "tok",
		"AT_JAM_BASE_URL":           "https://jam.example",
		"AT_JAM_CONNECTOR":          `{"env":{"GH_HOST":"{host}"},"git_route":"/git/"}`,
	}
	cfg, err := buildAgentConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connector == nil || cfg.Connector.BaseURL != "https://jam.example" || cfg.Connector.Token != "tok" ||
		cfg.Connector.Initial.GitRoute != "/git/" || cfg.Connector.Initial.Env["GH_HOST"] != "{host}" {
		t.Fatalf("connector cfg = %+v", cfg.Connector)
	}
	delete(env, "AT_JAM_BASE_URL")
	if cfg, _ := buildAgentConfig(func(k string) string { return env[k] }); cfg.Connector != nil {
		t.Fatal("no AT_JAM_BASE_URL (older launcher) must disable the refresh")
	}
	env["AT_JAM_BASE_URL"], env["AT_JAM_CONNECTOR"] = "https://jam.example", "{not json"
	if cfg, err := buildAgentConfig(func(k string) string { return env[k] }); err != nil || cfg.Connector == nil || len(cfg.Connector.Initial.Env) != 0 {
		t.Fatalf("malformed AT_JAM_CONNECTOR must start empty, not fail: %+v %v", cfg.Connector, err)
	}
}

func TestBuildAgentConfigLoadsContext(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt")
	os.WriteFile(pf, []byte("P"), 0o600)
	cf := filepath.Join(dir, "ctx")
	os.WriteFile(cf, []byte(`{"core":"C","files":{"INDEX.md":"I"},"fingerprint":"f"}`), 0o600)
	env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": pf, "AT_COVE_AGENT_CONTEXT_FILE": cf}
	cfg, err := buildAgentConfig(func(k string) string { return env[k] })
	if err != nil || cfg.Context == nil || cfg.Context.Core != "C" {
		t.Fatalf("cfg.Context = %+v, err %v", cfg.Context, err)
	}
}

func TestBuildAgentConfigBadOrMissingContextIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt")
	os.WriteFile(pf, []byte("P"), 0o600)
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("{not json"), 0o600)
	for _, cf := range []string{"", filepath.Join(dir, "missing"), bad} {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": pf, "AT_COVE_AGENT_CONTEXT_FILE": cf}
		cfg, err := buildAgentConfig(func(k string) string { return env[k] })
		if err != nil || cfg.Context != nil {
			t.Errorf("%q: want no context and no error, got %+v, %v", cf, cfg.Context, err)
		}
	}
}

func TestBuildAgentConfigContextRefreshAndKind(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "prompt")
	os.WriteFile(pf, []byte("P"), 0o600)
	cf := filepath.Join(dir, "ctx")
	os.WriteFile(cf, []byte(`{"core":"C","files":{"INDEX.md":"I"},"fingerprint":"f"}`), 0o600)
	env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": pf, "AT_COVE_AGENT_CONTEXT_FILE": cf,
		"AT_JAM_BASE_URL": "https://jam.example", "AT_JAM_IDENTITY_TOKEN": "tok", "AT_COVE_SESSION_KIND": "standing"}
	cfg, err := buildAgentConfig(func(k string) string { return env[k] })
	if err != nil || cfg.ContextSource == nil || cfg.SessionKind != "standing" {
		t.Fatalf("cfg = source %v kind %q, err %v", cfg.ContextSource, cfg.SessionKind, err)
	}
	delete(env, "AT_COVE_AGENT_CONTEXT_FILE")
	if cfg, _ := buildAgentConfig(func(k string) string { return env[k] }); cfg.ContextSource != nil {
		t.Fatal("no initial bundle → no refresh source")
	}
}
