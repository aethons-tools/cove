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
// docs/modules/ROOT/pages/usage/jam/turn-end.adoc.
type TurnEndPolicy struct {
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"` // 0 = no idle timeout
	OnIdle      string        `json:"on_idle,omitempty"`      // OnIdleWake | OnIdleTeardown; "" = wake
	TimeZone    string        `json:"time_zone,omitempty"`    // IANA zone cron alarms run in; "" = UTC
}

// Location is the zone cron alarms are evaluated in: TimeZone, or UTC.
func (p TurnEndPolicy) Location() *time.Location {
	if p.TimeZone == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(p.TimeZone); err == nil {
		return loc
	}
	return time.UTC
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
	if p.TimeZone != "" {
		if _, err := time.LoadLocation(p.TimeZone); err != nil {
			return fmt.Errorf("unknown time zone %q", p.TimeZone)
		}
	}
	switch p.OnIdle {
	case "", OnIdleWake, OnIdleTeardown:
		return nil
	}
	return fmt.Errorf("on-idle must be %q or %q", OnIdleWake, OnIdleTeardown)
}

// CachePolicy is a role's prompt-cache profile (the role file's cache:): how
// long the prompt cache lives, how long Jam keeps it warm while a session
// waits, and the context size below which it doesn't. See
// docs/modules/ROOT/pages/usage/jam/prompt-cache.adoc.
type CachePolicy struct {
	TTL       time.Duration `json:"ttl,omitempty"`       // 5m | 1h; 0 = the harness's default
	KeepWarm  time.Duration `json:"keep_warm,omitempty"` // 0 to 24h; 0 = not kept warm
	Threshold int           `json:"threshold,omitempty"` // tokens; 0 = every session
}

// The values cache.ttl may take, and the longest cache.keep-warm.
const (
	CacheTTLShort    = 5 * time.Minute
	CacheTTLLong     = time.Hour
	MaxCacheKeepWarm = 24 * time.Hour
)

// ValidateCache refuses a cache profile out of range.
func ValidateCache(p CachePolicy) error {
	if p.TTL != 0 && p.TTL != CacheTTLShort && p.TTL != CacheTTLLong {
		return fmt.Errorf("cache.ttl must be 5m or 1h")
	}
	if p.KeepWarm < 0 || p.KeepWarm > MaxCacheKeepWarm {
		return fmt.Errorf("cache.keep-warm must be 0 to 24h")
	}
	if p.Threshold < 0 {
		return fmt.Errorf("cache.threshold must be >= 0")
	}
	return nil
}

// CacheLead is how long before the prompt cache would expire a keep-warm wake
// refreshes it, and before keep-warm runs out the compact-due wake is due.
const CacheLead = time.Minute

// KeepWarmEvery is how often a keep-warm wake is due: the cache's ttl (the
// harness's default, 5m, when unset) less CacheLead.
func (p CachePolicy) KeepWarmEvery() time.Duration {
	ttl := p.TTL
	if ttl == 0 {
		ttl = CacheTTLShort
	}
	return ttl - CacheLead
}

// KeepWarm is a session's keep-warm schedule
// (docs/modules/ROOT/pages/usage/jam/prompt-cache.adoc#keeping-it-warm): armed
// from a turn's end when its role keeps the cache warm, cancelled by any other
// wake. The wake-on engine sends its wakes; the supervisor marks the turns
// they start, which leave the session's waiting state alone.
type KeepWarm struct {
	Until time.Time `json:"until,omitzero"` // keep-warm runs out; compact-due is due CacheLead before
	Next  time.Time `json:"next,omitzero"`  // the next keep-warm wake
	// Pending is the kind of a keep-warm or compact-due wake sent and not yet
	// answered (re-sent every tick until the session runs).
	Pending string `json:"pending,omitempty"`
	// Turn is the kind of the wake whose turn is running, until the session
	// waits again.
	Turn string `json:"turn,omitempty"`
	// Resets is how many compactions and Clears the session's events showed
	// when its compact-due wake was sent.
	Resets int `json:"resets,omitempty"`
	// CompactCheck is when a compact-due turn ended: Jam compacts the
	// session unless its events show a compaction since the wake (more
	// resets than Resets).
	CompactCheck time.Time `json:"compact_check,omitzero"`
}

// Armed reports whether a keep-warm schedule is running.
func (k KeepWarm) Armed() bool { return !k.Until.IsZero() }

// Due is the kind of wake due at now ("" = none): compact-due within
// CacheLead of Until, else keep-warm once Next has passed.
func (k KeepWarm) Due(now time.Time) string {
	switch {
	case !k.Armed():
		return ""
	case !now.Before(k.Until.Add(-CacheLead)):
		return WakeCompactDue
	case !now.Before(k.Next):
		return WakeKeepWarm
	}
	return ""
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
