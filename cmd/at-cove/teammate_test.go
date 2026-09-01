package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/state"
)

// teammateKitYAML declares one teammate class ("helper") with a discord block
// (two channels, no explicit error-channel so it defaults to the first) and the
// DISCORD_BOT_TOKEN secret it demands.
const teammateKitYAML = `name: box
teammates:
  helper:
    prompt: "be helpful"
    secrets:
      DISCORD_BOT_TOKEN: {description: bot}
    allowed-domains: [discord.com]
    discord:
      channels: ["111", "222"]
      bot-token-secret: DISCORD_BOT_TOKEN
`

// writeTeammateKit writes a kit config declaring the "helper" teammate class and
// returns the .at-cove kit dir.
func writeTeammateKit(t *testing.T, dir string) string {
	t.Helper()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(teammateKitYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return kitDir
}

// TestTeammate_AppliesPersistentEgressAndLaunchesDetached is the security test for
// `at-cove teammate`: Discord egress must be applied and PERSIST (no clear-on-exit,
// unlike chat's session egress), at-switchboard must launch detached (setsid, no
// PTY), and the bot token must never appear on any ssh/docker argv the runner
// received (only on stdin, staged in tmpfs).
func TestTeammate_AppliesPersistentEgressAndLaunchesDetached(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeTeammateKit(t, dir)
	seedConfigDir(t) // hermetic XDG_CONFIG_HOME: fresh temp dir, pre-seeded keypair
	// The teammate class keys its own instance (mirrors collaborator instance
	// keying, COV-71): state file helper.json, container box-helper.
	writeStateFor(t, kitDir, state.Instance("helper"), "box", "box-helper")
	writeInstall(t, kitDir) // teammate domains/secrets are sourced from install.json

	const token = "shh-super-secret-token"
	if err := os.WriteFile(filepath.Join(configDir(), "secrets.yml"),
		[]byte("kits:\n  box:\n    DISCORD_BOT_TOKEN: { value: \""+token+"\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// [0] Dial's `docker port` lookup, [1] the ssh auth probe (already authed, so
	// no interactive login and no credentials save follow).
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "127.0.0.1:49153\n"},
		{Stdout: "cove-authed\n"},
	}}
	var out, errOut bytes.Buffer
	code := run([]string{"teammate", "--project-dir", dir, "helper"}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}

	// (1) Discord egress applied, and PERSISTS: exactly one session-egress call
	// (apply only — no clear-on-exit), carrying discord.com on stdin.
	egr := sessionEgressCalls(f.Calls)
	if len(egr) != 1 {
		t.Fatalf("want exactly 1 session-egress call (apply, no clear-on-exit); got %d: %+v", len(egr), egr)
	}
	if !strings.Contains(egr[0].Stdin, "discord.com") {
		t.Fatalf("egress apply stdin = %q; want it to carry discord.com", egr[0].Stdin)
	}

	// (2) at-switchboard launched detached: a non-tty ssh call containing setsid
	// + at-switchboard, no -tt anywhere.
	var detached bool
	for _, c := range f.Calls {
		if c.Name != "ssh" {
			continue
		}
		for _, a := range c.Args {
			if a == "-tt" {
				t.Fatalf("teammate launch must not use a PTY: %+v", c)
			}
		}
		for _, a := range c.Args {
			if strings.Contains(a, "setsid") && strings.Contains(a, "at-switchboard") {
				detached = true
			}
		}
	}
	if !detached {
		t.Fatalf("expected a detached setsid at-switchboard launch over ssh; calls=%+v", f.Calls)
	}

	// (3) The bot token never appears on any argv — only on stdin (staged via
	// writeVM into the tmpfs env file).
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if strings.Contains(a, token) {
				t.Fatalf("token leaked onto argv: %+v", c)
			}
		}
	}
	var stagedViaStdin bool
	for _, c := range f.Calls {
		if strings.Contains(c.Stdin, token) {
			stagedViaStdin = true
		}
	}
	if !stagedViaStdin {
		t.Fatalf("expected the token staged via ssh stdin; calls=%+v", f.Calls)
	}
}

// TestTeammate_UnknownClassIsUsageError guards that an unresolvable teammate
// class is a clear usage error (exit 2), not a panic or an opaque failure.
func TestTeammate_UnknownClassIsUsageError(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeTeammateKit(t, dir)
	seedConfigDir(t)
	writeInstall(t, kitDir)
	var out, errOut bytes.Buffer
	code := run([]string{"teammate", "--project-dir", dir, "nope"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (usage error); stderr=%s", code, errOut.String())
	}
}
