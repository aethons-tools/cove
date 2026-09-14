package harbor_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/storetest"
)

func TestFileStoreConformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) harbor.Store {
		s, err := harbor.NewFileStore(t.TempDir() + "/store.json")
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		return s
	})
}
