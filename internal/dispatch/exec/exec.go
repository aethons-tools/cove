// Package exec runs dispatch commands headlessly with an injected environment and a
// context timeout. It is at-dispatch's real scheduler.Executor.
package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
)

// Executor runs commands via os/exec. Command stdout/stderr stream to Log (default
// os.Stderr) for observability; the command's structured result goes to its
// DISPATCH_RESULT file, not stdout.
type Executor struct {
	Log io.Writer
}

func New() *Executor { return &Executor{Log: os.Stderr} }

// Run executes argv with env (appended to the parent environment so the command
// still has PATH etc.), bounded by ctx. Returns nil on exit 0, else an error.
func (e *Executor) Run(ctx context.Context, argv []string, env []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("exec: empty command")
	}
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	w := e.Log
	if w == nil {
		w = os.Stderr
	}
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}
