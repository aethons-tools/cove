// Package logging provides structured slog-based logging for the cove
// binaries: an attended (human, colorized stderr) mode and an unattended
// (plain stderr plus an optional JSON debug-level file sink) mode, along
// with correlation helpers for tagging related log lines across a run.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

type Mode int

const (
	Auto Mode = iota
	Attended
	Unattended
)

// ModeFrom maps a --log-mode flag value to a Mode. An unrecognized value falls
// back to Auto (TTY auto-detect). Shared by the at-cove and at-task CLIs.
func ModeFrom(s string) Mode {
	switch s {
	case "attended":
		return Attended
	case "unattended":
		return Unattended
	default:
		return Auto
	}
}

// LevelFrom maps a --log-level flag value to a slog.Level. An unrecognized value
// falls back to slog.LevelInfo. Shared by the at-cove and at-task CLIs.
func LevelFrom(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// EnvOr returns flag when it is non-empty, else os.Getenv(key) — the env fallback
// for a global logging flag left at its zero value (AT_LOG_MODE, AT_LOG_LEVEL).
func EnvOr(flag, key string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv(key)
}

type Options struct {
	Mode     Mode
	Stderr   io.Writer
	FilePath string
	Level    slog.Level
}

type Logger struct {
	*slog.Logger
	sink   *slog.Logger
	human  io.Writer
	mode   Mode
	closer io.Closer
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func New(o Options) (*Logger, error) {
	mode := o.Mode
	if mode == Auto {
		if isTerminal(o.Stderr) {
			mode = Attended
		} else {
			mode = Unattended
		}
	}

	if mode == Unattended {
		h := slog.NewJSONHandler(o.Stderr, &slog.HandlerOptions{Level: o.Level})
		lg := slog.New(h)
		return &Logger{Logger: lg, sink: lg, human: o.Stderr, mode: mode}, nil
	}

	// Attended: text→stderr @ Level (skipping user-shown twins), JSON→file @ Debug.
	stderrH := skipUserShown(slog.NewTextHandler(o.Stderr, &slog.HandlerOptions{Level: o.Level}))
	handlers := []slog.Handler{stderrH}
	var closer io.Closer
	var sink *slog.Logger
	if o.FilePath != "" {
		if err := os.MkdirAll(filepath.Dir(o.FilePath), 0o755); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
		f, err := os.OpenFile(o.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		fileH := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
		handlers = append(handlers, fileH)
		closer = f
		sink = slog.New(fileH) // structured-only sink: file (never re-hits stderr)
	}
	lg := slog.New(newMulti(handlers...))
	if sink == nil {
		sink = lg // no file: sink falls back to the tee
	}
	return &Logger{Logger: lg, sink: sink, human: o.Stderr, mode: mode, closer: closer}, nil
}

// With returns a child Logger with attrs bound to both the tee logger (the
// embedded *slog.Logger, used for human/JSON output) and the structured-only
// sink (used by UserError etc). mode/human/closer are copied unchanged — the
// child is a view onto the same destinations, not an independently closable
// Logger; only the top-level Logger returned by New should be Close()d.
func (l *Logger) With(attrs ...slog.Attr) *Logger {
	as := make([]any, 0, len(attrs))
	for _, a := range attrs {
		as = append(as, a)
	}
	child := *l
	child.Logger = l.Logger.With(as...)
	child.sink = l.sink.With(as...)
	return &child
}

// Close closes the attended-mode log file if one is open. It is idempotent:
// the closer is cleared after the first call, so a second Close (e.g. a deferred
// Close after an explicit one) is a no-op rather than a double os.File.Close.
func (l *Logger) Close() error {
	if l.closer == nil {
		return nil
	}
	c := l.closer
	l.closer = nil
	return c.Close()
}
