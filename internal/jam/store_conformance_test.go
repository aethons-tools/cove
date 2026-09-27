package jam_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/storetest"
)

func TestFileStoreConformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) jam.Store {
		s, err := jam.NewFileStore(t.TempDir() + "/store.json")
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		return s
	})
}
