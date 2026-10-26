package jam

import (
	"slices"
	"testing"
)

func putMgrInst(t *testing.T, store Store, id, mgr, loc string, ph Phase) {
	t.Helper()
	if err := store.PutInstance(Instance{ActorID: id, Role: "guest", Manager: mgr, Location: loc, Phase: ph}); err != nil {
		t.Fatal(err)
	}
}

func phaseOf(t *testing.T, store Store, id string) Phase {
	t.Helper()
	i, ok := store.GetInstance(id)
	if !ok {
		t.Fatalf("instance %s missing", id)
	}
	return i.Phase
}

func TestReconcileManagerMarksMissingLost(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{})
	putMgrInst(t, store, "x1", "m", "L1", PhaseLive)
	got := sup.ReconcileManager("m", ManagerInventory{})
	if !slices.Equal(got.Lost, []string{"x1"}) {
		t.Fatalf("Lost = %v", got.Lost)
	}
	if p := phaseOf(t, store, "x1"); p != PhaseLost {
		t.Fatalf("phase = %s", p)
	}
}

func TestReconcileManagerLocationMismatch(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{})
	putMgrInst(t, store, "x1", "m", "L1", PhaseLive)
	got := sup.ReconcileManager("m", ManagerInventory{Studios: []ManagedStudio{{ActorID: "x1", Location: "L2"}}})
	if phaseOf(t, store, "x1") != PhaseLost || !slices.Equal(got.Lost, []string{"x1"}) {
		t.Fatalf("got %+v", got)
	}
	if !slices.Equal(got.Strays, []ManagedStudio{{ActorID: "x1", Location: "L2"}}) {
		t.Fatalf("Strays = %v", got.Strays)
	}
}

func TestReconcileManagerIgnoresOtherManagers(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{})
	putMgrInst(t, store, "y1", "n", "L", PhaseLive)
	putMgrInst(t, store, "z1", "", "L", PhaseLive)
	got := sup.ReconcileManager("m", ManagerInventory{})
	if len(got.Lost) != 0 || phaseOf(t, store, "y1") != PhaseLive || phaseOf(t, store, "z1") != PhaseLive {
		t.Fatalf("got %+v", got)
	}
}

func TestReconcileManagerKeepsIdledPresent(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{})
	putMgrInst(t, store, "x1", "m", "L1", PhaseIdled)
	got := sup.ReconcileManager("m", ManagerInventory{Studios: []ManagedStudio{{ActorID: "x1", Location: "L1"}}})
	if phaseOf(t, store, "x1") != PhaseIdled || len(got.Lost) != 0 || len(got.Strays) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestReconcileManagerRaisingUntouched(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{})
	putMgrInst(t, store, "r1", "m", "", PhaseRaising)
	putMgrInst(t, store, "r2", "m", "", PhaseRaising)
	got := sup.ReconcileManager("m", ManagerInventory{Studios: []ManagedStudio{{ActorID: "r2", Location: "Lr"}}})
	if phaseOf(t, store, "r1") != PhaseRaising || phaseOf(t, store, "r2") != PhaseRaising {
		t.Fatalf("raising changed: %+v", got)
	}
	if len(got.Lost) != 0 || len(got.Strays) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestReconcileManagerStrays(t *testing.T) {
	f := &fakeLauncher{}
	sup, _, _ := supTestKit(t, f)
	got := sup.ReconcileManager("m", ManagerInventory{Studios: []ManagedStudio{{ActorID: "q", Location: "Lq"}}})
	if !slices.Equal(got.Strays, []ManagedStudio{{ActorID: "q", Location: "Lq"}}) {
		t.Fatalf("Strays = %v", got.Strays)
	}
	if len(f.tornDown) != 0 {
		t.Fatalf("teardown called: %v", f.tornDown)
	}
}

func TestReconcileManagerTerminatingNotStray(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{})
	putMgrInst(t, store, "t1", "m", "L1", PhaseTerminating)
	got := sup.ReconcileManager("m", ManagerInventory{Studios: []ManagedStudio{{ActorID: "t1", Location: "L1"}}})
	if len(got.Strays) != 0 || len(got.Lost) != 0 || phaseOf(t, store, "t1") != PhaseTerminating {
		t.Fatalf("got %+v", got)
	}
}

func TestReconcileManagerMissingState(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{})
	pid, _ := ProjectIDOf(store, "default")
	if err := store.PutStandingSession(pid, "guest", "spider", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStandingSessionManager(pid, "guest", "spider", "m"); err != nil {
		t.Fatal(err)
	}
	got := sup.ReconcileManager("m", ManagerInventory{StateOwners: []string{}})
	if !slices.Equal(got.MissingState, []string{"s1"}) {
		t.Fatalf("MissingState = %v", got.MissingState)
	}
	if store.StandingSessionManager(pid, "guest", "spider") != "m" {
		t.Fatal("pin dropped")
	}
	got = sup.ReconcileManager("m", ManagerInventory{StateOwners: []string{"s1"}})
	if len(got.MissingState) != 0 {
		t.Fatalf("MissingState = %v", got.MissingState)
	}
}
