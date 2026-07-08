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
	env := []string{"FOO=bar", "BAZ=" + out}
	// a command that writes $FOO to $BAZ
	err := New().Run(context.Background(), []string{"sh", "-c", `printf '%s' "$FOO" > "$BAZ"`}, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "bar" {
		t.Fatalf("output = %q; want bar (env not injected?)", got)
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
