package intercom_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercomtest"
)

func TestMemLogConformance(t *testing.T) {
	intercomtest.RunConformance(t, func(t *testing.T) intercom.Store {
		return intercom.NewMemLog()
	})
}
