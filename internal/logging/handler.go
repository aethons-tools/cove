package logging

import (
	"context"
	"log/slog"
)

const userShownKey = "user_shown"

type multiHandler struct{ hs []slog.Handler }

func newMulti(hs ...slog.Handler) slog.Handler { return multiHandler{hs: hs} }

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.hs {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range m.hs {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m multiHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		out[i] = h.WithAttrs(as)
	}
	return multiHandler{hs: out}
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		out[i] = h.WithGroup(name)
	}
	return multiHandler{hs: out}
}

type skipHandler struct{ slog.Handler }

func skipUserShown(h slog.Handler) slog.Handler { return skipHandler{Handler: h} }

func (s skipHandler) Handle(ctx context.Context, r slog.Record) error {
	shown := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == userShownKey && a.Value.Bool() {
			shown = true
			return false
		}
		return true
	})
	if shown {
		return nil
	}
	return s.Handler.Handle(ctx, r)
}

func (s skipHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return skipHandler{Handler: s.Handler.WithAttrs(as)}
}
func (s skipHandler) WithGroup(name string) slog.Handler {
	return skipHandler{Handler: s.Handler.WithGroup(name)}
}
