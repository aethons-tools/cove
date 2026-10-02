package sessionevents

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// ParseRetention parses session-events-retention: "" (keep forever → 0),
// "<N>d" (days), or any time.ParseDuration string. Negative is an error.
func ParseRetention(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("session-events-retention %q: %w", s, err)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("session-events-retention %q: %w", s, err)
		}
	}
	if d < 0 {
		return 0, fmt.Errorf("session-events-retention %q: must not be negative", s)
	}
	return d, nil
}

// RunRetention deletes events older than keep, once at start and then every
// `every`, until ctx is done.
func RunRetention(ctx context.Context, store Store, keep, every time.Duration, now func() time.Time, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if now == nil {
		now = time.Now
	}
	sweep := func() {
		n, err := store.DeleteBefore(now().Add(-keep))
		if err != nil {
			log.Warn("session events retention sweep failed", "err", err.Error())
			return
		}
		if n > 0 {
			log.Info("session events retention sweep", "removed", n)
		}
	}
	sweep()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
