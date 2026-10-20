package jam

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// recCloser records the supervisor's tunnel calls ("close:", "unused:",
// "reopen:" + id); inUse names studios with tunnels in use.
type recCloser struct {
	ops   []string
	inUse map[string]bool
}

func (r *recCloser) CloseAll(id string) { r.ops = append(r.ops, "close:"+id) }
func (r *recCloser) CloseIfUnused(id string) bool {
	r.ops = append(r.ops, "unused:"+id)
	return !r.inUse[id]
}
func (r *recCloser) Reopen(id string) { r.ops = append(r.ops, "reopen:"+id) }

func raiseAll(t *testing.T, sup *Supervisor, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Role: "guest"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSupervisorClosesTunnelsOnTeardownAndIdle(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	rc := &recCloser{}
	sup.SetTunnelCloser(rc)
	raiseAll(t, sup, "ses_a", "ses_b")
	if err := sup.Idle(context.Background(), "ses_a"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Resume(context.Background(), "ses_a"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "ses_b"); err != nil {
		t.Fatal(err)
	}
	// A raise and a resume reopen; a teardown closes, then forgets the gone
	// studio's closed mark.
	want := []string{"reopen:ses_a", "reopen:ses_b", "unused:ses_a", "reopen:ses_a", "close:ses_b", "reopen:ses_b"}
	if !slices.Equal(rc.ops, want) {
		t.Fatalf("tunnel calls = %v, want %v", rc.ops, want)
	}
}

// A studio with a tunnel in use is not paused: Idle refuses, the studio stays
// live and its tunnels open.
func TestSupervisorIdleRefusesStudioInUse(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, st, _ := supTestKit(t, fl)
	rc := &recCloser{inUse: map[string]bool{"ses_a": true}}
	sup.SetTunnelCloser(rc)
	raiseAll(t, sup, "ses_a")
	if err := sup.Idle(context.Background(), "ses_a"); !errors.Is(err, ErrStudioInUse) {
		t.Fatalf("Idle err = %v, want ErrStudioInUse", err)
	}
	if len(fl.paused) != 0 {
		t.Fatalf("paused %v", fl.paused)
	}
	if inst, _ := st.GetInstance("ses_a"); inst.Phase != PhaseLive {
		t.Fatalf("phase = %s, want live", inst.Phase)
	}
}

// A failed pause reopens the studio's tunnels: it is still running.
func TestSupervisorFailedPauseReopensTunnels(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive, pauseErr: errors.New("docker: no")})
	rc := &recCloser{}
	sup.SetTunnelCloser(rc)
	raiseAll(t, sup, "ses_a")
	if err := sup.Idle(context.Background(), "ses_a"); err == nil {
		t.Fatal("Idle succeeded with a failing pause")
	}
	if want := []string{"reopen:ses_a", "unused:ses_a", "reopen:ses_a"}; !slices.Equal(rc.ops, want) {
		t.Fatalf("tunnel calls = %v, want %v", rc.ops, want)
	}
}

func TestSupervisorNilTunnelCloserIsSafe(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	raiseAll(t, sup, "w1")
	if err := sup.Idle(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Resume(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
}
