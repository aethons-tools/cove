package intercom_test

import (
	"fmt"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercomtest"
)

func TestMemChannelLogConformance(t *testing.T) {
	intercomtest.RunConformance(t, func(t *testing.T, legacy int) intercomtest.Fixture {
		old := intercom.NewLegacyMemLog()
		for i := range legacy {
			if _, err := old.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "c"}, To: []intercom.Target{{Kind: "human", Ref: "a"}}, Body: fmt.Sprint(i)}); err != nil {
				t.Fatal(err)
			}
		}
		return intercomtest.Fixture{Store: intercom.NewMemLog(old), Legacy: old, LegacyCount: legacy}
	})
}
