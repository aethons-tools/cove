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
