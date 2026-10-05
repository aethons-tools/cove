package jam

import (
	"fmt"
	"time"
)

// What a session does when its idle deadline passes (TurnEndPolicy.OnIdle).
const (
	OnIdleWake     = "wake"
	OnIdleTeardown = "teardown"
)

// TurnEndPolicy is a role's turn-end policy: how long a session may sit after
// a turn ends with nothing waking it, and what happens then. See
// docs/usage/jam/turn-end.md.
type TurnEndPolicy struct {
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"` // 0 = no idle timeout
	OnIdle      string        `json:"on_idle,omitempty"`      // OnIdleWake | OnIdleTeardown; "" = wake
}

// Action is OnIdle with its default applied.
func (p TurnEndPolicy) Action() string {
	if p.OnIdle == "" {
		return OnIdleWake
	}
	return p.OnIdle
}

// ValidateTurnEnd rejects a negative timeout or an unknown on-idle action.
func ValidateTurnEnd(p TurnEndPolicy) error {
	if p.IdleTimeout < 0 {
		return fmt.Errorf("idle timeout must be >= 0")
	}
	switch p.OnIdle {
	case "", OnIdleWake, OnIdleTeardown:
		return nil
	}
	return fmt.Errorf("on-idle must be %q or %q", OnIdleWake, OnIdleTeardown)
}
