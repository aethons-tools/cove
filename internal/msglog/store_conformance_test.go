package msglog_test

import (
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msglog/msglogtest"
)

func TestFileLogConformance(t *testing.T) {
	msglogtest.RunConformance(t, func(t *testing.T) msglog.Store {
		lg, err := msglog.Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { lg.Close() })
		return lg
	})
}
