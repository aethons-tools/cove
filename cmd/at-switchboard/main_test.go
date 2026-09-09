package main

import (
	"strings"
	"testing"
)

func TestRun_MissingToken_IsUsageError(t *testing.T) {
	var out, errb strings.Builder
	env := func(k string) string { return "" } // nothing set
	code := run([]string{"run"}, env, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "DISCORD_BOT_TOKEN") {
		t.Fatalf("stderr = %q", errb.String())
	}
	if strings.Contains(errb.String(), "secret") { // never leak values
		t.Fatalf("stderr should not mention secret values: %q", errb.String())
	}
}

func TestRun_WiresErrorChannelDefaultToFirstChannel(t *testing.T) {
	// With channels set and no explicit error channel, Config.ErrorChannel should
	// default to the first channel. Assert via a seam: run() should build a Config
	// whose ErrorChannel == "111" when SWITCHBOARD_CHANNELS="111,222" and no
	// SWITCHBOARD_ERROR_CHANNEL. (Extract config-building into buildConfig for test.)
	env := map[string]string{"DISCORD_BOT_TOKEN": "t", "SWITCHBOARD_CHANNELS": "111,222"}
	cfg, channels, _, err := buildConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ErrorChannel != "111" {
		t.Fatalf("error channel default = %q, want 111", cfg.ErrorChannel)
	}
	if len(channels) != 2 {
		t.Fatalf("channels = %v", channels)
	}
	if cfg.Log == nil {
		t.Fatal("Log should be wired (fail-soft must not be silent)")
	}
}

func TestRun_WiresErrorChannelExplicit(t *testing.T) {
	env := map[string]string{
		"DISCORD_BOT_TOKEN":         "t",
		"SWITCHBOARD_CHANNELS":      "111,222",
		"SWITCHBOARD_ERROR_CHANNEL": "999",
	}
	cfg, _, _, err := buildConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ErrorChannel != "999" {
		t.Fatalf("error channel = %q, want 999 (explicit override)", cfg.ErrorChannel)
	}
}
