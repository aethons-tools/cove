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

func TestUnattendedWritesJSONToStderrNoFile(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, err := New(Options{Mode: Unattended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()
	lg.Info("hi", "k", 1)
	if !strings.Contains(errb.String(), `"msg":"hi"`) {
		t.Fatalf("unattended stderr should be JSON; got %q", errb.String())
	}
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Fatalf("unattended must not create the log file")
	}
}

func TestAttendedTextToStderrJSONToFile(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, err := New(Options{Mode: Attended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	lg.Info("hi", "k", 1)
	lg.Close()
	if strings.Contains(errb.String(), "{") {
		t.Fatalf("attended stderr should be human text, not JSON; got %q", errb.String())
	}
	b, _ := os.ReadFile(fp)
	if !strings.Contains(string(b), `"msg":"hi"`) {
		t.Fatalf("attended must write JSON to the file; got %q", string(b))
	}
}

func TestAttendedFileCapturesDebugWhileStderrDoesNot(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, _ := New(Options{Mode: Attended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	lg.Debug("verbose")
	lg.Close()
	if strings.Contains(errb.String(), "verbose") {
		t.Fatalf("stderr at info+ must not show debug; got %q", errb.String())
	}
	b, _ := os.ReadFile(fp)
	if !strings.Contains(string(b), "verbose") {
		t.Fatalf("file at debug+ must capture debug; got %q", string(b))
	}
}

// TestWithReturnsLoggerAndAddsAttrs pins two things a plain promoted
// slog.Logger.With would NOT give us: (1) With must return a *Logger (not a
// bare *slog.Logger) so the result still exposes Logger-only methods like
// UserError — that's what this test exercises via child.UserError; (2) the
// bound attrs must reach BOTH the tee logger and the structured-only sink,
// since UserError in Unattended mode logs through l.sink exclusively.
func TestWithReturnsLoggerAndAddsAttrs(t *testing.T) {
	var errb bytes.Buffer
	lg, err := New(Options{Mode: Unattended, Stderr: &errb, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	child := lg.With(slog.String("run", "run_AET-9_ab12"))
	child.UserError(context.Background(), errors.New("boom"))
	if !strings.Contains(errb.String(), `"run":"run_AET-9_ab12"`) {
		t.Fatalf("expected run attr (bound via With) on the sink-routed UserError record; got %q", errb.String())
	}
	// The parent logger must be unaffected (With returns a distinct child).
	errb.Reset()
	lg.UserError(context.Background(), errors.New("boom2"))
	if strings.Contains(errb.String(), "run_AET-9_ab12") {
		t.Fatalf("With must not mutate the parent logger; got %q", errb.String())
	}
}

// TestWithAddsAttrsToFileSink covers the Attended-mode case, where sink is
// the separate file-only JSON handler (distinct from the human tee to
// stderr) — With must bind attrs onto that sink too, not just the tee.
func TestWithAddsAttrsToFileSink(t *testing.T) {
	var errb bytes.Buffer
	dir := t.TempDir()
	fp := filepath.Join(dir, "x.jsonl")
	lg, err := New(Options{Mode: Attended, Stderr: &errb, FilePath: fp, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	child := lg.With(slog.String("run", "run_AET-9_ab12"))
	child.Info("hi")
	lg.Close()
	b, _ := os.ReadFile(fp)
	if !strings.Contains(string(b), `"run":"run_AET-9_ab12"`) {
		t.Fatalf("expected run attr on file sink from With; got %q", string(b))
	}
}
