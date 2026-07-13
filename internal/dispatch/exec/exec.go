// Package exec runs dispatch commands headlessly with an injected environment and a
// context timeout. It is at-cove dispatch's real scheduler.Executor.
package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
)

// Executor runs commands via os/exec with an injected environment and context timeout.
// Command stdout/stderr stream to Log (default os.Stderr) for observability.
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
	// Stream the child's output to the log for live observability, and keep a
	// bounded tail so a non-zero exit's error carries *why* it failed (the worker's
	// last output) rather than a bare "exit status N".
	tail := &tailWriter{max: 4096}
	sink := io.MultiWriter(w, tail)
	cmd.Stdout = sink
	cmd.Stderr = sink
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(tail.String()); s != "" {
			return fmt.Errorf("%w; command output:\n%s", err, s)
		}
		return err
	}
	return nil
}

// tailWriter keeps only the last max bytes written to it — a bounded tail of a
// command's output for error diagnostics, discarding earlier bytes so a chatty
// agent step can't grow it without limit.
type tailWriter struct {
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.buf) }
