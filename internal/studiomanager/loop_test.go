package studiomanager

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// scriptLoop runs a Loop whose sessions follow script (one entry per Run;
// each gets the welcomed callback) and whose Sleep records its durations; the
// loop's ctx ends when the script runs out.
func scriptLoop(t *testing.T, log *slog.Logger, script ...func(welcomed func()) error) []time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sleeps []time.Duration
	i := 0
	l := Loop{
		Run: func(ctx context.Context, welcomed func()) error {
			if i == len(script) {
				cancel()
				return ctx.Err()
			}
			i++
			return script[i-1](welcomed)
		},
		Sleep: func(_ context.Context, d time.Duration) { sleeps = append(sleeps, d) },
		Log:   log,
	}
	if log == nil {
		l.Log = slog.New(slog.DiscardHandler)
	}
	if err := l.Serve(ctx); err != nil {
		t.Fatalf("Serve = %v, want nil", err)
	}
	return sleeps
}

func fail(func()) error { return errors.New("connection reset") }

func TestLoopBackoffDoublesAndCaps(t *testing.T) {
	got := scriptLoop(t, nil, fail, fail, fail, fail, fail, fail, fail)
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
}

func TestLoopResetsAfterWelcome(t *testing.T) {
	welcomedThenFail := func(welcomed func()) error { welcomed(); return errors.New("stream ended") }
	got := scriptLoop(t, nil, fail, fail, welcomedThenFail, fail)
	want := []time.Duration{time.Second, 2 * time.Second, time.Second, 2 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
}

func TestLoopRefusalBacksOffToMax(t *testing.T) {
	var buf syncBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	got := scriptLoop(t, log, func(func()) error {
		return status.Error(codes.FailedPrecondition, "version mismatch: jam A, manager B")
	})
	if want := []time.Duration{30 * time.Second}; !slices.Equal(got, want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
	var errLines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=ERROR") {
			errLines = append(errLines, line)
		}
	}
	if len(errLines) != 1 || !strings.Contains(errLines[0], "refused") || !strings.Contains(errLines[0], "version mismatch") {
		t.Fatalf("error lines = %q; log:\n%s", errLines, buf.String())
	}
}

func TestLoopStopsOnCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := Loop{
		Run: func(context.Context, func()) error { return errors.New("down") },
		Log: slog.New(slog.DiscardHandler),
	}
	done := make(chan error, 1)
	go func() { done <- l.Serve(ctx) }()
	time.Sleep(50 * time.Millisecond) // inside the default 1s Sleep
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v, want nil", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Serve did not return promptly after cancel")
	}
}
