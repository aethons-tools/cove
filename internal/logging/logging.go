package logging

import (
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
			return nil, err
		}
		f, err := os.OpenFile(o.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
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

func (l *Logger) Close() error {
	if l.closer != nil {
		return l.closer.Close()
	}
	return nil
}
