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
	for id, ph := range map[string]jam.Phase{
		"a": jam.PhaseLive, "b": jam.PhaseLive, "c": jam.PhaseRaising,
		"d": jam.PhaseLost, "e": jam.PhaseTerminating, "f": jam.PhaseIdled,
	} {
		if err := st.PutInstance(jam.Instance{ActorID: id, Project: "acme", Role: "w", Phase: ph}); err != nil {
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
	// Agents: six studios plus actor x, which has none. Specs: kit k plus the
	// built-in default model-spec.
	want := stats{Live: 2, Raising: 1, Attention: 2, Idled: 1, Studios: 6, Projects: 1, Agents: 7, Specs: 1 + len(st.ListModelSpecs())}
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
