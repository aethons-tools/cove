package jam

import (
	"context"
	"slices"
	"testing"
)

type recCloser struct{ ids []string }

func (r *recCloser) CloseAll(id string) { r.ids = append(r.ids, id) }

func TestSupervisorClosesTunnelsOnTeardownAndIdle(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	rc := &recCloser{}
	sup.SetTunnelCloser(rc)
	for _, id := range []string{"ses_a", "ses_b"} {
		if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Role: "guest"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sup.Idle(context.Background(), "ses_a"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "ses_b"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rc.ids, []string{"ses_a", "ses_b"}) {
		t.Fatalf("CloseAll calls = %v", rc.ids)
	}
}

func TestSupervisorNilTunnelCloserIsSafe(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Idle(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
}
