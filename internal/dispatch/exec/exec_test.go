package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunInjectsEnvAndRuns(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	env := []string{"DISPATCH_ISSUE=AET-7", "DISPATCH_RESULT=" + out}
	// a command that writes $DISPATCH_ISSUE to $DISPATCH_RESULT
	err := New().Run(context.Background(), []string{"sh", "-c", `printf '%s' "$DISPATCH_ISSUE" > "$DISPATCH_RESULT"`}, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "AET-7" {
		t.Fatalf("output = %q; want AET-7 (env not injected?)", got)
	}
}

func TestRunNonZeroExitIsError(t *testing.T) {
	if err := New().Run(context.Background(), []string{"sh", "-c", "exit 3"}, nil); err == nil {
		t.Fatal("expected an error for non-zero exit")
	}
}

func TestRunTimeoutIsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := New().Run(ctx, []string{"sh", "-c", "sleep 5"}, nil); err == nil {
		t.Fatal("expected an error when the command exceeds the deadline")
	}
}

func TestRunEmptyArgv(t *testing.T) {
	if err := New().Run(context.Background(), nil, nil); err == nil {
		t.Fatal("expected an error for empty argv")
	}
}
