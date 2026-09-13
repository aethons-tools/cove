// Package agentrun is cove-master's agent wrapper: a covemaster.Workload that
// runs the claude agent as a headless one-shot and maps its lifecycle onto the
// Attach Activity stream. It depends on internal/covemaster (the Workload seam)
// and internal/dispatch/worker (the worker-result.json contract); it never
// imports internal/harbor.
package agentrun

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Spawner launches the agent process. Production uses execSpawner; tests inject
// a fake. Spawn returns once the process has started (or failed to start).
type Spawner interface {
	Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error)
}

// Process is a started agent process. Wait blocks until it exits, returning the
// process's exit error (nil on exit 0). If ctx is cancelled, the runtime sends
// SIGTERM then SIGKILL (after grace) and Wait returns a non-nil error.
type Process interface {
	Wait() error
}

// execSpawner launches the agent as a real child process. On ctx cancellation it
// sends SIGTERM, then SIGKILL after grace (via exec.Cmd.WaitDelay).
type execSpawner struct{ grace time.Duration }

type execProcess struct{ cmd *exec.Cmd }

func (s execSpawner) Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = s.grace
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProcess{cmd: cmd}, nil
}

func (p execProcess) Wait() error { return p.cmd.Wait() }
