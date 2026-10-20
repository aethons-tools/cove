package jam

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

type sinkCall struct {
	actor string
	ports []int32
}

type recSink struct {
	mu    sync.Mutex
	calls []sinkCall
}

func (r *recSink) ExposuresChanged(actorID string, ports []int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sinkCall{actorID, ports})
}

func (r *recSink) last() sinkCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return sinkCall{}
	}
	return r.calls[len(r.calls)-1]
}

func exposureTestSupervisor(t *testing.T, id string) (*Supervisor, Store) {
	t.Helper()
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	reraise(t, sup, id)
	return sup, store
}

// reraise raises id (again, if it is already up), as a standing restart does.
func reraise(t *testing.T, sup *Supervisor, id string) {
	t.Helper()
	_ = sup.store.RemoveActor(id) // a restart re-enrolls: the previous identity was revoked at teardown
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Role: "guest"}); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorExposures(t *testing.T) {
	sup, _ := exposureTestSupervisor(t, "ses_a")
	rec := &recSink{}
	sup.SetExposureSink(rec)
	if _, err := sup.Expose("ses_a", "app", 5173); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Expose("ses_a", "app", 3000); err != nil { // replace
		t.Fatal(err)
	}
	if got := sup.Exposures("ses_a"); len(got) != 1 || got[0].Port != 3000 {
		t.Fatalf("exposures %+v", got)
	}
	for i := range 7 {
		if _, err := sup.Expose("ses_a", fmt.Sprintf("x%d", i), 4000+i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sup.Expose("ses_a", "ninth", 4100); !errors.Is(err, ErrExposureLimit) {
		t.Fatalf("9th expose err = %v", err)
	}
	if _, err := sup.Expose("ses_a", "bad--name", 4100); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := sup.Expose("ses_a", "ssh", 2222); err == nil {
		t.Fatal("sshd port accepted")
	}
	if err := sup.Unexpose("ses_a", "app"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(sup.Unexpose("ses_a", "app"), ErrNoExposure) {
		t.Fatal("second unexpose")
	}
	last := rec.last()
	if last.actor != "ses_a" || slices.Contains(last.ports, 3000) || len(last.ports) != 7 {
		t.Fatalf("sink last = %+v", last)
	}
}

func TestRaiseStartsWithNoExposures(t *testing.T) {
	sup, store := exposureTestSupervisor(t, "ses_a")
	rec := &recSink{}
	sup.SetExposureSink(rec)
	if _, err := sup.Expose("ses_a", "app", 5173); err != nil {
		t.Fatal(err)
	}
	reraise(t, sup, "ses_a")
	if inst, _ := store.GetInstance("ses_a"); len(inst.Exposures) != 0 {
		t.Fatalf("exposures survived a raise: %+v", inst.Exposures)
	}
	if last := rec.last(); last.actor != "ses_a" || len(last.ports) != 0 {
		t.Fatalf("sink not told the set is empty: %+v", last)
	}
}

func TestExposureSinkEndsOnLatestState(t *testing.T) {
	sup, store := exposureTestSupervisor(t, "ses_a")
	rec := &recSink{}
	sup.SetExposureSink(rec)
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				name := fmt.Sprintf("n%d", (g+i)%6)
				if i%3 == 2 {
					_ = sup.Unexpose("ses_a", name)
				} else {
					_, _ = sup.Expose("ses_a", name, 4000+g*100+i)
				}
			}
		}()
	}
	wg.Wait()
	inst, _ := store.GetInstance("ses_a")
	want := exposedPorts(inst.Exposures)
	if last := rec.last(); !slices.Equal(last.ports, want) {
		t.Fatalf("sink last = %v, stored = %v", last.ports, want)
	}
}

func TestExposeRefusesDeadSession(t *testing.T) {
	sup, store := exposureTestSupervisor(t, "ses_a")
	inst, _ := store.GetInstance("ses_a")
	for _, ph := range []Phase{PhaseTerminating, PhaseGone} {
		inst.Phase = ph
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		if _, err := sup.Expose("ses_a", "app", 5173); err == nil {
			t.Errorf("Expose accepted phase %s", ph)
		}
	}
	inst.Phase = PhaseIdled
	_ = store.PutInstance(inst)
	if _, err := sup.Expose("ses_a", "app", 5173); err != nil {
		t.Fatalf("idled refused: %v", err)
	}
}

func TestTeardownTellsSinkTheSetIsEmpty(t *testing.T) {
	sup, _ := exposureTestSupervisor(t, "ses_a")
	rec := &recSink{}
	sup.SetExposureSink(rec)
	if _, err := sup.Expose("ses_a", "app", 5173); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "ses_a"); err != nil {
		t.Fatal(err)
	}
	if last := rec.last(); last.actor != "ses_a" || len(last.ports) != 0 {
		t.Fatalf("sink not told the torn-down set is empty: %+v", last)
	}
}
