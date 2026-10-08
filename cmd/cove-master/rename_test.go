package main

import "testing"

// cove-master reads the AT_JAM_* environment, falling back to the deprecated
// AT_HARBOR_* names an older Jam launcher sets (docs/usage/jam/renamed-from-harbor.md).

func TestBuildConfigNewNamesWin(t *testing.T) {
	env := map[string]string{
		"AT_JAM_RUNTIME_ADDR": "new:443", "AT_JAM_IDENTITY_TOKEN": "tok-new", "AT_JAM_LAUNCH_SECRET": "sec-new",
		"AT_HARBOR_RUNTIME_ADDR": "old:443", "AT_HARBOR_IDENTITY_TOKEN": "tok-old", "AT_HARBOR_LAUNCH_SECRET": "sec-old",
	}
	cfg, err := buildConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "new:443" || cfg.Token != "tok-new" || cfg.LaunchSecret != "sec-new" {
		t.Fatalf("new names must win: %+v", cfg)
	}
}

func TestBuildConfigOldNamesAloneWork(t *testing.T) {
	env := map[string]string{
		"AT_HARBOR_RUNTIME_ADDR": "old:443", "AT_HARBOR_IDENTITY_TOKEN": "tok-old", "AT_HARBOR_LAUNCH_SECRET": "sec-old",
	}
	cfg, err := buildConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("the deprecated names alone must still work: %v", err)
	}
	if cfg.Addr != "old:443" || cfg.Token != "tok-old" || cfg.LaunchSecret != "sec-old" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestMessagingClientEnvFallback(t *testing.T) {
	newer := map[string]string{
		"AT_JAM_RUNTIME_ADDR": "new.example:443", "AT_JAM_IDENTITY_TOKEN": "tok-new",
		"AT_HARBOR_RUNTIME_ADDR": "old.example:443", "AT_HARBOR_IDENTITY_TOKEN": "tok-old",
	}
	c, err := newMessagingClient(func(k string) string { return newer[k] })
	if err != nil || c.baseURL != "https://new.example" || c.token != "tok-new" {
		t.Fatalf("new names must win: %+v err=%v", c, err)
	}
	older := map[string]string{"AT_HARBOR_RUNTIME_ADDR": "old.example:443", "AT_HARBOR_IDENTITY_TOKEN": "tok-old"}
	c, err = newMessagingClient(func(k string) string { return older[k] })
	if err != nil || c.baseURL != "https://old.example" || c.token != "tok-old" {
		t.Fatalf("the deprecated names alone must still work: %+v err=%v", c, err)
	}
}
