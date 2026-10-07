package jam

// AgentStatus is an agent's status as people see it: one axis folding its
// studio's Phase (supervisor-owned) and the agent's reported Activity
// (meaningful only while live). It is display-only — Phase and Activity stay
// the stored truth.
type AgentStatus string

const (
	StatusPending     AgentStatus = "pending"     // no studio: enrolled only, or never raised
	StatusSettingUp   AgentStatus = "setting up"  // raising
	StatusOrienting   AgentStatus = "orienting"   // live, but the agent hasn't reported yet
	StatusRunning     AgentStatus = "running"     // live, in a turn
	StatusWaiting     AgentStatus = "waiting"     // live, waiting on a person
	StatusHolding     AgentStatus = "holding"     // live, turn over, background tasks running
	StatusBlocked     AgentStatus = "blocked"     // live, says it can't proceed
	StatusIdled       AgentStatus = "idled"       // paused
	StatusDone        AgentStatus = "done"        // live, reported done (terminating next)
	StatusTerminating AgentStatus = "terminating" // teardown in flight
	StatusLost        AgentStatus = "lost"        // declared dead
	StatusGone        AgentStatus = "gone"        // torn down
)

// StatusOf is the status of an agent whose studio has phase and activity;
// hasStudio false (no studio at all) is pending.
func StatusOf(phase Phase, activity Activity, hasStudio bool) AgentStatus {
	if !hasStudio {
		return StatusPending
	}
	switch phase {
	case PhaseRaising:
		return StatusSettingUp
	case PhaseIdled:
		return StatusIdled
	case PhaseTerminating:
		return StatusTerminating
	case PhaseLost:
		return StatusLost
	case PhaseGone:
		return StatusGone
	}
	switch activity {
	case ActivityRunning:
		return StatusRunning
	case ActivityWaiting:
		return StatusWaiting
	case ActivityHolding:
		return StatusHolding
	case ActivityBlocked:
		return StatusBlocked
	case ActivityDone:
		return StatusDone
	}
	return StatusOrienting
}
