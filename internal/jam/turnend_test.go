package jam

import (
	"testing"
	"time"
)

func TestTurnEndPolicyAction(t *testing.T) {
	if got := (TurnEndPolicy{}).Action(); got != OnIdleWake {
		t.Fatalf("default action = %q, want wake", got)
	}
	if got := (TurnEndPolicy{OnIdle: OnIdleTeardown}).Action(); got != OnIdleTeardown {
		t.Fatalf("action = %q", got)
	}
}

func TestValidateTurnEnd(t *testing.T) {
	for _, ok := range []TurnEndPolicy{{}, {IdleTimeout: time.Hour, OnIdle: OnIdleWake}, {OnIdle: OnIdleTeardown}} {
		if err := ValidateTurnEnd(ok); err != nil {
			t.Errorf("ValidateTurnEnd(%+v) = %v", ok, err)
		}
	}
	for _, bad := range []TurnEndPolicy{{IdleTimeout: -1}, {OnIdle: "nap"}} {
		if err := ValidateTurnEnd(bad); err == nil {
			t.Errorf("ValidateTurnEnd(%+v) accepted", bad)
		}
	}
}
