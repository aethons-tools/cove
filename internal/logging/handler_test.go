package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestMultiFansOutToAllChildren(t *testing.T) {
	var a, b bytes.Buffer
	h := newMulti(slog.NewTextHandler(&a, nil), slog.NewJSONHandler(&b, nil))
	slog.New(h).Info("hello", "k", "v")
	if !strings.Contains(a.String(), "hello") || !strings.Contains(b.String(), `"msg":"hello"`) {
		t.Fatalf("both children should receive the record; a=%q b=%q", a.String(), b.String())
	}
}

func TestSkipUserShownIgnoresNonBoolValue(t *testing.T) {
	var text bytes.Buffer
	h := skipUserShown(slog.NewTextHandler(&text, nil))
	log := slog.New(h)

	// A record carrying a non-bool user_shown attr must not panic (slog.Value.Bool
	// panics on a non-bool Kind) and must be treated as "not shown" — the guard
	// only skips a genuine bool true.
	log.LogAttrs(context.Background(), slog.LevelError, "boom", slog.String(userShownKey, "yes"))
	if !strings.Contains(text.String(), "boom") {
		t.Fatalf("non-bool user_shown must not suppress the record; got %q", text.String())
	}
}

func TestSkipUserShownDropsMarkedRecords(t *testing.T) {
	var text, json bytes.Buffer
	stderr := skipUserShown(slog.NewTextHandler(&text, nil))
	file := slog.NewJSONHandler(&json, nil)
	log := slog.New(newMulti(stderr, file))

	log.LogAttrs(context.Background(), slog.LevelError, "boom", slog.Bool(userShownKey, true))
	if text.Len() != 0 {
		t.Fatalf("stderr text handler must skip user-shown records; got %q", text.String())
	}
	if !strings.Contains(json.String(), `"msg":"boom"`) {
		t.Fatalf("file handler must keep user-shown records; got %q", json.String())
	}
}
