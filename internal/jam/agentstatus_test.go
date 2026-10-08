package jam

import "testing"

// An agent's display status folds its studio's phase and the agent's own
// activity into one axis.
func TestStatusOf(t *testing.T) {
	for _, c := range []struct {
		phase     Phase
		activity  Activity
		hasStudio bool
		want      AgentStatus
	}{
		{"", "", false, StatusPending},
		{PhaseRaising, "", true, StatusSettingUp},
		{PhaseLive, "", true, StatusOrienting},
		{PhaseLive, ActivityRunning, true, StatusRunning},
		{PhaseLive, ActivityWaiting, true, StatusWaiting},
		{PhaseLive, ActivityHolding, true, StatusHolding},
		{PhaseLive, ActivityBlocked, true, StatusBlocked},
		{PhaseLive, ActivityDone, true, StatusDone},
		{PhaseIdled, ActivityWaiting, true, StatusIdled}, // the phase wins outside live
		{PhaseTerminating, ActivityDone, true, StatusTerminating},
		{PhaseLost, ActivityRunning, true, StatusLost},
		{PhaseGone, "", true, StatusGone},
	} {
		if got := StatusOf(c.phase, c.activity, c.hasStudio); got != c.want {
			t.Errorf("StatusOf(%q, %q, %v) = %q, want %q", c.phase, c.activity, c.hasStudio, got, c.want)
		}
	}
	if StatusSettingUp != "setting up" {
		t.Errorf("statuses read as words: %q", StatusSettingUp)
	}
}
