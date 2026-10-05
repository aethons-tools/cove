// Package agentrun is cove-master's agent wrapper: a covemaster.Workload that
// runs the agent headless through a Harness (the agent-CLI seam; default
// Claude) as a sequence of episodes and maps its lifecycle onto the Attach
// Activity stream.
// An episode stays open while the agent works or has background tasks, and
// takes Wakes as stdin messages. It depends on internal/covemaster (the
// Workload seam); it never imports internal/jam.
package agentrun

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Spawner launches the agent process. Production uses execSpawner; tests inject
// a fake. Spawn returns once the process has started (or failed to start).
type Spawner interface {
	// Spawn starts bin. stdout, when non-nil, receives the process's stdout
	// instead of cove-master's own stdout; nil falls back to os.Stdout.
	Spawn(ctx context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error)
}

// Process is a started agent process. Wait blocks until it exits, returning the
// process's exit error (nil on exit 0). If ctx is cancelled, the runtime sends
// SIGTERM then SIGKILL (after grace) and Wait returns a non-nil error. Input is
// the process's stdin: closing it signals EOF; a write after the process has
// exited fails.
type Process interface {
	Wait() error
	Input() io.WriteCloser
}

// execSpawner launches the agent as a real child process. On ctx cancellation it
// sends SIGTERM, then SIGKILL after grace (via exec.Cmd.WaitDelay).
type execSpawner struct{ grace time.Duration }

type execProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func (s execSpawner) Spawn(ctx context.Context, bin string, args []string, dir string, env []string, stdout io.Writer) (Process, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env // nil inherits cove-master's env
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = s.grace
	cmd.Stdout = os.Stdout
	if stdout != nil {
		// Not an *os.File, so exec copies through a pipe and Wait returns only
		// after the copy drains — every line reaches stdout before Wait returns.
		// Deliberately NOT also os.Stdout: that is cove-master.log, which Jam
		// tails into its own log; raw agent output must stay out of it.
		cmd.Stdout = stdout
	}
	cmd.Stderr = os.Stderr
	// StdinPipe (not cmd.Stdin = reader): Wait closes it once the child exits,
	// so a child that exits on its own never leaves Wait blocked on a copy.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProcess{cmd: cmd, stdin: stdin}, nil
}

func (p execProcess) Wait() error           { return p.cmd.Wait() }
func (p execProcess) Input() io.WriteCloser { return p.stdin }
