//go:build unix

package agentrun

import (
	"os/exec"
	"syscall"
)

// inOwnGroup puts the gate in its own process group and makes cancellation
// (the timeout) kill the whole group, so a gate's children can't outlive it.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// signalExit is 128+signal for a gate whose shell was killed by a signal, or
// 0 when it was not.
func signalExit(st *exec.ExitError) int {
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 0
}
