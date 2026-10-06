package intercom_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercomtest"
)

func TestMemLogConformance(t *testing.T) {
	intercomtest.RunLegacyConformance(t, func(t *testing.T) intercom.LegacyStore {
		return intercom.NewLegacyMemLog()
	})
}
