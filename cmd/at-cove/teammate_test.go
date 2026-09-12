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
// A harbor teammate must have a pre-supplied harbor.identity: auto-enroll is
// unsupported for a detached conductor (no exit hook to revoke on), so doTeammate
// rejects a harbor teammate that omits identity (COV-142).
func TestTeammate_HarborRequiresIdentity(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// teammate kit + a harbor block WITHOUT identity (parses; auto-enroll intent).
	yml := teammateKitYAML + "harbor:\n  host: harbor.local.aethons.tools\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	seedConfigDir(t)
	writeStateFor(t, kitDir, state.Instance("helper"), "box", "box-helper")
	writeInstall(t, kitDir)
	if err := os.WriteFile(filepath.Join(configDir(), "secrets.yml"),
		[]byte("kits:\n  box:\n    DISCORD_BOT_TOKEN: { value: \"tok\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "127.0.0.1:49153\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"teammate", "--project-dir", dir, "helper"}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("harbor teammate without identity must fail; stdout=%s", out.String())
	}
	if !strings.Contains(errOut.String(), "harbor.identity") {
		t.Fatalf("error should name harbor.identity; got: %s", errOut.String())
	}
}

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

// TestTeammate_CreateProvisionsIsolatedInstance is the lifecycle-side test for
// COV-136 Task 2: a teammates-only kit's `at-cove create <class>` must provision
// an instance exactly like a collaborator's (class-keyed container/volumes,
// state.json written) but ALWAYS isolated (no share-repo-dir/shadow-dirs concept
// for a teammate). `at-cove chat <class>` on that same class must reject with a
// teammate-specific usage error instead of trying to drive a collaborator chat
// session.
func TestTeammate_CreateProvisionsIsolatedInstance(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeTeammateKit(t, dir)
	writeInstall(t, kitDir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"create", "--project-dir", dir, "helper"}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("create teammate: exit %d: %s", code, errOut.String())
	}
	// Class-keyed container/volumes, mirroring a collaborator create.
	if !dockerRunHasArg(t, f.Calls, "atcove-box-helper-workspace:/home/agent/workspace") {
		t.Fatalf("teammate create must use an isolated, class-keyed workspace volume; calls=%+v", f.Calls)
	}
	st, err := state.LoadFor(kitDir, state.Instance("helper"))
	if err != nil {
		t.Fatalf("class-keyed state not written: %v", err)
	}
	if st.Container != "atcove-box-helper" || st.Name != "box" {
		t.Fatalf("state = %+v (want container atcove-box-helper, kit name box)", st)
	}
	if st.WorkspaceMode != "isolated" {
		t.Fatalf("a teammate instance must always be isolated; state=%+v", st)
	}

	// chat must reject a teammate class with a helpful message naming the right
	// command, not attempt to drive a collaborator-style chat session.
	var o2, e2 bytes.Buffer
	code = run([]string{"chat", "--project-dir", dir, "helper"}, f, os.LookupEnv, dummyLookPath, &o2, &e2)
	if code == 0 || !strings.Contains(e2.String(), "teammate") {
		t.Fatalf("chat should reject a teammate class; exit=%d err=%q", code, e2.String())
	}
	if !strings.Contains(e2.String(), "at-cove teammate helper") {
		t.Fatalf("chat's rejection should point at `at-cove teammate helper`; err=%q", e2.String())
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
