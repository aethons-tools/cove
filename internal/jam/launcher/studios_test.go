package launcher

import (
	"context"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

func managerLauncher(ops Backend, manager string) *Launcher {
	return New(Config{
		Ops: ops, Runner: &runner.Fake{}, Manager: manager,
		JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
		IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: readyInv(),
		sleep:     func(time.Duration) {},
	})
}

func raiseLabelsOf(t *testing.T, manager string, kind string) *fakeOps {
	t.Helper()
	ops := &fakeOps{}
	l := managerLauncher(ops, manager)
	if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, SessionKind: kind}, jam.LaunchCreds{}); err != nil {
		t.Fatal(err)
	}
	return ops
}

func TestRaiseLabelsLocal(t *testing.T) {
	ops := raiseLabelsOf(t, "", "")
	if want := []string{"harbor.cove", "harbor.cove.actor=w1"}; !slices.Equal(ops.runLabels, want) {
		t.Fatalf("labels = %v, want %v", ops.runLabels, want)
	}
}

func TestRaiseLabelsManager(t *testing.T) {
	ops := raiseLabelsOf(t, "m1", jam.SessionKindStanding)
	if want := []string{"harbor.cove", "harbor.cove.actor=w1", "harbor.cove.manager=m1"}; !slices.Equal(ops.runLabels, want) {
		t.Fatalf("labels = %v, want %v", ops.runLabels, want)
	}
	if len(ops.volLabels) != 2 {
		t.Fatalf("volumes = %v", ops.volLabels)
	}
	for name, ls := range ops.volLabels {
		if ls[ManagerLabel] != "m1" || ls[JamLabel] != "jam.example.com:443#m1" || ls[StateLabel] != "w1" {
			t.Fatalf("volume %s labels = %v", name, ls)
		}
	}
}

func TestRemoteManagerStateScopedAwayFromLocal(t *testing.T) {
	ops := &fakeOps{}
	for manager, actor := range map[string]string{"": "local-actor", "m1": "m1-actor"} {
		l := managerLauncher(ops, manager)
		if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: actor, Kit: testKitRef, SessionKind: jam.SessionKindStanding}, jam.LaunchCreds{}); err != nil {
			t.Fatal(err)
		}
	}
	for manager, want := range map[string]string{"": "local-actor", "m1": "m1-actor"} {
		got, err := managerLauncher(ops, manager).StateOwners(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []string{want}) {
			t.Fatalf("manager %q owners = %v, want [%s]", manager, got, want)
		}
	}
}

func TestStudiosListsOnlyThisManager(t *testing.T) {
	ops := &fakeOps{containers: map[string]map[string]string{
		"cove-a": {ActorLabel: "a", ManagerLabel: "m1"},
		"cove-b": {ActorLabel: "b", ManagerLabel: "m2"},
		"cove-c": {ActorLabel: "c"},
	}}
	got, err := managerLauncher(ops, "m1").Studios(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []jam.ManagedStudio{{ActorID: "a", Location: "cove-a"}}; !slices.Equal(got, want) {
		t.Fatalf("studios = %v, want %v", got, want)
	}
}

func TestStudiosSortedByActor(t *testing.T) {
	ops := &fakeOps{containers: map[string]map[string]string{
		"cove-z": {ActorLabel: "a", ManagerLabel: "m1"},
		"cove-a": {ActorLabel: "b", ManagerLabel: "m1"},
	}}
	got, _ := managerLauncher(ops, "m1").Studios(context.Background())
	if len(got) != 2 || got[0].ActorID != "a" || got[1].ActorID != "b" {
		t.Fatalf("studios = %v", got)
	}
}

func TestStudiosLocalIsEmpty(t *testing.T) {
	ops := &fakeOps{containers: map[string]map[string]string{"cove-a": {ActorLabel: "a"}}}
	got, err := managerLauncher(ops, "").Studios(context.Background())
	if err != nil || got != nil {
		t.Fatalf("studios = %v, %v; want nil, nil", got, err)
	}
}

type noListOps struct{ Backend }

func TestStudiosNeedsContainerLister(t *testing.T) {
	if _, err := managerLauncher(noListOps{&fakeOps{}}, "m1").Studios(context.Background()); err == nil {
		t.Fatal("want an error without ContainerLister")
	}
}

func TestSetSSHUserCAUsedByNextRaise(t *testing.T) {
	r := &runner.Fake{}
	ops := &sshCAOps{egressOps: &egressOps{fakeOps: &fakeOps{}, r: r}}
	l := newSSHCALauncher(ops, r, io.Discard, nil)
	raise := func() {
		if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef}, jam.LaunchCreds{}); err != nil {
			t.Error(err)
		}
	}
	l.SetSSHUserCA([]byte("ca"))
	raise()
	if ops.installs != 1 || string(ops.key) != "ca" {
		t.Fatalf("installs = %d key = %q, want 1 \"ca\"", ops.installs, ops.key)
	}
	l.SetSSHUserCA(nil)
	raise()
	if ops.installs != 1 {
		t.Fatalf("installs = %d after clearing the CA, want still 1", ops.installs)
	}
	// Concurrent with a Raise (meaningful under -race).
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); raise() }()
	go func() { defer wg.Done(); l.SetSSHUserCA([]byte("ca2")) }()
	wg.Wait()
}
