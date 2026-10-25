// Package runner abstracts execution of external commands so the rest of
// cove can be tested without sbx (or any binary) installed.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Runner executes external commands.
type Runner interface {
	Run(name string, args ...string) error
	// RunEnv is Run with extra "KEY=VALUE" entries appended to the child env.
	RunEnv(extraEnv []string, name string, args ...string) error
	// Output runs the command and returns its captured stdout.
	Output(name string, args ...string) (string, error)
	// OutputEnv is Output with extra "KEY=VALUE" entries appended to the child env.
	OutputEnv(extraEnv []string, name string, args ...string) (string, error)
	// Probe runs the command purely for its exit status, discarding both stdout
	// and stderr. Use it for reachability checks (e.g. `docker info`) whose
	// output — including benign daemon warnings on stderr — would otherwise leak
	// to the terminal, unlike Output which streams the child's stderr live.
	Probe(name string, args ...string) error
	// RunStdin is Run with the child's stdin connected to the given reader
	// (or the process's os.Stdin when stdin is nil, for interactive use).
	RunStdin(stdin io.Reader, name string, args ...string) error
	// RunIO is the general form of Run: the caller supplies the child's stdin
	// reader and its stdout/stderr writers. A nil stream falls back to the
	// process default (os.Stdin/os.Stdout/os.Stderr), so RunIO(nil, nil, nil, …)
	// behaves exactly like Run. It is the injection seam a host caller uses to
	// capture a child's output — e.g. redirect an SSH channel into a buffer —
	// instead of letting it spill to the host console.
	RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

// orReader returns r, or def when r is nil.
func orReader(r, def io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return def
}

// orWriter returns w, or def when w is nil.
func orWriter(w, def io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return def
}

// ExitError reports that a command exited with a non-zero status. It carries
// the child's exit code so the caller can propagate it.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return fmt.Sprintf("command exited with status %d", e.Code) }
func (e *ExitError) Unwrap() error { return e.Err }
func (e *ExitError) ExitCode() int { return e.Code }

// OS is the production Runner: it shells out, streaming stdio live and
// translating a non-zero exit into *ExitError.
type OS struct{}

func (o OS) Run(name string, args ...string) error {
	return o.RunIO(nil, nil, nil, name, args...)
}

func (OS) RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = orReader(stdin, os.Stdin)
	cmd.Stdout = orWriter(stdout, os.Stdout)
	cmd.Stderr = orWriter(stderr, os.Stderr)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return err
}

func (OS) RunEnv(extraEnv []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return err
}

func (OS) Output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return string(out), err
}

func (OS) OutputEnv(extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return string(out), err
}

func (OS) Probe(name string, args ...string) error {
	// stdout/stderr left nil so exec discards them: a probe cares only about the
	// exit status, and the child's stderr (docker daemon warnings, etc.) is noise.
	cmd := exec.Command(name, args...)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return err
}

func (o OS) RunStdin(stdin io.Reader, name string, args ...string) error {
	return o.RunIO(stdin, nil, nil, name, args...)
}

// Call records a single Run invocation for the Fake runner.
type Call struct {
	Name   string
	Args   []string
	Env    []string
	Stdin  string    // bytes the caller piped via RunStdin/RunIO (empty for other methods or a nil reader)
	Stdout io.Writer // stdout writer the caller injected via RunIO (nil if none)
	Stderr io.Writer // stderr writer the caller injected via RunIO (nil if none)
}

// FakeResult is one queued result for Fake.Output.
type FakeResult struct {
	Stdout string
	Stderr string // streamed to RunIO's stderr writer, when one is injected
	Err    error
}

// Fake is a test Runner that records every call and returns Err.
type Fake struct {
	Calls   []Call
	Err     error
	Outputs []FakeResult // consumed in order by Output
	// OnRun, when set, runs inside every RunIOContext call once it is
	// recorded: a test holds a command on its ctx, or fails one.
	OnRun func(ctx context.Context, c Call) error
	out   int
}

func (f *Fake) Run(name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	return f.Err
}

func (f *Fake) RunEnv(extraEnv []string, name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...), Env: append([]string(nil), extraEnv...)})
	return f.Err
}

func (f *Fake) RunStdin(stdin io.Reader, name string, args ...string) error {
	return f.RunIO(stdin, nil, nil, name, args...)
}

func (f *Fake) RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	f.recordIO(stdin, stdout, stderr, name, args)
	return f.streamIO(stdout, stderr)
}

// recordIO records a RunIO-shaped call, reading stdin to its end.
func (f *Fake) recordIO(stdin io.Reader, stdout, stderr io.Writer, name string, args []string) Call {
	var in string
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	c := Call{Name: name, Args: append([]string(nil), args...), Stdin: in, Stdout: stdout, Stderr: stderr}
	f.Calls = append(f.Calls, c)
	return c
}

// streamIO is RunIO's result: the next queued result streamed to the
// injected writers, when there is a stdout writer, else just Err.
func (f *Fake) streamIO(stdout, stderr io.Writer) error {
	// Mirror OS: when a caller injects a stdout writer, stream the next queued
	// output to it so a host test can read the redirected child output back.
	if stdout != nil && f.out < len(f.Outputs) {
		r := f.Outputs[f.out]
		f.out++
		_, _ = io.WriteString(stdout, r.Stdout)
		if stderr != nil {
			_, _ = io.WriteString(stderr, r.Stderr)
		}
		if r.Err != nil {
			return r.Err
		}
	}
	return f.Err
}

func (f *Fake) Probe(name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	return f.Err
}

func (f *Fake) Output(name string, args ...string) (string, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	if f.out < len(f.Outputs) {
		r := f.Outputs[f.out]
		f.out++
		return r.Stdout, r.Err
	}
	return "", f.Err
}

func (f *Fake) OutputEnv(extraEnv []string, name string, args ...string) (string, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...), Env: append([]string(nil), extraEnv...)})
	if f.out < len(f.Outputs) {
		r := f.Outputs[f.out]
		f.out++
		return r.Stdout, r.Err
	}
	return "", f.Err
}

// ContextRunner is a Runner that can run a command under a context: a done
// ctx stops the child, and the call returns ctx.Err(). Scoped uses it.
type ContextRunner interface {
	RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

// osWaitDelay is how long a cancelled child has to exit after its
// interrupt before it is killed.
const osWaitDelay = 10 * time.Second

// RunIOContext is RunIO under ctx: when ctx is done the child gets an
// interrupt, and a kill osWaitDelay later; the call then returns ctx.Err().
func (OS) RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = orReader(stdin, os.Stdin)
	cmd.Stdout = orWriter(stdout, os.Stdout)
	cmd.Stderr = orWriter(stderr, os.Stderr)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = osWaitDelay
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return err
}

// RunIOContext is RunIO under ctx: it records the call, fails with
// ctx.Err() when ctx is done, runs OnRun, then answers exactly as RunIO
// does (the next queued result streamed to the writers when stdout is
// given). Scoped always gives a stdout, so through Scoped every command —
// Run and Probe included, which consume nothing on a bare Fake — takes one
// queued result: a scripted docker sequence is one result per command.
func (f *Fake) RunIOContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	c := f.recordIO(stdin, stdout, stderr, name, args)
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.OnRun != nil {
		if err := f.OnRun(ctx, c); err != nil {
			return err
		}
	}
	return f.streamIO(stdout, stderr)
}

// Scoped is r with every command run under ctx and its output copied to
// log. That is Run's stdout and stderr, and Output's and RunIO's stderr;
// their stdout stays the caller's. Probe's output is dropped. A done ctx
// stops a running command (when r is a ContextRunner) or refuses the next
// one. RunEnv and OutputEnv only check ctx (no build uses them). It is how
// a kit draft test build streams and cancels its docker commands without
// changing the build code. log must be safe for concurrent writes (a
// command's stdout and stderr); nil drops the output.
func Scoped(ctx context.Context, r Runner, log io.Writer) Runner {
	if log == nil {
		log = io.Discard
	}
	return scoped{ctx: ctx, r: r, log: log}
}

type scoped struct {
	ctx context.Context
	r   Runner
	log io.Writer
}

func (s scoped) runIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	if cr, ok := s.r.(ContextRunner); ok {
		return cr.RunIOContext(s.ctx, stdin, stdout, stderr, name, args...)
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.r.RunIO(stdin, stdout, stderr, name, args...)
}

func (s scoped) Run(name string, args ...string) error {
	return s.runIO(nil, s.log, s.log, name, args...)
}

func (s scoped) RunStdin(stdin io.Reader, name string, args ...string) error {
	return s.runIO(stdin, s.log, s.log, name, args...)
}

func (s scoped) RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	return s.runIO(stdin, orWriter(stdout, s.log), tee(stderr, s.log), name, args...)
}

func (s scoped) Output(name string, args ...string) (string, error) {
	var out bytes.Buffer
	err := s.runIO(nil, &out, s.log, name, args...)
	return out.String(), err
}

func (s scoped) Probe(name string, args ...string) error {
	return s.runIO(nil, io.Discard, io.Discard, name, args...)
}

func (s scoped) RunEnv(extraEnv []string, name string, args ...string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.r.RunEnv(extraEnv, name, args...)
}

func (s scoped) OutputEnv(extraEnv []string, name string, args ...string) (string, error) {
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	return s.r.OutputEnv(extraEnv, name, args...)
}

// tee is w and log both, or log alone when w is nil.
func tee(w, log io.Writer) io.Writer {
	if w == nil {
		return log
	}
	return io.MultiWriter(w, log)
}
