// Package connect launches jam-studio-agent inside a raised studio over ssh
// (LaunchStudioAgent), staging its prompt, context and env into tmpfs.
package connect

import (
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// writeVM writes data to vmPath in the VM over ssh stdin (mode 077, never on
// argv), so secrets never touch argv or persistent disk.
func writeVM(r runner.Runner, tgt sshargs.Target, data, vmPath string) error {
	args := append(sshargs.Base(tgt), "umask 077; cat > "+vmPath)
	return r.RunStdin(strings.NewReader(data), "ssh", args...)
}

// shellQuote single-quotes s for safe inclusion in a POSIX shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
