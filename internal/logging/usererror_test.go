package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserErrorAttendedHumanLinePlusFileRecordNoStderrTwin(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, _ := New(Options{Mode: Attended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	lg.UserError(context.Background(), errors.New("bad token"), slog.String("step", "secrets"))
	lg.Close()

	if !strings.Contains(errb.String(), "at-cove: bad token") {
		t.Fatalf("human line missing from stderr; got %q", errb.String())
	}
	if strings.Count(errb.String(), "bad token") != 1 {
		t.Fatalf("error must appear on stderr exactly once (no structured twin); got %q", errb.String())
	}
	b, _ := os.ReadFile(fp)
	if !strings.Contains(string(b), `"step":"secrets"`) || !strings.Contains(string(b), `"level":"ERROR"`) {
		t.Fatalf("structured error record missing from file; got %q", string(b))
	}
}

func TestUserErrorUnattendedStructuredOnlyNoHumanLine(t *testing.T) {
	var errb bytes.Buffer
	lg, _ := New(Options{Mode: Unattended, Stderr: &errb, Level: slog.LevelInfo})
	lg.UserError(context.Background(), errors.New("bad token"), slog.String("step", "secrets"))
	if strings.Contains(errb.String(), "at-cove: bad token") {
		t.Fatalf("unattended must not print a human line; got %q", errb.String())
	}
	if !strings.Contains(errb.String(), `"step":"secrets"`) {
		t.Fatalf("unattended must emit a structured record to stderr; got %q", errb.String())
	}
}
