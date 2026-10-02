package adminui

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestDashboardStatsCountsPhasesAndObjects(t *testing.T) {
	st, err := jam.NewFileStore(t.TempDir() + "/store.json")
	if err != nil {
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
	want := stats{Live: 2, Raising: 1, Attention: 2, Idled: 1, Studios: 6, Actors: 1, Roles: 1, Kits: 1}
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
