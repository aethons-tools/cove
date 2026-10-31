package jam

import "testing"

// signalled drains ch, reporting whether it held a signal.
func signalled(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestInstanceChangesSignalsCardFieldsOnly(t *testing.T) {
	c := NewInstanceChanges(NewMemStore())
	ch, cancel := c.Subscribe()
	defer cancel()
	inst := Instance{ActorID: "a", Phase: PhaseLive, Activity: ActivityRunning}
	put := func() {
		t.Helper()
		if err := c.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	put()
	if !signalled(ch) {
		t.Fatal("a new instance should signal")
	}
	inst.LastSeen = inst.LastSeen.Add(1)
	inst.CommitSeq = 9
	put()
	if signalled(ch) {
		t.Fatal("a heartbeat or cursor write changes no card field: no signal")
	}
	// In order: asking needs a person only once the session waits.
	for _, c := range []struct {
		name   string
		change func()
	}{
		{"phase", func() { inst.Phase = PhaseIdled }},
		{"activity", func() { inst.Activity = ActivityWaiting }},
		{"asked", func() { inst.EscalationAsked = true }},
		{"mission", func() { inst.Mission = "m" }},
		{"focus", func() { inst.Focus = "f" }},
		{"asks", func() { inst.OpenAsks = 1 }},
		{"alarms", func() { inst.Alarms = []Alarm{{Name: "x"}} }},
		{"exposures", func() { inst.Exposures = []Exposure{{Name: "p"}} }},
		{"kept", func() { inst.KeptProcesses = 2 }},
		{"name", func() { inst.Name = "n" }},
	} {
		c.change()
		put()
		if !signalled(ch) {
			t.Errorf("%s change should signal", c.name)
		}
	}
	if err := c.RemoveInstance("a"); err != nil {
		t.Fatal(err)
	}
	if !signalled(ch) {
		t.Fatal("a removal should signal")
	}
	if _, ok := c.GetInstance("a"); ok {
		t.Fatal("reads pass through to the store")
	}
}
