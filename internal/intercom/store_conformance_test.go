package intercom_test

import (
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercomtest"
)

func TestFileLogConformance(t *testing.T) {
	intercomtest.RunConformance(t, func(t *testing.T) intercom.Store {
		lg, err := intercom.Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { lg.Close() })
		return lg
	})
}
