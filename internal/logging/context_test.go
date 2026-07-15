package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestIntoFromRoundTrips(t *testing.T) {
	var b bytes.Buffer
	lg, _ := New(Options{Mode: Unattended, Stderr: &b, Level: slog.LevelInfo})
	ctx := Into(context.Background(), lg)
	From(ctx).Info("via-context")
	if !strings.Contains(b.String(), "via-context") {
		t.Fatalf("From(ctx) should return the stored logger; got %q", b.String())
	}
}

func TestFromWithoutLoggerDoesNotPanic(t *testing.T) {
	From(context.Background()).Info("no-panic") // discard logger; must not panic
}
