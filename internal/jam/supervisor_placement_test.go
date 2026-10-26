package jam

import (
	"context"
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

// placingFake is a minimal in-package PlacingLauncher over named fakeLaunchers.
// (fleet.Router is the real one, but fleet imports jam, so jam tests cannot use it.)
// It honours Placement.Managers order (registration order when empty) and
// per-manager availability; timeouts are not modelled.
type placingFake struct {
	*fakeLauncher // default Launcher behaviour: the first manager
	order         []string
	by            map[string]*fakeLauncher
	avail         map[string]bool
}

func newPlacingFake(names ...string) *placingFake {
	p := &placingFake{order: names, by: map[string]*fakeLauncher{}, avail: map[string]bool{}}
	for _, n := range names {
		p.by[n] = &fakeLauncher{liveness: LivenessAlive, loc: n + ":loc"}
		p.avail[n] = true
	}
	p.fakeLauncher = p.by[names[0]]
	return p
}

func (p *placingFake) RaiseOn(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (Placed, error) {
	names := spec.Placement.Managers
	if len(names) == 0 {
		names = p.order
	}
	var reasons []string
	for _, n := range names {
		fl, ok := p.by[n]
		if !ok {
			reasons = append(reasons, n+": "+ErrUnknownManager.Error())
			continue
		}
		if !p.avail[n] {
			reasons = append(reasons, n+": "+ErrManagerUnavailable.Error())
			continue
		}
		loc, err := fl.Raise(ctx, spec, creds)
		if errors.Is(err, ErrKitNotReady) {
			return Placed{}, &KitNotReadyError{Manager: n}
		}
		if err != nil {
			reasons = append(reasons, n+": "+err.Error())
			continue
		}
		return Placed{Manager: n, Location: loc}, nil
	}
	return Placed{}, &NoManagerError{Reasons: reasons}
}

func (p *placingFake) PrepareKitOn(ctx context.Context, m string, def KitDefinition) (KitStatus, error) {
	fl, ok := p.by[m]
	if !ok {
		return KitStatus{}, ErrUnknownManager
	}
	return fl.PrepareKit(ctx, def)
}

func (p *placingFake) ImageTagOn(m string, ref KitRef) string {
	if fl, ok := p.by[m]; ok {
		return fl.ImageTag(ref)
	}
	return ""
}

func (p *placingFake) HasManager(name string) bool { _, ok := p.by[name]; return ok }

func withDefaultKit(t *testing.T, sup *Supervisor, store Store) KitRef {
	t.Helper()
	ref, err := EnsureStudioKit(store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetDefaultStudioKit(ref)
	return ref
}

func TestRaiseRecordsPlacedManager(t *testing.T) {
	p := newPlacingFake("a", "b")
	sup, _, _ := supTestKit(t, p)
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Placement: Placement{Managers: []string{"b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Manager != "b" || inst.Location != "b:loc" {
		t.Fatalf("manager/location = %q/%q, want b / b:loc", inst.Manager, inst.Location)
	}
	if len(p.by["a"].raised) != 0 || len(p.by["b"].raised) != 1 {
		t.Fatalf("raised a=%v b=%v", p.by["a"].raised, p.by["b"].raised)
	}
}

func TestRaiseWithoutPlacingLauncherLeavesManagerEmpty(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetInstance("w1"); inst.Manager != "" || got.Manager != "" {
		t.Fatalf("Manager = %q/%q, want empty", inst.Manager, got.Manager)
	}
}

func TestPrepareKitRunsOnManagerThatNeededIt(t *testing.T) {
	p := newPlacingFake("a", "b")
	p.avail["a"] = false
	p.by["b"].notReadyOnce = true
	p.by["b"].prepareState = KitReady
	sup, store, _ := supTestKit(t, p)
	withDefaultKit(t, sup, store)
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if p.by["a"].prepareCalls != 0 || p.by["b"].prepareCalls != 1 {
		t.Fatalf("prepareCalls a=%d b=%d, want 0/1", p.by["a"].prepareCalls, p.by["b"].prepareCalls)
	}
	if len(p.by["b"].raised) != 1 || inst.Manager != "b" {
		t.Fatalf("retry raised=%v manager=%q, want one raise on b", p.by["b"].raised, inst.Manager)
	}
}

func TestRaiseRecordsImageTagFromPlacedManager(t *testing.T) {
	p := newPlacingFake("a", "b")
	p.by["a"].asm, p.by["b"].asm = "asmA", "asmB"
	sup, store, _ := supTestKit(t, p)
	ref := withDefaultKit(t, sup, store)
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Placement: Placement{Managers: []string{"b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := p.by["b"].ImageTag(ref); inst.ImageTag != want || want == "" {
		t.Fatalf("ImageTag = %q, want %q", inst.ImageTag, want)
	}
}

func TestRaiseNoManagerRollsBackActor(t *testing.T) {
	p := newPlacingFake("a")
	p.avail["a"] = false
	sup, store, _ := supTestKit(t, p)
	_, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	var nm *NoManagerError
	if !errors.As(err, &nm) {
		t.Fatalf("err = %v, want NoManagerError", err)
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("failed raise must leave no dangling identity")
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("failed raise must record no instance")
	}
}
