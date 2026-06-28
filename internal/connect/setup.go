package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// setupEmptyProbe reports whether the workspace is empty. It echoes a marker on
// stdout either way, so a non-zero ssh exit means the connection failed, not
// "non-empty". ls -A omits . and .. so a pristine volume reads as empty.
const setupEmptyProbe = `[ -z "$(ls -A ` + workspaceDir + ` 2>/dev/null)" ] && echo cove-empty || echo cove-nonempty`

const setupEmptyMark = "cove-empty"

// runInjected writes env to a tmpfs script over ssh (values arrive via stdin,
// never on argv), then runs remoteTail in a shell that sources and removes the
// script first. The run attaches EMPTY stdin, so a credential or other prompt
// fails fast instead of hanging an unattended caller. label distinguishes the
// tmpfs file and error messages across concurrent uses.
func runInjected(r runner.Runner, tgt sshargs.Target, env map[string]string, label, remoteTail string) error {
	file := fmt.Sprintf("/dev/shm/cove-%s-%s-%d", label, tgt.Host, tgt.Port)
	writeArgs := append(sshargs.Base(tgt), "umask 077; cat > "+file)
	if err := r.RunStdin(strings.NewReader(envScript(env)), "ssh", writeArgs...); err != nil {
		return fmt.Errorf("writing %s env: %w", label, err)
	}
	remote := "set -a; . " + file + "; rm -f " + file + "; " + remoteTail
	if err := r.RunStdin(strings.NewReader(""), "ssh", append(sshargs.Base(tgt), remote)...); err != nil {
		return fmt.Errorf("running %s: %w", label, err)
	}
	return nil
}

// RunSetup populates an isolated workspace by running setupCmd in it, once,
// when the workspace is empty. It is a no-op when setupCmd is empty or the
// workspace already has contents (so reconnects don't re-clone). Secrets are
// injected via a tmpfs script sourced in the remote shell — values never reach
// argv — so a private `git clone` authenticates through the in-VM credential
// helper. A non-interactive ssh runs the command; its stdout/stderr stream live.
func RunSetup(r runner.Runner, tgt sshargs.Target, env map[string]string, setupCmd string) error {
	if setupCmd == "" {
		return nil
	}
	out, err := r.Output("ssh", append(sshargs.Base(tgt), setupEmptyProbe)...)
	if err != nil {
		return fmt.Errorf("checking workspace before setup: %w", err)
	}
	if !strings.Contains(out, setupEmptyMark) {
		return nil // already populated; leave it alone
	}
	return runInjected(r, tgt, env, "setup", "cd "+workspaceDir+" && "+setupCmd)
}
