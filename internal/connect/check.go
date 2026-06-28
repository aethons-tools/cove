package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// checkTriggerMark is echoed on stdout when the check exits 0, so a true trigger
// is distinguishable from an ssh connection failure (which yields no marker and
// a non-nil error). The check's own output is discarded.
const checkTriggerMark = "cove-trigger"

// RunCheck runs the loop's check command in the workspace with secrets injected
// and reports whether it triggered (the check exited 0). A non-zero check is a
// normal "no work" result (false, nil); only an ssh/connection failure returns
// an error.
func RunCheck(r runner.Runner, tgt sshargs.Target, env map[string]string, checkCmd string) (bool, error) {
	file := fmt.Sprintf("/dev/shm/cove-check-%s-%d", tgt.Host, tgt.Port)
	writeArgs := append(sshargs.Base(tgt), "umask 077; cat > "+file)
	if err := r.RunStdin(strings.NewReader(envScript(env)), "ssh", writeArgs...); err != nil {
		return false, fmt.Errorf("writing check env: %w", err)
	}
	remote := "set -a; . " + file + "; rm -f " + file + "; cd " + workspaceDir +
		" && if " + checkCmd + " >/dev/null 2>&1; then echo " + checkTriggerMark + "; fi"
	out, err := r.Output("ssh", append(sshargs.Base(tgt), remote)...)
	if err != nil {
		return false, fmt.Errorf("running check: %w", err)
	}
	return strings.Contains(out, checkTriggerMark), nil
}
