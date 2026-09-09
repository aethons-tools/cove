package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

// detachedLaunchCmd is a pure builder: source the tmpfs env, remove it, then
// start at-switchboard detached (setsid, stdin from /dev/null, output appended
// to a log) — no PTY, so it survives the ssh channel closing.
func TestDetachedLaunchCmd(t *testing.T) {
	got := detachedLaunchCmd("/dev/shm/cove-teammate-env", []string{"111", "222"}, "999")
	for _, want := range []string{
		". /dev/shm/cove-teammate-env",
		"rm -f /dev/shm/cove-teammate-env",
		"setsid",
		"at-switchboard",
		"</dev/null", // detached from the closing ssh channel
		">>/agent-data/switchboard.log",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cmd missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-tt") {
		t.Fatal("detached launch must not use a PTY")
	}
}

// TestLaunchTeammateDetachedTokenNeverOnArgv is the launch-wiring security test:
// the bot token must never appear on any argv the runner received (only on
// stdin, via writeVM), and the actual launch command must be a non-tty,
// detached (setsid) ssh invocation of at-switchboard.
func TestLaunchTeammateDetachedTokenNeverOnArgv(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	// [0] the ssh auth probe (`claude auth status`) — already authed, so
	// ensureAuthenticated returns without an interactive login or a save.
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-authed\n"}}}

	const token = "shh-super-secret-token"
	err := LaunchTeammate(r, b, TeammateOptions{
		Container:      "box-helper",
		BotTokenSpec:   secret.Spec{Name: "DISCORD_BOT_TOKEN", Value: token, Literal: true},
		Channels:       []string{"111", "222"},
		ErrorChannel:   "111",
		IdentityFile:   "/id",
		KnownHostsFile: "/kh",
	})
	if err != nil {
		t.Fatalf("LaunchTeammate: %v", err)
	}

	// Security assertion (1): the token must NEVER appear on any command argv.
	for _, c := range r.Calls {
		for _, a := range c.Args {
			if strings.Contains(a, token) {
				t.Fatalf("token leaked onto argv: %+v", c)
			}
		}
	}

	// Security assertion (2): the token IS staged via ssh stdin (writeVM), not
	// dropped entirely — proves it reached the VM through the intended channel.
	var stagedViaStdin bool
	for _, c := range r.Calls {
		if strings.Contains(c.Stdin, token) {
			stagedViaStdin = true
		}
	}
	if !stagedViaStdin {
		t.Fatalf("expected the token staged via ssh stdin; calls=%+v", r.Calls)
	}

	// Security assertion (3): the actual launch is detached (setsid, no PTY).
	var detached bool
	for _, c := range r.Calls {
		if c.Name != "ssh" {
			continue
		}
		for _, a := range c.Args {
			if a == "-tt" {
				t.Fatalf("detached launch must not use a PTY: %+v", c)
			}
		}
		for _, a := range c.Args {
			if strings.Contains(a, "setsid") && strings.Contains(a, "at-switchboard") {
				detached = true
			}
		}
	}
	if !detached {
		t.Fatalf("expected a detached setsid at-switchboard launch over ssh; calls=%+v", r.Calls)
	}
}
