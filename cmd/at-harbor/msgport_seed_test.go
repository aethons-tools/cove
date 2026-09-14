package main

import (
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
)

func TestLogTailID(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := logTailID(lg); got != "" {
		t.Fatalf("empty log tail = %q, want \"\"", got)
	}
	var lastID string
	for i := 0; i < 3; i++ {
		m, err := lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "c"}, To: []msglog.Target{{Kind: "channel", Ref: "x"}}, Body: "hi", Project: "p"})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		lastID = m.ID
	}
	if got := logTailID(lg); got != lastID {
		t.Fatalf("tail = %q, want %q", got, lastID)
	}
}
