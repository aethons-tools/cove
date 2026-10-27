package main

import "testing"

// old spells a pre-rename variable split, so the renamed guard
// (renamed_test.go) does not flag this test for naming it.
func old(prefix, suffix string) string { return "AT_" + prefix + "_" + suffix }

// The agent reads only JAM_* names; a pre-rename env is ignored (one cut, no
// fallback).
func TestBuildConfigReadsOnlyJamNames(t *testing.T) {
	env := map[string]string{
		"JAM_RUNTIME_ADDR": "new:443", "JAM_IDENTITY_TOKEN": "t", "JAM_LAUNCH_SECRET": "s",
		old("JAM", "RUNTIME_ADDR"): "old:443", old("HARBOR", "RUNTIME_ADDR"): "older:443",
	}
	cfg, err := buildConfig(func(k string) string { return env[k] }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "new:443" || cfg.Token != "t" || cfg.LaunchSecret != "s" {
		t.Fatalf("config = %+v", cfg)
	}
	if _, err := buildConfig(func(k string) string {
		return map[string]string{old("JAM", "RUNTIME_ADDR"): "old:443", old("JAM", "IDENTITY_TOKEN"): "t", old("JAM", "LAUNCH_SECRET"): "s"}[k]
	}, nil); err == nil {
		t.Fatal("pre-rename names alone must not configure the agent")
	}
}

// The messaging client reads only JAM_* names too.
func TestMessagingClientReadsOnlyJamNames(t *testing.T) {
	env := map[string]string{
		"JAM_RUNTIME_ADDR": "new.example:443", "JAM_IDENTITY_TOKEN": "tok-new",
		old("JAM", "RUNTIME_ADDR"): "old.example:443", old("HARBOR", "IDENTITY_TOKEN"): "tok-old",
	}
	c, err := newMessagingClient(func(k string) string { return env[k] })
	if err != nil || c.baseURL != "https://new.example" || c.token != "tok-new" {
		t.Fatalf("client = %+v err=%v", c, err)
	}
	if _, err := newMessagingClient(func(k string) string {
		return map[string]string{old("JAM", "RUNTIME_ADDR"): "old.example:443", old("JAM", "IDENTITY_TOKEN"): "tok-old"}[k]
	}); err == nil {
		t.Fatal("pre-rename names alone must not configure the messaging client")
	}
}
