package jam

import "time"

// IdleSink is told what jam-studio-agent must know of a session's idle timeout
// and deadline (usage/jam/studio-agent.adoc#idle-push). The supervisor
// takes it from its ControlSink (SetControlSink) when that implements it. Like
// ExposureSink it is called outside instMu, while holding idleMu, so
// implementations MUST be fast and non-blocking (store and signal only) and
// MUST NOT call back into the Supervisor.
type IdleSink interface {
	// IdleTimeoutChanged carries the timeout that applies at the session's
	// next turn end (0 = none). It is called after anything that may change
	// it, so the sink drops a repeat of the value it last had.
	IdleTimeoutChanged(actorID string, d time.Duration)
	// IdleDeadlineChanged carries the armed deadline (zero = none) at each
	// turn end and each change while the session is holding or waiting; never
	// the one a turn start clears.
	IdleDeadlineChanged(actorID string, at time.Time)
}

// nextIdleTimeout is the idle timeout that applies at inst's next turn end: its
// override, or else its role's.
func (s *Supervisor) nextIdleTimeout(inst Instance) time.Duration {
	if o := inst.IdleOverride; o != nil {
		return o.Duration
	}
	role, _ := s.store.GetRole(inst.Project, inst.Role)
	return role.TurnEnd.IdleTimeout
}

// IdleTimeout is the idle timeout that applies at actorID's next turn end
// (ok=false: no such instance).
func (s *Supervisor) IdleTimeout(actorID string) (time.Duration, bool) {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return 0, false
	}
	return s.nextIdleTimeout(inst), true
}

// IdleDeadline is actorID's armed idle deadline while it is holding or
// waiting; zero otherwise.
func (s *Supervisor) IdleDeadline(actorID string) time.Time {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || !idleActivity(inst.Activity) {
		return time.Time{}
	}
	return inst.IdleDeadline
}

// idleActivity reports whether a holds an idle deadline: holding or waiting.
func idleActivity(a Activity) bool {
	return a == ActivityHolding || a == ActivityWaiting
}

// idleDeadlinePush is the deadline to push after inst went from old to cur:
// at a turn end, and at any change of it while holding or waiting. A turn start
// leaves cur running, so the deadline it clears is never pushed.
func idleDeadlinePush(old, cur Instance) (time.Time, bool) {
	if !idleActivity(cur.Activity) {
		return time.Time{}, false
	}
	turnEnded := old.Activity == ActivityRunning
	if !turnEnded && old.IdleDeadline.Equal(cur.IdleDeadline) {
		return time.Time{}, false
	}
	return cur.IdleDeadline, true
}

// notifyIdle tells the idle sink about inst after it went from old to cur.
// The caller holds idleMu (not instMu), so pushes reach the sink in the order
// the changes were stored.
func (s *Supervisor) notifyIdle(old, cur Instance) {
	if s.idleSink == nil {
		return
	}
	s.idleSink.IdleTimeoutChanged(cur.ActorID, s.nextIdleTimeout(cur))
	if at, ok := idleDeadlinePush(old, cur); ok {
		s.idleSink.IdleDeadlineChanged(cur.ActorID, at)
	}
}

// notifyIdleTimeouts tells the idle sink every session's current timeout, so a
// change of a role's idle timeout reaches its sessions (Reconcile's tick).
func (s *Supervisor) notifyIdleTimeouts() {
	if s.idleSink == nil {
		return
	}
	for _, inst := range s.store.ListInstances() {
		if inst.Phase == PhaseGone {
			continue
		}
		s.idleMu.Lock()
		if cur, ok := s.store.GetInstance(inst.ActorID); ok {
			s.idleSink.IdleTimeoutChanged(cur.ActorID, s.nextIdleTimeout(cur))
		}
		s.idleMu.Unlock()
	}
}
