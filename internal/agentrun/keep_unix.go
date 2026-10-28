//go:build unix

package agentrun

import (
	"os/exec"
	"syscall"
)

// keepGroup starts a kept process in its own process group, so stopping it
// stops its children too.
func keepGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// signalGroup signals the process group pgid; term false sends SIGKILL.
func signalGroup(pgid int, term bool) error {
	sig := syscall.SIGKILL
	if term {
		sig = syscall.SIGTERM
	}
	return syscall.Kill(-pgid, sig)
}
