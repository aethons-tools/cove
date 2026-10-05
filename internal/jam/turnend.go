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

// Idle-override scopes (IdleOverride.Scope).
const (
	IdleScopeNext   = "next"   // only the next turn end
	IdleScopeAlways = "always" // every turn end until the session ends
)

// IdleOverride is a session's own override of its role's idle timeout.
type IdleOverride struct {
	Duration time.Duration `json:"duration"` // 0 = off
	Scope    string        `json:"scope"`    // IdleScopeNext | IdleScopeAlways
}

// EndRequest records that a session asked to end (the `end` tool).
type EndRequest struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}
