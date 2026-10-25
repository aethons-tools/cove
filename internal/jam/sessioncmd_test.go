package jam

import "testing"

func TestCommandAllowed(t *testing.T) {
	allowed := map[SessionCommand][]Activity{
		CmdInterrupt: {ActivityRunning},
		CmdCompact:   {ActivityRunning, ActivityHolding, ActivityWaiting},
		CmdClear:     {ActivityWaiting},
	}
	all := []Activity{ActivityRunning, ActivityWaiting, ActivityHolding, ActivityBlocked, ActivityDone, ""}
	for c, yes := range allowed {
		for _, a := range all {
			want := false
			for _, y := range yes {
				want = want || y == a
			}
			if got := CommandAllowed(c, a); got != want {
				t.Errorf("CommandAllowed(%s, %q) = %v, want %v", c, a, got, want)
			}
		}
	}
	if CommandAllowed("reboot", ActivityWaiting) {
		t.Error("an unknown command is allowed")
	}
}

func TestCommandActivity(t *testing.T) {
	tests := []struct {
		name string
		inst Instance
		want Activity
	}{
		{"live reports its activity", Instance{Phase: PhaseLive, Activity: ActivityRunning}, ActivityRunning},
		{"paused is idle", Instance{Phase: PhaseIdled, Activity: ActivityRunning}, ActivityWaiting},
		{"raising is its phase", Instance{Phase: PhaseRaising}, Activity(PhaseRaising)},
		{"terminating is its phase", Instance{Phase: PhaseTerminating, Activity: ActivityWaiting}, Activity(PhaseTerminating)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommandActivity(tt.inst); got != tt.want {
				t.Errorf("CommandActivity = %q, want %q", got, tt.want)
			}
		})
	}
}
