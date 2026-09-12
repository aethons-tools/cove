package connect

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/harbor/snippet"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// teammateEnvVMPath is the tmpfs file the bot token + channels are staged into,
// sourced then removed by the detached launch command (never written to disk,
// never on argv — mirrors cloneEnvVMPath).
const teammateEnvVMPath = "/dev/shm/cove-teammate-env"

// teammateLogVMPath is where the detached at-switchboard process's stdout/stderr
// go, since its own ssh channel closes immediately after launch (fire-and-forget).
const teammateLogVMPath = "/agent-data/switchboard.log"

// TeammateOptions carries what LaunchTeammate needs (all host-side inputs). It
// has no SkipAuth/dry-run knob: the caller (doTeammate) decides those before
// calling in.
type TeammateOptions struct {
	Container       string
	BotTokenSpec    secret.Spec
	Channels        []string
	ErrorChannel    string
	IdentityFile    string
	KnownHostsFile  string
	CredentialsFile string
	// Stderr is where ensureAuthenticated's best-effort "could not save
	// credentials" warning goes; nil defaults to os.Stderr (mirrors Options.Stderr
	// in connect.go). Kept nil-safe here rather than passed as a bare nil to
	// ensureAuthenticated, which would panic on that warning path.
	Stderr io.Writer
	// HarborHost/HarborToken, when set, route the conductor's Anthropic + git
	// through a harbor broker (COV-142), superseding the OAuth login: the connector
	// env is staged and git is configured. Token pre-supplied only (teammate
	// auto-enroll is unsupported — no exit hook to revoke on).
	HarborHost  string
	HarborToken string
}

// detachedLaunchCmd builds the remote shell command: source the tmpfs env,
// remove it, then start at-switchboard detached (setsid, stdin from /dev/null,
// output appended to a log) so it survives the ssh channel closing. channels and
// errorChannel are already exported into the env file by the caller (LaunchTeammate)
// before this command runs; they're accepted here only so the builder itself is
// self-describing and directly testable without staging a real env file.
func detachedLaunchCmd(envVMPath string, channels []string, errorChannel string) string {
	_ = channels
	_ = errorChannel
	return "set -a; . " + envVMPath + "; set +a; rm -f " + envVMPath + "; " +
		"setsid nohup at-switchboard </dev/null >>" + teammateLogVMPath + " 2>&1 &"
}

// LaunchTeammate dials the container, ensures the saved login is seeded (reusing
// the one-time `at-cove chat` login — no bearer secret for the conductor), stages
// the bot token + channels into tmpfs over ssh stdin, and starts at-switchboard
// detached over a non-tty ssh so the command returns as soon as the conductor is
// backgrounded (fire-and-forget) rather than blocking on a session.
//
// Deliberately does NOT reuse connect.Connect: Connect's aw.Inhibit and
// saveCredentials-after-Launch assume a blocking interactive session, which a
// detached background launch is not.
func LaunchTeammate(r runner.Runner, b backend.Backend, o TeammateOptions) error {
	ep, cleanup, err := b.Dial(o.Container)
	if err != nil {
		return err
	}
	defer cleanup()

	tgt := sshargs.Target{
		Host:           ep.Host,
		User:           ep.User,
		Port:           ep.Port,
		IdentityFile:   o.IdentityFile,
		KnownHostsFile: o.KnownHostsFile,
	}

	stderr := o.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	// Harbor supersedes the OAuth login: route git through harbor (token-free
	// config) and skip the auth probe; the connector env is staged below.
	if o.HarborHost != "" {
		if err := applyHarborGit(r, tgt, &HarborAuth{Host: o.HarborHost, Token: o.HarborToken}); err != nil {
			return err
		}
	} else if err := ensureAuthenticated(r, tgt, o.CredentialsFile, stderr); err != nil {
		return fmt.Errorf("teammate auth: %w", err)
	}

	// Resolve ONLY the bot token — it never enters the agent's session env (there
	// is none here; the conductor is a standing process, not an interactive
	// launch), and it never touches disk or argv: it flows into a tmpfs file over
	// ssh stdin, exactly like the workspace-clone git token (ensureWorkspace).
	env, err := secret.Resolve(r, nil, []secret.Spec{o.BotTokenSpec})
	if err != nil {
		return err
	}
	var script strings.Builder
	fmt.Fprintf(&script, "export DISCORD_BOT_TOKEN=%s\n", shellQuote(env[o.BotTokenSpec.Name]))
	fmt.Fprintf(&script, "export SWITCHBOARD_CHANNELS=%s\n", shellQuote(strings.Join(o.Channels, ",")))
	if o.ErrorChannel != "" {
		fmt.Fprintf(&script, "export SWITCHBOARD_ERROR_CHANNEL=%s\n", shellQuote(o.ErrorChannel))
	}
	// The harbor connector env (Anthropic base URL + x-api-key/token) is sourced
	// with the rest — env-only, never argv.
	if o.HarborHost != "" {
		script.WriteString(envScript(snippet.Env("https://"+o.HarborHost, o.HarborToken)))
	}
	if err := writeVM(r, tgt, script.String(), teammateEnvVMPath); err != nil {
		return err
	}

	cmd := detachedLaunchCmd(teammateEnvVMPath, o.Channels, o.ErrorChannel)
	if err := r.Run("ssh", append(sshargs.Base(tgt), cmd)...); err != nil {
		return fmt.Errorf("teammate launch: %w", err)
	}
	return nil
}
