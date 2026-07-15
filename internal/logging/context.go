package logging

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey struct{}

func Into(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

func From(ctx context.Context) *Logger {
	if l, ok := ctx.Value(ctxKey{}).(*Logger); ok && l != nil {
		return l
	}
	l, _ := New(Options{Mode: Unattended, Stderr: io.Discard, Level: slog.LevelInfo})
	return l
}
