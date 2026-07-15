package logging

import (
	"context"
	"fmt"
	"log/slog"
)

func (l *Logger) UserError(ctx context.Context, err error, attrs ...slog.Attr) {
	if l.mode == Unattended {
		l.sink.LogAttrs(ctx, slog.LevelError, err.Error(), attrs...)
		return
	}
	fmt.Fprintf(l.human, "at-cove: %s\n", err.Error())
	l.Logger.LogAttrs(ctx, slog.LevelError, err.Error(), append(attrs, slog.Bool(userShownKey, true))...)
}
