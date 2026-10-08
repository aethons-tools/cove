package agentrun

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// A turn that exits non-zero is a FAILED
// turn (auth/model error, crash) — it must be logged loudly at WARN with the
// exit, not as a benign INFO idle wait.
func TestLogTurnFailedExitIsLoud(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w := New(Config{WorkDir: t.TempDir()}, log)
	w.logTurn(errors.New("exit status 1"))
	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("a failed resident turn (non-zero exit, no result) must log WARN:\n%s", out)
	}
	if !strings.Contains(out, "exit status 1") {
		t.Fatalf("the failure log must include the exit error:\n%s", out)
	}
}

// A resident turn that exits cleanly (waitErr nil) but wrote no result is not a
// crash — it stays INFO (no false WARN/ERROR alarm).
func TestLogTurnCleanStaysInfo(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w := New(Config{WorkDir: t.TempDir()}, log)
	w.logTurn(nil)
	out := buf.String()
	if strings.Contains(out, "level=WARN") || strings.Contains(out, "level=ERROR") {
		t.Fatalf("a clean resident turn with no result must not be WARN/ERROR:\n%s", out)
	}
}
