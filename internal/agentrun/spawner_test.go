package agentrun

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
}

func TestExecSpawnerCleanExit(t *testing.T) {
	needSh(t)
	p, err := execSpawner{grace: time.Second}.Spawn(context.Background(), "sh", []string{"-c", "exit 0"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: want nil, got %v", err)
	}
}

func TestExecSpawnerNonzeroExit(t *testing.T) {
	needSh(t)
	p, err := execSpawner{grace: time.Second}.Spawn(context.Background(), "sh", []string{"-c", "exit 3"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	var ee *exec.ExitError
	if err := p.Wait(); !errors.As(err, &ee) {
		t.Fatalf("Wait: want *exec.ExitError, got %v", err)
	}
}

func TestExecSpawnerCancelSIGTERM(t *testing.T) {
	needSh(t)
	ctx, cancel := context.WithCancel(context.Background())
	p, err := execSpawner{grace: 5 * time.Second}.Spawn(ctx, "sh", []string{"-c", "sleep 30"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait: want non-nil after cancel (SIGTERM), got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after SIGTERM")
	}
}

func TestExecSpawnerCancelSIGKILLAfterGrace(t *testing.T) {
	needSh(t)
	ctx, cancel := context.WithCancel(context.Background())
	// Ignores SIGTERM, so only the WaitDelay SIGKILL can stop it.
	p, err := execSpawner{grace: 200 * time.Millisecond}.Spawn(ctx, "sh", []string{"-c", "trap '' TERM; sleep 30"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait: want non-nil after SIGKILL, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after grace SIGKILL")
	}
}
