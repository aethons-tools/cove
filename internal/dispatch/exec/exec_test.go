package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// A failing command's output tail must be folded into the returned error, so a
// failed worker run is self-diagnosing from the tracker (not only the terminal).
func TestRunNonZeroExitIncludesOutputTail(t *testing.T) {
	err := New().Run(context.Background(), []string{"sh", "-c", "echo boom-marker >&2; exit 2"}, nil)
	if err == nil {
		t.Fatal("expected an error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom-marker") {
		t.Fatalf("error should include the command's output tail; got: %v", err)
	}
}
