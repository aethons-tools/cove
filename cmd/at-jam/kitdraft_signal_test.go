package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// After the first signal stops the following, a second one terminates the
// process by its default action, even with a request in flight. Run in a
// child process: the second SIGINT must kill it.
func TestFollowContextSecondSignalTerminates(t *testing.T) {
	if os.Getenv("AT_JAM_SIGNAL_CHILD") == "1" {
		ctx, stop := followContext()
		defer stop()
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond) // the handler is released after Done
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(5 * time.Second) // an in-flight request
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFollowContextSecondSignalTerminates$")
	cmd.Env = append(os.Environ(), "AT_JAM_SIGNAL_CHILD=1")
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("child = %v, want killed by SIGINT", err)
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("child status = %v, want killed by SIGINT", ee)
	}
}
