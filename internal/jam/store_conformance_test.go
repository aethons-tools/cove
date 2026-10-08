package jam_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/storetest"
)

func TestMemStoreConformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) jam.Store {
		s := jam.NewMemStore()
		return s
	})
}
