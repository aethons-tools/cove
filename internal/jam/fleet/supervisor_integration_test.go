package fleet

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// routedSupervisor is a jam.Supervisor over a MemStore with a guest role whose
// launcher is a real Router holding only loc.
func routedSupervisor(t *testing.T, loc *fakeManager) (*jam.Supervisor, jam.Store) {
	t.Helper()
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	r := newRouter()
	mustAdd(t, r, loc)
	clk := time.Unix(1000, 0).UTC()
	sup := jam.NewSupervisor(store, r, "holder-A", time.Minute, 30*time.Second,
		func() time.Time { return clk }, slog.New(slog.DiscardHandler))
	return sup, store
}

func TestSupervisorOverRouterRecordsManager(t *testing.T) {
	loc := newFake("m0")
	sup, store := routedSupervisor(t, loc)
	inst, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetInstance("w1"); inst.Manager != loc.Name() || got.Manager != loc.Name() {
		t.Fatalf("Manager = %q / stored %q, want %q", inst.Manager, got.Manager, loc.Name())
	}
	if inst.Location != "m0-loc" || !slices.Equal(loc.get(&loc.raised), []string{"guest"}) {
		t.Fatalf("location %q raised %v", inst.Location, loc.get(&loc.raised))
	}
}

func TestSupervisorOverRouterPreparesKitOnManagerThenRaises(t *testing.T) {
	loc := newFake("m0")
	loc.notReadyOnce.Store(true)
	loc.prepareState = jam.KitReady
	sup, store := routedSupervisor(t, loc)
	ref, err := jam.EnsureStudioKit(store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetDefaultStudioKit(ref)
	inst, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.get(&loc.prepared); len(got) != 1 {
		t.Fatalf("PrepareKit calls = %v, want one", got)
	}
	if inst.Manager != loc.Name() || len(loc.get(&loc.raised)) != 1 {
		t.Fatalf("manager %q raised %v, want the retry on the manager", inst.Manager, loc.get(&loc.raised))
	}
}

func TestSupervisorOverRouterPinsStandingSessionToManager(t *testing.T) {
	loc := newFake("m0")
	sup, store := routedSupervisor(t, loc)
	pid, ok := jam.ProjectIDOf(store, "default")
	if !ok {
		t.Fatal("no default project")
	}
	sid := string(ident.New(ident.Session))
	if err := store.PutStandingSession(pid, "guest", "spider", sid); err != nil {
		t.Fatal(err)
	}
	inst, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{
		ActorID: sid, Role: "guest", Name: "spider", SessionKind: jam.SessionKindStanding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Manager != loc.Name() {
		t.Fatalf("manager = %q, want %q", inst.Manager, loc.Name())
	}
	if got := store.StandingSessionManager(pid, "guest", "spider"); got != loc.Name() {
		t.Fatalf("pin = %q, want %q", got, loc.Name())
	}
}

// A raise whose success lands after a forced revoke's ForgetManager leaves a
// live instance on a manager the router no longer has. Reconcile must still
// converge: tear down (no-op), revoke the identity, drop the record.
func TestRaiseLandingAfterForcedRevokeConverges(t *testing.T) {
	loc := newFake("m0")
	sup, store := routedSupervisor(t, loc)
	ctx := context.Background()
	inst, _, _, err := sup.Raise(ctx, jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	inst.Manager = "gone"
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := sup.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := store.GetInstance("w1"); ok && got.Phase != jam.PhaseGone {
		t.Fatalf("instance = %+v, want gone or dropped", got)
	}
	for _, a := range store.ListActors() {
		if a.ID == "w1" {
			t.Fatalf("actor %s still stored", a.ID)
		}
	}
}

func TestReconcileKeepsStudioOnDisconnectedManager(t *testing.T) {
	loc := newFake("m0")
	sup, store := routedSupervisor(t, loc)
	// routedSupervisor's router is private; build one with a disconnected m1.
	r := newRouter()
	mustAdd(t, r, loc)
	m1 := newFake("m1")
	m1.avail.Store(false) // registered but disconnected
	mustAdd(t, r, m1)
	clk := time.Unix(1000, 0).UTC()
	sup = jam.NewSupervisor(store, r, "holder-A", time.Minute, 30*time.Second,
		func() time.Time { return clk }, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	inst, _, _, err := sup.Raise(ctx, jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	inst.Manager = "m1"
	inst.Lease.Expiry = clk.Add(-time.Hour) // expired: Reconcile probes
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	if m1.Available() {
		t.Fatal("m1 should be disconnected")
	}
	for i := 0; i < 2; i++ {
		if err := sup.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := store.GetInstance("w1")
	if !ok || got.Phase != jam.PhaseLive {
		t.Fatalf("instance = %+v ok=%v, want still live", got, ok)
	}
}

func TestReconcileLeavesOtherHoldersLeaseOnMissingManager(t *testing.T) {
	loc := newFake("m0")
	sup, store := routedSupervisor(t, loc)
	ctx := context.Background()
	inst, _, _, err := sup.Raise(ctx, jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	inst.Manager = "gone"
	inst.Lease.Holder = "holder-B"
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	if err := sup.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.GetInstance("w1"); !ok || got.Phase != jam.PhaseLive {
		t.Fatalf("instance = %+v ok=%v, want untouched", got, ok)
	}
}

// cutoverSupervisor is routedSupervisor with a movable clock and a captured log.
func cutoverSupervisor(t *testing.T, m1 *fakeManager) (*jam.Supervisor, jam.Store, *time.Time, *bytes.Buffer) {
	t.Helper()
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	r := newRouter()
	mustAdd(t, r, m1)
	clk := time.Unix(1000, 0).UTC()
	var buf bytes.Buffer
	sup := jam.NewSupervisor(store, r, "holder-A", time.Minute, 30*time.Second,
		func() time.Time { return clk }, slog.New(slog.NewTextHandler(&buf, nil)))
	return sup, store, &clk, &buf
}

// Studios the removed in-process launcher raised are recorded on "local" or on
// "" (legacy). "local" is never registered now, so neither may reach a manager.
func TestCutoverReapsLocalInstances(t *testing.T) {
	m1 := newFake("m1")
	sup, store, clk, _ := cutoverSupervisor(t, m1)
	ctx := context.Background()
	for _, id := range []string{"w-local", "w-legacy"} {
		inst, _, _, err := sup.Raise(ctx, jam.RaiseSpec{ActorID: id, Role: "guest"})
		if err != nil {
			t.Fatal(err)
		}
		inst.Manager = jam.LocalManager
		if id == "w-legacy" {
			inst.Manager = ""
		}
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	// Manager "" is skipped by the named-manager reap and is only reaped when
	// its lease has expired and Probe (routed to the unregistered "local")
	// reports it dead, so move the clock past the lease TTL.
	*clk = clk.Add(time.Hour)
	// Snapshot after the raises: only calls made during Reconcile matter.
	tornBefore := len(m1.get(&m1.tornDown))
	for i := 0; i < 2; i++ {
		if err := sup.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"w-local", "w-legacy"} {
		if got, ok := store.GetInstance(id); ok && got.Phase != jam.PhaseGone {
			t.Fatalf("%s = %+v, want gone or dropped", id, got)
		}
		for _, a := range store.ListActors() {
			if a.ID == id {
				t.Fatalf("actor %s still stored", id)
			}
		}
	}
	if got := m1.get(&m1.tornDown); len(got) != tornBefore {
		t.Fatalf("m1 Teardown calls = %v, want none for local studios", got)
	}
	if got := m1.get(&m1.probed); len(got) != 0 {
		t.Fatalf("m1 Probe calls = %v, want none", got)
	}
	if got := m1.get(&m1.paused); len(got) != 0 {
		t.Fatalf("m1 Pause calls = %v, want none", got)
	}
}

// A standing session pinned to the retired "local" manager is placed fresh on
// a connected manager, and the stale pin is replaced with that manager.
func TestCutoverResetsLocalPin(t *testing.T) {
	m1 := newFake("m1")
	sup, store, _, logs := cutoverSupervisor(t, m1)
	pid, ok := jam.ProjectIDOf(store, "default")
	if !ok {
		t.Fatal("no default project")
	}
	sid := string(ident.New(ident.Session))
	if err := store.PutStandingSession(pid, "guest", "spider", sid); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStandingSessionManager(pid, "guest", "spider", jam.LocalManager); err != nil {
		t.Fatal(err)
	}
	inst, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{
		ActorID: sid, Role: "guest", Name: "spider", SessionKind: jam.SessionKindStanding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Manager != "m1" {
		t.Fatalf("manager = %q, want m1", inst.Manager)
	}
	if got := store.StandingSessionManager(pid, "guest", "spider"); got != "m1" {
		t.Fatalf("pin = %q, want m1", got)
	}
	want := "standing session's pinned manager is not configured; placing fresh, state not carried over"
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, want) || !strings.Contains(out, "pin=local") {
		t.Fatalf("log missing the pin warning:\n%s", out)
	}
}
