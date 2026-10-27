package studiomanager

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

// Loop keeps a manager connected to Jam: it runs one session after another,
// backing off between them.
type Loop struct {
	Run   func(ctx context.Context, welcomed func()) error // one session; calls welcomed once Welcome arrives
	Sleep func(ctx context.Context, d time.Duration)       // nil: a timer that returns early when ctx ends
	Log   *slog.Logger
}

// Serve runs sessions until ctx ends, then returns nil. The backoff starts at
// 1s, doubles to 30s and resets once a session is welcomed; a refusal by Jam
// (bad token, revoked, version mismatch) waits the full 30s.
func (l Loop) Serve(ctx context.Context) error {
	sleep := l.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	log := l.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	backoff := minBackoff
	for {
		var welcomed atomic.Bool
		err := l.Run(ctx, func() { welcomed.Store(true) })
		if ctx.Err() != nil {
			return nil
		}
		if welcomed.Load() {
			backoff = minBackoff
		}
		var wait time.Duration
		if refused(err) {
			wait, backoff = maxBackoff, maxBackoff
			log.Error("manager: Jam refused the connection", "err", err)
		} else {
			wait, backoff = backoff, min(backoff*2, maxBackoff)
			log.Warn("manager: disconnected from Jam", "err", err, "retry", wait)
		}
		sleep(ctx, wait)
		if ctx.Err() != nil {
			return nil
		}
	}
}

// refused reports whether err is Jam turning the manager away, which retrying
// soon will not fix.
func refused(err error) bool {
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.FailedPrecondition:
		return true
	}
	return false
}

// sleepCtx waits d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
