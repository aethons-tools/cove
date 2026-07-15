package logging

import (
	"bytes"
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
