package adminui

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestDashboardStatsCountsPhasesAndObjects(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	for id, i := range map[string]jam.Instance{
		"a": {Phase: jam.PhaseLive, Activity: jam.ActivityRunning},      // running
		"b": {Phase: jam.PhaseLive, Activity: jam.ActivityHolding},      // running
		"g": {Phase: jam.PhaseLive, Activity: jam.ActivityBlocked},      // waiting
		"c": {Phase: jam.PhaseRaising},                                  // setting up
		"h": {Phase: jam.PhaseLive},                                     // setting up (orienting)
		"d": {Phase: jam.PhaseLost}, "e": {Phase: jam.PhaseTerminating}, // attention
		"f": {Phase: jam.PhaseIdled},
	} {
		i.ActorID, i.Project, i.Role = id, "acme", "w"
		if err := st.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutRole("acme", jam.Role{Name: "w"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddActor(jam.Actor{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKit("k", "x: 1\n"); err != nil {
		t.Fatal(err)
	}

	got := dashboardStats(st)
	// Agents: eight studios plus actor x, which has none. Specs: kit k plus the
	// built-in default model-spec.
	want := stats{Running: 2, Waiting: 1, SettingUp: 2, Attention: 2, Idled: 1, Studios: 8, Projects: 1, Agents: 9, Specs: 1 + len(st.ListModelSpecs())}
	if got != want {
		t.Fatalf("dashboardStats = %+v, want %+v", got, want)
	}
}

func TestTTLFormat(t *testing.T) {
	ttl := funcs["ttl"].(func(time.Duration) string)
	for d, want := range map[time.Duration]string{
		0: "—", time.Hour: "1h", 90 * time.Minute: "1h30m", 30 * time.Minute: "30m", 45 * time.Second: "45s",
	} {
		if got := ttl(d); got != want {
			t.Errorf("ttl(%v) = %q, want %q", d, got, want)
		}
	}
}
