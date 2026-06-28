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

	file := fmt.Sprintf("/dev/shm/cove-setup-%s-%d", tgt.Host, tgt.Port)
	writeArgs := append(sshargs.Base(tgt), "umask 077; cat > "+file)
	if err := r.RunStdin(strings.NewReader(envScript(env)), "ssh", writeArgs...); err != nil {
		return fmt.Errorf("writing setup env: %w", err)
	}
	remote := "set -a; . " + file + "; rm -f " + file + "; cd " + workspaceDir + " && " + setupCmd
	if err := r.RunStdin(nil, "ssh", append(sshargs.Base(tgt), remote)...); err != nil {
		return fmt.Errorf("running setup (%s): %w", setupCmd, err)
	}
	return nil
}
