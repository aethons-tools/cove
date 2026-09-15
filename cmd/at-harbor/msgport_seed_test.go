package main

import (
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
)

func TestLogTailSeq(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := logTailSeq(lg); got != 0 {
		t.Fatalf("empty log tail = %d, want 0", got)
	}
	var lastSeq int64
	for i := 0; i < 3; i++ {
		m, err := lg.Append(msglog.Message{From: msglog.Target{Kind: "actor", Ref: "c"}, To: []msglog.Target{{Kind: "channel", Ref: "x"}}, Body: "hi", Project: "p"})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		lastSeq = m.Seq
	}
	if got := logTailSeq(lg); got != lastSeq {
		t.Fatalf("tail = %d, want %d", got, lastSeq)
	}
}
