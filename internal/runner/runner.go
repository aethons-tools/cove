// Package runner abstracts execution of external commands so the rest of
// atsbx can be tested without sbx (or any binary) installed.
package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Runner executes a command, returning an error if it fails.
type Runner interface {
	Run(name string, args ...string) error
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

func (OS) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return err
}

// Call records a single Run invocation for the Fake runner.
type Call struct {
	Name string
	Args []string
}

// Fake is a test Runner that records every call and returns Err.
type Fake struct {
	Calls []Call
	Err   error
}

func (f *Fake) Run(name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	return f.Err
}
